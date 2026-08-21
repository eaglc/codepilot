package credential

import (
	"context"
	"errors"
	"sync"

	"github.com/eaglc/codepilot/internal/provider"
	"github.com/eaglc/codepilot/internal/session"
)

// ErrStoreClosed reports that a credential store has been closed and can no
// longer serve requests.
var ErrStoreClosed = errors.New("credential store is closed")

var _ provider.CredentialStore = (*MemoryStore)(nil)

// MemoryStore keeps copied credentials for the lifetime of the current process.
type MemoryStore struct {
	mu      sync.RWMutex
	secrets map[session.ProviderProfileID][]byte
	closed  bool
}

// NewMemoryStore creates an empty process-local credential store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{secrets: make(map[session.ProviderProfileID][]byte)}
}

// Get returns a defensive copy of a stored credential.
func (s *MemoryStore) Get(ctx context.Context, id session.ProviderProfileID) (provider.Secret, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.closed {
		return nil, false, ErrStoreClosed
	}
	secret, exists := s.secrets[id]
	if !exists {
		return nil, false, nil
	}

	return append(provider.Secret(nil), secret...), true, nil
}

// Put copies a credential into process-local memory.
func (s *MemoryStore) Put(ctx context.Context, id session.ProviderProfileID, secret provider.Secret) (provider.CredentialLocation, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if id == "" {
		return "", errors.New("put credential: provider profile ID is empty")
	}
	if len(secret) == 0 {
		return "", errors.New("put credential: secret is empty")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return "", ErrStoreClosed
	}
	if previous := s.secrets[id]; previous != nil {
		wipe(previous)
	}
	s.secrets[id] = append([]byte(nil), secret...)

	return provider.CredentialInMemory, nil
}

// Delete removes and overwrites the store-owned copy when possible.
func (s *MemoryStore) Delete(ctx context.Context, id session.ProviderProfileID) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return ErrStoreClosed
	}
	if secret := s.secrets[id]; secret != nil {
		wipe(secret)
		delete(s.secrets, id)
	}

	return nil
}

// Close overwrites store-owned buffers and makes the store unusable.
func (s *MemoryStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil
	}
	for id, secret := range s.secrets {
		wipe(secret)
		delete(s.secrets, id)
	}
	s.closed = true

	return nil
}

func wipe(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
