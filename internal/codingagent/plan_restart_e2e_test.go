package codingagent_test

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/eaglc/codepilot/internal/agent"
	"github.com/eaglc/codepilot/internal/codingagent"
	codingprompt "github.com/eaglc/codepilot/internal/codingagent/prompt"
	codingtools "github.com/eaglc/codepilot/internal/codingagent/tools"
	codingfile "github.com/eaglc/codepilot/internal/codingstore/file"
	"github.com/eaglc/codepilot/internal/contextmanager"
	"github.com/eaglc/codepilot/internal/llm"
	sessionfile "github.com/eaglc/codepilot/internal/sessionstore/file"
)

func TestPlanApprovalSurvivesFileStoreRestartAndExecutesOnce(t *testing.T) {
	worktreeRoot := t.TempDir()
	for _, arguments := range [][]string{{"init", "--quiet"}, {"config", "user.name", "CodePilot Test"}, {"config", "user.email", "test@example.invalid"}, {"commit", "--allow-empty", "--quiet", "-m", "initial"}} {
		command := exec.Command("git", append([]string{"-C", worktreeRoot}, arguments...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", arguments, err, output)
		}
	}
	stateDir := t.TempDir()
	products, err := codingfile.NewRepository(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	agentSessions, err := sessionfile.NewRepository(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	workspace := codingagent.Workspace{ID: "workspace-plan-restart", DisplayName: "plan-restart", GitCommonDir: filepath.Join(worktreeRoot, ".git"), Trusted: true, CreatedAt: now, UpdatedAt: now}
	worktree := codingagent.Worktree{ID: "worktree-plan-restart", WorkspaceID: workspace.ID, Root: worktreeRoot, GitDir: workspace.GitCommonDir, CreatedAt: now, LastUsedAt: now}
	if err := products.SaveWorkspace(context.Background(), workspace); err != nil {
		t.Fatal(err)
	}
	if err := products.SaveWorktree(context.Background(), worktree); err != nil {
		t.Fatal(err)
	}
	planArguments, _ := json.Marshal(codingagent.PlanSubmission{
		Goal: "Verify durable Plan approval.", Scope: codingagent.PlanScope{Included: []string{"internal/codingagent"}},
		Findings: []string{"The test uses real file repositories."}, Risks: []string{"Execution must not be duplicated after restart."},
		Steps:              []codingagent.PlanStep{{ID: "verify", Goal: "Continue the approved Plan once.", Files: []string{"internal/codingagent/service.go"}, Validation: []string{"Reopen both repositories."}}},
		AcceptanceCriteria: []string{"The exact Plan remains approvable after restart."}, RecommendedStrategy: codingagent.ExecutionSingle,
		WorkspaceRelevant: true, CompletionMode: codingagent.PlanCompletionExecute,
	})
	model := &sequentialModel{responses: []llm.Message{
		{Role: llm.RoleAssistant, Provider: "profile-1", Model: "model-1", StopReason: llm.StopReasonToolUse, Content: []llm.Content{{Type: llm.ContentToolCall, ToolCall: &llm.ToolCall{ID: "workspace-restart", Name: "request_workspace_context", Arguments: json.RawMessage(`{"reason":"The implementation Plan depends on the current repository."}`)}}}},
		{Role: llm.RoleAssistant, Provider: "profile-1", Model: "model-1", StopReason: llm.StopReasonToolUse, Content: []llm.Content{{Type: llm.ContentToolCall, ToolCall: &llm.ToolCall{ID: "plan-restart", Name: "exit_plan_mode", Arguments: planArguments}}}},
		finalAssistant(),
	}}
	newService := func(t *testing.T, products *codingfile.Repository, agentSessions *sessionfile.Repository) *codingagent.Service {
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
			Agent: runtime, Tools: codingtools.NewFactory(codingtools.Options{}), Prompts: codingprompt.NewBuilder(), Events: &productEvents{},
		})
		if err != nil {
			t.Fatal(err)
		}
		return service
	}
	first := newService(t, products, agentSessions)
	session, err := first.CreateSession(context.Background(), codingagent.Session{
		ID: "coding-plan-restart", AgentSessionID: "agent-plan-restart", WorkspaceID: workspace.ID, WorktreeID: worktree.ID,
		ProviderProfileID: "profile-1", ModelID: "model-1", PermissionMode: codingagent.PermissionAsk,
	})
	if err != nil {
		t.Fatal(err)
	}
	waiting, err := first.StartTurn(context.Background(), codingagent.TurnRequest{SessionID: session.ID, Text: "Plan the durable restart change", Mode: codingagent.TurnModePlan})
	if err != nil || waiting.InterruptKind != "plan_approval" {
		t.Fatalf("initial Plan = %#v, %v", waiting, err)
	}

	reopenedProducts, err := codingfile.NewRepository(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	reopenedAgentSessions, err := sessionfile.NewRepository(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	second := newService(t, reopenedProducts, reopenedAgentSessions)
	snapshot, err := second.Snapshot(context.Background(), session.ID)
	if err != nil || !snapshot.PendingPlanApproval || snapshot.ActivePlan == nil || snapshot.ActivePlan.Version != 1 || len(snapshot.PendingInterrupts) != 1 || snapshot.PendingInterrupts[0].InterruptID != waiting.InterruptID {
		t.Fatalf("reopened Plan snapshot = %#v, %v", snapshot, err)
	}
	completed, err := second.ResumeTurn(context.Background(), codingagent.ResumeTurnRequest{
		SessionID: session.ID, TurnID: waiting.TurnID, InterruptID: waiting.InterruptID,
		Decision: codingagent.ResolutionApproved, GrantScope: codingagent.PermissionGrantOnce,
	})
	if err != nil || completed.Status != string(agent.RunCompleted) || completed.Response != "done" {
		t.Fatalf("restarted Plan approval = %#v, %v", completed, err)
	}
	turn, err := reopenedProducts.LoadTurn(context.Background(), waiting.TurnID)
	if err != nil || turn.Status != codingagent.TurnCompleted || turn.Phase != codingagent.TurnPhaseExecuting || len(turn.Runs) != 3 {
		t.Fatalf("restarted Product Turn = %#v, %v", turn, err)
	}
	if len(model.responses) != 0 {
		t.Fatalf("unused model responses = %d", len(model.responses))
	}
}
