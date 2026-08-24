package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/eaglc/codepilot/internal/llm"
	"github.com/eaglc/codepilot/internal/provider"
)

const maxCatalogBytes = 4 << 20

// ListOpenAICompatibleModels discovers the authenticated /models catalog.
func ListOpenAICompatibleModels(ctx context.Context, client *http.Client, profile provider.Profile, credential provider.Credential, defaults Defaults, reasoning bool) ([]llm.Model, error) {
	baseURL, _, err := ValidateConfig(provider.ModelConfig{Profile: profile, Credential: credential}, profile.Kind, defaults)
	if err != nil {
		return nil, provider.NewProductError(provider.ErrorNotConfigured, "provider.list_models", "Provider profile is incomplete. Check its type, Base URL, and default model.", false, err)
	}
	endpoint, err := catalogURL(baseURL, "/models")
	if err != nil {
		return nil, provider.NewProductError(provider.ErrorNotConfigured, "provider.list_models", "Provider Base URL is invalid.", false, err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, provider.NewProductError(provider.ErrorNotConfigured, "provider.list_models", "Provider Base URL is invalid.", false, err)
	}
	request.Header.Set("Authorization", "Bearer "+string(credential))
	request.Header.Set("Accept", "application/json")
	response, err := HTTPClient(client).Do(request)
	if err != nil {
		return nil, provider.ClassifyTransportError("provider.list_models", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, provider.HTTPStatusError("provider.list_models", response.StatusCode)
	}
	var catalog struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := decodeCatalog(response.Body, &catalog); err != nil {
		return nil, provider.NewProductError(provider.ErrorConnectionFailed, "provider.list_models", "Provider returned an invalid model catalog response.", false, err)
	}
	ids := make([]string, 0, len(catalog.Data))
	for _, item := range catalog.Data {
		ids = append(ids, item.ID)
	}
	return catalogModels(profile.ID, ids, reasoning), nil
}

// ListOllamaModels checks the local service and returns installed models from
// /api/tags.
func ListOllamaModels(ctx context.Context, client *http.Client, profile provider.Profile, defaults Defaults) ([]llm.Model, error) {
	baseURL, _, err := ValidateConfig(provider.ModelConfig{Profile: profile}, provider.KindOllama, defaults)
	if err != nil {
		return nil, provider.NewProductError(provider.ErrorNotConfigured, "provider.list_models", "Ollama profile is incomplete. Check its Base URL and default model.", false, err)
	}
	endpoint, err := catalogURL(baseURL, "/api/tags")
	if err != nil {
		return nil, provider.NewProductError(provider.ErrorNotConfigured, "provider.list_models", "Ollama Base URL is invalid.", false, err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, provider.NewProductError(provider.ErrorNotConfigured, "provider.list_models", "Ollama Base URL is invalid.", false, err)
	}
	request.Header.Set("Accept", "application/json")
	response, err := HTTPClient(client).Do(request)
	if err != nil {
		return nil, provider.ClassifyTransportError("provider.list_models", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, provider.HTTPStatusError("provider.list_models", response.StatusCode)
	}
	var catalog struct {
		Models []struct {
			Name  string `json:"name"`
			Model string `json:"model"`
		} `json:"models"`
	}
	if err := decodeCatalog(response.Body, &catalog); err != nil {
		return nil, provider.NewProductError(provider.ErrorConnectionFailed, "provider.list_models", "Ollama returned an invalid model catalog response.", false, err)
	}
	ids := make([]string, 0, len(catalog.Models))
	for _, item := range catalog.Models {
		if strings.TrimSpace(item.Name) != "" {
			ids = append(ids, item.Name)
		} else {
			ids = append(ids, item.Model)
		}
	}
	models := catalogModels(profile.ID, ids, false)
	for index := range models {
		metadata, metadataErr := showOllamaModel(ctx, client, baseURL, models[index].Ref.Model)
		if metadataErr == nil {
			models[index].ContextWindow = metadata.ContextWindow
			models[index].MaxOutput = metadata.MaxOutput
			models[index].Tokenizer = metadata.Tokenizer
		}
	}
	return models, nil
}

type ollamaMetadata struct {
	ContextWindow int
	MaxOutput     int
	Tokenizer     llm.TokenizerMetadata
}

func showOllamaModel(ctx context.Context, client *http.Client, baseURL, name string) (ollamaMetadata, error) {
	endpoint, err := catalogURL(baseURL, "/api/show")
	if err != nil {
		return ollamaMetadata{}, err
	}
	body, err := json.Marshal(map[string]string{"model": name})
	if err != nil {
		return ollamaMetadata{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return ollamaMetadata{}, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	response, err := HTTPClient(client).Do(request)
	if err != nil {
		return ollamaMetadata{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return ollamaMetadata{}, fmt.Errorf("Ollama show returned status %d", response.StatusCode)
	}
	var value struct {
		ModelInfo map[string]any `json:"model_info"`
	}
	if err := decodeCatalog(response.Body, &value); err != nil {
		return ollamaMetadata{}, err
	}
	metadata := ollamaMetadata{}
	var architecture string
	for key, item := range value.ModelInfo {
		switch {
		case strings.HasSuffix(key, ".context_length"):
			metadata.ContextWindow = positiveJSONInt(item)
		case key == "general.architecture":
			architecture, _ = item.(string)
		case strings.HasSuffix(key, ".max_position_embeddings") && metadata.ContextWindow == 0:
			metadata.ContextWindow = positiveJSONInt(item)
		}
	}
	if metadata.ContextWindow > 0 {
		metadata.MaxOutput = metadata.ContextWindow / 8
	}
	if architecture != "" {
		metadata.Tokenizer = llm.TokenizerMetadata{ID: "ollama:" + architecture, Source: "provider_model_info"}
	}
	return metadata, nil
}

func positiveJSONInt(value any) int {
	number, ok := value.(float64)
	if !ok || number <= 0 || number > float64(^uint(0)>>1) {
		return 0
	}
	return int(number)
}

func catalogURL(baseURL, suffix string) (string, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", errors.New("catalog base URL is invalid")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + suffix
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

func decodeCatalog(reader io.Reader, target any) error {
	limited := &io.LimitedReader{R: reader, N: maxCatalogBytes + 1}
	decoder := json.NewDecoder(limited)
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if limited.N <= 0 {
		return errors.New("model catalog is too large")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return fmt.Errorf("model catalog contains multiple JSON values")
	}
	return nil
}

func catalogModels(profileID provider.ProfileID, ids []string, reasoning bool) []llm.Model {
	unique := make(map[string]struct{}, len(ids))
	values := make([]llm.Model, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || len(id) > 512 || strings.ContainsAny(id, "\r\n\x00") {
			continue
		}
		if _, exists := unique[id]; exists {
			continue
		}
		unique[id] = struct{}{}
		values = append(values, llm.Model{Ref: llm.ModelRef{Provider: string(profileID), Model: id}, DisplayName: id, Reasoning: reasoning})
	}
	sort.Slice(values, func(left, right int) bool { return values[left].Ref.Model < values[right].Ref.Model })
	return values
}
