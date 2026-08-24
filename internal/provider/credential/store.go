// Package credential resolves Provider credentials without persisting secret
// material alongside Provider profiles or Agent session data.
package credential

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/eaglc/codepilot/internal/provider"
	keyring "github.com/zalando/go-keyring"
)

var (
	validEnvironment = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// EnvironmentStore is a read-only, explicitly allow-listed environment
// fallback. References are opaque identifiers rather than environment names.
type EnvironmentStore struct {
	references map[string]string
	lookup     func(string) (string, bool)
}

// NewEnvironmentStore creates an environment store from credential reference
// to environment variable name mappings.
func NewEnvironmentStore(references map[string]string) (*EnvironmentStore, error) {
	return newEnvironmentStore(references, os.LookupEnv)
}

func newEnvironmentStore(references map[string]string, lookup func(string) (string, bool)) (*EnvironmentStore, error) {
	if lookup == nil {
		return nil, errors.New("create environment credential store: lookup function is required")
	}
	values := make(map[string]string, len(references))
	for reference, name := range references {
		if err := validateReference(reference); err != nil {
			return nil, fmt.Errorf("create environment credential store: %w", err)
		}
		name = strings.TrimSpace(name)
		if !validEnvironment.MatchString(name) {
			return nil, fmt.Errorf("create environment credential store: invalid environment variable for %q", reference)
		}
		values[reference] = name
	}
	return &EnvironmentStore{references: values, lookup: lookup}, nil
}

// LoadCredential loads a defensive byte copy from an allow-listed variable.
func (s *EnvironmentStore) LoadCredential(ctx context.Context, reference string) (provider.Credential, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if s == nil {
		return nil, false, errors.New("load environment credential: store is nil")
	}
	name, allowed := s.references[reference]
	if !allowed {
		return nil, false, nil
	}
	value, found := s.lookup(name)
	if !found || value == "" {
		return nil, false, nil
	}
	return provider.Credential([]byte(value)), true, nil
}

// KeyringStore stores credentials in the operating system's protected
// credential service under a stable application service name.
type KeyringStore struct {
	service string
	backend keyringBackend
}

type keyringBackend interface {
	Get(service, user string) (string, error)
	Set(service, user, password string) error
	Delete(service, user string) error
}

type systemKeyring struct{}

func (systemKeyring) Get(service, user string) (string, error) { return keyring.Get(service, user) }
func (systemKeyring) Set(service, user, password string) error {
	return keyring.Set(service, user, password)
}
func (systemKeyring) Delete(service, user string) error { return keyring.Delete(service, user) }

// NewKeyringStore creates a mutable system Keyring repository.
func NewKeyringStore(service string) (*KeyringStore, error) {
	return newKeyringStore(service, systemKeyring{})
}

func newKeyringStore(service string, backend keyringBackend) (*KeyringStore, error) {
	service = strings.TrimSpace(service)
	if service == "" || len(service) > 128 || strings.ContainsAny(service, "\r\n\x00") {
		return nil, errors.New("create Keyring credential store: service name is invalid")
	}
	if backend == nil {
		return nil, errors.New("create Keyring credential store: backend is required")
	}
	return &KeyringStore{service: service, backend: backend}, nil
}

// LoadCredential resolves one secret from Keyring. Backend error details are
// deliberately excluded because Provider/UI errors must never echo secrets.
func (s *KeyringStore) LoadCredential(ctx context.Context, reference string) (provider.Credential, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if err := validateReference(reference); err != nil {
		return nil, false, fmt.Errorf("load Keyring credential: %w", err)
	}
	value, err := s.backend.Get(s.service, reference)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, keyringUnavailable("load", reference)
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if value == "" {
		return nil, false, nil
	}
	return provider.Credential([]byte(value)), true, nil
}

// SaveCredential creates or overwrites one Keyring entry.
func (s *KeyringStore) SaveCredential(ctx context.Context, reference string, value provider.Credential) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateReference(reference); err != nil {
		return fmt.Errorf("save Keyring credential: %w", err)
	}
	if len(value) == 0 {
		return errors.New("save Keyring credential: credential is empty; delete it instead")
	}
	if err := s.backend.Set(s.service, reference, string(value)); err != nil {
		return keyringUnavailable("save", reference)
	}
	return ctx.Err()
}

// DeleteCredential idempotently removes one Keyring entry.
func (s *KeyringStore) DeleteCredential(ctx context.Context, reference string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateReference(reference); err != nil {
		return fmt.Errorf("delete Keyring credential: %w", err)
	}
	err := s.backend.Delete(s.service, reference)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	if err != nil {
		return keyringUnavailable("delete", reference)
	}
	return ctx.Err()
}

// ChainStore reads credentials in order and sends all mutations only to the
// protected primary repository. This keeps environment fallbacks read-only.
type ChainStore struct {
	primary provider.CredentialRepository
	stores  []provider.CredentialStore
}

// NewChainStore creates a Keyring-first store with optional read-only
// fallbacks such as EnvironmentStore.
func NewChainStore(primary provider.CredentialRepository, fallbacks ...provider.CredentialStore) (*ChainStore, error) {
	if primary == nil {
		return nil, errors.New("create credential chain: primary repository is required")
	}
	stores := make([]provider.CredentialStore, 0, len(fallbacks)+1)
	stores = append(stores, primary)
	for _, fallback := range fallbacks {
		if fallback == nil {
			return nil, errors.New("create credential chain: fallback store is nil")
		}
		stores = append(stores, fallback)
	}
	return &ChainStore{primary: primary, stores: stores}, nil
}

// LoadCredential returns the first available value. An unavailable Keyring is
// tolerated when a later environment fallback resolves the credential.
func (s *ChainStore) LoadCredential(ctx context.Context, reference string) (provider.Credential, bool, error) {
	var firstError error
	for _, store := range s.stores {
		value, found, err := store.LoadCredential(ctx, reference)
		if err != nil {
			if firstError == nil {
				firstError = err
			}
			continue
		}
		if found {
			return value, true, nil
		}
	}
	if firstError != nil {
		return nil, false, firstError
	}
	return nil, false, nil
}

// SaveCredential creates or overwrites the protected primary entry.
func (s *ChainStore) SaveCredential(ctx context.Context, reference string, value provider.Credential) error {
	return s.primary.SaveCredential(ctx, reference, value)
}

// DeleteCredential removes only the protected primary entry; environment
// fallbacks remain controlled by the process owner.
func (s *ChainStore) DeleteCredential(ctx context.Context, reference string) error {
	return s.primary.DeleteCredential(ctx, reference)
}

func validateReference(reference string) error {
	return provider.ValidateCredentialReference(reference)
}

func keyringUnavailable(operation, reference string) error {
	return fmt.Errorf("%w: cannot %s credential %q", provider.ErrCredentialStoreUnavailable, operation, reference)
}

var (
	_ provider.CredentialStore      = (*EnvironmentStore)(nil)
	_ provider.CredentialRepository = (*KeyringStore)(nil)
	_ provider.CredentialRepository = (*ChainStore)(nil)
)
