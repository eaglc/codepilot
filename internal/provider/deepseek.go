package provider

import (
	"context"
	"net/http"

	deepseek "github.com/cloudwego/eino-ext/components/model/deepseek"
	"github.com/cloudwego/eino/components/model"
)

const (
	deepSeekBaseURL     = "https://api.deepseek.com"
	deepSeekRecommended = "deepseek-v4-flash"
)

// DeepSeekAdapter implements the built-in DeepSeek provider.
type DeepSeekAdapter struct {
	client *http.Client
}

// NewDeepSeekAdapter creates a DeepSeek adapter.
func NewDeepSeekAdapter(client *http.Client) *DeepSeekAdapter {
	return &DeepSeekAdapter{client: providerHTTPClient(client)}
}

// Kind returns KindDeepSeek.
func (a *DeepSeekAdapter) Kind() Kind {
	return KindDeepSeek
}

// Defaults returns the DeepSeek base URL, recommended model, and credential requirement.
func (a *DeepSeekAdapter) Defaults() Defaults {
	return Defaults{
		DisplayName:     "DeepSeek",
		BaseURL:         deepSeekBaseURL,
		ModelID:         deepSeekRecommended,
		NeedsCredential: true,
	}
}

// Validate probes endpoint, credential, and tool-calling support for the requested model.
func (a *DeepSeekAdapter) Validate(ctx context.Context, request ValidationRequest) (ValidationResult, error) {
	if result := validateAdapterRequest(request.BaseURL, request.ModelID, request.Secret, true); !result.Valid {
		return result, nil
	}
	chatModel, err := a.NewChatModel(ctx, ChatModelRequest(request))
	if err != nil {
		return validationFromError(err), nil
	}
	// DeepSeek V4 defaults to thinking mode, where the API rejects tool_choice
	// even though the model supports tool calls. The single registered tool and
	// explicit prompt still verify the capability without disabling reasoning.
	return probeToolCallingWithoutChoice(ctx, chatModel)
}

// ListModels lists the DeepSeek account's available models, marking the configured one as recommended.
func (a *DeepSeekAdapter) ListModels(ctx context.Context, request ModelListRequest) ([]Model, error) {
	if err := validateModelListRequest(request, true); err != nil {
		return nil, err
	}
	models, err := listOpenAIModels(ctx, a.client, request.BaseURL, request.Secret)
	if err != nil {
		return nil, err
	}
	return markRecommendedModel(models, request.ModelID), nil
}

// NewChatModel creates a DeepSeek chat model for the validated request.
func (a *DeepSeekAdapter) NewChatModel(ctx context.Context, request ChatModelRequest) (model.ToolCallingChatModel, error) {
	if result := validateAdapterRequest(request.BaseURL, request.ModelID, request.Secret, true); !result.Valid {
		return nil, &adapterOperationError{operation: "create DeepSeek model", cause: nil}
	}
	chatModel, err := deepseek.NewChatModel(ctx, &deepseek.ChatModelConfig{
		APIKey:     string(request.Secret),
		BaseURL:    request.BaseURL,
		Model:      request.ModelID,
		HTTPClient: a.client,
	})
	if err != nil {
		return nil, &adapterOperationError{operation: "create DeepSeek model", cause: err}
	}
	return chatModel, nil
}
