package codingtools

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/eaglc/codepilot/internal/codingagent"
	"github.com/eaglc/codepilot/internal/llm"
	"github.com/eaglc/codepilot/internal/tool"
)

type permissionBoundaryTestTool struct {
	automatic bool
	executed  int
}

func (*permissionBoundaryTestTool) Definition() llm.ToolDefinition {
	return llm.ToolDefinition{Name: "controlled", Description: "permission boundary test tool", InputSchema: json.RawMessage(`{"type":"object"}`)}
}

func (*permissionBoundaryTestTool) ReplayPolicy() tool.ReplayPolicy { return tool.ReplayNever }

func (t *permissionBoundaryTestTool) PermissionRequirement(context.Context, tool.Call) (permissionRequirement, tool.Result, error) {
	return permissionRequirement{
		required: true,
		request: codingagent.PermissionRequest{
			ToolName: "controlled", Action: codingagent.PermissionActionModify, Paths: []string{"main.go"},
		},
		automatic:       t.automatic,
		readOnlyMessage: "read only",
		approval: tool.Result{
			Status: tool.ResultInterrupted, Content: []llm.Content{{Type: llm.ContentText, Text: "approval required"}},
			Interrupt: &tool.Interrupt{ID: "approval", Kind: "approval", Payload: json.RawMessage(`{"kind":"test"}`)},
		},
	}, tool.Result{}, nil
}

func (t *permissionBoundaryTestTool) Execute(context.Context, tool.Call, tool.ProgressSink) (tool.Result, error) {
	t.executed++
	return completedResult("executed", nil), nil
}

func (t *permissionBoundaryTestTool) Resume(context.Context, tool.Call, tool.Interrupt, tool.Result, tool.ProgressSink) (tool.Result, error) {
	t.executed++
	return completedResult("resumed", nil), nil
}

func TestPermissionBoundaryOwnsModeGrantAndAutomaticDecisions(t *testing.T) {
	now := time.Now().UTC()
	grant := codingagent.PermissionGrant{
		ID: "grant", Scope: codingagent.PermissionGrantSession, ToolName: "controlled", Action: codingagent.PermissionActionModify,
		Paths: []string{"main.go"}, SourceTurnID: "turn", SourceInterruptID: "approval",
		CreatedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour),
	}
	tests := []struct {
		name      string
		mode      codingagent.PermissionMode
		automatic bool
		grants    []codingagent.PermissionGrant
		status    tool.ResultStatus
		executed  int
	}{
		{name: "read only denies", mode: codingagent.PermissionReadOnly, status: tool.ResultDenied},
		{name: "ask interrupts", mode: codingagent.PermissionAsk, status: tool.ResultInterrupted},
		{name: "auto safe executes", mode: codingagent.PermissionAutoEdit, automatic: true, status: tool.ResultCompleted, executed: 1},
		{name: "auto guarded interrupts", mode: codingagent.PermissionAutoEdit, status: tool.ResultInterrupted},
		{name: "exact session grant executes", mode: codingagent.PermissionAsk, grants: []codingagent.PermissionGrant{grant}, status: tool.ResultCompleted, executed: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			inner := &permissionBoundaryTestTool{automatic: test.automatic}
			controlled := withPermissionBoundary(inner, test.mode, test.grants)
			result, err := controlled.Execute(context.Background(), tool.Call{ID: "call", Name: "controlled", Arguments: json.RawMessage(`{}`)}, nil)
			if err != nil || result.Status != test.status || inner.executed != test.executed {
				t.Fatalf("result=%#v executed=%d err=%v", result, inner.executed, err)
			}
		})
	}
}

func TestPermissionBoundaryLeavesOrdinaryReadToolsUnwrapped(t *testing.T) {
	inner := testReadOnlyTool{}
	wrapped := withPermissionBoundary(inner, codingagent.PermissionReadOnly, nil)
	if _, changed := wrapped.(*permissionBoundary); changed {
		t.Fatal("ordinary read tool was unnecessarily permission wrapped")
	}
	result, err := wrapped.Execute(context.Background(), tool.Call{}, nil)
	if err != nil || result.Status != tool.ResultCompleted {
		t.Fatalf("read result=%#v err=%v", result, err)
	}
}

func TestPermissionBoundaryRechecksReadOnlyModeOnResume(t *testing.T) {
	inner := &permissionBoundaryTestTool{}
	controlled := withPermissionBoundary(inner, codingagent.PermissionReadOnly, nil).(tool.ResumableTool)
	result, err := controlled.Resume(context.Background(), tool.Call{}, tool.Interrupt{ID: "approval", Kind: "approval"}, tool.Result{
		Status: tool.ResultCompleted, Content: []llm.Content{{Type: llm.ContentText, Text: "approved"}},
	}, nil)
	if err != nil || result.Status != tool.ResultDenied || inner.executed != 0 {
		t.Fatalf("result=%#v executed=%d err=%v", result, inner.executed, err)
	}
}

type testReadOnlyTool struct{}

func (testReadOnlyTool) Definition() llm.ToolDefinition {
	return llm.ToolDefinition{Name: "read", Description: "read", InputSchema: json.RawMessage(`{"type":"object"}`)}
}
func (testReadOnlyTool) ReplayPolicy() tool.ReplayPolicy { return tool.ReplaySafe }
func (testReadOnlyTool) Execute(context.Context, tool.Call, tool.ProgressSink) (tool.Result, error) {
	return completedResult("read", nil), nil
}

var _ permissionRequirementProvider = (*permissionBoundaryTestTool)(nil)
var _ tool.ResumableTool = (*permissionBoundaryTestTool)(nil)
