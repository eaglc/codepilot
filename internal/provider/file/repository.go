// Package file persists secret-free Provider profiles in a versioned JSON file.
package file

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/eaglc/codepilot/internal/provider"
)

const (
	formatVersion = 1
	fileName      = "provider-profiles.json"
	maxFileBytes  = 4 << 20
)

var (
	validProfileID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
)

// Repository serializes all secret-free profiles through one atomic file.
type Repository struct {
	path string
	mu   sync.RWMutex
}

type profileFile struct {
	Version  int             `json:"version"`
	Profiles []profileRecord `json:"profiles"`
}

type profileRecord struct {
	ID            provider.ProfileID `json:"id"`
	Kind          provider.Kind      `json:"kind"`
	DisplayName   string             `json:"display_name"`
	BaseURL       string             `json:"base_url,omitempty"`
	DefaultModel  string             `json:"default_model"`
	CredentialRef string             `json:"credential_ref,omitempty"`
	ValidatedAt   time.Time          `json:"validated_at,omitempty"`
}

// NewRepository opens a Provider configuration directory without creating a
// profile file until the first successful SaveProfile.
func NewRepository(root string) (*Repository, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("create Provider profile repository: config root is required")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("create Provider profile repository: resolve root: %w", err)
	}
	absolute = filepath.Clean(absolute)
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return nil, fmt.Errorf("create Provider profile repository: create root: %w", err)
	}
	return &Repository{path: filepath.Join(absolute, fileName)}, nil
}

// LoadProfile returns one profile by stable ID.
func (r *Repository) LoadProfile(ctx context.Context, id provider.ProfileID) (provider.Profile, error) {
	if err := ctx.Err(); err != nil {
		return provider.Profile{}, err
	}
	if !validProfileID.MatchString(string(id)) {
		return provider.Profile{}, fmt.Errorf("load Provider profile: invalid id %q", id)
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	profiles, err := r.readLocked()
	if err != nil {
		return provider.Profile{}, err
	}
	for _, profile := range profiles {
		if profile.ID == id {
			return profile, nil
		}
	}
	return provider.Profile{}, fmt.Errorf("Provider profile %q not found", id)
}

// ListProfiles returns profiles in stable ID order.
func (r *Repository) ListProfiles(ctx context.Context) ([]provider.Profile, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	profiles, err := r.readLocked()
	if err != nil {
		return nil, err
	}
	return append([]provider.Profile(nil), profiles...), nil
}

// SaveProfile validates and atomically creates or updates one profile.
func (r *Repository) SaveProfile(ctx context.Context, profile provider.Profile) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	profile = normalizeProfile(profile)
	if err := validateProfile(profile); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	profiles, err := r.readLocked()
	if err != nil {
		return err
	}
	updated := false
	for index := range profiles {
		if profiles[index].ID == profile.ID {
			profiles[index] = profile
			updated = true
			break
		}
	}
	if !updated {
		profiles = append(profiles, profile)
	}
	sort.Slice(profiles, func(left, right int) bool { return profiles[left].ID < profiles[right].ID })
	return r.writeLocked(profiles)
}

func (r *Repository) readLocked() ([]provider.Profile, error) {
	info, err := os.Stat(r.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read Provider profiles: inspect file: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > maxFileBytes {
		return nil, errors.New("read Provider profiles: configuration file is invalid or too large")
	}
	file, err := os.Open(r.path)
	if err != nil {
		return nil, fmt.Errorf("read Provider profiles: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, maxFileBytes+1))
	decoder.DisallowUnknownFields()
	var stored profileFile
	if err := decoder.Decode(&stored); err != nil {
		return nil, fmt.Errorf("read Provider profiles: decode: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("read Provider profiles: multiple JSON values are not allowed")
	}
	if stored.Version != formatVersion {
		return nil, fmt.Errorf("read Provider profiles: unsupported format version %d", stored.Version)
	}
	profiles := make([]provider.Profile, 0, len(stored.Profiles))
	seen := make(map[provider.ProfileID]struct{}, len(stored.Profiles))
	for index, record := range stored.Profiles {
		profile := normalizeProfile(record.profile())
		if err := validateProfile(profile); err != nil {
			return nil, fmt.Errorf("read Provider profiles: profile %d: %w", index+1, err)
		}
		if _, duplicate := seen[profile.ID]; duplicate {
			return nil, fmt.Errorf("read Provider profiles: duplicate profile id %q", profile.ID)
		}
		seen[profile.ID] = struct{}{}
		profiles = append(profiles, profile)
	}
	sort.Slice(profiles, func(left, right int) bool { return profiles[left].ID < profiles[right].ID })
	return profiles, nil
}

func (r *Repository) writeLocked(profiles []provider.Profile) error {
	records := make([]profileRecord, len(profiles))
	for index, profile := range profiles {
		records[index] = recordFromProfile(profile)
	}
	temporary, err := os.CreateTemp(filepath.Dir(r.path), ".provider-profiles-*.tmp")
	if err != nil {
		return fmt.Errorf("save Provider profiles: create temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("save Provider profiles: secure temporary file: %w", err)
	}
	encoder := json.NewEncoder(temporary)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(profileFile{Version: formatVersion, Profiles: records}); err != nil {
		temporary.Close()
		return fmt.Errorf("save Provider profiles: encode: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("save Provider profiles: sync: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("save Provider profiles: close: %w", err)
	}
	if err := os.Rename(temporaryPath, r.path); err != nil {
		return fmt.Errorf("save Provider profiles: replace: %w", err)
	}
	return nil
}

func validateProfile(profile provider.Profile) error {
	if !validProfileID.MatchString(string(profile.ID)) {
		return fmt.Errorf("save Provider profile: invalid id %q", profile.ID)
	}
	if !validProfileID.MatchString(string(profile.Kind)) {
		return fmt.Errorf("save Provider profile %q: invalid kind %q", profile.ID, profile.Kind)
	}
	if profile.DisplayName == "" || len(profile.DisplayName) > 256 {
		return fmt.Errorf("save Provider profile %q: display name is required and must be at most 256 bytes", profile.ID)
	}
	if profile.DefaultModel == "" || len(profile.DefaultModel) > 512 || strings.ContainsAny(profile.DefaultModel, "\r\n\x00") {
		return fmt.Errorf("save Provider profile %q: default model is invalid", profile.ID)
	}
	if profile.CredentialRef != "" {
		if err := provider.ValidateCredentialReference(profile.CredentialRef); err != nil {
			return fmt.Errorf("save Provider profile %q: %w", profile.ID, err)
		}
	}
	if profile.BaseURL != "" {
		parsed, err := url.Parse(profile.BaseURL)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
			return fmt.Errorf("save Provider profile %q: base URL must be an HTTP(S) URL without user information", profile.ID)
		}
	}
	return nil
}

func normalizeProfile(profile provider.Profile) provider.Profile {
	profile.DisplayName = strings.TrimSpace(profile.DisplayName)
	profile.BaseURL = strings.TrimRight(strings.TrimSpace(profile.BaseURL), "/")
	profile.DefaultModel = strings.TrimSpace(profile.DefaultModel)
	profile.CredentialRef = strings.TrimSpace(profile.CredentialRef)
	return profile
}

func recordFromProfile(profile provider.Profile) profileRecord {
	return profileRecord{
		ID: profile.ID, Kind: profile.Kind, DisplayName: profile.DisplayName, BaseURL: profile.BaseURL,
		DefaultModel: profile.DefaultModel, CredentialRef: profile.CredentialRef, ValidatedAt: profile.ValidatedAt,
	}
}

func (r profileRecord) profile() provider.Profile {
	return provider.Profile{
		ID: r.ID, Kind: r.Kind, DisplayName: r.DisplayName, BaseURL: r.BaseURL,
		DefaultModel: r.DefaultModel, CredentialRef: r.CredentialRef, ValidatedAt: r.ValidatedAt,
	}
}

var _ provider.ProfileRepository = (*Repository)(nil)
