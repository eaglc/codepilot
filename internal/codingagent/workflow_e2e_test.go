package codingagent_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eaglc/codepilot/internal/agent"
	agentsession "github.com/eaglc/codepilot/internal/agent/session"
	"github.com/eaglc/codepilot/internal/codingagent"
	codingprompt "github.com/eaglc/codepilot/internal/codingagent/prompt"
	codingtools "github.com/eaglc/codepilot/internal/codingagent/tools"
	workspaceinfra "github.com/eaglc/codepilot/internal/codingagent/workspace"
	codingfile "github.com/eaglc/codepilot/internal/codingstore/file"
	"github.com/eaglc/codepilot/internal/contextmanager"
	"github.com/eaglc/codepilot/internal/llm"
	sessionfile "github.com/eaglc/codepilot/internal/sessionstore/file"
	"github.com/eaglc/codepilot/internal/workflow"
)

func TestSingleAgentWorkflowRepairsRealRepositoryWithFileStoresAndTools(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("Git is required for Workflow E2E")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	files := map[string]string{
		"go.mod":       "module example.invalid/workflow-calc\n\ngo 1.23\n",
		"calc.go":      "package calc\n\nfunc Add(a, b int) int {\n\treturn a - b\n}\n",
		"calc_test.go": "package calc\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif got := Add(2, 3); got != 5 {\n\t\tt.Fatalf(\"Add(2, 3) = %d, want 5\", got)\n\t}\n}\n",
	}
	patch := "diff --git a/calc.go b/calc.go\n--- a/calc.go\n+++ b/calc.go\n@@ -1,5 +1,5 @@\n package calc\n \n func Add(a, b int) int {\n-\treturn a - b\n+\treturn a + b\n }\n"
	root := createBrokenGitRepository(t, files)
	check := commandSpec{name: "go", args: []string{"test", "./..."}}
	if output, err := runE2ECommand(ctx, root, check); err == nil {
		t.Fatalf("broken Workflow fixture unexpectedly passed before repair: %s", output)
	}

	stateDir := filepath.Join(t.TempDir(), "state")
	products, err := codingfile.NewRepository(stateDir)
	if err != nil {
		t.Fatalf("create product store: %v", err)
	}
	agentSessions, err := sessionfile.NewRepository(stateDir)
	if err != nil {
		t.Fatalf("create Agent store: %v", err)
	}
	resolved, err := workspaceinfra.ResolveWorktree(ctx, root)
	if err != nil {
		t.Fatalf("resolve worktree: %v", err)
	}
	now := time.Now().UTC()
	workspace := codingagent.Workspace{
		ID: codingagent.WorkspaceID("workspace-workflow-e2e"), DisplayName: "workflow e2e", GitCommonDir: resolved.GitCommonDir,
		RepositoryFingerprint: resolved.RepositoryFingerprint, Trusted: true, CreatedAt: now, UpdatedAt: now,
	}
	worktree := codingagent.Worktree{
		ID: codingagent.WorktreeID("worktree-workflow-e2e"), WorkspaceID: workspace.ID, Root: resolved.Root,
		GitDir: resolved.GitDir, CreatedAt: now, LastUsedAt: now,
	}
	if err := products.SaveWorkspace(ctx, workspace); err != nil {
		t.Fatalf("save workspace: %v", err)
	}
	if err := products.SaveWorktree(ctx, worktree); err != nil {
		t.Fatalf("save worktree: %v", err)
	}
	security, err := codingagent.NewSecurityPolicy(nil)
	if err != nil {
		t.Fatalf("create security policy: %v", err)
	}
	model := &workflowE2EModel{profile: "scripted", model: "scripted", patch: patch}
	contexts, err := contextmanager.NewManager()
	if err != nil {
		t.Fatalf("create context manager: %v", err)
	}
	runtimeAgent, err := agent.NewRuntime(agent.Dependencies{
		Models: workflowE2EModelFactory{model: model}, Contexts: contexts, Sessions: agentSessions, DataPolicy: security,
	})
	if err != nil {
		t.Fatalf("create Agent runtime: %v", err)
	}
	service, err := codingagent.NewService(codingagent.Dependencies{
		Sessions: products, Turns: products, Plans: products, Workflows: products, AgentSessions: agentSessions, Worktrees: products,
		Agent: runtimeAgent, Tools: codingtools.NewFactory(codingtools.Options{Artifacts: products, Security: security}),
		Prompts: codingprompt.NewBuilder(), Events: &e2eEventSink{},
		Limits: agent.RunLimits{MaxSteps: 8, MaxDuration: time.Minute, MaxModelAttempts: 1, MaxToolCalls: 8, MaxRepeatedToolCalls: 3, MaxOutputBytes: 1 << 20, MaxNoProgressSteps: 3},
	})
	if err != nil {
		t.Fatalf("create Coding service: %v", err)
	}
	session, err := service.CreateSession(ctx, codingagent.Session{
		ID: "session-workflow-e2e", AgentSessionID: "agent-workflow-e2e", WorkspaceID: workspace.ID, WorktreeID: worktree.ID,
		ProviderProfileID: "scripted", ModelID: "scripted", PermissionMode: codingagent.PermissionAutoEdit,
	})
	if err != nil {
		t.Fatalf("create Coding session: %v", err)
	}
	planned, err := service.StartTurn(ctx, codingagent.TurnRequest{SessionID: session.ID, Text: "Plan and repair the failing Go test.", Mode: codingagent.TurnModePlan})
	if err != nil || planned.InterruptKind != "plan_approval" {
		t.Fatalf("Plan result = %#v, %v", planned, err)
	}
	result, err := service.ResumeTurn(ctx, codingagent.ResumeTurnRequest{
		SessionID: session.ID, TurnID: planned.TurnID, InterruptID: planned.InterruptID,
		Decision: codingagent.ResolutionApproved, GrantScope: codingagent.PermissionGrantOnce, Strategy: codingagent.ExecutionWorkflowSingle,
	})
	if err != nil || result.Status != string(workflow.StatusCompleted) {
		t.Fatalf("Workflow result = %#v, %v", result, err)
	}
	content, err := os.ReadFile(filepath.Join(root, "calc.go"))
	if err != nil || !strings.Contains(string(content), "return a + b") {
		t.Fatalf("Workflow did not apply expected change: content=%q err=%v", content, err)
	}
	if output, err := runE2ECommand(ctx, root, check); err != nil {
		t.Fatalf("repaired Workflow fixture failed: %v\n%s", err, output)
	}
	if len(model.requests) != 6 || !requestHasToolResult(model.requests[3], "apply_patch") || !requestHasToolResult(model.requests[5], "read_file") {
		t.Fatalf("model did not receive both real Tool Results: requests=%#v", model.requests)
	}

	snapshot, err := service.Snapshot(ctx, session.ID)
	if err != nil {
		t.Fatalf("project Workflow snapshot: %v", err)
	}
	if snapshot.ActiveWorkflow == nil || snapshot.ActiveWorkflow.Status != string(workflow.StatusCompleted) || snapshot.ActiveWorkflow.CompletedNodes != 2 || snapshot.ActiveWorkflow.UsedRuns != 2 || snapshot.ActiveWorkflow.UsedAgentSteps != 4 {
		t.Fatalf("Workflow snapshot = %#v", snapshot.ActiveWorkflow)
	}
	if snapshot.Metrics.Workflow.CompletedTurns != 1 || snapshot.Metrics.Workflow.NodeRuns != 2 || snapshot.Metrics.Workflow.Steps != 4 || !snapshotHasAppliedDiff(snapshot, "calc.go") {
		t.Fatalf("Workflow metrics/transcript = %#v / %#v", snapshot.Metrics.Workflow, snapshot.Transcript)
	}

	reopenedProducts, err := codingfile.NewRepository(stateDir)
	if err != nil {
		t.Fatalf("reopen product store: %v", err)
	}
	turn, err := reopenedProducts.LoadTurn(ctx, planned.TurnID)
	if err != nil || turn.Status != codingagent.TurnCompleted || turn.WorkflowID == "" {
		t.Fatalf("reloaded Workflow Turn = %#v, %v", turn, err)
	}
	durableWorkflow, err := reopenedProducts.LoadWorkflow(ctx, workflow.ID(turn.WorkflowID))
	if err != nil || durableWorkflow.Status != workflow.StatusCompleted || durableWorkflow.Budget.UsedAgentSteps != 4 {
		t.Fatalf("reloaded Workflow = %#v, %v", durableWorkflow, err)
	}
	usedRuns := 0
	for _, node := range durableWorkflow.Nodes {
		usedRuns += node.Attempts
		if node.Status != workflow.NodeCompleted || node.ResultRef == "" {
			t.Fatalf("reloaded Workflow node = %#v", node)
		}
	}
	if usedRuns != 2 {
		t.Fatalf("reloaded Workflow used runs = %d", usedRuns)
	}
	reopenedAgents, err := sessionfile.OpenRepository(stateDir)
	if err != nil {
		t.Fatalf("reopen Agent store: %v", err)
	}
	durableAgent, err := reopenedAgents.Load(ctx, session.AgentSessionID)
	if err != nil {
		t.Fatalf("reload Agent session: %v", err)
	}
	if recovery := agentsession.BuildRecoveryPlan(durableAgent); len(recovery.Actions) != 0 {
		t.Fatalf("completed Workflow E2E left recovery work: %#v", recovery)
	}
	if !durableHasToolLifecycle(durableAgent, "apply_patch") || !durableHasToolLifecycle(durableAgent, "read_file") {
		t.Fatalf("reloaded Agent journal lacks complete Workflow Tool lifecycle: %#v", durableAgent.Records)
	}
}

type workflowE2EModelFactory struct{ model *workflowE2EModel }

func (factory workflowE2EModelFactory) CreateModel(context.Context, llm.ModelRef) (llm.ChatModel, error) {
	return factory.model, nil
}

type workflowE2EModel struct {
	profile, model, patch string
	requests              []llm.ChatRequest
}

func (*workflowE2EModel) Complete(context.Context, llm.ChatRequest) (llm.Message, error) {
	return llm.Message{}, errors.New("Workflow E2E expects streaming Agent calls")
}

func (model *workflowE2EModel) Stream(_ context.Context, request llm.ChatRequest) (llm.Stream, error) {
	model.requests = append(model.requests, request)
	var response llm.Message
	switch len(model.requests) {
	case 1:
		response = workflowE2EToolCall(model, "workflow-context", "request_workspace_context", json.RawMessage(`{"reason":"The repair Plan depends on the current worktree."}`))
	case 2:
		arguments, _ := json.Marshal(codingagent.PlanSubmission{
			Goal:     "Repair the failing calculation and validate the combined result.",
			Scope:    codingagent.PlanScope{Included: []string{"calc.go"}},
			Findings: []string{"The failing calculation is implemented in calc.go."},
			Risks:    []string{"The repair must preserve the public function contract."},
			Steps: []codingagent.PlanStep{{
				ID: "repair", Goal: "Repair Add to return the sum.", Files: []string{"calc.go"},
				Validation: []string{"calc.go contains the corrected addition."}, Role: workflow.RoleImplement,
				FailureAction: workflow.FailureRetry, MaxAttempts: 2,
			}},
			AcceptanceCriteria:  []string{"The repository test passes after the repair."},
			RecommendedStrategy: codingagent.ExecutionWorkflowSingle, WorkspaceRelevant: true, CompletionMode: codingagent.PlanCompletionExecute,
		})
		response = workflowE2EToolCall(model, "workflow-plan", "exit_plan_mode", arguments)
	case 3:
		arguments, _ := json.Marshal(map[string]string{"patch": model.patch, "intent": "Repair Add according to the approved Workflow node"})
		response = workflowE2EToolCall(model, "workflow-apply", "apply_patch", arguments)
	case 4:
		response = workflowE2EText(model, "The implementation node repaired calc.go.")
	case 5:
		response = workflowE2EToolCall(model, "workflow-read", "read_file", json.RawMessage(`{"path":"calc.go"}`))
	case 6:
		response = workflowE2EText(model, "The combined Workflow result is validated against the approved Plan.")
	default:
		return nil, fmt.Errorf("unexpected Workflow E2E model step %d", len(model.requests))
	}
	return &patchModelStream{events: []llm.StreamEvent{{Kind: llm.StreamResponseFinished, Message: &response}}}, nil
}

func workflowE2EToolCall(model *workflowE2EModel, id, name string, arguments json.RawMessage) llm.Message {
	return llm.Message{
		Role: llm.RoleAssistant, Provider: model.profile, Model: model.model, StopReason: llm.StopReasonToolUse, Timestamp: time.Now().UTC(),
		Content: []llm.Content{{Type: llm.ContentToolCall, ToolCall: &llm.ToolCall{ID: id, Name: name, Arguments: arguments}}},
	}
}

func workflowE2EText(model *workflowE2EModel, value string) llm.Message {
	return llm.Message{
		Role: llm.RoleAssistant, Provider: model.profile, Model: model.model, StopReason: llm.StopReasonStop, Timestamp: time.Now().UTC(),
		Content: []llm.Content{{Type: llm.ContentText, Text: value}},
	}
}
