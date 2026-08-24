// Package deepseek implements the built-in DeepSeek provider adapter.
package deepseek

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	sdk "github.com/cloudwego/eino-ext/components/model/deepseek"
	"github.com/cloudwego/eino/components/model"

	"github.com/eaglc/codepilot/internal/llm"
	"github.com/eaglc/codepilot/internal/provider"
	"github.com/eaglc/codepilot/internal/provider/internal/builtin"
	einoadapter "github.com/eaglc/codepilot/internal/provider/internal/eino"
)

// Defaults returns immutable fallback configuration for the DeepSeek adapter.
func Defaults() builtin.Defaults {
	return builtin.Defaults{BaseURL: "https://api.deepseek.com", ModelID: "deepseek-v4-flash", NeedsCredential: true}
}

// Adapter creates DeepSeek SDK models behind the provider-neutral boundary.
type Adapter struct{ client *http.Client }

// New creates a DeepSeek adapter.
func New(client *http.Client) *Adapter { return &Adapter{client: builtin.HTTPClient(client)} }

// Kind returns provider.KindDeepSeek.
func (*Adapter) Kind() provider.Kind { return provider.KindDeepSeek }

// ListModels discovers models through the authenticated Provider endpoint.
func (a *Adapter) ListModels(ctx context.Context, profile provider.Profile, credential provider.Credential) ([]llm.Model, error) {
	models, err := builtin.ListOpenAICompatibleModels(ctx, a.client, profile, credential, Defaults(), true)
	return builtin.EnrichDeepSeekModels(models), err
}

// CreateModel constructs a DeepSeek-backed normalized chat model.
func (a *Adapter) CreateModel(ctx context.Context, config provider.ModelConfig) (llm.ChatModel, error) {
	baseURL, modelID, err := builtin.ValidateConfig(config, provider.KindDeepSeek, Defaults())
	if err != nil {
		return nil, err
	}
	inner, err := sdk.NewChatModel(ctx, &sdk.ChatModelConfig{APIKey: string(config.Credential), BaseURL: baseURL, Model: modelID, HTTPClient: a.client})
	if err != nil {
		return nil, fmt.Errorf("create DeepSeek SDK model: %w", err)
	}
	return einoadapter.New(inner, llm.ModelRef{Provider: string(config.Profile.ID), Model: modelID}, requestOptions)
}

func requestOptions(request llm.ChatRequest) ([]model.Option, error) {
	var options []model.Option
	if request.MaxOutputTokens > 0 {
		options = append(options, model.WithMaxTokens(request.MaxOutputTokens))
	}
	switch strings.ToLower(request.ThinkingLevel) {
	case "":
	case "none", "off", "disabled":
		options = append(options, sdk.WithExtraFields(map[string]any{"thinking": map[string]string{"type": "disabled"}}))
	case "on", "enabled", "low", "medium", "high":
		options = append(options, sdk.WithExtraFields(map[string]any{"thinking": map[string]string{"type": "enabled"}}))
	default:
		return nil, fmt.Errorf("DeepSeek thinking level %q is unsupported", request.ThinkingLevel)
	}
	return options, nil
}
