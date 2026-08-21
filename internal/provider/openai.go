package provider

import (
	"context"
	"net/http"

	openai "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
)

const (
	openAIBaseURL     = "https://api.openai.com/v1"
	openAIRecommended = "gpt-5.6-sol"
)

// OpenAIAdapter implements the built-in OpenAI provider.
type OpenAIAdapter struct {
	client *http.Client
}

// NewOpenAIAdapter creates an OpenAI adapter.
func NewOpenAIAdapter(client *http.Client) *OpenAIAdapter {
	return &OpenAIAdapter{client: providerHTTPClient(client)}
}

// Kind returns KindOpenAI.
func (a *OpenAIAdapter) Kind() Kind {
	return KindOpenAI
}

// Defaults returns the OpenAI base URL, recommended model, and credential requirement.
func (a *OpenAIAdapter) Defaults() Defaults {
	return Defaults{
		DisplayName:     "OpenAI",
		BaseURL:         openAIBaseURL,
		ModelID:         openAIRecommended,
		NeedsCredential: true,
	}
}

// Validate probes endpoint, credential, and tool-calling support for the requested model.
func (a *OpenAIAdapter) Validate(ctx context.Context, request ValidationRequest) (ValidationResult, error) {
	if result := validateAdapterRequest(request.BaseURL, request.ModelID, request.Secret, true); !result.Valid {
		return result, nil
	}
	chatModel, err := a.NewChatModel(ctx, ChatModelRequest(request))
	if err != nil {
		return validationFromError(err), nil
	}
	return probeToolCalling(ctx, chatModel)
}

// ListModels lists the OpenAI account's available models, marking the configured one as recommended.
func (a *OpenAIAdapter) ListModels(ctx context.Context, request ModelListRequest) ([]Model, error) {
	if err := validateModelListRequest(request, true); err != nil {
		return nil, err
	}
	models, err := listOpenAIModels(ctx, a.client, request.BaseURL, request.Secret)
	if err != nil {
		return nil, err
	}
	return markRecommendedModel(models, request.ModelID), nil
}

// NewChatModel creates an OpenAI chat model for the validated request.
func (a *OpenAIAdapter) NewChatModel(ctx context.Context, request ChatModelRequest) (model.ToolCallingChatModel, error) {
	if result := validateAdapterRequest(request.BaseURL, request.ModelID, request.Secret, true); !result.Valid {
		return nil, &adapterOperationError{operation: "create OpenAI model", cause: nil}
	}
	chatModel, err := openai.NewChatModel(ctx, &openai.ChatModelConfig{
		APIKey:     string(request.Secret),
		BaseURL:    request.BaseURL,
		Model:      request.ModelID,
		HTTPClient: a.client,
	})
	if err != nil {
		return nil, &adapterOperationError{operation: "create OpenAI model", cause: err}
	}
	return chatModel, nil
}
