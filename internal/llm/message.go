package llm

import (
	"encoding/json"
	"fmt"
	"time"
)

// Role identifies the semantic role of a model conversation message.
type Role string

const (
	// RoleUser marks user-controlled input.
	RoleUser Role = "user"
	// RoleAssistant marks model output, including tool calls.
	RoleAssistant Role = "assistant"
	// RoleTool marks the result corresponding to one assistant tool call.
	RoleTool Role = "tool"
)

// StopReason describes why an assistant response stopped.
type StopReason string

const (
	// StopReasonStop means the model completed normally.
	StopReasonStop StopReason = "stop"
	// StopReasonLength means an output limit stopped generation.
	StopReasonLength StopReason = "length"
	// StopReasonToolUse means the assistant requested one or more tools.
	StopReasonToolUse StopReason = "tool_use"
	// StopReasonError means provider generation failed.
	StopReasonError StopReason = "error"
	// StopReasonAborted means the caller cancelled generation.
	StopReasonAborted StopReason = "aborted"
)

// Message is the canonical provider-neutral conversation message.
type Message struct {
	Role          Role            `json:"role"`
	Content       []Content       `json:"content"`
	ToolCallID    string          `json:"tool_call_id,omitempty"`
	ToolName      string          `json:"tool_name,omitempty"`
	IsError       bool            `json:"is_error,omitempty"`
	Details       json.RawMessage `json:"details,omitempty"`
	Provider      string          `json:"provider,omitempty"`
	Model         string          `json:"model,omitempty"`
	ResponseModel string          `json:"response_model,omitempty"`
	Usage         *Usage          `json:"usage,omitempty"`
	StopReason    StopReason      `json:"stop_reason,omitempty"`
	ErrorMessage  string          `json:"error_message,omitempty"`
	Timestamp     time.Time       `json:"timestamp"`
}

// Validate checks role-specific invariants without imposing provider policy.
func (m Message) Validate() error {
	if m.Role != RoleUser && m.Role != RoleAssistant && m.Role != RoleTool {
		return fmt.Errorf("validate message: unsupported role %q", m.Role)
	}
	if len(m.Content) == 0 {
		return fmt.Errorf("validate %s message: content is empty", m.Role)
	}
	for index, content := range m.Content {
		if err := content.Validate(); err != nil {
			return fmt.Errorf("validate %s message content %d: %w", m.Role, index, err)
		}
		switch m.Role {
		case RoleUser:
			if content.Type != ContentText && content.Type != ContentImage {
				return fmt.Errorf("validate user message: content type %q is not allowed", content.Type)
			}
		case RoleAssistant:
			if content.Type != ContentText && content.Type != ContentThinking && content.Type != ContentToolCall {
				return fmt.Errorf("validate assistant message: content type %q is not allowed", content.Type)
			}
		case RoleTool:
			if content.Type != ContentText && content.Type != ContentImage {
				return fmt.Errorf("validate tool message: content type %q is not allowed", content.Type)
			}
		}
	}
	if m.Role == RoleTool {
		if m.ToolCallID == "" || m.ToolName == "" {
			return fmt.Errorf("validate tool message: tool call id and name are required")
		}
		if len(m.Details) != 0 && !json.Valid(m.Details) {
			return fmt.Errorf("validate tool message: details are not valid JSON")
		}
	} else if m.ToolCallID != "" || m.ToolName != "" || m.IsError || len(m.Details) != 0 {
		return fmt.Errorf("validate %s message: tool-result fields are populated", m.Role)
	}
	if (m.Provider == "") != (m.Model == "") {
		return fmt.Errorf("validate message: provider and model must be set together")
	}
	return nil
}

// ToolCalls returns defensive copies of all assistant tool calls in display order.
func (m Message) ToolCalls() []ToolCall {
	var calls []ToolCall
	for _, content := range m.Content {
		if content.Type != ContentToolCall || content.ToolCall == nil {
			continue
		}
		call := *content.ToolCall
		call.Arguments = cloneRawMessage(call.Arguments)
		calls = append(calls, call)
	}
	return calls
}

// Clone returns a defensive copy suitable for crossing package boundaries.
func (m Message) Clone() Message {
	clone := m
	clone.Content = make([]Content, len(m.Content))
	for index, content := range m.Content {
		clone.Content[index] = content.Clone()
	}
	if m.Usage != nil {
		usage := *m.Usage
		clone.Usage = &usage
	}
	clone.Details = cloneRawMessage(m.Details)
	return clone
}
