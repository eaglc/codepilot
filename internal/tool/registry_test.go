package tool

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/eaglc/codepilot/internal/llm"
)

type testTool struct {
	name   string
	result Result
}

func (t testTool) Definition() llm.ToolDefinition {
	return llm.ToolDefinition{Name: t.name, Description: "test tool", InputSchema: json.RawMessage(`{"type":"object"}`)}
}

func (testTool) ReplayPolicy() ReplayPolicy { return ReplayNever }

func (t testTool) Execute(context.Context, Call, ProgressSink) (Result, error) {
	return t.result, nil
}

func TestRegistryExecutesWithoutOwningActivityPersistence(t *testing.T) {
	registry, err := NewRegistry(testTool{name: "read", result: Result{
		Status:  ResultCompleted,
		Content: []llm.Content{{Type: llm.ContentText, Text: "ok"}},
	}})
	if err != nil {
		t.Fatalf("create registry: %v", err)
	}
	result, err := registry.Execute(context.Background(), Call{ID: "call-1", Name: "read", Arguments: json.RawMessage(`{}`)}, nil)
	if err != nil {
		t.Fatalf("execute tool: %v", err)
	}
	if result.Status != ResultCompleted || result.Content[0].Text != "ok" {
		t.Fatalf("result = %#v", result)
	}
}

func TestRegistryRejectsDuplicateNames(t *testing.T) {
	executable := testTool{name: "read", result: Result{Status: ResultCompleted, Content: []llm.Content{{Type: llm.ContentText, Text: "ok"}}}}
	if _, err := NewRegistry(executable, executable); err == nil {
		t.Fatal("expected duplicate tool name error")
	}
}
