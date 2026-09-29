package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/eaglc/codepilot/internal/codingagent"
)

func TestHeaderProjectsDurableWorkspaceModeAndContextBadges(t *testing.T) {
	snapshot := codingagent.Snapshot{
		Session: codingagent.Session{
			ID: "session", Title: "fallback", ProviderProfileID: "openai", ModelID: "gpt-test",
			PermissionMode: codingagent.PermissionAsk,
		},
		Workspace: codingagent.WorkspaceSnapshot{DisplayName: "codepilot", Branch: "feature/status", Available: true, Dirty: true, ChangedFiles: 3},
		Metrics:   codingagent.SessionMetrics{ContextTokens: 4200},
		ActiveWorkflow: &codingagent.WorkflowSnapshot{
			Status: "running",
		},
	}
	model := Model{snapshot: snapshot, contextReport: codingagent.ContextReport{Available: true, InputBudget: 8000}}

	view := ansi.Strip(model.headerLine("fallback", 200, 12))
	for _, expected := range []string{"codepilot", "feature/status dirty:3", "Workflow running", "ask", "ctx 4200/8000", "openai/gpt-test", "12 lines below"} {
		if !strings.Contains(view, expected) {
			t.Fatalf("header does not contain %q: %q", expected, view)
		}
	}
}

func TestHeaderShrinksWithoutDroppingRequiredAction(t *testing.T) {
	model := Model{snapshot: codingagent.Snapshot{
		Session:           codingagent.Session{PermissionMode: codingagent.PermissionAsk, ProviderProfileID: "provider", ModelID: "model"},
		Workspace:         codingagent.WorkspaceSnapshot{DisplayName: "workspace", Branch: "feature/very-long-branch", Available: true, Dirty: true, ChangedFiles: 9},
		ActiveWorkflow:    &codingagent.WorkflowSnapshot{Status: "needs_replan"},
		PendingPlanReplan: true,
		PendingInterrupts: []codingagent.PendingInterrupt{{Kind: "plan_replan_approval"}},
	}}

	view := ansi.Strip(model.headerLine("fallback", 48, 0))
	if ansi.StringWidth(view) > 48 || !strings.Contains(view, "ACTION replan") || !strings.Contains(view, "Workflow waiting") {
		t.Fatalf("compact header = %q width=%d", view, ansi.StringWidth(view))
	}
	if strings.Contains(view, "provider/model") {
		t.Fatalf("compact header retained low-priority provider badge: %q", view)
	}
}

func TestTaskHierarchyUsesDurableStatesAndExpandedArtifacts(t *testing.T) {
	started := time.Now().Add(-2 * time.Minute)
	snapshot := codingagent.Snapshot{
		ActiveWorkflow: &codingagent.WorkflowSnapshot{
			ID: "workflow", Status: "needs_replan", CompletedNodes: 1, MaxAgentSteps: 10, UsedAgentSteps: 4, WaitingReason: "approval required",
			Nodes: []codingagent.WorkflowNodeSnapshot{
				{ID: "done", Goal: "Inspect repository", Role: "explore", Status: "completed", Attempts: 1, MaxAttempts: 1, ResultRef: "run:done", ReadPaths: []string{"internal"}, StartedAt: started, FinishedAt: started.Add(time.Minute)},
				{ID: "write", Goal: "Apply update", Role: "implement", Status: "running", Attempts: 2, MaxAttempts: 3, WritePaths: []string{"internal/ui"}, StartedAt: started},
			},
		},
		ChildAgents: []codingagent.ChildAgentSnapshot{{
			ID: "child", WorkflowID: "workflow", NodeID: "write", Role: "implement", Goal: "Apply update", Status: codingagent.ChildAgentAwaitingApproval, Attempt: 2,
			WritePaths: []string{"internal/ui"}, ChangeSetID: "changeset-1", ChangeFiles: []string{"internal/ui/model.go"}, Validation: []string{"go test ./internal/ui"}, Conclusion: "Patch ready", StartedAt: started,
		}},
	}
	model := Model{snapshot: snapshot, expanded: map[string]bool{taskNodeKey("workflow", "write"): true}}

	view := ansi.Strip(renderedRows(model.taskHierarchyRows(140, true)))
	for _, expected := range []string{"Workflow waiting", "1/2 nodes complete", "4/10 steps", "approval required", "completed [explore] Inspect repository", "waiting [implement] Apply update", "attempt 2/3", "Write scope", "internal/ui", "ChangeSet", "changeset-1", "Checks", "go test ./internal/ui"} {
		if !strings.Contains(view, expected) {
			t.Fatalf("task hierarchy does not contain %q: %s", expected, view)
		}
	}
}

func TestTaskStateMappingCoversDurableLifecycleStates(t *testing.T) {
	tests := []struct {
		status string
		want   string
	}{
		{"pending", "queued"}, {"running", "running"}, {"needs_replan", "waiting"}, {"blocked", "blocked"},
		{"failed", "failed"}, {"completed", "completed"}, {"cancelled", "cancelled"},
	}
	for _, test := range tests {
		if got := taskStateForWorkflow(test.status); got != test.want {
			t.Fatalf("workflow state %q = %q, want %q", test.status, got, test.want)
		}
	}
}

func TestNarrowTerminalUsesDedicatedStatusPage(t *testing.T) {
	bridge, _ := NewEventBridge(2)
	defer bridge.Close()
	snapshot := codingagent.Snapshot{
		Session:        codingagent.Session{ID: "session", Title: "repo"},
		ActiveWorkflow: &codingagent.WorkflowSnapshot{ID: "workflow", Status: "running", Nodes: []codingagent.WorkflowNodeSnapshot{{ID: "node", Role: "validate", Goal: "Run checks", Status: "running"}}},
	}
	model, err := NewModel(context.Background(), fakeClient{snapshot: snapshot}, bridge, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	model.width, model.height = 72, 20
	conversation := ansi.Strip(model.View().Content)
	if !strings.Contains(conversation, "/status opens the full task tree") || strings.Contains(conversation, "[validate] Run checks") {
		t.Fatalf("narrow task fallback = %q", conversation)
	}

	runStatusCommand(model, "")
	status := ansi.Strip(model.View().Content)
	if !model.taskStatusActive || !strings.Contains(status, "durable Snapshot state only") || !strings.Contains(status, "[validate] Run checks") {
		t.Fatalf("dedicated task status view = %q", status)
	}
	model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	if model.taskStatusActive {
		t.Fatal("Escape did not close the task status page")
	}
}

func TestSelectedDiffTogglesFullScreen(t *testing.T) {
	bridge, _ := NewEventBridge(2)
	defer bridge.Close()
	snapshot := codingagent.Snapshot{
		Session: codingagent.Session{ID: "session", Title: "repo"},
		Transcript: []codingagent.TranscriptItem{{Kind: codingagent.TranscriptToolResult, Tool: &codingagent.TranscriptTool{
			CallID: "call", Name: "apply_patch", Status: "completed", Diff: &codingagent.InlineDiff{Text: "--- a/main.go\n+++ b/main.go\n@@ -1 +1 @@\n-old\n+new\n", Files: []string{"main.go"}},
		}}},
	}
	model, err := NewModel(context.Background(), fakeClient{snapshot: snapshot}, bridge, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	model.width, model.height = 72, 20
	model.selectBlock(model.selectableBlocks()[0])
	model.Update(tea.KeyPressMsg(tea.Key{Code: 'd', Text: "d"}))
	view := ansi.Strip(model.View().Content)
	if !model.diffFocus || !strings.Contains(view, "Changes  •  apply_patch") || !strings.Contains(view, "D, q, or Esc returns") {
		t.Fatalf("full-screen diff = %q", view)
	}
	model.Update(tea.KeyPressMsg(tea.Key{Code: 'd', Text: "d"}))
	if model.diffFocus {
		t.Fatal("D did not return from full-screen diff")
	}
}
