package provider

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/eaglc/codepilot/internal/session"
)

// Kind identifies one supported provider adapter.
type Kind string

const (
	// KindOpenAI selects the built-in OpenAI adapter.
	KindOpenAI Kind = "openai"
	// KindDeepSeek selects the built-in DeepSeek adapter.
	KindDeepSeek Kind = "deepseek"
	// KindOllama selects the local Ollama adapter.
	KindOllama Kind = "ollama"
	// KindOpenAICompatible selects a custom OpenAI-compatible endpoint.
	KindOpenAICompatible Kind = "openai-compatible"
)

// Profile is the secret-free persisted configuration for one provider.
type Profile struct {
	ID            session.ProviderProfileID
	Kind          Kind
	DisplayName   string
	BaseURL       string
	ModelID       string
	CredentialRef string
	ValidatedAt   time.Time
}

// Defaults describes the built-in configuration for one provider kind.
type Defaults struct {
	DisplayName     string
	BaseURL         string
	ModelID         string
	NeedsCredential bool
}

// ValidationStage identifies which provider capability failed.
type ValidationStage string

const (
	// ValidationStageAuthentication indicates a missing or rejected credential.
	ValidationStageAuthentication ValidationStage = "authentication"
	// ValidationStageModel indicates that the requested model is unavailable.
	ValidationStageModel ValidationStage = "model"
	// ValidationStageToolCalling indicates that the model did not call the probe tool.
	ValidationStageToolCalling ValidationStage = "tool-calling"
	// ValidationStageNetwork indicates that the endpoint could not be reached reliably.
	ValidationStageNetwork ValidationStage = "network"
)

// ValidationRequest contains short-lived settings used for a provider probe.
type ValidationRequest struct {
	BaseURL string
	ModelID string
	Secret  Secret
}

// ValidationResult is the structured, secret-free result of a provider probe.
type ValidationResult struct {
	Valid       bool
	Stage       ValidationStage
	UserMessage string
	Retryable   bool
}

// ModelSource describes where an enumerated model came from.
type ModelSource string

const (
	// ModelSourceRemote indicates that the provider enumerated the model.
	ModelSourceRemote ModelSource = "remote"
	// ModelSourceCatalog indicates a built-in recommended model.
	ModelSourceCatalog ModelSource = "catalog"
	// ModelSourceConfigured indicates a configured fallback when enumeration is unsupported.
	ModelSourceConfigured ModelSource = "configured"
)

// Model is one secret-free model choice returned by an Adapter.
type Model struct {
	ID          string
	DisplayName string
	Recommended bool
	Source      ModelSource
}

// ModelListRequest contains the endpoint and temporary credential for enumeration.
type ModelListRequest struct {
	BaseURL string
	ModelID string
	Secret  Secret
}

// ChatModelRequest contains the settings needed to create one Eino model.
type ChatModelRequest struct {
	BaseURL string
	ModelID string
	Secret  Secret
}

// Adapter is the explicit provider-specific variation boundary.
type Adapter interface {
	Kind() Kind
	Defaults() Defaults
	Validate(ctx context.Context, request ValidationRequest) (ValidationResult, error)
	ListModels(ctx context.Context, request ModelListRequest) ([]Model, error)
	NewChatModel(ctx context.Context, request ChatModelRequest) (model.ToolCallingChatModel, error)
}

// Valid reports whether kind is part of the MVP provider catalog.
func (k Kind) Valid() bool {
	switch k {
	case KindOpenAI, KindDeepSeek, KindOllama, KindOpenAICompatible:
		return true
	default:
		return false
	}
}

// ValidateProfile checks the provider-file invariants that do not require a
// network request or a credential lookup.
func ValidateProfile(value Profile) error {
	if strings.TrimSpace(string(value.ID)) == "" {
		return newProfileError("provider profile ID is empty")
	}
	if !value.Kind.Valid() {
		return newProfileError("provider kind is unsupported")
	}
	if strings.TrimSpace(value.DisplayName) == "" {
		return newProfileError("provider display name is empty")
	}
	if strings.TrimSpace(value.ModelID) == "" {
		return newProfileError("provider model ID is empty")
	}
	if value.Kind == KindOpenAICompatible {
		if err := validateBaseURL(value.BaseURL); err != nil {
			return err
		}
	}
	if value.Kind == KindOpenAI || value.Kind == KindDeepSeek {
		if strings.TrimSpace(value.BaseURL) != "" {
			return newProfileError("built-in provider base URL must not be persisted")
		}
	}
	if value.ValidatedAt.IsZero() {
		return newProfileError("provider validation time is empty")
	}

	return nil
}

func validateBaseURL(value string) error {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return newProfileError("custom provider base URL must be an absolute HTTP URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return newProfileError("custom provider base URL must not contain credentials, query parameters, or a fragment")
	}

	return nil
}

type profileError string

func (e profileError) Error() string {
	return string(e)
}

func newProfileError(message string) error {
	return profileError(message)
}
