package codingagent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ProviderProfile is the secret-free Provider view exposed to product UIs.
// It intentionally does not reuse provider.Profile across the layer boundary.
type ProviderProfile struct {
	ID                   string
	Kind                 string
	DisplayName          string
	BaseURL              string
	DefaultModel         string
	RequiresCredential   bool
	CredentialConfigured bool
	ValidatedAt          time.Time
}

// ProviderModel is a presentation-safe discovered model option.
type ProviderModel struct {
	ID          string
	DisplayName string
	Reasoning   bool
}

// ProviderIssue describes a safe startup validation failure that should open
// the product configuration flow instead of terminating the TUI.
type ProviderIssue struct {
	Code      string
	Message   string
	Retryable bool
}

// ConfigureProviderRequest creates or edits one Profile. Credential is
// transient input and must never be placed in events, snapshots or journals.
type ConfigureProviderRequest struct {
	ID           string
	Kind         string
	DisplayName  string
	BaseURL      string
	DefaultModel string
	Credential   []byte
}

// ProviderManager is implemented at the application boundary and hides the
// Provider package, HTTP adapters and Keyring from Coding Agent and TUI.
type ProviderManager interface {
	ListProfiles(ctx context.Context) ([]ProviderProfile, error)
	ConfigureProfile(ctx context.Context, request ConfigureProviderRequest) (ProviderProfile, error)
	ListModels(ctx context.Context, profileID string) ([]ProviderModel, error)
	ValidateSelection(ctx context.Context, profileID, modelID string) error
}

// ListProviderProfiles returns secret-free configuration choices.
func (s *Service) ListProviderProfiles(ctx context.Context) ([]ProviderProfile, error) {
	if s == nil || s.deps.Providers == nil {
		return nil, errors.New("list Provider profiles: Provider configuration is unavailable")
	}
	return s.deps.Providers.ListProfiles(ctx)
}

// ConfigureProvider creates or edits a Provider while defensively clearing
// the service-owned credential copy after the product use case completes.
func (s *Service) ConfigureProvider(ctx context.Context, request ConfigureProviderRequest) (ProviderProfile, error) {
	if s == nil || s.deps.Providers == nil {
		return ProviderProfile{}, errors.New("configure Provider: Provider configuration is unavailable")
	}
	credential := append([]byte(nil), request.Credential...)
	request.Credential = credential
	defer func() {
		for index := range credential {
			credential[index] = 0
		}
	}()
	return s.deps.Providers.ConfigureProfile(ctx, request)
}

// ListProviderModels discovers available models through the product manager.
func (s *Service) ListProviderModels(ctx context.Context, profileID string) ([]ProviderModel, error) {
	if s == nil || s.deps.Providers == nil {
		return nil, errors.New("list Provider models: Provider configuration is unavailable")
	}
	if strings.TrimSpace(profileID) == "" {
		return nil, errors.New("list Provider models: profile id is required")
	}
	return s.deps.Providers.ListModels(ctx, profileID)
}

// SelectProviderModel validates the exact selection before atomically updating
// the active Coding Session binding.
func (s *Service) SelectProviderModel(ctx context.Context, sessionID SessionID, profileID, modelID string) (Session, error) {
	if s == nil || s.deps.Providers == nil {
		return Session{}, errors.New("select Provider model: Provider configuration is unavailable")
	}
	if sessionID == "" || strings.TrimSpace(profileID) == "" || strings.TrimSpace(modelID) == "" {
		return Session{}, errors.New("select Provider model: session, profile, and model are required")
	}
	operation := s.operationLock(sessionID)
	operation.Lock()
	defer operation.Unlock()
	if err := s.deps.Providers.ValidateSelection(ctx, profileID, modelID); err != nil {
		return Session{}, err
	}
	product, err := s.deps.Sessions.LoadSession(ctx, sessionID)
	if err != nil {
		return Session{}, fmt.Errorf("select Provider model: load session: %w", err)
	}
	product.ProviderProfileID = strings.TrimSpace(profileID)
	product.ModelID = strings.TrimSpace(modelID)
	product.UpdatedAt = time.Now().UTC()
	if err := s.deps.Sessions.SaveSession(ctx, product); err != nil {
		return Session{}, fmt.Errorf("select Provider model: save session: %w", err)
	}
	_ = s.publishSessionEvent(ctx, EventSessionUpdated, product, 0)
	return product, nil
}
