package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	openai "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

const (
	providerProbeToolName    = "codepilot_provider_probe"
	providerResponseMaxBytes = 1 << 20
	providerModelLimit       = 200
)

// CompatibleAdapter implements a custom OpenAI-compatible endpoint.
type CompatibleAdapter struct {
	client *http.Client
}

// NewCompatibleAdapter creates an OpenAI-compatible adapter.
func NewCompatibleAdapter(client *http.Client) *CompatibleAdapter {
	return &CompatibleAdapter{client: providerHTTPClient(client)}
}

// Kind returns KindOpenAICompatible.
func (a *CompatibleAdapter) Kind() Kind {
	return KindOpenAICompatible
}

// Defaults returns the display name and credential requirement for a custom endpoint.
func (a *CompatibleAdapter) Defaults() Defaults {
	return Defaults{
		DisplayName:     "Custom OpenAI-compatible",
		NeedsCredential: true,
	}
}

// Validate probes the custom endpoint, credential, and tool-calling support.
func (a *CompatibleAdapter) Validate(ctx context.Context, request ValidationRequest) (ValidationResult, error) {
	if result := validateAdapterRequest(request.BaseURL, request.ModelID, request.Secret, true); !result.Valid {
		return result, nil
	}
	chatModel, err := a.NewChatModel(ctx, ChatModelRequest(request))
	if err != nil {
		return validationFromError(err), nil
	}

	return probeToolCalling(ctx, chatModel)
}

// ListModels lists models from the custom endpoint, falling back to the configured model.
func (a *CompatibleAdapter) ListModels(ctx context.Context, request ModelListRequest) ([]Model, error) {
	if err := validateModelListRequest(request, true); err != nil {
		return nil, err
	}
	models, err := listOpenAIModels(ctx, a.client, request.BaseURL, request.Secret)
	var endpointError *providerEndpointError
	if errors.As(err, &endpointError) && (endpointError.status == http.StatusNotFound || endpointError.status == http.StatusMethodNotAllowed) {
		return configuredModel(request.ModelID), nil
	}
	if err != nil {
		return nil, err
	}

	return ensureConfiguredModel(models, request.ModelID, ModelSourceConfigured), nil
}

// NewChatModel creates an OpenAI-compatible chat model for the validated request.
func (a *CompatibleAdapter) NewChatModel(ctx context.Context, request ChatModelRequest) (model.ToolCallingChatModel, error) {
	if result := validateAdapterRequest(request.BaseURL, request.ModelID, request.Secret, true); !result.Valid {
		return nil, errors.New(result.UserMessage)
	}
	chatModel, err := openai.NewChatModel(ctx, &openai.ChatModelConfig{
		APIKey:     string(request.Secret),
		BaseURL:    strings.TrimRight(request.BaseURL, "/"),
		Model:      request.ModelID,
		HTTPClient: a.client,
	})
	if err != nil {
		return nil, &adapterOperationError{operation: "create OpenAI-compatible model", cause: err}
	}

	return chatModel, nil
}

func providerHTTPClient(client *http.Client) *http.Client {
	if client != nil {
		return client
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func validateAdapterRequest(baseURL string, modelID string, secret Secret, needsCredential bool) ValidationResult {
	if needsCredential && len(secret) == 0 {
		return ValidationResult{
			Stage:       ValidationStageAuthentication,
			UserMessage: "A provider API key is required.",
		}
	}
	if strings.TrimSpace(modelID) == "" {
		return ValidationResult{
			Stage:       ValidationStageModel,
			UserMessage: "A model must be selected.",
		}
	}
	if _, err := parseProviderBaseURL(baseURL); err != nil {
		return ValidationResult{
			Stage:       ValidationStageNetwork,
			UserMessage: "The provider base URL is invalid.",
		}
	}

	return ValidationResult{Valid: true}
}

func validateModelListRequest(request ModelListRequest, needsCredential bool) error {
	result := validateAdapterRequest(request.BaseURL, request.ModelID, request.Secret, needsCredential)
	if !result.Valid {
		return errors.New(result.UserMessage)
	}
	return nil
}

func parseProviderBaseURL(value string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, errors.New("provider base URL must be an absolute HTTP URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("provider base URL contains unsupported sensitive components")
	}
	return parsed, nil
}

func probeToolCalling(ctx context.Context, chatModel model.ToolCallingChatModel) (ValidationResult, error) {
	withProbe, err := chatModel.WithTools(providerProbeTools())
	if err != nil {
		return ValidationResult{
			Stage:       ValidationStageToolCalling,
			UserMessage: "The model does not support the required tool-calling interface.",
		}, nil
	}
	return generateToolCallingProbe(ctx, withProbe, model.WithToolChoice(schema.ToolChoiceForced, providerProbeToolName))
}

func probeToolCallingWithoutChoice(ctx context.Context, chatModel model.ToolCallingChatModel) (ValidationResult, error) {
	// Passing tools at call time avoids DeepSeek's WithTools behavior, which
	// implicitly emits tool_choice=auto and breaks V4's default thinking mode.
	return generateToolCallingProbe(ctx, chatModel, model.WithTools(providerProbeTools()))
}

func providerProbeTools() []*schema.ToolInfo {
	return []*schema.ToolInfo{
		{
			Name: providerProbeToolName,
			Desc: "Call this function to confirm that structured function calling is available.",
		},
	}
}

func generateToolCallingProbe(ctx context.Context, chatModel model.ToolCallingChatModel, options ...model.Option) (ValidationResult, error) {
	if err := ctx.Err(); err != nil {
		return ValidationResult{}, err
	}
	response, err := chatModel.Generate(
		ctx,
		[]*schema.Message{schema.UserMessage("Call codepilot_provider_probe now. Do not answer with text.")},
		options...,
	)
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return ValidationResult{}, contextErr
		}
		return validationFromError(err), nil
	}
	if response == nil {
		return ValidationResult{
			Stage:       ValidationStageToolCalling,
			UserMessage: "The model returned an empty response to the tool-calling probe.",
		}, nil
	}
	for _, call := range response.ToolCalls {
		if call.Function.Name == providerProbeToolName {
			return ValidationResult{Valid: true}, nil
		}
	}

	return ValidationResult{
		Stage:       ValidationStageToolCalling,
		UserMessage: "The model responded, but did not produce the required tool call.",
	}, nil
}

func validationFromError(err error) ValidationResult {
	message := validationErrorText(err)
	switch {
	case strings.Contains(message, "401"), strings.Contains(message, "403"), strings.Contains(message, "unauthorized"), strings.Contains(message, "api key"), strings.Contains(message, "authentication"):
		return ValidationResult{
			Stage:       ValidationStageAuthentication,
			UserMessage: "Provider authentication failed. Check the API key.",
		}
	case strings.Contains(message, "404"), strings.Contains(message, "model not found"), strings.Contains(message, "unknown model"):
		return ValidationResult{
			Stage:       ValidationStageModel,
			UserMessage: "The selected model is unavailable.",
		}
	case strings.Contains(message, "429"), strings.Contains(message, "rate limit"), strings.Contains(message, "too many requests"):
		return ValidationResult{
			Stage:       ValidationStageNetwork,
			UserMessage: "The provider rate limit was reached. Try again later.",
			Retryable:   true,
		}
	case strings.Contains(message, "quota"), strings.Contains(message, "billing"), strings.Contains(message, "insufficient_quota"):
		return ValidationResult{
			Stage:       ValidationStageAuthentication,
			UserMessage: "The provider account has no available quota. Check billing and account limits.",
		}
	case strings.Contains(message, "tool_choice"), strings.Contains(message, "tool calling"):
		return ValidationResult{
			Stage:       ValidationStageToolCalling,
			UserMessage: "The provider rejected the tool-calling validation request.",
		}
	case strings.Contains(message, "400"), strings.Contains(message, "bad request"):
		return ValidationResult{
			Stage:       ValidationStageModel,
			UserMessage: "The provider rejected the selected model validation request.",
		}
	case strings.Contains(message, "timeout"), strings.Contains(message, "connection"), strings.Contains(message, "no such host"), strings.Contains(message, "tls"):
		return ValidationResult{
			Stage:       ValidationStageNetwork,
			UserMessage: "The provider request failed. Check the endpoint and network connection.",
			Retryable:   true,
		}
	default:
		return ValidationResult{
			Stage:       ValidationStageModel,
			UserMessage: "The selected model could not complete the validation request.",
			Retryable:   true,
		}
	}
}

func validationErrorText(err error) string {
	var messages strings.Builder
	for current := err; current != nil; current = errors.Unwrap(current) {
		if messages.Len() > 0 {
			messages.WriteByte(' ')
		}
		messages.WriteString(strings.ToLower(current.Error()))
	}
	return messages.String()
}

func listOpenAIModels(ctx context.Context, client *http.Client, baseURL string, secret Secret) ([]Model, error) {
	parsed, err := parseProviderBaseURL(baseURL)
	if err != nil {
		return nil, err
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/models"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create provider model request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+string(secret))

	response, err := client.Do(request)
	if err != nil {
		return nil, &adapterOperationError{operation: "list provider models", cause: err}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, providerResponseMaxBytes))
		return nil, &providerEndpointError{operation: "list provider models", status: response.StatusCode}
	}

	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, providerResponseMaxBytes))
	if err := decoder.Decode(&payload); err != nil {
		return nil, &adapterOperationError{operation: "decode provider models", cause: err}
	}
	models := make([]Model, 0, min(len(payload.Data), providerModelLimit))
	seen := make(map[string]struct{}, len(payload.Data))
	for _, item := range payload.Data {
		id := strings.TrimSpace(item.ID)
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

	return models, nil
}

func ensureConfiguredModel(models []Model, modelID string, source ModelSource) []Model {
	for index := range models {
		if models[index].ID == modelID {
			models[index].Recommended = true
			return models
		}
	}
	return append([]Model{{ID: modelID, DisplayName: modelID, Recommended: true, Source: source}}, models...)
}

func markRecommendedModel(models []Model, modelID string) []Model {
	for index := range models {
		if models[index].ID == modelID {
			models[index].Recommended = true
			break
		}
	}
	return models
}

func configuredModel(modelID string) []Model {
	return []Model{{
		ID:          modelID,
		DisplayName: modelID,
		Recommended: true,
		Source:      ModelSourceConfigured,
	}}
}

type adapterOperationError struct {
	operation string
	cause     error
}

func (e *adapterOperationError) Error() string {
	return e.operation + " failed"
}

func (e *adapterOperationError) Unwrap() error {
	return e.cause
}

type providerEndpointError struct {
	operation string
	status    int
}

func (e *providerEndpointError) Error() string {
	return fmt.Sprintf("%s returned HTTP %d", e.operation, e.status)
}
