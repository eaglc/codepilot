// Package llm defines provider-neutral model and message contracts.
package llm

import "fmt"

// ModelRef identifies one model without carrying provider configuration or credentials.
type ModelRef struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// Validate checks that the model reference is complete.
func (r ModelRef) Validate() error {
	if r.Provider == "" {
		return fmt.Errorf("validate model reference: provider is required")
	}
	if r.Model == "" {
		return fmt.Errorf("validate model reference: model is required")
	}
	return nil
}

// Model describes provider-neutral model capabilities.
type Model struct {
	Ref           ModelRef          `json:"ref"`
	DisplayName   string            `json:"display_name"`
	Reasoning     bool              `json:"reasoning"`
	ImageInput    bool              `json:"image_input"`
	ContextWindow int               `json:"context_window"`
	MaxOutput     int               `json:"max_output"`
	Tokenizer     TokenizerMetadata `json:"tokenizer"`
}

// TokenizerMetadata describes how a model counts input. Source is explicit so
// callers never mistake a fallback estimate for provider-reported metadata.
type TokenizerMetadata struct {
	ID     string `json:"id,omitempty"`
	Source string `json:"source,omitempty"`
}

// Usage records normalized token and monetary usage for one model operation.
type Usage struct {
	InputTokens      int     `json:"input_tokens"`
	OutputTokens     int     `json:"output_tokens"`
	CacheReadTokens  int     `json:"cache_read_tokens"`
	CacheWriteTokens int     `json:"cache_write_tokens"`
	ReasoningTokens  int     `json:"reasoning_tokens,omitempty"`
	TotalTokens      int     `json:"total_tokens"`
	Cost             float64 `json:"cost"`
}
