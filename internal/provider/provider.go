// Package provider resolves configured provider profiles into provider-neutral LLM models.
package provider

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/eaglc/codepilot/internal/llm"
)

var validCredentialReference = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]*$`)

// ProfileID uniquely identifies one configured provider profile.
type ProfileID string

// Kind identifies a concrete provider adapter family.
type Kind string

const (
	// KindOpenAI identifies the built-in OpenAI adapter family.
	KindOpenAI Kind = "openai"
	// KindDeepSeek identifies the built-in DeepSeek adapter family.
	KindDeepSeek Kind = "deepseek"
	// KindOllama identifies the built-in local Ollama adapter family.
	KindOllama Kind = "ollama"
)

// Profile contains secret-free provider configuration.
type Profile struct {
	ID            ProfileID
	Kind          Kind
	DisplayName   string
	BaseURL       string
	DefaultModel  string
	CredentialRef string
	ValidatedAt   time.Time
}

// Credential is mutable secret material that callers must not persist in events or messages.
type Credential []byte

// ValidateCredentialReference verifies the shared opaque-reference format used
// by profile files and credential stores.
func ValidateCredentialReference(reference string) error {
	if !validCredentialReference.MatchString(reference) || len(reference) > 256 {
		return fmt.Errorf("credential reference %q is invalid", reference)
	}
	return nil
}

// ErrCredentialStoreUnavailable classifies failures to access the operating
// system's protected credential service without exposing backend details.
var ErrCredentialStoreUnavailable = errors.New("secure credential store is unavailable")

// ProfileRepository persists secret-free provider profiles.
type ProfileRepository interface {
	LoadProfile(ctx context.Context, id ProfileID) (Profile, error)
	ListProfiles(ctx context.Context) ([]Profile, error)
	SaveProfile(ctx context.Context, profile Profile) error
}

// CredentialStore resolves secret bytes by an opaque reference.
type CredentialStore interface {
	LoadCredential(ctx context.Context, reference string) (Credential, bool, error)
}

// CredentialRepository owns mutable credentials. Implementations must never
// persist the secret in Provider profile files, events or session journals.
type CredentialRepository interface {
	CredentialStore
	SaveCredential(ctx context.Context, reference string, credential Credential) error
	DeleteCredential(ctx context.Context, reference string) error
}

// ModelConfig contains one complete provider adapter request.
type ModelConfig struct {
	Profile    Profile
	ModelID    string
	Credential Credential
}

// PreflightResult records a successful endpoint, credential and model check.
type PreflightResult struct {
	ProfileID   ProfileID
	ModelID     string
	ValidatedAt time.Time
}

// Adapter converts one provider profile into the common LLM contract.
type Adapter interface {
	Kind() Kind
	ListModels(ctx context.Context, profile Profile, credential Credential) ([]llm.Model, error)
	CreateModel(ctx context.Context, config ModelConfig) (llm.ChatModel, error)
}

// Service is a provider registry and implements llm.ModelFactory.
type Service struct {
	profiles             ProfileRepository
	credentials          CredentialStore
	credentialRepository CredentialRepository
	adapters             map[Kind]Adapter
	models               map[llm.ModelRef]llm.Model
	mu                   sync.RWMutex
}

// NewService creates a Provider service from explicitly supplied adapters.
func NewService(profiles ProfileRepository, credentials CredentialStore, adapters ...Adapter) (*Service, error) {
	if profiles == nil || credentials == nil {
		return nil, fmt.Errorf("create provider service: profile repository and credential store are required")
	}
	service := &Service{profiles: profiles, credentials: credentials, adapters: make(map[Kind]Adapter, len(adapters)), models: make(map[llm.ModelRef]llm.Model)}
	service.credentialRepository, _ = credentials.(CredentialRepository)
	for _, adapter := range adapters {
		if isNilAdapter(adapter) || adapter.Kind() == "" {
			return nil, fmt.Errorf("create provider service: adapter and kind are required")
		}
		if _, exists := service.adapters[adapter.Kind()]; exists {
			return nil, fmt.Errorf("create provider service: duplicate adapter %q", adapter.Kind())
		}
		service.adapters[adapter.Kind()] = adapter
	}
	return service, nil
}

// SaveCredential creates or overwrites secret material in the configured
// protected repository. The Provider service never writes it to a Profile.
func (s *Service) SaveCredential(ctx context.Context, reference string, credential Credential) error {
	if s == nil || s.credentialRepository == nil {
		return errors.New("save Provider credential: protected credential repository is unavailable")
	}
	copyOfCredential := append(Credential(nil), credential...)
	defer wipeCredential(copyOfCredential)
	if err := s.credentialRepository.SaveCredential(ctx, reference, copyOfCredential); err != nil {
		return fmt.Errorf("save Provider credential %q: %w", reference, err)
	}
	return nil
}

// DeleteCredential removes secret material from the protected repository.
func (s *Service) DeleteCredential(ctx context.Context, reference string) error {
	if s == nil || s.credentialRepository == nil {
		return errors.New("delete Provider credential: protected credential repository is unavailable")
	}
	if err := s.credentialRepository.DeleteCredential(ctx, reference); err != nil {
		return fmt.Errorf("delete Provider credential %q: %w", reference, err)
	}
	return nil
}

// CredentialStatus reports whether one Profile requires and currently resolves
// a credential without exposing the secret bytes.
func (s *Service) CredentialStatus(ctx context.Context, profileID ProfileID) (required bool, configured bool, err error) {
	if s == nil {
		return false, false, errors.New("inspect Provider credential: service is unavailable")
	}
	profile, err := s.profiles.LoadProfile(ctx, profileID)
	if err != nil {
		return false, false, err
	}
	if profile.CredentialRef == "" {
		return false, true, nil
	}
	value, found, err := s.credentials.LoadCredential(ctx, profile.CredentialRef)
	wipeCredential(value)
	return true, found, err
}

// CreateModel resolves ModelRef.Provider as a configured profile ID and hides all adapter SDK types.
func (s *Service) CreateModel(ctx context.Context, ref llm.ModelRef) (llm.ChatModel, error) {
	if err := ref.Validate(); err != nil {
		return nil, NewProductError(ErrorNotConfigured, "provider.create_model", "Provider profile and model selection are incomplete.", false, err)
	}
	profile, adapter, credential, err := s.resolve(ctx, ProfileID(ref.Provider))
	if err != nil {
		return nil, err
	}
	defer wipeCredential(credential)
	model, err := adapter.CreateModel(ctx, ModelConfig{Profile: profile, ModelID: ref.Model, Credential: credential})
	if err != nil {
		var productError *ProductError
		if errors.As(err, &productError) {
			return nil, err
		}
		return nil, NewProductError(ErrorNotConfigured, "provider.create_model", "Provider model could not be initialized. Check the profile and selected model.", false, err)
	}
	if isNilChatModel(model) {
		return nil, fmt.Errorf("create provider model %s/%s: adapter returned nil model", ref.Provider, ref.Model)
	}
	return model, nil
}

// Preflight verifies the profile, credential, endpoint and selected model
// before an Agent run starts, then persists the successful validation time.
func (s *Service) Preflight(ctx context.Context, ref llm.ModelRef) (PreflightResult, error) {
	if err := ref.Validate(); err != nil {
		return PreflightResult{}, NewProductError(ErrorNotConfigured, "provider.preflight", "Provider profile and model selection are incomplete.", false, err)
	}
	profile, adapter, credential, err := s.resolve(ctx, ProfileID(ref.Provider))
	if err != nil {
		return PreflightResult{}, err
	}
	defer wipeCredential(credential)
	models, err := adapter.ListModels(ctx, profile, credential)
	if err != nil {
		return PreflightResult{}, ClassifyTransportError("provider.preflight", err)
	}
	s.cacheModels(models)
	found := false
	for _, model := range models {
		if modelMatches(model.Ref.Model, ref.Model) {
			found = true
			break
		}
	}
	if !found {
		return PreflightResult{}, NewProductError(ErrorModelNotFound, "provider.preflight", fmt.Sprintf("Model %q is not available for Provider profile %q. Choose an installed or accessible model.", ref.Model, ref.Provider), false, nil)
	}
	validatedAt := time.Now().UTC()
	profile.ValidatedAt = validatedAt
	if err := s.profiles.SaveProfile(ctx, profile); err != nil {
		return PreflightResult{}, NewProductError(ErrorNotConfigured, "provider.preflight", "Provider validation succeeded, but the profile could not be updated.", true, err)
	}
	return PreflightResult{ProfileID: profile.ID, ModelID: ref.Model, ValidatedAt: validatedAt}, nil
}

// ListModels returns normalized model metadata for one configured profile.
func (s *Service) ListModels(ctx context.Context, profileID ProfileID) ([]llm.Model, error) {
	profile, adapter, credential, err := s.resolve(ctx, profileID)
	if err != nil {
		return nil, err
	}
	defer wipeCredential(credential)
	models, err := adapter.ListModels(ctx, profile, credential)
	if err != nil {
		return nil, ClassifyTransportError("provider.list_models", err)
	}
	s.cacheModels(models)
	values := append([]llm.Model(nil), models...)
	sort.Slice(values, func(left, right int) bool {
		if values[left].Ref.Provider == values[right].Ref.Provider {
			return values[left].Ref.Model < values[right].Ref.Model
		}
		return values[left].Ref.Provider < values[right].Ref.Provider
	})
	return values, nil
}

// DescribeModel implements llm.ModelCatalog. Catalog values are cached after
// discovery/preflight so an Agent step does not normally perform network I/O.
func (s *Service) DescribeModel(ctx context.Context, ref llm.ModelRef) (llm.Model, error) {
	if err := ref.Validate(); err != nil {
		return llm.Model{}, err
	}
	s.mu.RLock()
	model, found := s.models[ref]
	s.mu.RUnlock()
	if found {
		return model, nil
	}
	models, err := s.ListModels(ctx, ProfileID(ref.Provider))
	if err != nil {
		return llm.Model{}, err
	}
	for _, candidate := range models {
		if modelMatches(candidate.Ref.Model, ref.Model) {
			candidate.Ref = ref
			s.cacheModels([]llm.Model{candidate})
			return candidate, nil
		}
	}
	return llm.Model{}, NewProductError(ErrorModelNotFound, "provider.describe_model", fmt.Sprintf("Model %q is not available for Provider profile %q.", ref.Model, ref.Provider), false, nil)
}

func (s *Service) cacheModels(models []llm.Model) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, model := range models {
		if model.Ref.Provider != "" && model.Ref.Model != "" {
			s.models[model.Ref] = model
		}
	}
}

func (s *Service) resolve(ctx context.Context, profileID ProfileID) (Profile, Adapter, Credential, error) {
	if err := ctx.Err(); err != nil {
		return Profile{}, nil, nil, err
	}
	if profileID == "" {
		return Profile{}, nil, nil, fmt.Errorf("resolve provider: profile id is required")
	}
	profile, err := s.profiles.LoadProfile(ctx, profileID)
	if err != nil {
		return Profile{}, nil, nil, NewProductError(ErrorNotConfigured, "provider.resolve_profile", fmt.Sprintf("Provider profile %q is unavailable. Configure or select another profile.", profileID), false, err)
	}
	s.mu.RLock()
	adapter, exists := s.adapters[profile.Kind]
	s.mu.RUnlock()
	if !exists {
		return Profile{}, nil, nil, NewProductError(ErrorNotConfigured, "provider.resolve_profile", fmt.Sprintf("Provider type %q is unavailable for profile %q.", profile.Kind, profileID), false, nil)
	}
	if profile.CredentialRef == "" {
		return profile, adapter, nil, nil
	}
	credential, found, err := s.credentials.LoadCredential(ctx, profile.CredentialRef)
	if err != nil {
		message := fmt.Sprintf("Credential for Provider profile %q could not be loaded. Unlock or configure secure credential storage.", profileID)
		return Profile{}, nil, nil, NewProductError(ErrorCredentialMissing, "provider.resolve_credential", message, true, err)
	}
	if !found {
		return Profile{}, nil, nil, NewProductError(ErrorCredentialMissing, "provider.resolve_credential", fmt.Sprintf("Credential for Provider profile %q is missing. Configure an API key.", profileID), false, nil)
	}
	return profile, adapter, append(Credential(nil), credential...), nil
}

func modelMatches(available, requested string) bool {
	available = strings.TrimSpace(available)
	requested = strings.TrimSpace(requested)
	return available == requested || available == requested+":latest" || requested == available+":latest"
}

func wipeCredential(value Credential) {
	for index := range value {
		value[index] = 0
	}
}

func isNilAdapter(value Adapter) bool {
	return isNilInterface(value)
}

func isNilChatModel(value llm.ChatModel) bool {
	return isNilInterface(value)
}

func isNilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
