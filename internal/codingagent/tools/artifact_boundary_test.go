package codingtools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/eaglc/codepilot/internal/llm"
	"github.com/eaglc/codepilot/internal/tool"
)

type largeResultTool struct {
	resumed bool
}

func (*largeResultTool) Definition() llm.ToolDefinition {
	return llm.ToolDefinition{Name: "large", Description: "Return a large result.", InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)}
}
func (*largeResultTool) ReplayPolicy() tool.ReplayPolicy { return tool.ReplayNever }
func (*largeResultTool) Execute(context.Context, tool.Call, tool.ProgressSink) (tool.Result, error) {
	return tool.Result{Status: tool.ResultCompleted, Content: []llm.Content{{Type: llm.ContentText, Text: strings.Repeat("x", 1024)}}, Details: json.RawMessage(`{"detail":"large detail","diff":{"text":"-old\n+new\n","files":["main.go"]}}`)}, nil
}
func (t *largeResultTool) Resume(context.Context, tool.Call, tool.Interrupt, tool.Result, tool.ProgressSink) (tool.Result, error) {
	t.resumed = true
	return t.Execute(context.Background(), tool.Call{}, nil)
}

func TestArtifactBoundaryExternalizesExecuteAndResumeWithoutToolPersistence(t *testing.T) {
	store := &memoryArtifactStore{}
	inner := &largeResultTool{}
	registry, err := tool.NewRegistry(withArtifactBoundary(inner, store, 128, 32))
	if err != nil {
		t.Fatalf("create registry: %v", err)
	}
	call := tool.Call{ID: "call-1", Name: "large", Arguments: json.RawMessage(`{}`)}
	result, err := registry.Execute(context.Background(), call, nil)
	if err != nil {
		t.Fatalf("execute wrapped tool: %v", err)
	}
	if len(store.artifacts) != 1 || len(result.Content[0].Text) >= 1024 || !strings.Contains(result.Content[0].Text, "sha256:test-1") || !strings.Contains(string(result.Details), `"kind":"coding_tool_artifact_v1"`) {
		t.Fatalf("externalized result=%#v artifacts=%#v", result, store.artifacts)
	}
	var persisted durableToolResult
	if err := json.Unmarshal(store.artifacts[0].Data, &persisted); err != nil || persisted.Result.Content[0].Text != strings.Repeat("x", 1024) {
		t.Fatalf("persisted exact result=%#v err=%v", persisted, err)
	}
	resolution := tool.Result{Status: tool.ResultCompleted, Content: []llm.Content{{Type: llm.ContentText, Text: "approved"}}}
	if _, err := registry.Resume(context.Background(), call, tool.Interrupt{ID: "approval", Kind: "approval", Payload: json.RawMessage(`{}`)}, resolution, nil); err != nil {
		t.Fatalf("resume wrapped tool: %v", err)
	}
	if !inner.resumed || len(store.artifacts) != 2 {
		t.Fatalf("resume was not delegated/externalized: resumed=%v artifacts=%d", inner.resumed, len(store.artifacts))
	}
}

func TestArtifactBoundaryLeavesInterruptAndSaveFailureUnchanged(t *testing.T) {
	interrupt := tool.Result{Status: tool.ResultInterrupted, Content: []llm.Content{{Type: llm.ContentText, Text: strings.Repeat("x", 1024)}}, Interrupt: &tool.Interrupt{ID: "approval", Kind: "approval", Payload: json.RawMessage(`{}`)}}
	boundary := artifactBoundary{store: &memoryArtifactStore{}, threshold: 10, preview: 5}
	if got := boundary.externalize(context.Background(), tool.Call{Name: "large"}, interrupt); got.Interrupt == nil || got.Content[0].Text != interrupt.Content[0].Text {
		t.Fatalf("interrupt was rewritten: %#v", got)
	}
}

func TestArtifactBoundaryPreservesReadRangePreview(t *testing.T) {
	boundary := artifactBoundary{store: &memoryArtifactStore{}, threshold: 10, preview: 5}
	result := tool.Result{
		Status:  tool.ResultCompleted,
		Content: []llm.Content{{Type: llm.ContentText, Text: strings.Repeat("x", 1024)}},
		Details: json.RawMessage(`{"path":"internal/ui/model.go","start_line":10,"end_line":20,"bytes":1024}`),
	}
	got := boundary.externalize(context.Background(), tool.Call{ID: "read", Name: "read_file"}, result)
	var details artifactResultDetails
	if err := json.Unmarshal(got.Details, &details); err != nil || details.Path != "internal/ui/model.go" || details.StartLine != 10 || details.EndLine != 20 {
		t.Fatalf("artifact read preview = %#v err=%v", details, err)
	}
}
