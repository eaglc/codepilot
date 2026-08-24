package llm

import (
	"encoding/json"
	"testing"
)

func TestMessageToolRoundTripPreservesCallAndResult(t *testing.T) {
	assistant := Message{
		Role: RoleAssistant,
		Content: []Content{{
			Type:     ContentToolCall,
			ToolCall: &ToolCall{ID: "call-1", Name: "read_file", Arguments: json.RawMessage(`{"path":"main.go"}`)},
		}},
		Provider:   "test",
		Model:      "model",
		StopReason: StopReasonToolUse,
	}
	result := Message{
		Role:       RoleTool,
		ToolCallID: "call-1",
		ToolName:   "read_file",
		Content:    []Content{{Type: ContentText, Text: "package main"}},
	}
	for _, message := range []Message{assistant, result} {
		if err := message.Validate(); err != nil {
			t.Fatalf("validate message: %v", err)
		}
		encoded, err := json.Marshal(message)
		if err != nil {
			t.Fatalf("marshal message: %v", err)
		}
		var decoded Message
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatalf("unmarshal message: %v", err)
		}
		if err := decoded.Validate(); err != nil {
			t.Fatalf("validate decoded message: %v", err)
		}
	}
	if calls := assistant.ToolCalls(); len(calls) != 1 || calls[0].ID != "call-1" || calls[0].Name != "read_file" {
		t.Fatalf("tool calls = %#v", calls)
	}
}

func TestMessageRejectsToolCallInUserMessage(t *testing.T) {
	message := Message{Role: RoleUser, Content: []Content{{
		Type:     ContentToolCall,
		ToolCall: &ToolCall{ID: "call-1", Name: "read", Arguments: json.RawMessage(`{}`)},
	}}}
	if err := message.Validate(); err == nil {
		t.Fatal("expected user tool call to be rejected")
	}
}
