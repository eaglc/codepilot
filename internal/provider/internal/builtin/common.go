// Package builtin contains shared validation for concrete built-in provider adapters.
package builtin

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/eaglc/codepilot/internal/provider"
)

// Defaults contains secret-free adapter defaults.
type Defaults struct {
	BaseURL         string
	ModelID         string
	NeedsCredential bool
}

// ValidateConfig applies common profile, URL, model, and credential rules.
func ValidateConfig(config provider.ModelConfig, kind provider.Kind, defaults Defaults) (string, string, error) {
	if config.Profile.ID == "" || config.Profile.Kind != kind {
		return "", "", fmt.Errorf("create %s model: profile id and kind %q are required", kind, kind)
	}
	if defaults.NeedsCredential && len(config.Credential) == 0 {
		return "", "", fmt.Errorf("create %s model: credential is required", kind)
	}
	baseURL := strings.TrimSpace(config.Profile.BaseURL)
	if baseURL == "" {
		baseURL = defaults.BaseURL
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		return "", "", fmt.Errorf("create %s model: base URL must be an absolute HTTP URL", kind)
	}
	modelID := strings.TrimSpace(config.ModelID)
	if modelID == "" {
		modelID = strings.TrimSpace(config.Profile.DefaultModel)
	}
	if modelID == "" {
		modelID = defaults.ModelID
	}
	return strings.TrimRight(baseURL, "/"), modelID, nil
}

// HTTPClient returns the injected client or a bounded default.
func HTTPClient(client *http.Client) *http.Client {
	if client != nil {
		return client
	}
	return &http.Client{Timeout: 5 * time.Minute}
}
