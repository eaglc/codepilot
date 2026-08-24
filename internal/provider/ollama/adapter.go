// Package ollama implements the built-in local Ollama provider adapter.
package ollama

import (
	"context"
	"fmt"
	"net/http"

	sdk "github.com/cloudwego/eino-ext/components/model/ollama"
	"github.com/cloudwego/eino/components/model"

	"github.com/eaglc/codepilot/internal/llm"
	"github.com/eaglc/codepilot/internal/provider"
	"github.com/eaglc/codepilot/internal/provider/internal/builtin"
	einoadapter "github.com/eaglc/codepilot/internal/provider/internal/eino"
)

// Defaults returns immutable fallback configuration for the Ollama adapter.
func Defaults() builtin.Defaults {
	return builtin.Defaults{BaseURL: "http://127.0.0.1:11434", ModelID: "qwen-coder"}
}

// Adapter creates Ollama SDK models behind the provider-neutral boundary.
type Adapter struct{ client *http.Client }

// New creates an Ollama adapter.
func New(client *http.Client) *Adapter { return &Adapter{client: builtin.HTTPClient(client)} }

// Kind returns provider.KindOllama.
func (*Adapter) Kind() provider.Kind { return provider.KindOllama }

// ListModels checks Ollama and returns its installed local models.
func (a *Adapter) ListModels(ctx context.Context, profile provider.Profile, _ provider.Credential) ([]llm.Model, error) {
	return builtin.ListOllamaModels(ctx, a.client, profile, Defaults())
}

// CreateModel constructs an Ollama-backed normalized chat model.
func (a *Adapter) CreateModel(ctx context.Context, config provider.ModelConfig) (llm.ChatModel, error) {
	baseURL, modelID, err := builtin.ValidateConfig(config, provider.KindOllama, Defaults())
	if err != nil {
		return nil, err
	}
	inner, err := sdk.NewChatModel(ctx, &sdk.ChatModelConfig{BaseURL: baseURL, Model: modelID, HTTPClient: a.client})
	if err != nil {
		return nil, fmt.Errorf("create Ollama SDK model: %w", err)
	}
	return einoadapter.New(inner, llm.ModelRef{Provider: string(config.Profile.ID), Model: modelID}, requestOptions)
}

func requestOptions(request llm.ChatRequest) ([]model.Option, error) {
	if request.ThinkingLevel != "" {
		return nil, fmt.Errorf("Ollama thinking level %q must be configured on the provider profile", request.ThinkingLevel)
	}
	if request.MaxOutputTokens > 0 {
		return []model.Option{model.WithMaxTokens(request.MaxOutputTokens)}, nil
	}
	return nil, nil
}
