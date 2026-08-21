package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/eaglc/codepilot/internal/provider"
	"github.com/eaglc/codepilot/internal/session"
	"go.yaml.in/yaml/v3"
)

const currentProviderFileVersion = 1

var _ provider.ProviderProfileStore = (*ProviderFileStore)(nil)

// ProviderFileStore persists secret-free provider profiles in providers.yaml.
type ProviderFileStore struct {
	path string
	mu   sync.Mutex
}

type providerFile struct {
	Version  int                  `yaml:"version"`
	Profiles []providerProfileDTO `yaml:"profiles"`
}

type providerProfileDTO struct {
	ID            session.ProviderProfileID `yaml:"id"`
	Kind          provider.Kind             `yaml:"kind"`
	DisplayName   string                    `yaml:"display_name"`
	BaseURL       string                    `yaml:"base_url,omitempty"`
	ModelID       string                    `yaml:"model_id"`
	CredentialRef string                    `yaml:"credential_ref,omitempty"`
	ValidatedAt   time.Time                 `yaml:"validated_at"`
}

// NewProviderFileStore creates a provider profile store at path.
func NewProviderFileStore(path string) *ProviderFileStore {
	return &ProviderFileStore{path: path}
}

// ListProfiles returns profiles in their stable persisted order.
func (s *ProviderFileStore) ListProfiles(ctx context.Context) ([]provider.Profile, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	file, err := s.loadLocked()
	if err != nil {
		return nil, err
	}
	values := make([]provider.Profile, 0, len(file.Profiles))
	for _, stored := range file.Profiles {
		values = append(values, stored.profile())
	}

	return values, nil
}

// LoadProfile loads one profile without accessing its credential.
func (s *ProviderFileStore) LoadProfile(ctx context.Context, id session.ProviderProfileID) (provider.Profile, error) {
	if err := ctx.Err(); err != nil {
		return provider.Profile{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	file, err := s.loadLocked()
	if err != nil {
		return provider.Profile{}, err
	}
	for _, stored := range file.Profiles {
		if stored.ID == id {
			return stored.profile(), nil
		}
	}

	return provider.Profile{}, &session.AppError{
		Code:        session.ErrNotFound,
		Operation:   "config.load_provider_profile",
		UserMessage: "Provider profile not found.",
	}
}

// SaveProfile inserts or replaces one validated profile atomically.
func (s *ProviderFileStore) SaveProfile(ctx context.Context, value provider.Profile) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateProviderProfile(value); err != nil {
		return fmt.Errorf("save provider profile: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	file, err := s.loadLocked()
	if err != nil {
		return err
	}
	stored := newProviderProfileDTO(value)
	replaced := false
	for index := range file.Profiles {
		if file.Profiles[index].ID == value.ID {
			file.Profiles[index] = stored
			replaced = true
			break
		}
	}
	if !replaced {
		file.Profiles = append(file.Profiles, stored)
	}
	if err := writeYAMLAtomic(s.path, file); err != nil {
		return fmt.Errorf("save provider profiles %q: %w", s.path, err)
	}

	return nil
}

// DeleteProfile removes one profile without deleting its keyring credential.
func (s *ProviderFileStore) DeleteProfile(ctx context.Context, id session.ProviderProfileID) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	file, err := s.loadLocked()
	if err != nil {
		return err
	}
	profiles := file.Profiles[:0]
	for _, stored := range file.Profiles {
		if stored.ID != id {
			profiles = append(profiles, stored)
		}
	}
	if len(profiles) == len(file.Profiles) {
		return nil
	}
	file.Profiles = profiles
	if err := writeYAMLAtomic(s.path, file); err != nil {
		return fmt.Errorf("delete provider profile %q: %w", id, err)
	}

	return nil
}

func (s *ProviderFileStore) loadLocked() (providerFile, error) {
	if strings.TrimSpace(s.path) == "" {
		return providerFile{}, errors.New("load provider profiles: path is empty")
	}

	file, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return providerFile{Version: currentProviderFileVersion}, nil
	}
	if err != nil {
		return providerFile{}, fmt.Errorf("open provider profiles %q: %w", s.path, err)
	}
	defer file.Close()

	var value providerFile
	decoder := yaml.NewDecoder(file)
	decoder.KnownFields(true)
	if err := decoder.Decode(&value); err != nil {
		return providerFile{}, fmt.Errorf("decode provider profiles %q: %w", s.path, err)
	}
	if err := ensureYAMLEOF(decoder); err != nil {
		return providerFile{}, fmt.Errorf("decode provider profiles %q: %w", s.path, err)
	}
	if value.Version != currentProviderFileVersion {
		return providerFile{}, fmt.Errorf("decode provider profiles %q: unsupported version %d", s.path, value.Version)
	}

	seen := make(map[session.ProviderProfileID]struct{}, len(value.Profiles))
	for _, stored := range value.Profiles {
		profile := stored.profile()
		if err := validateProviderProfile(profile); err != nil {
			return providerFile{}, fmt.Errorf("decode provider profile %q: %w", stored.ID, err)
		}
		if _, exists := seen[stored.ID]; exists {
			return providerFile{}, fmt.Errorf("decode provider profiles %q: duplicate profile ID %q", s.path, stored.ID)
		}
		seen[stored.ID] = struct{}{}
	}

	return value, nil
}

func validateProviderProfile(value provider.Profile) error {
	if err := provider.ValidateProfile(value); err != nil {
		return err
	}
	wantCredentialRef := "keyring:provider/" + string(value.ID)
	if value.CredentialRef != "" && value.CredentialRef != wantCredentialRef {
		return errors.New("credential reference is not a CodePilot keyring reference")
	}
	if value.Kind == provider.KindOllama && value.CredentialRef != "" {
		return errors.New("Ollama profile must not contain a credential reference")
	}

	return nil
}

func newProviderProfileDTO(value provider.Profile) providerProfileDTO {
	return providerProfileDTO{
		ID:            value.ID,
		Kind:          value.Kind,
		DisplayName:   value.DisplayName,
		BaseURL:       value.BaseURL,
		ModelID:       value.ModelID,
		CredentialRef: value.CredentialRef,
		ValidatedAt:   value.ValidatedAt.UTC(),
	}
}

func (d providerProfileDTO) profile() provider.Profile {
	return provider.Profile{
		ID:            d.ID,
		Kind:          d.Kind,
		DisplayName:   d.DisplayName,
		BaseURL:       d.BaseURL,
		ModelID:       d.ModelID,
		CredentialRef: d.CredentialRef,
		ValidatedAt:   d.ValidatedAt,
	}
}
