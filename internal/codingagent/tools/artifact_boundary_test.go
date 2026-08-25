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

func TestReadToolResultLoadsCompleteExternalizedResultInChunks(t *testing.T) {
	store := &memoryArtifactStore{}
	original := "begin-" + strings.Repeat("世界", 400) + "-end"
	boundary := artifactBoundary{store: store, threshold: 128, preview: 32}
	externalized := boundary.externalize(context.Background(), tool.Call{ID: "call-large", Name: "large"}, tool.Result{
		Status: tool.ResultCompleted, Content: []llm.Content{{Type: llm.ContentText, Text: original}}, Details: json.RawMessage(`{"detail":"complete metadata"}`),
	})
	var externalizedDetails artifactResultDetails
	if err := json.Unmarshal(externalized.Details, &externalizedDetails); err != nil {
		t.Fatalf("decode externalized details: %v", err)
	}
	if !strings.Contains(externalized.Content[0].Text, "Use read_tool_result") {
		t.Fatalf("externalized result did not explain recovery: %q", externalized.Content[0].Text)
	}
	reader := &readToolResultTool{artifacts: store}
	offset := 0
	var chunks strings.Builder
	for attempts := 0; attempts < 20; attempts++ {
		arguments, _ := json.Marshal(map[string]any{
			"artifact_id": externalizedDetails.Artifact.ID, "artifact_size": externalizedDetails.Artifact.Size,
			"offset": offset, "max_bytes": 256,
		})
		result, err := reader.Execute(context.Background(), tool.Call{ID: "read-result", Name: "read_tool_result", Arguments: arguments}, nil)
		if err != nil || result.Status != tool.ResultCompleted {
			t.Fatalf("read result chunk: result=%#v err=%v", result, err)
		}
		var details toolResultChunkDetails
		if err := json.Unmarshal(result.Details, &details); err != nil {
			t.Fatalf("decode chunk details: %v", err)
		}
		marker := strings.LastIndex(result.Content[0].Text, "\n\n[Tool result bytes ")
		if marker < 0 {
			t.Fatalf("chunk marker missing: %q", result.Content[0].Text)
		}
		chunks.WriteString(result.Content[0].Text[:marker])
		if details.Complete {
			if !strings.Contains(chunks.String(), original) || !strings.Contains(chunks.String(), `Details: {"detail":"complete metadata"}`) {
				t.Fatalf("reconstructed result is incomplete: %q", chunks.String())
			}
			return
		}
		if details.NextOffset <= offset {
			t.Fatalf("chunk did not advance: %#v", details)
		}
		offset = details.NextOffset
	}
	t.Fatal("complete result required too many chunks")
}
