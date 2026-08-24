package provider

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// MemoryProfileRepository stores provider profiles for tests and ephemeral applications.
type MemoryProfileRepository struct {
	mu       sync.RWMutex
	profiles map[ProfileID]Profile
}

// NewMemoryProfileRepository creates an empty profile repository.
func NewMemoryProfileRepository() *MemoryProfileRepository {
	return &MemoryProfileRepository{profiles: make(map[ProfileID]Profile)}
}

// LoadProfile returns one secret-free provider profile.
func (r *MemoryProfileRepository) LoadProfile(ctx context.Context, id ProfileID) (Profile, error) {
	if err := ctx.Err(); err != nil {
		return Profile{}, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	profile, exists := r.profiles[id]
	if !exists {
		return Profile{}, fmt.Errorf("provider profile %q not found", id)
	}
	return profile, nil
}

// ListProfiles returns stable profile ordering by ID.
func (r *MemoryProfileRepository) ListProfiles(ctx context.Context) ([]Profile, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	profiles := make([]Profile, 0, len(r.profiles))
	for _, profile := range r.profiles {
		profiles = append(profiles, profile)
	}
	sort.Slice(profiles, func(left, right int) bool { return profiles[left].ID < profiles[right].ID })
	return profiles, nil
}

// SaveProfile validates and stores one profile.
func (r *MemoryProfileRepository) SaveProfile(ctx context.Context, profile Profile) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if profile.ID == "" || profile.Kind == "" {
		return fmt.Errorf("save provider profile: id and kind are required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.profiles[profile.ID] = profile
	return nil
}

// MemoryCredentialStore stores defensive secret copies for tests and fallback use.
type MemoryCredentialStore struct {
	mu     sync.RWMutex
	values map[string]Credential
}

// NewMemoryCredentialStore creates an empty credential store.
func NewMemoryCredentialStore() *MemoryCredentialStore {
	return &MemoryCredentialStore{values: make(map[string]Credential)}
}

// SaveCredential stores a defensive secret copy.
func (s *MemoryCredentialStore) SaveCredential(ctx context.Context, reference string, credential Credential) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if reference == "" {
		return fmt.Errorf("save provider credential: reference is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[reference] = append(Credential(nil), credential...)
	return nil
}

// DeleteCredential removes a credential. Deleting an absent reference is
// intentionally idempotent.
func (s *MemoryCredentialStore) DeleteCredential(ctx context.Context, reference string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if reference == "" {
		return fmt.Errorf("delete provider credential: reference is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if value, exists := s.values[reference]; exists {
		wipeCredential(value)
		delete(s.values, reference)
	}
	return nil
}

// LoadCredential returns a defensive secret copy.
func (s *MemoryCredentialStore) LoadCredential(ctx context.Context, reference string) (Credential, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if reference == "" {
		return nil, false, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, exists := s.values[reference]
	return append(Credential(nil), value...), exists, nil
}

var _ CredentialRepository = (*MemoryCredentialStore)(nil)
