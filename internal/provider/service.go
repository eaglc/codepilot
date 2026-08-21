package provider

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/eaglc/codepilot/internal/session"
)

const (
	credentialLocationNone    = "none"
	credentialLocationMissing = "missing"
)

var _ session.ModelCatalog = (*Service)(nil)

// Service coordinates provider profiles, credentials, validation, and model creation.
type Service struct {
	profiles    ProviderProfileStore
	credentials CredentialStore
	catalog     *Catalog
	idGenerator func() (session.ProviderProfileID, error)
	now         func() time.Time
}

// NewService creates a provider service with explicit adapters.
func NewService(profiles ProviderProfileStore, credentials CredentialStore, adapters []Adapter) (*Service, error) {
	if profiles == nil {
		return nil, errors.New("create provider service: profile store is nil")
	}
	if credentials == nil {
		return nil, errors.New("create provider service: credential store is nil")
	}
	catalog, err := NewCatalog(adapters)
	if err != nil {
		return nil, err
	}

	return &Service{
		profiles:    profiles,
		credentials: credentials,
		catalog:     catalog,
		idGenerator: newProviderProfileID,
		now:         func() time.Time { return time.Now().UTC() },
	}, nil
}

// ListProviderProfiles returns configured profiles without secret material.
func (s *Service) ListProviderProfiles(ctx context.Context) ([]session.ProviderProfile, error) {
	profiles, err := s.profiles.ListProfiles(ctx)
	if err != nil {
		return nil, providerAppError("provider.list_profiles", "Provider profiles could not be loaded.", true, err)
	}
	values := make([]session.ProviderProfile, 0, len(profiles))
	for _, profile := range profiles {
		adapter, err := s.adapterForProfile(profile)
		if err != nil {
			return nil, err
		}
		location, err := s.profileCredentialLocation(ctx, profile, adapter)
		if err != nil {
			return nil, err
		}
		values = append(values, profileView(profile, location))
	}

	return values, nil
}

// ConfigureProvider verifies endpoint and credential access before persisting a
// profile. Model-specific tool calling is validated only after model selection.
func (s *Service) ConfigureProvider(ctx context.Context, request session.ConfigureProviderRequest) (session.ProviderProfile, error) {
	adapter, profile, secret, err := s.prepareConfiguration(request)
	if err != nil {
		return session.ProviderProfile{}, err
	}
	defer wipeSecret(secret)

	baseURL := effectiveBaseURL(profile, adapter)
	// The configured ModelID is only a bootstrap default at this stage. Listing
	// the catalog checks endpoint and credential access without spending model
	// tokens or claiming that an unselected model has passed capability checks.
	models, err := adapter.ListModels(ctx, ModelListRequest{
		BaseURL: baseURL,
		ModelID: profile.ModelID,
		Secret:  secret,
	})
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return session.ProviderProfile{}, contextErr
		}
		validation := validationFromError(err)
		return session.ProviderProfile{}, &session.AppError{
			Code:        session.ErrProviderUnavailable,
			Operation:   "provider.configure",
			UserMessage: validation.UserMessage,
			Retryable:   validation.Retryable,
			Cause:       err,
		}
	}
	if len(models) == 0 {
		return session.ProviderProfile{}, &session.AppError{
			Code:        session.ErrProviderUnavailable,
			Operation:   "provider.configure",
			UserMessage: "The provider returned no available models.",
			Retryable:   true,
		}
	}

	profileID, err := s.idGenerator()
	if err != nil {
		return session.ProviderProfile{}, providerAppError("provider.configure", "Provider profile ID could not be created.", true, err)
	}
	profile.ID = profileID
	profile.ValidatedAt = s.now().UTC()
	location := CredentialLocation(credentialLocationNone)
	credentialWritten := false
	if adapter.Defaults().NeedsCredential {
		location, err = s.credentials.Put(ctx, profile.ID, secret)
		if err != nil {
			return session.ProviderProfile{}, providerAppError("provider.configure", "Provider credential could not be saved.", true, err)
		}
		credentialWritten = true
		if location == CredentialInKeyring {
			profile.CredentialRef = "keyring:provider/" + string(profile.ID)
		}
	}

	if err := s.profiles.SaveProfile(ctx, profile); err != nil {
		if credentialWritten {
			cleanupErr := s.credentials.Delete(context.Background(), profile.ID)
			if cleanupErr != nil {
				return session.ProviderProfile{}, providerAppError(
					"provider.configure",
					"Provider profile could not be saved, and its credential may require manual cleanup.",
					true,
					errors.Join(err, cleanupErr),
				)
			}
		}
		return session.ProviderProfile{}, providerAppError("provider.configure", "Provider profile could not be saved.", true, err)
	}

	return profileView(profile, string(location)), nil
}

// ListModels returns bounded model options for one configured profile.
func (s *Service) ListModels(ctx context.Context, profileID session.ProviderProfileID) ([]session.ModelOption, error) {
	profile, adapter, secret, err := s.loadProfileContext(ctx, profileID)
	if err != nil {
		return nil, err
	}
	defer wipeSecret(secret)

	models, err := adapter.ListModels(ctx, ModelListRequest{
		BaseURL: effectiveBaseURL(profile, adapter),
		ModelID: profile.ModelID,
		Secret:  secret,
	})
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return nil, contextErr
		}
		return nil, providerAppError("provider.list_models", "Provider models could not be loaded.", true, err)
	}
	values := make([]session.ModelOption, 0, len(models))
	for _, value := range models {
		values = append(values, session.ModelOption{
			ID:          value.ID,
			DisplayName: value.DisplayName,
			Recommended: value.Recommended,
			Source:      string(value.Source),
		})
	}

	return values, nil
}

// ValidateSelection verifies credential, model access, and tool calling before a switch.
func (s *Service) ValidateSelection(ctx context.Context, selection session.ModelSelection) (session.ModelValidation, error) {
	if selection.ProviderProfileID == "" || strings.TrimSpace(selection.ModelID) == "" {
		return session.ModelValidation{UserMessage: "A provider profile and model must be selected."}, nil
	}
	profile, adapter, secret, err := s.loadProfileContext(ctx, selection.ProviderProfileID)
	if err != nil {
		var appError *session.AppError
		if errors.As(err, &appError) && (appError.Code == session.ErrNotFound || appError.Code == session.ErrProviderUnavailable) {
			return session.ModelValidation{
				UserMessage: appError.Error(),
				Retryable:   appError.Retryable,
			}, nil
		}
		return session.ModelValidation{}, err
	}
	defer wipeSecret(secret)

	validation, err := adapter.Validate(ctx, ValidationRequest{
		BaseURL: effectiveBaseURL(profile, adapter),
		ModelID: selection.ModelID,
		Secret:  secret,
	})
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return session.ModelValidation{}, contextErr
		}
		return session.ModelValidation{}, providerAppError("provider.validate_selection", "Provider validation could not be completed.", true, err)
	}
	if validation.Valid {
		// A profile's model is the last model that passed the capability probe,
		// not the bootstrap default used before the user made a selection.
		profile.ModelID = strings.TrimSpace(selection.ModelID)
		profile.ValidatedAt = s.now().UTC()
		if err := s.profiles.SaveProfile(ctx, profile); err != nil {
			return session.ModelValidation{}, providerAppError("provider.validate_selection", "The validated model selection could not be saved.", true, err)
		}
	}

	return session.ModelValidation{
		Valid:       validation.Valid,
		UserMessage: validation.UserMessage,
		Retryable:   validation.Retryable,
	}, nil
}

// NewChatModel builds the Eino chat model for one validated profile and model.
func (s *Service) NewChatModel(ctx context.Context, modelRef ModelRef) (model.ToolCallingChatModel, error) {
	profileID := session.ProviderProfileID(modelRef.Provider)
	if profileID == "" || strings.TrimSpace(modelRef.Model) == "" {
		return nil, providerInputError("provider.new_chat_model", "Provider profile and model are required.")
	}
	profile, adapter, secret, err := s.loadProfileContext(ctx, profileID)
	if err != nil {
		return nil, err
	}
	defer wipeSecret(secret)

	chatModel, err := adapter.NewChatModel(ctx, ChatModelRequest{
		BaseURL: effectiveBaseURL(profile, adapter),
		ModelID: modelRef.Model,
		Secret:  secret,
	})
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return nil, contextErr
		}
		return nil, providerAppError("provider.new_chat_model", "Chat model could not be created.", true, err)
	}

	return chatModel, nil
}

func (s *Service) prepareConfiguration(request session.ConfigureProviderRequest) (Adapter, Profile, Secret, error) {
	kind := Kind(strings.TrimSpace(request.Kind))
	adapter, exists := s.catalog.Lookup(kind)
	if !exists {
		return nil, Profile{}, nil, providerInputError("provider.configure", "Provider kind is unsupported.")
	}
	defaults := adapter.Defaults()
	displayName := strings.TrimSpace(request.DisplayName)
	if displayName == "" {
		displayName = defaults.DisplayName
	}
	modelID := strings.TrimSpace(request.ModelID)
	if modelID == "" {
		modelID = defaults.ModelID
	}
	baseURL := strings.TrimSpace(request.BaseURL)
	switch kind {
	case KindOpenAI, KindDeepSeek:
		if baseURL != "" {
			return nil, Profile{}, nil, providerInputError("provider.configure", "Built-in provider base URL cannot be changed.")
		}
	case KindOllama:
		if baseURL == "" {
			baseURL = defaults.BaseURL
		}
	case KindOpenAICompatible:
		if baseURL == "" || modelID == "" {
			return nil, Profile{}, nil, providerInputError("provider.configure", "Custom provider base URL and model are required.")
		}
	}
	secret := append(Secret(nil), request.CredentialInput...)
	if defaults.NeedsCredential && len(secret) == 0 {
		return nil, Profile{}, nil, providerInputError("provider.configure", "A provider API key is required.")
	}

	profile := Profile{
		Kind:        kind,
		DisplayName: displayName,
		ModelID:     modelID,
	}
	if kind == KindOllama || kind == KindOpenAICompatible {
		profile.BaseURL = baseURL
	}

	return adapter, profile, secret, nil
}

func (s *Service) loadProfileContext(ctx context.Context, id session.ProviderProfileID) (Profile, Adapter, Secret, error) {
	profile, err := s.profiles.LoadProfile(ctx, id)
	if err != nil {
		return Profile{}, nil, nil, err
	}
	adapter, err := s.adapterForProfile(profile)
	if err != nil {
		return Profile{}, nil, nil, err
	}
	if !adapter.Defaults().NeedsCredential {
		return profile, adapter, nil, nil
	}
	secret, found, err := s.credentials.Get(ctx, id)
	if err != nil {
		return Profile{}, nil, nil, providerAppError("provider.load_credential", "Provider credential could not be loaded.", true, err)
	}
	if !found {
		return Profile{}, nil, nil, &session.AppError{
			Code:        session.ErrProviderUnavailable,
			Operation:   "provider.load_credential",
			UserMessage: "Provider credential is missing. Reconfigure this profile.",
		}
	}

	return profile, adapter, secret, nil
}

func (s *Service) adapterForProfile(profile Profile) (Adapter, error) {
	adapter, exists := s.catalog.Lookup(profile.Kind)
	if !exists {
		return nil, providerAppError("provider.lookup_adapter", "Provider adapter is unavailable.", false, nil)
	}
	return adapter, nil
}

func (s *Service) profileCredentialLocation(ctx context.Context, profile Profile, adapter Adapter) (string, error) {
	if !adapter.Defaults().NeedsCredential {
		return credentialLocationNone, nil
	}
	if profile.CredentialRef != "" {
		return string(CredentialInKeyring), nil
	}
	secret, found, err := s.credentials.Get(ctx, profile.ID)
	if err != nil {
		return "", providerAppError("provider.list_profiles", "Provider credential status could not be loaded.", true, err)
	}
	wipeSecret(secret)
	if found {
		return string(CredentialInMemory), nil
	}
	return credentialLocationMissing, nil
}

func effectiveBaseURL(profile Profile, adapter Adapter) string {
	if profile.BaseURL != "" {
		return profile.BaseURL
	}
	return adapter.Defaults().BaseURL
}

func profileView(profile Profile, credentialLocation string) session.ProviderProfile {
	return session.ProviderProfile{
		ID:                 profile.ID,
		Kind:               string(profile.Kind),
		DisplayName:        profile.DisplayName,
		BaseURL:            profile.BaseURL,
		ModelID:            profile.ModelID,
		CredentialRef:      profile.CredentialRef,
		CredentialLocation: credentialLocation,
		ValidatedAt:        profile.ValidatedAt,
	}
}

func newProviderProfileID() (session.ProviderProfileID, error) {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("generate provider profile ID: %w", err)
	}
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(random)
	return session.ProviderProfileID("prv_" + strings.ToLower(encoded)), nil
}

func wipeSecret(secret Secret) {
	for index := range secret {
		secret[index] = 0
	}
}

func providerAppError(operation string, userMessage string, retryable bool, cause error) error {
	return &session.AppError{
		Code:        session.ErrProviderUnavailable,
		Operation:   operation,
		UserMessage: userMessage,
		Cause:       cause,
		Retryable:   retryable,
	}
}

func providerInputError(operation string, userMessage string) error {
	return &session.AppError{
		Code:        session.ErrInvalidInput,
		Operation:   operation,
		UserMessage: userMessage,
	}
}
