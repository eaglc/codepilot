// Package openai implements the built-in OpenAI provider adapter.
package openai

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	sdk "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"

	"github.com/eaglc/codepilot/internal/llm"
	"github.com/eaglc/codepilot/internal/provider"
	"github.com/eaglc/codepilot/internal/provider/internal/builtin"
	einoadapter "github.com/eaglc/codepilot/internal/provider/internal/eino"
)

// Defaults returns immutable fallback configuration for the OpenAI adapter.
func Defaults() builtin.Defaults {
	return builtin.Defaults{BaseURL: "https://api.openai.com/v1", ModelID: "gpt-5.6-sol", NeedsCredential: true}
}

// Adapter creates OpenAI SDK models behind the provider-neutral boundary.
type Adapter struct{ client *http.Client }

// New creates an OpenAI adapter.
func New(client *http.Client) *Adapter { return &Adapter{client: builtin.HTTPClient(client)} }

// Kind returns provider.KindOpenAI.
func (*Adapter) Kind() provider.Kind { return provider.KindOpenAI }

// ListModels discovers models through the authenticated Provider endpoint.
func (a *Adapter) ListModels(ctx context.Context, profile provider.Profile, credential provider.Credential) ([]llm.Model, error) {
	models, err := builtin.ListOpenAICompatibleModels(ctx, a.client, profile, credential, Defaults(), true)
	return builtin.EnrichOpenAIModels(models), err
}

// CreateModel constructs an OpenAI-backed normalized chat model.
func (a *Adapter) CreateModel(ctx context.Context, config provider.ModelConfig) (llm.ChatModel, error) {
	baseURL, modelID, err := builtin.ValidateConfig(config, provider.KindOpenAI, Defaults())
	if err != nil {
		return nil, err
	}
	inner, err := sdk.NewChatModel(ctx, &sdk.ChatModelConfig{APIKey: string(config.Credential), BaseURL: baseURL, Model: modelID, HTTPClient: a.client})
	if err != nil {
		return nil, fmt.Errorf("create OpenAI SDK model: %w", err)
	}
	return einoadapter.New(inner, llm.ModelRef{Provider: string(config.Profile.ID), Model: modelID}, requestOptions)
}

func requestOptions(request llm.ChatRequest) ([]model.Option, error) {
	var options []model.Option
	if request.MaxOutputTokens > 0 {
		options = append(options, sdk.WithMaxCompletionTokens(request.MaxOutputTokens))
	}
	switch strings.ToLower(request.ThinkingLevel) {
	case "":
	case "low":
		options = append(options, sdk.WithReasoningEffort(sdk.ReasoningEffortLevelLow))
	case "medium":
		options = append(options, sdk.WithReasoningEffort(sdk.ReasoningEffortLevelMedium))
	case "high":
		options = append(options, sdk.WithReasoningEffort(sdk.ReasoningEffortLevelHigh))
	default:
		return nil, fmt.Errorf("OpenAI thinking level %q is unsupported", request.ThinkingLevel)
	}
	return options, nil
}
