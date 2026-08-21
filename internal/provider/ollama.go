package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	ollama "github.com/cloudwego/eino-ext/components/model/ollama"
	"github.com/cloudwego/eino/components/model"
)

const (
	ollamaBaseURL     = "http://127.0.0.1:11434"
	ollamaRecommended = "qwen-coder"
)

// OllamaAdapter implements the local Ollama provider.
type OllamaAdapter struct {
	client *http.Client
}

// NewOllamaAdapter creates an Ollama adapter.
func NewOllamaAdapter(client *http.Client) *OllamaAdapter {
	return &OllamaAdapter{client: providerHTTPClient(client)}
}

// Kind returns KindOllama.
func (a *OllamaAdapter) Kind() Kind {
	return KindOllama
}

// Defaults returns the local Ollama base URL and recommended model.
func (a *OllamaAdapter) Defaults() Defaults {
	return Defaults{
		DisplayName: "Ollama",
		BaseURL:     ollamaBaseURL,
		ModelID:     ollamaRecommended,
	}
}

// Validate probes endpoint and tool-calling support for the requested local model.
func (a *OllamaAdapter) Validate(ctx context.Context, request ValidationRequest) (ValidationResult, error) {
	if result := validateAdapterRequest(request.BaseURL, request.ModelID, nil, false); !result.Valid {
		return result, nil
	}
	chatModel, err := a.NewChatModel(ctx, ChatModelRequest{BaseURL: request.BaseURL, ModelID: request.ModelID})
	if err != nil {
		return validationFromError(err), nil
	}
	return probeToolCalling(ctx, chatModel)
}

// ListModels lists the models available from the local Ollama server.
func (a *OllamaAdapter) ListModels(ctx context.Context, request ModelListRequest) ([]Model, error) {
	if err := validateModelListRequest(ModelListRequest{BaseURL: request.BaseURL, ModelID: request.ModelID}, false); err != nil {
		return nil, err
	}
	parsed, err := parseProviderBaseURL(request.BaseURL)
	if err != nil {
		return nil, err
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/api/tags"
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create Ollama model request: %w", err)
	}
	response, err := a.client.Do(httpRequest)
	if err != nil {
		return nil, &adapterOperationError{operation: "list Ollama models", cause: err}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, providerResponseMaxBytes))
		return nil, &providerEndpointError{operation: "list Ollama models", status: response.StatusCode}
	}

	var payload struct {
		Models []struct {
			Name  string `json:"name"`
			Model string `json:"model"`
		} `json:"models"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, providerResponseMaxBytes)).Decode(&payload); err != nil {
		return nil, &adapterOperationError{operation: "decode Ollama models", cause: err}
	}
	models := make([]Model, 0, min(len(payload.Models), providerModelLimit))
	seen := make(map[string]struct{}, len(payload.Models))
	for _, item := range payload.Models {
		id := strings.TrimSpace(item.Model)
		if id == "" {
			id = strings.TrimSpace(item.Name)
		}
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		models = append(models, Model{ID: id, DisplayName: id, Source: ModelSourceRemote})
		if len(models) == providerModelLimit {
			break
		}
	}
	sort.Slice(models, func(left int, right int) bool {
		return models[left].ID < models[right].ID
	})
	return markRecommendedModel(models, request.ModelID), nil
}

// NewChatModel creates an Ollama chat model for the validated request.
func (a *OllamaAdapter) NewChatModel(ctx context.Context, request ChatModelRequest) (model.ToolCallingChatModel, error) {
	if result := validateAdapterRequest(request.BaseURL, request.ModelID, nil, false); !result.Valid {
		return nil, &adapterOperationError{operation: "create Ollama model", cause: nil}
	}
	chatModel, err := ollama.NewChatModel(ctx, &ollama.ChatModelConfig{
		BaseURL:    request.BaseURL,
		Model:      request.ModelID,
		HTTPClient: a.client,
	})
	if err != nil {
		return nil, &adapterOperationError{operation: "create Ollama model", cause: err}
	}
	return chatModel, nil
}
