package codingtools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/eaglc/codepilot/internal/llm"
	"github.com/eaglc/codepilot/internal/tool"
)

func TestWorkflowNodeScopeRejectsWritesOutsideDeclaredPaths(t *testing.T) {
	inner := &recordingNodeScopeTool{}
	bounded := withNodeScopeBoundary(inner, nil, []string{"internal/workflow"})
	inside, err := bounded.Execute(context.Background(), tool.Call{Name: "edit_file", Arguments: json.RawMessage(`{"path":"internal/workflow/workflow.go"}`)}, nil)
	if err != nil || inside.Status != tool.ResultCompleted || inner.calls != 1 {
		t.Fatalf("inside Execute = %#v, %v, calls=%d", inside, err, inner.calls)
	}
	outside, err := bounded.Execute(context.Background(), tool.Call{Name: "edit_file", Arguments: json.RawMessage(`{"path":"internal/codingagent/service.go"}`)}, nil)
	if err != nil || outside.Status != tool.ResultDenied || inner.calls != 1 {
		t.Fatalf("outside Execute = %#v, %v, calls=%d", outside, err, inner.calls)
	}
}

func TestWorkflowNodeScopeRejectsReadsOutsideDeclaredPathsAndUnscopedSearch(t *testing.T) {
	inner := &recordingNodeReadScopeTool{}
	bounded := withNodeScopeBoundary(inner, []string{"internal/workflow"}, nil)
	inside, err := bounded.Execute(context.Background(), tool.Call{Name: "read_file", Arguments: json.RawMessage(`{"path":"internal/workflow/workflow.go"}`)}, nil)
	if err != nil || inside.Status != tool.ResultCompleted || inner.calls != 1 {
		t.Fatalf("inside read = %#v, %v, calls=%d", inside, err, inner.calls)
	}
	outside, err := bounded.Execute(context.Background(), tool.Call{Name: "read_file", Arguments: json.RawMessage(`{"path":"internal/codingagent/service.go"}`)}, nil)
	if err != nil || outside.Status != tool.ResultDenied || inner.calls != 1 {
		t.Fatalf("outside read = %#v, %v, calls=%d", outside, err, inner.calls)
	}
	unscoped, err := bounded.Execute(context.Background(), tool.Call{Name: "search_code", Arguments: json.RawMessage(`{"query":"secret"}`)}, nil)
	if err != nil || unscoped.Status != tool.ResultDenied || inner.calls != 1 {
		t.Fatalf("unscoped search = %#v, %v, calls=%d", unscoped, err, inner.calls)
	}
}

type recordingNodeScopeTool struct{ calls int }

type recordingNodeReadScopeTool struct{ calls int }

func (*recordingNodeScopeTool) Definition() llm.ToolDefinition {
	return llm.ToolDefinition{Name: "edit_file", InputSchema: json.RawMessage(`{"type":"object"}`)}
}
func (*recordingNodeScopeTool) ReplayPolicy() tool.ReplayPolicy { return tool.ReplayNever }
func (t *recordingNodeScopeTool) Execute(context.Context, tool.Call, tool.ProgressSink) (tool.Result, error) {
	t.calls++
	return tool.Result{Status: tool.ResultCompleted}, nil
}

func (*recordingNodeReadScopeTool) Definition() llm.ToolDefinition {
	return llm.ToolDefinition{Name: "read_file", InputSchema: json.RawMessage(`{"type":"object"}`)}
}
func (*recordingNodeReadScopeTool) ReplayPolicy() tool.ReplayPolicy { return tool.ReplaySafe }
func (t *recordingNodeReadScopeTool) Execute(context.Context, tool.Call, tool.ProgressSink) (tool.Result, error) {
	t.calls++
	return tool.Result{Status: tool.ResultCompleted}, nil
}
