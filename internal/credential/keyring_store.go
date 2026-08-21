package credential

import (
	"context"
	"errors"
	"fmt"

	"github.com/eaglc/codepilot/internal/provider"
	"github.com/eaglc/codepilot/internal/session"
	keyring "github.com/zalando/go-keyring"
)

const keyringService = "CodePilot"

// ErrKeyringUnavailable indicates that durable credential storage cannot be
// used in the current environment and an explicit memory fallback is allowed.
var ErrKeyringUnavailable = errors.New("OS keyring is unavailable")

var _ provider.CredentialStore = (*KeyringStore)(nil)

type keyringBackend interface {
	Get(service string, account string) (string, error)
	Set(service string, account string, secret string) error
	Delete(service string, account string) error
}

type systemKeyringBackend struct{}

func (systemKeyringBackend) Get(service string, account string) (string, error) {
	return keyring.Get(service, account)
}

func (systemKeyringBackend) Set(service string, account string, secret string) error {
	return keyring.Set(service, account, secret)
}

func (systemKeyringBackend) Delete(service string, account string) error {
	return keyring.Delete(service, account)
}

// KeyringStore persists credentials in the current user's OS keyring.
type KeyringStore struct {
	backend keyringBackend
}

// NewKeyringStore creates the OS keyring adapter. Availability is determined
// on the first operation because supported keyring backends have no safe probe.
func NewKeyringStore() (*KeyringStore, error) {
	return &KeyringStore{backend: systemKeyringBackend{}}, nil
}

// Get returns a copied credential or found=false when the account is absent.
func (s *KeyringStore) Get(ctx context.Context, id session.ProviderProfileID) (provider.Secret, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	account, err := keyringAccount(id)
	if err != nil {
		return nil, false, err
	}

	secret, err := s.backend.Get(keyringService, account)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, classifyKeyringError("get", err)
	}

	return provider.Secret([]byte(secret)), true, nil
}

// Put persists one credential in the OS keyring.
func (s *KeyringStore) Put(ctx context.Context, id session.ProviderProfileID, secret provider.Secret) (provider.CredentialLocation, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	account, err := keyringAccount(id)
	if err != nil {
		return "", err
	}
	if len(secret) == 0 {
		return "", errors.New("put keyring credential: secret is empty")
	}

	if err := s.backend.Set(keyringService, account, string(secret)); err != nil {
		return "", classifyKeyringError("put", err)
	}

	return provider.CredentialInKeyring, nil
}

// Delete removes one credential. Deleting an absent account is idempotent.
func (s *KeyringStore) Delete(ctx context.Context, id session.ProviderProfileID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	account, err := keyringAccount(id)
	if err != nil {
		return err
	}

	err = s.backend.Delete(keyringService, account)
	if err == nil || errors.Is(err, keyring.ErrNotFound) {
		return nil
	}

	return classifyKeyringError("delete", err)
}

func keyringAccount(id session.ProviderProfileID) (string, error) {
	if id == "" {
		return "", errors.New("keyring credential: provider profile ID is empty")
	}

	return "provider/" + string(id), nil
}

func classifyKeyringError(operation string, err error) error {
	if errors.Is(err, keyring.ErrSetDataTooBig) {
		return fmt.Errorf("%s keyring credential: %w", operation, err)
	}

	return &keyringOperationError{
		operation: operation,
		cause:     err,
	}
}

type keyringOperationError struct {
	operation string
	cause     error
}

func (e *keyringOperationError) Error() string {
	return e.operation + " keyring credential: OS keyring is unavailable"
}

func (e *keyringOperationError) Unwrap() []error {
	return []error{ErrKeyringUnavailable, e.cause}
}
