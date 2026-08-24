package llm

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// ReplayPolicy controls how recovery treats a tool execution that lacks a
// durable result. It lives in the protocol layer so executable tools and
// durable sessions share one stable representation without depending on each
// other.
type ReplayPolicy string

const (
	// ReplayNever requires an explicit external decision before another execution.
	ReplayNever ReplayPolicy = "never"
	// ReplaySafe permits recovery to execute the tool again after validation.
	ReplaySafe ReplayPolicy = "safe"
	// ReplayIdempotent permits retry with the original idempotency key.
	ReplayIdempotent ReplayPolicy = "idempotent"
)

// ToolDefinition is the model-visible declaration of a callable tool.
// It contains no executable function or trusted runtime context.
type ToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// Validate checks the stable model-facing tool declaration.
func (d ToolDefinition) Validate() error {
	if d.Name == "" {
		return fmt.Errorf("validate tool definition: name is required")
	}
	if d.Description == "" {
		return fmt.Errorf("validate tool definition %q: description is required", d.Name)
	}
	if !validJSONObject(d.InputSchema) {
		return fmt.Errorf("validate tool definition %q: input schema must be a JSON object", d.Name)
	}
	return nil
}

// ToolCall is a complete provider-neutral request emitted by an assistant.
type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// Validate checks that a tool call is complete and can be persisted safely.
func (c ToolCall) Validate() error {
	if c.ID == "" {
		return fmt.Errorf("validate tool call: id is required")
	}
	if c.Name == "" {
		return fmt.Errorf("validate tool call %q: name is required", c.ID)
	}
	if !validJSONObject(c.Arguments) {
		return fmt.Errorf("validate tool call %q: arguments must be a JSON object", c.ID)
	}
	return nil
}

func validJSONObject(value json.RawMessage) bool {
	trimmed := bytes.TrimSpace(value)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return false
	}
	var decoded map[string]json.RawMessage
	return json.Unmarshal(trimmed, &decoded) == nil
}

func cloneRawMessage(value json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), value...)
}
