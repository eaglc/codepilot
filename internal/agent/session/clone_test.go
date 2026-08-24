package session

import (
	"encoding/json"
	"testing"

	"github.com/eaglc/codepilot/internal/llm"
)

func TestEntryAndRecordCloneAreDeepCopies(t *testing.T) {
	entry := Entry{
		ActiveTools: []string{"read"},
		Message: &llm.Message{Content: []llm.Content{{
			Type:     llm.ContentToolCall,
			ToolCall: &llm.ToolCall{ID: "call", Name: "read", Arguments: json.RawMessage(`{"path":"main.go"}`)},
		}}},
		CustomMessage: &CustomMessage{Content: []llm.Content{{Type: llm.ContentImage, Data: []byte("image"), MIMEType: "image/png"}}, Details: json.RawMessage(`{"kind":"test"}`)},
	}
	entryClone := entry.Clone()
	entry.ActiveTools[0] = "changed"
	entry.Message.Content[0].ToolCall.Arguments[2] = 'X'
	entry.CustomMessage.Content[0].Data[0] = 'X'
	entry.CustomMessage.Details[2] = 'X'
	if entryClone.ActiveTools[0] != "read" || string(entryClone.Message.Content[0].ToolCall.Arguments) != `{"path":"main.go"}` || string(entryClone.CustomMessage.Content[0].Data) != "image" || string(entryClone.CustomMessage.Details) != `{"kind":"test"}` {
		t.Fatalf("entry clone retained mutable input: %#v", entryClone)
	}

	record := Record{
		Tool:      &ToolData{EffectiveArgs: json.RawMessage(`{"path":"main.go"}`)},
		Interrupt: &InterruptData{Payload: json.RawMessage(`{"decision":"ask"}`)},
	}
	recordClone := record.Clone()
	record.Tool.EffectiveArgs[2] = 'X'
	record.Interrupt.Payload[2] = 'X'
	if string(recordClone.Tool.EffectiveArgs) != `{"path":"main.go"}` || string(recordClone.Interrupt.Payload) != `{"decision":"ask"}` {
		t.Fatalf("record clone retained mutable input: %#v", recordClone)
	}
}
