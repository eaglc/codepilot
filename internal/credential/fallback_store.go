package credential

import (
	"context"
	"errors"
	"fmt"

	"github.com/eaglc/codepilot/internal/provider"
	"github.com/eaglc/codepilot/internal/session"
)

var _ provider.CredentialStore = (*FallbackStore)(nil)

// FallbackStore uses the OS keyring when available and otherwise explicitly
// keeps credentials in process memory.
type FallbackStore struct {
	keyring *KeyringStore
	memory  *MemoryStore
}

// NewFallbackStore composes durable and process-local credential backends.
func NewFallbackStore(keyringStore *KeyringStore, memoryStore *MemoryStore) *FallbackStore {
	if memoryStore == nil {
		memoryStore = NewMemoryStore()
	}

	return &FallbackStore{
		keyring: keyringStore,
		memory:  memoryStore,
	}
}

// Get loads from the keyring and checks the current-process fallback when the
// durable credential is missing or the keyring is unavailable.
func (s *FallbackStore) Get(ctx context.Context, id session.ProviderProfileID) (provider.Secret, bool, error) {
	if s.keyring != nil {
		secret, found, err := s.keyring.Get(ctx, id)
		if err == nil && found {
			return secret, true, nil
		}
		if err != nil && !errors.Is(err, ErrKeyringUnavailable) {
			return nil, false, err
		}
	}

	return s.memory.Get(ctx, id)
}

// Put prefers durable storage and falls back only for a recognized keyring
// availability failure.
func (s *FallbackStore) Put(ctx context.Context, id session.ProviderProfileID, secret provider.Secret) (provider.CredentialLocation, error) {
	if s.keyring != nil {
		location, err := s.keyring.Put(ctx, id, secret)
		if err == nil {
			if deleteErr := s.memory.Delete(ctx, id); deleteErr != nil && !errors.Is(deleteErr, ErrStoreClosed) {
				return "", fmt.Errorf("remove stale memory credential: %w", deleteErr)
			}
			return location, nil
		}
		if !errors.Is(err, ErrKeyringUnavailable) {
			return "", err
		}
	}

	return s.memory.Put(ctx, id, secret)
}

// Delete always clears process memory. If the keyring is unavailable, the
// error remains visible so the UI can warn about possible durable cleanup.
func (s *FallbackStore) Delete(ctx context.Context, id session.ProviderProfileID) error {
	memoryErr := s.memory.Delete(ctx, id)
	if s.keyring == nil {
		return memoryErr
	}

	keyringErr := s.keyring.Delete(ctx, id)
	return errors.Join(keyringErr, memoryErr)
}

// Close clears the process-local fallback store.
func (s *FallbackStore) Close() error {
	return s.memory.Close()
}
