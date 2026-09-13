package codingagent_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/eaglc/codepilot/internal/agent"
	agentsession "github.com/eaglc/codepilot/internal/agent/session"
	"github.com/eaglc/codepilot/internal/codingagent"
	codingmemory "github.com/eaglc/codepilot/internal/codingstore/memory"
	"github.com/eaglc/codepilot/internal/contextmanager"
	"github.com/eaglc/codepilot/internal/llm"
)

func TestPlanApprovalMaterialDriftForcesNewVersionBeforeExecution(t *testing.T) {
	planOne := lifecyclePlanSubmission("Implement the first reviewed Plan.")
	planTwo := lifecyclePlanSubmission("Implement the Plan against the refreshed workspace baseline.")
	root, service, products, _, session, _, events := newPlanLifecycleFixture(t, []llm.Message{
		lifecycleWorkspaceCall("workspace"), lifecyclePlanCall("plan-1", planOne), lifecyclePlanCall("plan-2", planTwo), finalAssistant(),
	})
	planned, err := service.StartTurn(context.Background(), codingagent.TurnRequest{SessionID: session.ID, Text: "Plan and implement the change", Mode: codingagent.TurnModePlan})
	if err != nil || planned.InterruptKind != "plan_approval" {
		t.Fatalf("initial Plan = %#v, %v", planned, err)
	}
	writeLifecycleFile(t, root, "planned.txt", "changed while approval was pending\n")
	revised, err := service.ResumeTurn(context.Background(), codingagent.ResumeTurnRequest{
		SessionID: session.ID, TurnID: planned.TurnID, InterruptID: planned.InterruptID,
		Decision: codingagent.ResolutionApproved, GrantScope: codingagent.PermissionGrantOnce,
	})
	if err != nil || revised.InterruptKind != "plan_approval" || revised.InterruptID == planned.InterruptID {
		t.Fatalf("drift-triggered revision = %#v, %v", revised, err)
	}
	snapshot, err := service.Snapshot(context.Background(), session.ID)
	if err != nil || snapshot.ActivePlan == nil || snapshot.ActivePlan.Version != 2 || snapshot.ActivePlan.WorkspaceDrift != nil || snapshot.ActivePlan.RevisionReason == "" || len(snapshot.PlanHistory) != 2 || snapshot.Metrics.WorkspaceDrifts != 1 || snapshot.Metrics.PlanRevisions != 1 {
		t.Fatalf("revised snapshot = %#v, %v", snapshot, err)
	}
	if len(snapshot.PlanHistory[1].Changes) == 0 {
		t.Fatalf("Plan version diff is missing: %#v", snapshot.PlanHistory)
	}
	assertPlanLifecycleEvents(t, events.values, codingagent.EventPlanReplanRequested)
	completed, err := service.ResumeTurn(context.Background(), codingagent.ResumeTurnRequest{
		SessionID: session.ID, TurnID: planned.TurnID, InterruptID: revised.InterruptID,
		Decision: codingagent.ResolutionApproved, GrantScope: codingagent.PermissionGrantOnce,
	})
	if err != nil || completed.Status != string(agent.RunCompleted) {
		t.Fatalf("revised Plan execution = %#v, %v", completed, err)
	}
	turn, err := products.LoadTurn(context.Background(), planned.TurnID)
	if err != nil || turn.PlanVersion != 2 || turn.ApprovedPlanVersion != 2 || turn.ApprovedPlanDigest != turn.PlanDigest || turn.WorkspaceDrift == nil || turn.WorkspaceDrift.Severity != codingagent.WorkspaceDriftMaterial || turn.WorkspaceDriftCount != 1 {
		t.Fatalf("drift-audited Product Turn = %#v, %v", turn, err)
	}
}

func TestPlanApprovalUnrelatedDriftContinuesExactVersion(t *testing.T) {
	root, service, products, _, session, _, events := newPlanLifecycleFixture(t, []llm.Message{
		lifecycleWorkspaceCall("workspace"), lifecyclePlanCall("plan-1", lifecyclePlanSubmission("Implement the reviewed Plan.")), finalAssistant(),
	})
	planned, err := service.StartTurn(context.Background(), codingagent.TurnRequest{SessionID: session.ID, Text: "Plan and implement the change", Mode: codingagent.TurnModePlan})
	if err != nil {
		t.Fatal(err)
	}
	writeLifecycleFile(t, root, "unrelated.txt", "unrelated pending change\n")
	completed, err := service.ResumeTurn(context.Background(), codingagent.ResumeTurnRequest{
		SessionID: session.ID, TurnID: planned.TurnID, InterruptID: planned.InterruptID,
		Decision: codingagent.ResolutionApproved, GrantScope: codingagent.PermissionGrantOnce,
	})
	if err != nil || completed.Status != string(agent.RunCompleted) {
		t.Fatalf("unrelated drift execution = %#v, %v", completed, err)
	}
	turn, err := products.LoadTurn(context.Background(), planned.TurnID)
	if err != nil || turn.PlanVersion != 1 || turn.ApprovedPlanVersion != 1 || turn.WorkspaceDrift == nil || turn.WorkspaceDrift.Severity != codingagent.WorkspaceDriftInformational || turn.WorkspaceDriftCount != 1 {
		t.Fatalf("unrelated drift Product Turn = %#v, %v", turn, err)
	}
	assertPlanLifecycleEvents(t, events.values, codingagent.EventPlanDriftDetected)
}

func TestMaterialDriftCannotFinishWithoutSubmittingANewPlan(t *testing.T) {
	root, service, products, _, session, _, _ := newPlanLifecycleFixture(t, []llm.Message{
		lifecycleWorkspaceCall("workspace"), lifecyclePlanCall("plan-1", lifecyclePlanSubmission("Implement the reviewed Plan.")), finalAssistant(),
	})
	planned, err := service.StartTurn(context.Background(), codingagent.TurnRequest{SessionID: session.ID, Text: "Plan and implement the change", Mode: codingagent.TurnModePlan})
	if err != nil {
		t.Fatal(err)
	}
	writeLifecycleFile(t, root, "planned.txt", "material change\n")
	result, err := service.ResumeTurn(context.Background(), codingagent.ResumeTurnRequest{
		SessionID: session.ID, TurnID: planned.TurnID, InterruptID: planned.InterruptID,
		Decision: codingagent.ResolutionApproved, GrantScope: codingagent.PermissionGrantOnce,
	})
	if err == nil || result.Status != string(agent.RunFailed) {
		t.Fatalf("missing mandatory revised Plan = %#v, %v", result, err)
	}
	turn, loadErr := products.LoadTurn(context.Background(), planned.TurnID)
	if loadErr != nil || turn.Status != codingagent.TurnFailed || turn.Phase != codingagent.TurnPhasePlanning || turn.PlanVersion != 1 || turn.ApprovedPlanVersion != 0 || len(turn.Runs) != 2 {
		t.Fatalf("failed mandatory revision Product Turn = %#v, %v", turn, loadErr)
	}
}

func TestExecutingAgentCanPauseForUserApprovedReplan(t *testing.T) {
	replanArguments := json.RawMessage(`{"reason_code":"invalid_assumption","summary":"The approved Plan assumes an API that the current implementation does not provide."}`)
	replanCall := llm.Message{Role: llm.RoleAssistant, StopReason: llm.StopReasonToolUse, Content: []llm.Content{{Type: llm.ContentToolCall, ToolCall: &llm.ToolCall{ID: "replan-1", Name: "request_plan_replan", Arguments: replanArguments}}}}
	_, service, products, agentSessions, session, model, events := newPlanLifecycleFixture(t, []llm.Message{
		lifecycleWorkspaceCall("workspace"), lifecyclePlanCall("plan-1", lifecyclePlanSubmission("Implement the initial Plan.")),
		replanCall, lifecyclePlanCall("plan-2", lifecyclePlanSubmission("Implement the revised Plan after validating the API.")), finalAssistant(),
	})
	planned, err := service.StartTurn(context.Background(), codingagent.TurnRequest{SessionID: session.ID, Text: "Plan and implement the change", Mode: codingagent.TurnModePlan})
	if err != nil {
		t.Fatal(err)
	}
	paused, err := service.ResumeTurn(context.Background(), codingagent.ResumeTurnRequest{
		SessionID: session.ID, TurnID: planned.TurnID, InterruptID: planned.InterruptID,
		Decision: codingagent.ResolutionApproved, GrantScope: codingagent.PermissionGrantOnce,
	})
	if err != nil || paused.Status != string(agent.RunInterrupted) || paused.InterruptKind != "plan_replan_approval" {
		t.Fatalf("execution replan boundary = %#v, %v", paused, err)
	}
	if len(model.responses) != 2 {
		t.Fatalf("execution continued past the replan boundary: remaining responses = %d", len(model.responses))
	}
	service = newPlanLifecycleService(t, products, agentSessions, model, events)
	snapshot, err := service.Snapshot(context.Background(), session.ID)
	if err != nil || !snapshot.PendingPlanReplan || snapshot.ActiveTurn == nil || snapshot.ActiveTurn.Phase != codingagent.TurnPhaseNeedsReplan || len(snapshot.PendingInterrupts) != 1 || snapshot.PendingInterrupts[0].PlanReplanReason != codingagent.PlanReplanInvalidAssumption {
		t.Fatalf("pending replan snapshot = %#v, %v", snapshot, err)
	}
	revised, err := service.ResumeTurn(context.Background(), codingagent.ResumeTurnRequest{
		SessionID: session.ID, TurnID: planned.TurnID, InterruptID: paused.InterruptID,
		Decision: codingagent.ResolutionApproved, GrantScope: codingagent.PermissionGrantOnce,
	})
	if err != nil || revised.InterruptKind != "plan_approval" {
		t.Fatalf("approved return to planning = %#v, %v", revised, err)
	}
	completed, err := service.ResumeTurn(context.Background(), codingagent.ResumeTurnRequest{
		SessionID: session.ID, TurnID: planned.TurnID, InterruptID: revised.InterruptID,
		Decision: codingagent.ResolutionApproved, GrantScope: codingagent.PermissionGrantOnce,
	})
	if err != nil || completed.Status != string(agent.RunCompleted) {
		t.Fatalf("replanned execution = %#v, %v", completed, err)
	}
	turn, err := products.LoadTurn(context.Background(), planned.TurnID)
	if err != nil || turn.PlanVersion != 2 || turn.ApprovedPlanVersion != 2 || turn.PlanReplan == nil || turn.PlanReplan.Decision != "replan" || turn.PlanReplanCount != 1 || len(turn.Runs) != 5 {
		t.Fatalf("replanned Product Turn = %#v, %v", turn, err)
	}
	assertPlanLifecycleEvents(t, events.values, codingagent.EventPlanReplanRequested, codingagent.EventPlanReplanResolved)
	durable, err := agentSessions.Load(context.Background(), session.AgentSessionID)
	if err != nil {
		t.Fatal(err)
	}
	userEntries := 0
	for _, entry := range durable.Entries {
		if entry.Message != nil && entry.Message.Role == llm.RoleUser {
			userEntries++
		}
	}
	if userEntries != 1 {
		t.Fatalf("replan appended synthetic user entries: %d", userEntries)
	}
}

func TestExecutingAgentReplanCanBeDeclinedWithoutNewRun(t *testing.T) {
	replanCall := llm.Message{Role: llm.RoleAssistant, StopReason: llm.StopReasonToolUse, Content: []llm.Content{{Type: llm.ContentToolCall, ToolCall: &llm.ToolCall{ID: "replan-continue", Name: "request_plan_replan", Arguments: json.RawMessage(`{"reason_code":"material_scope_change","summary":"A broader refactor may be useful but is not required for the approved fix."}`)}}}}
	_, service, products, _, session, _, events := newPlanLifecycleFixture(t, []llm.Message{
		lifecycleWorkspaceCall("workspace"), lifecyclePlanCall("plan-1", lifecyclePlanSubmission("Implement the exact reviewed fix.")), replanCall, finalAssistant(),
	})
	planned, err := service.StartTurn(context.Background(), codingagent.TurnRequest{SessionID: session.ID, Text: "Plan and implement the change", Mode: codingagent.TurnModePlan})
	if err != nil {
		t.Fatal(err)
	}
	paused, err := service.ResumeTurn(context.Background(), codingagent.ResumeTurnRequest{SessionID: session.ID, TurnID: planned.TurnID, InterruptID: planned.InterruptID, Decision: codingagent.ResolutionApproved})
	if err != nil || paused.InterruptKind != "plan_replan_approval" {
		t.Fatalf("execution pause = %#v, %v", paused, err)
	}
	completed, err := service.ResumeTurn(context.Background(), codingagent.ResumeTurnRequest{SessionID: session.ID, TurnID: planned.TurnID, InterruptID: paused.InterruptID, Decision: codingagent.ResolutionDenied})
	if err != nil || completed.Status != string(agent.RunCompleted) {
		t.Fatalf("continue approved Plan = %#v, %v", completed, err)
	}
	turn, err := products.LoadTurn(context.Background(), planned.TurnID)
	if err != nil || turn.Phase != codingagent.TurnPhaseExecuting || turn.PlanReplan == nil || turn.PlanReplan.Decision != "continue" || len(turn.Runs) != 3 {
		t.Fatalf("continued Product Turn = %#v, %v", turn, err)
	}
	assertPlanLifecycleEvents(t, events.values, codingagent.EventPlanReplanRequested, codingagent.EventPlanReplanResolved)
}

func newPlanLifecycleFixture(t *testing.T, responses []llm.Message) (string, *codingagent.Service, *codingmemory.Repository, agentsession.Repository, codingagent.Session, *sequentialModel, *productEvents) {
	t.Helper()
	root := t.TempDir()
	runLifecycleGit(t, root, "init", "--quiet")
	runLifecycleGit(t, root, "config", "user.name", "CodePilot Test")
	runLifecycleGit(t, root, "config", "user.email", "test@example.invalid")
	writeLifecycleFile(t, root, "planned.txt", "baseline\n")
	writeLifecycleFile(t, root, "unrelated.txt", "baseline\n")
	runLifecycleGit(t, root, "add", "planned.txt", "unrelated.txt")
	runLifecycleGit(t, root, "commit", "--quiet", "-m", "initial")
	products := codingmemory.NewRepository()
	now := time.Now().UTC()
	workspace := codingagent.Workspace{ID: "workspace-p3", DisplayName: "p3", GitCommonDir: filepath.Join(root, ".git"), Trusted: true, CreatedAt: now, UpdatedAt: now}
	worktree := codingagent.Worktree{ID: "worktree-p3", WorkspaceID: workspace.ID, Root: root, GitDir: workspace.GitCommonDir, CreatedAt: now, LastUsedAt: now}
	if err := products.SaveWorkspace(context.Background(), workspace); err != nil {
		t.Fatal(err)
	}
	if err := products.SaveWorktree(context.Background(), worktree); err != nil {
		t.Fatal(err)
	}
	agentSessions := agentsession.NewMemoryRepository()
	model := &sequentialModel{responses: responses}
	events := &productEvents{}
	service := newPlanLifecycleService(t, products, agentSessions, model, events)
	session, err := service.CreateSession(context.Background(), codingagent.Session{
		ID: "coding-p3", AgentSessionID: "agent-p3", WorkspaceID: workspace.ID, WorktreeID: worktree.ID,
		ProviderProfileID: "profile-1", ModelID: "model-1", PermissionMode: codingagent.PermissionAsk,
	})
	if err != nil {
		t.Fatal(err)
	}
	return root, service, products, agentSessions, session, model, events
}

func newPlanLifecycleService(t *testing.T, products *codingmemory.Repository, agentSessions agentsession.Repository, model *sequentialModel, events *productEvents) *codingagent.Service {
	t.Helper()
	contexts, err := contextmanager.NewManager()
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := agent.NewRuntime(agent.Dependencies{Models: sequentialModelFactory{model: model}, Contexts: contexts, Sessions: agentSessions})
	if err != nil {
		t.Fatal(err)
	}
	service, err := codingagent.NewService(codingagent.Dependencies{
		Sessions: products, Turns: products, Plans: products, AgentSessions: agentSessions, Worktrees: products,
		Agent: runtime, Tools: emptyToolFactory{}, Prompts: staticPrompt{}, Events: events,
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func assertPlanLifecycleEvents(t *testing.T, events []codingagent.Event, expected ...codingagent.EventKind) {
	t.Helper()
	found := make(map[codingagent.EventKind]bool, len(expected))
	for _, event := range events {
		if event.Payload.Plan == nil {
			continue
		}
		found[event.Kind] = true
	}
	for _, kind := range expected {
		if !found[kind] {
			t.Fatalf("Plan lifecycle event %q is missing from %#v", kind, events)
		}
	}
}

func lifecyclePlanSubmission(goal string) codingagent.PlanSubmission {
	return codingagent.PlanSubmission{
		Goal: goal, Scope: codingagent.PlanScope{Included: []string{"planned.txt"}},
		Findings: []string{"The planned file is present."}, Risks: []string{"Concurrent workspace changes may invalidate the Plan."},
		Steps:              []codingagent.PlanStep{{ID: "implement", Goal: "Update the planned file.", Files: []string{"planned.txt"}, Validation: []string{"Verify the updated file."}}},
		AcceptanceCriteria: []string{"The reviewed change is complete."}, RecommendedStrategy: codingagent.ExecutionSingle,
		WorkspaceRelevant: true, CompletionMode: codingagent.PlanCompletionExecute,
	}
}

func lifecycleWorkspaceCall(id string) llm.Message {
	return llm.Message{Role: llm.RoleAssistant, StopReason: llm.StopReasonToolUse, Content: []llm.Content{{Type: llm.ContentToolCall, ToolCall: &llm.ToolCall{ID: id, Name: "request_workspace_context", Arguments: json.RawMessage(`{"reason":"The implementation depends on current workspace facts."}`)}}}}
}

func lifecyclePlanCall(id string, submission codingagent.PlanSubmission) llm.Message {
	arguments, _ := json.Marshal(submission)
	return llm.Message{Role: llm.RoleAssistant, StopReason: llm.StopReasonToolUse, Content: []llm.Content{{Type: llm.ContentToolCall, ToolCall: &llm.ToolCall{ID: id, Name: "exit_plan_mode", Arguments: arguments}}}}
}

func writeLifecycleFile(t *testing.T, root, relative, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(relative)), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runLifecycleGit(t *testing.T, root string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", arguments, err, output)
	}
}
