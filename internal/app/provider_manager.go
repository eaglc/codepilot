package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/eaglc/codepilot/internal/codingagent"
	"github.com/eaglc/codepilot/internal/llm"
	"github.com/eaglc/codepilot/internal/provider"
)

type productProviderManager struct {
	profiles provider.ProfileRepository
	service  *provider.Service
}

func (m *productProviderManager) ListProfiles(ctx context.Context) ([]codingagent.ProviderProfile, error) {
	profiles, err := m.profiles.ListProfiles(ctx)
	if err != nil {
		return nil, fmt.Errorf("list Provider profiles: %w", err)
	}
	values := make([]codingagent.ProviderProfile, 0, len(profiles))
	for _, profile := range profiles {
		required, configured, statusErr := m.service.CredentialStatus(ctx, profile.ID)
		if statusErr != nil {
			required = profile.CredentialRef != ""
			configured = false
		}
		values = append(values, productProviderProfile(profile, required, configured))
	}
	return values, nil
}

func (m *productProviderManager) ConfigureProfile(ctx context.Context, request codingagent.ConfigureProviderRequest) (codingagent.ProviderProfile, error) {
	kind := provider.Kind(strings.ToLower(strings.TrimSpace(request.Kind)))
	if kind != provider.KindOpenAI && kind != provider.KindDeepSeek && kind != provider.KindOllama {
		return codingagent.ProviderProfile{}, errors.New("configure Provider profile: type must be openai, deepseek, or ollama")
	}
	id := provider.ProfileID(strings.TrimSpace(request.ID))
	var existing provider.Profile
	if id == "" {
		generated, err := newProviderProfileID()
		if err != nil {
			return codingagent.ProviderProfile{}, err
		}
		id = generated
	} else {
		profiles, err := m.profiles.ListProfiles(ctx)
		if err != nil {
			return codingagent.ProviderProfile{}, err
		}
		for _, candidate := range profiles {
			if candidate.ID == id {
				existing = candidate
				break
			}
		}
		if existing.ID != "" && existing.Kind != kind {
			return codingagent.ProviderProfile{}, errors.New("configure Provider profile: type cannot be changed after creation")
		}
	}
	displayName := strings.TrimSpace(request.DisplayName)
	if displayName == "" {
		displayName = defaultProviderDisplayName(kind)
	}
	defaultModel := strings.TrimSpace(request.DefaultModel)
	if defaultModel == "" {
		defaultModel = defaultProviderModel(kind)
	}
	credentialRef := existing.CredentialRef
	if kind == provider.KindOllama {
		credentialRef = ""
	} else if credentialRef == "" {
		credentialRef = string(id)
	}
	profile := provider.Profile{
		ID: id, Kind: kind, DisplayName: displayName, BaseURL: strings.TrimSpace(request.BaseURL),
		DefaultModel: defaultModel, CredentialRef: credentialRef, ValidatedAt: existing.ValidatedAt,
	}
	if err := m.profiles.SaveProfile(ctx, profile); err != nil {
		return codingagent.ProviderProfile{}, err
	}
	if len(request.Credential) != 0 {
		if credentialRef == "" {
			return codingagent.ProviderProfile{}, errors.New("configure Provider profile: Ollama does not accept an API key")
		}
		if err := m.service.SaveCredential(ctx, credentialRef, provider.Credential(request.Credential)); err != nil {
			return codingagent.ProviderProfile{}, err
		}
	}
	required, configured, err := m.service.CredentialStatus(ctx, profile.ID)
	if err != nil {
		return codingagent.ProviderProfile{}, err
	}
	return productProviderProfile(profile, required, configured), nil
}

func (m *productProviderManager) ListModels(ctx context.Context, profileID string) ([]codingagent.ProviderModel, error) {
	requestContext, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	models, err := m.service.ListModels(requestContext, provider.ProfileID(strings.TrimSpace(profileID)))
	if err != nil {
		return nil, err
	}
	values := make([]codingagent.ProviderModel, len(models))
	for index, model := range models {
		values[index] = codingagent.ProviderModel{ID: model.Ref.Model, DisplayName: model.DisplayName, Reasoning: model.Reasoning}
	}
	return values, nil
}

func (m *productProviderManager) ValidateSelection(ctx context.Context, profileID, modelID string) error {
	requestContext, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	_, err := m.service.Preflight(requestContext, llm.ModelRef{Provider: strings.TrimSpace(profileID), Model: strings.TrimSpace(modelID)})
	return err
}

func productProviderProfile(profile provider.Profile, required, configured bool) codingagent.ProviderProfile {
	return codingagent.ProviderProfile{
		ID: string(profile.ID), Kind: string(profile.Kind), DisplayName: profile.DisplayName, BaseURL: profile.BaseURL,
		DefaultModel: profile.DefaultModel, RequiresCredential: required, CredentialConfigured: configured, ValidatedAt: profile.ValidatedAt,
	}
}

func defaultProviderDisplayName(kind provider.Kind) string {
	switch kind {
	case provider.KindOpenAI:
		return "OpenAI"
	case provider.KindDeepSeek:
		return "DeepSeek"
	default:
		return "Ollama"
	}
}

func defaultProviderModel(kind provider.Kind) string {
	switch kind {
	case provider.KindOpenAI:
		return "gpt-5.6-sol"
	case provider.KindDeepSeek:
		return "deepseek-v4-flash"
	default:
		return "qwen-coder"
	}
}

func newProviderProfileID() (provider.ProfileID, error) {
	value := make([]byte, 12)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate Provider profile id: %w", err)
	}
	return provider.ProfileID("profile_" + hex.EncodeToString(value)), nil
}

var _ codingagent.ProviderManager = (*productProviderManager)(nil)
