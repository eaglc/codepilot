package codingagent_test

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eaglc/codepilot/internal/agent"
	agentsession "github.com/eaglc/codepilot/internal/agent/session"
	"github.com/eaglc/codepilot/internal/codingagent"
	codingmemory "github.com/eaglc/codepilot/internal/codingstore/memory"
	"github.com/eaglc/codepilot/internal/contextmanager"
	"github.com/eaglc/codepilot/internal/llm"
	"github.com/eaglc/codepilot/internal/tool"
	"github.com/eaglc/codepilot/internal/workflow"
)

func TestP5SerialMultiAgentWorkflowUsesIndependentRoleSessionsAndMainReview(t *testing.T) {
	root := t.TempDir()
	for _, arguments := range [][]string{{"init", "--quiet"}, {"config", "user.name", "CodePilot Test"}, {"config", "user.email", "test@example.invalid"}, {"commit", "--allow-empty", "--quiet", "-m", "initial"}} {
		command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", arguments, err, output)
		}
	}
	products := codingmemory.NewRepository()
	now := time.Now().UTC()
	workspace := codingagent.Workspace{ID: "workspace-p5", DisplayName: "p5", GitCommonDir: filepath.Join(root, ".git"), Trusted: true, CreatedAt: now, UpdatedAt: now}
	worktree := codingagent.Worktree{ID: "worktree-p5", WorkspaceID: workspace.ID, Root: root, GitDir: workspace.GitCommonDir, CreatedAt: now, LastUsedAt: now}
	if err := products.SaveWorkspace(context.Background(), workspace); err != nil {
		t.Fatal(err)
	}
	if err := products.SaveWorktree(context.Background(), worktree); err != nil {
		t.Fatal(err)
	}
	planArguments, _ := json.Marshal(codingagent.PlanSubmission{
		Goal: "Execute two isolated roles and aggregate their evidence.", Scope: codingagent.PlanScope{Included: []string{"bounded implementation and validation"}},
		Findings: []string{"Independent role context improves the result."}, Risks: []string{"Only structured child results may cross the boundary."},
		Steps: []codingagent.PlanStep{
			{ID: "implement", Goal: "Implement the bounded change.", Files: []string{"internal"}, Validation: []string{"Implementation evidence is recorded."}, Role: workflow.RoleImplement, FailureAction: workflow.FailureRetry, MaxAttempts: 1},
			{ID: "validate", Goal: "Validate the bounded change independently.", DependsOn: []string{"implement"}, Files: []string{"internal"}, Validation: []string{"Validation evidence is recorded."}, Role: workflow.RoleValidate, FailureAction: workflow.FailureBlock, MaxAttempts: 1},
		},
		AcceptanceCriteria: []string{"The parent validates and reviews the combined result."}, RecommendedStrategy: codingagent.ExecutionWorkflowMultiSerial,
		WorkspaceRelevant: true, CompletionMode: codingagent.PlanCompletionExecute,
	})
	responses := []llm.Message{
		p5ToolCall("p5-context", "request_workspace_context", json.RawMessage(`{"reason":"The requested Workflow depends on current repository facts."}`)),
		p5ToolCall("p5-plan", "exit_plan_mode", planArguments),
		p5ChildResult("p5-implement-result", "Implemented the bounded change.", []string{"internal/change.go"}, []string{"Implementation check passed."}),
		p5ChildResult("p5-validate-result", "Validated the bounded change.", nil, []string{"Independent validation passed."}),
		finalAssistant(),
		finalAssistant(),
	}
	model := &sequentialModel{responses: responses}
	agentSessions := agentsession.NewMemoryRepository()
	contexts, _ := contextmanager.NewManager()
	runtime, err := agent.NewRuntime(agent.Dependencies{Models: sequentialModelFactory{model: model}, Contexts: contexts, Sessions: agentSessions})
	if err != nil {
		t.Fatal(err)
	}
	service, err := codingagent.NewService(codingagent.Dependencies{
		Sessions: products, Turns: products, Plans: products, Workflows: products, Children: products,
		AgentSessions: agentSessions, Worktrees: products, Agent: runtime, Tools: emptyToolFactory{}, Prompts: staticPrompt{}, Events: &productEvents{},
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := service.CreateSession(context.Background(), codingagent.Session{
		ID: "coding-p5", AgentSessionID: "agent-p5", WorkspaceID: workspace.ID, WorktreeID: worktree.ID,
		ProviderProfileID: "profile-1", ModelID: "model-1", PermissionMode: codingagent.PermissionAsk,
	})
	if err != nil {
		t.Fatal(err)
	}
	planned, err := service.StartTurn(context.Background(), codingagent.TurnRequest{SessionID: session.ID, Text: "Plan and execute with serial multi-Agent roles", Mode: codingagent.TurnModePlan})
	if err != nil || planned.InterruptKind != "plan_approval" {
		t.Fatalf("planned = %#v, %v", planned, err)
	}
	beforeApproval, err := products.ListChildAgents(context.Background(), planned.TurnID)
	if err != nil || len(beforeApproval) != 0 {
		t.Fatalf("ordinary Planning created automatic children = %#v, %v", beforeApproval, err)
	}
	executed, err := service.ResumeTurn(context.Background(), codingagent.ResumeTurnRequest{
		SessionID: session.ID, TurnID: planned.TurnID, InterruptID: planned.InterruptID,
		Decision: codingagent.ResolutionApproved, GrantScope: codingagent.PermissionGrantOnce, Strategy: codingagent.ExecutionWorkflowMultiSerial,
	})
	if err != nil || executed.Status != string(workflow.StatusCompleted) {
		t.Fatalf("executed = %#v, %v", executed, err)
	}
	turn, err := products.LoadTurn(context.Background(), planned.TurnID)
	if err != nil || turn.Status != codingagent.TurnCompleted || turn.Strategy != codingagent.ExecutionWorkflowMultiSerial || len(turn.Runs) != 6 {
		t.Fatalf("turn = %#v, %v", turn, err)
	}
	children, err := products.ListChildAgents(context.Background(), turn.ID)
	if err != nil || len(children) != 2 {
		t.Fatalf("children = %#v, %v", children, err)
	}
	if children[0].Role == children[1].Role || children[0].AgentSessionID == children[1].AgentSessionID || children[0].RunID == children[1].RunID {
		t.Fatalf("children are not independent different-role executions: %#v", children)
	}
	for _, child := range children {
		if child.Status != codingagent.ChildAgentCompleted || child.PolicyVersion != 1 || child.Result == nil || child.Result.Status != codingagent.AgentTaskSucceeded {
			t.Fatalf("child result = %#v", child)
		}
		childSession := mustAgentSnapshot(t, agentSessions, child.AgentSessionID)
		if childSession.Metadata.ParentSessionID != session.AgentSessionID || len(childSession.Entries) == 0 {
			t.Fatalf("child Agent session = %#v", childSession.Metadata)
		}
	}
	durable, err := products.LoadWorkflow(context.Background(), workflow.ID(turn.WorkflowID))
	if err != nil || len(durable.Nodes) != 4 || workflow.NodeExecutor(durable.Nodes[0]) != workflow.ExecutorChild || workflow.NodeExecutor(durable.Nodes[1]) != workflow.ExecutorChild || workflow.NodeExecutor(durable.Nodes[2]) != workflow.ExecutorMain || workflow.NodeExecutor(durable.Nodes[3]) != workflow.ExecutorMain {
		t.Fatalf("durable multi-Agent Workflow = %#v, %v", durable, err)
	}
	if !strings.HasPrefix(durable.Nodes[0].ResultRef, "child:") || !strings.HasPrefix(durable.Nodes[1].ResultRef, "child:") {
		t.Fatalf("child result refs = %q, %q", durable.Nodes[0].ResultRef, durable.Nodes[1].ResultRef)
	}
	snapshot, err := service.Snapshot(context.Background(), session.ID)
	if err != nil || snapshot.ActiveWorkflow == nil || snapshot.ActiveWorkflow.Strategy != codingagent.ExecutionWorkflowMultiSerial || len(snapshot.ChildAgents) != 2 {
		t.Fatalf("snapshot = %#v, %v", snapshot, err)
	}
	parent := mustAgentSnapshot(t, agentSessions, session.AgentSessionID)
	parentUsers := 0
	for _, entry := range parent.Entries {
		if entry.Message != nil && entry.Message.Role == llm.RoleUser {
			parentUsers++
		}
	}
	if parentUsers != 1 {
		t.Fatalf("parent transcript has %d user entries; child transcripts leaked", parentUsers)
	}
}

func TestP5PlanExploreDelegationRunsOneReadOnlyChildAndReturnsStructuredEvidence(t *testing.T) {
	root := t.TempDir()
	for _, arguments := range [][]string{{"init", "--quiet"}, {"config", "user.name", "CodePilot Test"}, {"config", "user.email", "test@example.invalid"}, {"commit", "--allow-empty", "--quiet", "-m", "initial"}} {
		command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", arguments, err, output)
		}
	}
	products := codingmemory.NewRepository()
	now := time.Now().UTC()
	workspace := codingagent.Workspace{ID: "workspace-p5-explore", DisplayName: "p5-explore", GitCommonDir: filepath.Join(root, ".git"), Trusted: true, CreatedAt: now, UpdatedAt: now}
	worktree := codingagent.Worktree{ID: "worktree-p5-explore", WorkspaceID: workspace.ID, Root: root, GitDir: workspace.GitCommonDir, CreatedAt: now, LastUsedAt: now}
	if err := products.SaveWorkspace(context.Background(), workspace); err != nil {
		t.Fatal(err)
	}
	if err := products.SaveWorktree(context.Background(), worktree); err != nil {
		t.Fatal(err)
	}
	planArguments, _ := json.Marshal(codingagent.PlanSubmission{
		Goal: "Plan from focused exploration evidence.", Scope: codingagent.PlanScope{Included: []string{"internal"}},
		Findings: []string{"The exploration child found the relevant boundary."}, Risks: []string{"Evidence must remain read-only."},
		Steps:              []codingagent.PlanStep{{ID: "implement", Goal: "Implement after approval.", Files: []string{"internal"}, Validation: []string{"Run tests."}}},
		AcceptanceCriteria: []string{"The Plan cites the focused evidence."}, RecommendedStrategy: codingagent.ExecutionSingle,
		WorkspaceRelevant: true, CompletionMode: codingagent.PlanCompletionExecute,
	})
	responses := []llm.Message{
		p5ToolCall("explore-context", "request_workspace_context", json.RawMessage(`{"reason":"The Plan depends on the current internal architecture."}`)),
		p5ToolCall("delegate-explore", "delegate_plan_explore", json.RawMessage(`{"goal":"Inspect the internal coordination boundary.","read_paths":["internal"],"acceptance_criteria":["Identify the relevant boundary with evidence."]}`)),
		p5ChildResult("explore-result", "The coordination boundary is isolated.", nil, []string{"Read-only inspection completed."}),
		p5ToolCall("explore-plan", "exit_plan_mode", planArguments),
	}
	model := &sequentialModel{responses: responses}
	agentSessions := agentsession.NewMemoryRepository()
	contexts, _ := contextmanager.NewManager()
	runtime, err := agent.NewRuntime(agent.Dependencies{Models: sequentialModelFactory{model: model}, Contexts: contexts, Sessions: agentSessions})
	if err != nil {
		t.Fatal(err)
	}
	service, err := codingagent.NewService(codingagent.Dependencies{
		Sessions: products, Turns: products, Plans: products, Workflows: products, Children: products,
		AgentSessions: agentSessions, Worktrees: products, Agent: runtime, Tools: emptyToolFactory{}, Prompts: staticPrompt{}, Events: &productEvents{},
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := service.CreateSession(context.Background(), codingagent.Session{
		ID: "coding-p5-explore", AgentSessionID: "agent-p5-explore", WorkspaceID: workspace.ID, WorktreeID: worktree.ID,
		ProviderProfileID: "profile-1", ModelID: "model-1", PermissionMode: codingagent.PermissionAsk,
	})
	if err != nil {
		t.Fatal(err)
	}
	planned, err := service.StartTurn(context.Background(), codingagent.TurnRequest{SessionID: session.ID, Text: "Create a high-confidence implementation Plan", Mode: codingagent.TurnModePlan})
	if err != nil || planned.InterruptKind != "plan_approval" {
		t.Fatalf("planned = %#v, %v", planned, err)
	}
	children, err := products.ListChildAgents(context.Background(), planned.TurnID)
	if err != nil || len(children) != 1 || children[0].Kind != codingagent.ChildAgentPlanExplore || children[0].Role != workflow.RoleExplore || children[0].Status != codingagent.ChildAgentCompleted || len(children[0].Task.WritePaths) != 0 {
		t.Fatalf("Plan exploration children = %#v, %v", children, err)
	}
	turn, err := products.LoadTurn(context.Background(), planned.TurnID)
	if err != nil || turn.PendingPlanExploreID != "" || len(turn.Runs) != 4 || turn.Runs[2].ChildAgentID != children[0].ID || turn.Runs[2].Profile != codingagent.CapabilityExplore {
		t.Fatalf("Plan exploration Turn = %#v, %v", turn, err)
	}
	if len(model.requests) != 4 || !requestContainsText(model.requests[3], "The coordination boundary is isolated.") {
		t.Fatalf("parent Planning request did not receive structured child evidence")
	}
	parent := mustAgentSnapshot(t, agentSessions, session.AgentSessionID)
	parentUsers := 0
	for _, entry := range parent.Entries {
		if entry.Message != nil && entry.Message.Role == llm.RoleUser {
			parentUsers++
		}
	}
	if parentUsers != 1 {
		t.Fatalf("parent transcript has %d user messages; child transcript leaked", parentUsers)
	}
	if snapshot, err := service.Snapshot(context.Background(), session.ID); err != nil || len(snapshot.ChildAgents) != 1 || snapshot.ChildAgents[0].Conclusion == "" {
		t.Fatalf("Plan exploration snapshot = %#v, %v", snapshot.ChildAgents, err)
	}
}

type p5ApprovalToolFactory struct{}

func (p5ApprovalToolFactory) CreateTools(_ context.Context, scope codingagent.ToolScope) (*tool.Registry, error) {
	if scope.Profile == codingagent.CapabilityImplement {
		return tool.NewRegistry(approvalTool{})
	}
	return tool.NewRegistry()
}

func TestP5ChildApprovalIsProjectedAndResolvedThroughParentTask(t *testing.T) {
	root := t.TempDir()
	for _, arguments := range [][]string{{"init", "--quiet"}, {"config", "user.name", "CodePilot Test"}, {"config", "user.email", "test@example.invalid"}, {"commit", "--allow-empty", "--quiet", "-m", "initial"}} {
		command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", arguments, err, output)
		}
	}
	products := codingmemory.NewRepository()
	now := time.Now().UTC()
	workspace := codingagent.Workspace{ID: "workspace-p5-approval", DisplayName: "p5-approval", GitCommonDir: filepath.Join(root, ".git"), Trusted: true, CreatedAt: now, UpdatedAt: now}
	worktree := codingagent.Worktree{ID: "worktree-p5-approval", WorkspaceID: workspace.ID, Root: root, GitDir: workspace.GitCommonDir, CreatedAt: now, LastUsedAt: now}
	if err := products.SaveWorkspace(context.Background(), workspace); err != nil {
		t.Fatal(err)
	}
	if err := products.SaveWorktree(context.Background(), worktree); err != nil {
		t.Fatal(err)
	}
	planArguments, _ := json.Marshal(codingagent.PlanSubmission{
		Goal: "Exercise the unified child approval boundary.", Scope: codingagent.PlanScope{Included: []string{"internal"}},
		Findings: []string{"The child needs one approved operation."}, Risks: []string{"Approval identity must remain visible."},
		Steps: []codingagent.PlanStep{
			{ID: "implement", Goal: "Apply the approved bounded operation.", Files: []string{"internal"}, Validation: []string{"Operation completes."}, Role: workflow.RoleImplement, MaxAttempts: 1},
			{ID: "validate", Goal: "Validate the approved operation independently.", DependsOn: []string{"implement"}, Files: []string{"internal"}, Validation: []string{"Validation completes."}, Role: workflow.RoleValidate, MaxAttempts: 1},
		},
		AcceptanceCriteria: []string{"The parent resolves the child operation."}, RecommendedStrategy: codingagent.ExecutionWorkflowMultiSerial,
		WorkspaceRelevant: true, CompletionMode: codingagent.PlanCompletionExecute,
	})
	responses := []llm.Message{
		p5ToolCall("approval-context", "request_workspace_context", json.RawMessage(`{"reason":"The approved operation depends on this workspace."}`)),
		p5ToolCall("approval-plan", "exit_plan_mode", planArguments),
		p5ToolCall("child-edit", "apply_edit", json.RawMessage(`{}`)),
		p5ChildResult("child-approved-result", "Applied the user-approved operation.", []string{"internal/main.go"}, []string{"Operation completed."}),
		p5ChildResult("child-validation-result", "Validated the approved operation.", nil, []string{"Validation completed."}),
		finalAssistant(), finalAssistant(),
	}
	model := &sequentialModel{responses: responses}
	agentSessions := agentsession.NewMemoryRepository()
	contexts, _ := contextmanager.NewManager()
	runtime, err := agent.NewRuntime(agent.Dependencies{Models: sequentialModelFactory{model: model}, Contexts: contexts, Sessions: agentSessions})
	if err != nil {
		t.Fatal(err)
	}
	service, err := codingagent.NewService(codingagent.Dependencies{
		Sessions: products, Turns: products, Plans: products, Workflows: products, Children: products,
		AgentSessions: agentSessions, Worktrees: products, Agent: runtime, Tools: p5ApprovalToolFactory{}, Prompts: staticPrompt{}, Events: &productEvents{},
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := service.CreateSession(context.Background(), codingagent.Session{ID: "coding-p5-approval", AgentSessionID: "agent-p5-approval", WorkspaceID: workspace.ID, WorktreeID: worktree.ID, ProviderProfileID: "profile-1", ModelID: "model-1", PermissionMode: codingagent.PermissionAsk})
	if err != nil {
		t.Fatal(err)
	}
	planned, err := service.StartTurn(context.Background(), codingagent.TurnRequest{SessionID: session.ID, Text: "Plan the approved operation", Mode: codingagent.TurnModePlan})
	if err != nil || planned.InterruptKind != "plan_approval" {
		t.Fatalf("planned = %#v, %v", planned, err)
	}
	waiting, err := service.ResumeTurn(context.Background(), codingagent.ResumeTurnRequest{SessionID: session.ID, TurnID: planned.TurnID, InterruptID: planned.InterruptID, Decision: codingagent.ResolutionApproved, GrantScope: codingagent.PermissionGrantOnce, Strategy: codingagent.ExecutionWorkflowMultiSerial})
	if err != nil || waiting.Status != string(agent.RunInterrupted) || waiting.InterruptID != "approval-product-1" {
		t.Fatalf("child waiting = %#v, %v", waiting, err)
	}
	disabled := codingagent.DefaultFeatureFlags()
	disabled.Subagents = false
	service, err = codingagent.NewService(codingagent.Dependencies{
		Sessions: products, Turns: products, Plans: products, Workflows: products, Children: products,
		AgentSessions: agentSessions, Worktrees: products, Agent: runtime, Tools: p5ApprovalToolFactory{}, Prompts: staticPrompt{}, Events: &productEvents{}, Features: &disabled,
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := service.Snapshot(context.Background(), session.ID)
	if err != nil || len(snapshot.PendingInterrupts) != 1 {
		t.Fatalf("approval snapshot = %#v, %v", snapshot.PendingInterrupts, err)
	}
	pending := snapshot.PendingInterrupts[0]
	if pending.TurnID != planned.TurnID || pending.RunID != waiting.RunID || pending.ChildAgentID == "" || pending.NodeID != "implement" || pending.Role != string(workflow.RoleImplement) || pending.Summary != "Apply edit to main.go" {
		t.Fatalf("projected child approval = %#v", pending)
	}
	completed, err := service.ResumeTurn(context.Background(), codingagent.ResumeTurnRequest{SessionID: session.ID, TurnID: planned.TurnID, InterruptID: waiting.InterruptID, Decision: codingagent.ResolutionApproved, GrantScope: codingagent.PermissionGrantOnce})
	if err != nil || completed.Status != string(workflow.StatusCompleted) {
		t.Fatalf("completed child approval Workflow = %#v, %v", completed, err)
	}
	children, err := products.ListChildAgents(context.Background(), planned.TurnID)
	if err != nil || len(children) != 2 || children[0].Status != codingagent.ChildAgentCompleted || children[1].Status != codingagent.ChildAgentCompleted || children[1].Role != workflow.RoleValidate {
		t.Fatalf("approved child = %#v, %v", children, err)
	}
}

func TestP5RecoveryReconcilesChildTerminalResultWithoutRepeatingChildRun(t *testing.T) {
	root := t.TempDir()
	for _, arguments := range [][]string{{"init", "--quiet"}, {"config", "user.name", "CodePilot Test"}, {"config", "user.email", "test@example.invalid"}, {"commit", "--allow-empty", "--quiet", "-m", "initial"}} {
		command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", arguments, err, output)
		}
	}
	products := codingmemory.NewRepository()
	now := time.Now().UTC()
	workspace := codingagent.Workspace{ID: "workspace-p5-recovery", DisplayName: "p5-recovery", GitCommonDir: filepath.Join(root, ".git"), Trusted: true, CreatedAt: now, UpdatedAt: now}
	worktree := codingagent.Worktree{ID: "worktree-p5-recovery", WorkspaceID: workspace.ID, Root: root, GitDir: workspace.GitCommonDir, CreatedAt: now, LastUsedAt: now}
	if err := products.SaveWorkspace(context.Background(), workspace); err != nil {
		t.Fatal(err)
	}
	if err := products.SaveWorktree(context.Background(), worktree); err != nil {
		t.Fatal(err)
	}
	planArguments, _ := json.Marshal(codingagent.PlanSubmission{
		Goal: "Recover a durable child result.", Scope: codingagent.PlanScope{Included: []string{"internal"}},
		Findings: []string{"The child terminal output is journaled before parent projection."}, Risks: []string{"Recovery must not rerun the child."},
		Steps:              []codingagent.PlanStep{{ID: "implement", Goal: "Produce one durable child result.", Files: []string{"internal"}, Validation: []string{"Result is durable."}, Role: workflow.RoleImplement, MaxAttempts: 1}},
		AcceptanceCriteria: []string{"Restart completes without replay."}, RecommendedStrategy: codingagent.ExecutionWorkflowMultiSerial,
		WorkspaceRelevant: true, CompletionMode: codingagent.PlanCompletionExecute,
	})
	model := &sequentialModel{responses: []llm.Message{
		p5ToolCall("recovery-context", "request_workspace_context", json.RawMessage(`{"reason":"Recovery behavior depends on the workspace task."}`)),
		p5ToolCall("recovery-plan", "exit_plan_mode", planArguments),
		p5ChildResult("recovery-child-result", "Persisted once.", []string{"internal/recovered.go"}, []string{"Durable result recorded."}),
		finalAssistant(), finalAssistant(),
	}}
	agentSessions := agentsession.NewMemoryRepository()
	contexts, _ := contextmanager.NewManager()
	runtime, err := agent.NewRuntime(agent.Dependencies{Models: sequentialModelFactory{model: model}, Contexts: contexts, Sessions: agentSessions})
	if err != nil {
		t.Fatal(err)
	}
	turns := &failTerminalTurnRepository{TurnRepository: products, childOnly: true}
	service, err := codingagent.NewService(codingagent.Dependencies{
		Sessions: products, Turns: turns, Plans: products, Workflows: products, Children: products,
		AgentSessions: agentSessions, Worktrees: products, Agent: runtime, Tools: emptyToolFactory{}, Prompts: staticPrompt{}, Events: &productEvents{},
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := service.CreateSession(context.Background(), codingagent.Session{ID: "coding-p5-recovery", AgentSessionID: "agent-p5-recovery", WorkspaceID: workspace.ID, WorktreeID: worktree.ID, ProviderProfileID: "profile-1", ModelID: "model-1", PermissionMode: codingagent.PermissionAsk})
	if err != nil {
		t.Fatal(err)
	}
	planned, err := service.StartTurn(context.Background(), codingagent.TurnRequest{SessionID: session.ID, Text: "Plan a recoverable child run", Mode: codingagent.TurnModePlan})
	if err != nil || planned.InterruptKind != "plan_approval" {
		t.Fatalf("planned = %#v, %v", planned, err)
	}
	turns.failOnce = true
	if _, err := service.ResumeTurn(context.Background(), codingagent.ResumeTurnRequest{SessionID: session.ID, TurnID: planned.TurnID, InterruptID: planned.InterruptID, Decision: codingagent.ResolutionApproved, GrantScope: codingagent.PermissionGrantOnce, Strategy: codingagent.ExecutionWorkflowMultiSerial}); err == nil {
		t.Fatal("injected parent projection gap did not surface")
	}
	if len(model.requests) != 3 {
		t.Fatalf("model calls before restart = %d", len(model.requests))
	}
	restarted, err := codingagent.NewService(codingagent.Dependencies{
		Sessions: products, Turns: products, Plans: products, Workflows: products, Children: products,
		AgentSessions: agentSessions, Worktrees: products, Agent: runtime, Tools: emptyToolFactory{}, Prompts: staticPrompt{}, Events: &productEvents{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.RecoverAutomatically(context.Background(), session.ID); err != nil {
		t.Fatalf("recover automatically: %v", err)
	}
	turn, err := products.LoadTurn(context.Background(), planned.TurnID)
	if err != nil || turn.Status != codingagent.TurnCompleted {
		t.Fatalf("recovered Turn = %#v, %v", turn, err)
	}
	children, err := products.ListChildAgents(context.Background(), planned.TurnID)
	if err != nil || len(children) != 1 || children[0].Status != codingagent.ChildAgentCompleted || children[0].Result == nil || children[0].Result.Conclusion != "Persisted once." {
		t.Fatalf("recovered children = %#v, %v", children, err)
	}
	if len(model.requests) != 5 {
		t.Fatalf("recovery repeated or skipped model work; calls = %d", len(model.requests))
	}
}

func TestP5ParentCancellationLeavesNoRunningChildAgent(t *testing.T) {
	root := t.TempDir()
	for _, arguments := range [][]string{{"init", "--quiet"}, {"config", "user.name", "CodePilot Test"}, {"config", "user.email", "test@example.invalid"}, {"commit", "--allow-empty", "--quiet", "-m", "initial"}} {
		command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", arguments, err, output)
		}
	}
	products := codingmemory.NewRepository()
	now := time.Now().UTC()
	workspace := codingagent.Workspace{ID: "workspace-p5-cancel", DisplayName: "p5-cancel", GitCommonDir: filepath.Join(root, ".git"), Trusted: true, CreatedAt: now, UpdatedAt: now}
	worktree := codingagent.Worktree{ID: "worktree-p5-cancel", WorkspaceID: workspace.ID, Root: root, GitDir: workspace.GitCommonDir, CreatedAt: now, LastUsedAt: now}
	if err := products.SaveWorkspace(context.Background(), workspace); err != nil {
		t.Fatal(err)
	}
	if err := products.SaveWorktree(context.Background(), worktree); err != nil {
		t.Fatal(err)
	}
	planArguments, _ := json.Marshal(codingagent.PlanSubmission{
		Goal: "Cancel an active child safely.", Scope: codingagent.PlanScope{Included: []string{"internal"}}, Findings: []string{"The child is independently running."}, Risks: []string{"Cancellation must propagate."},
		Steps:              []codingagent.PlanStep{{ID: "implement", Goal: "Wait for cancellation.", Files: []string{"internal"}, Validation: []string{"No child remains running."}, Role: workflow.RoleImplement, MaxAttempts: 1}},
		AcceptanceCriteria: []string{"Cancellation is durable."}, RecommendedStrategy: codingagent.ExecutionWorkflowMultiSerial, WorkspaceRelevant: true, CompletionMode: codingagent.PlanCompletionExecute,
	})
	model := &workflowBlockingModel{responses: []llm.Message{
		p5ToolCall("cancel-context", "request_workspace_context", json.RawMessage(`{"reason":"The cancellation task depends on this workspace."}`)),
		p5ToolCall("cancel-plan", "exit_plan_mode", planArguments),
	}, started: make(chan struct{}, 1)}
	agentSessions := agentsession.NewMemoryRepository()
	contexts, _ := contextmanager.NewManager()
	runtime, err := agent.NewRuntime(agent.Dependencies{Models: workflowBlockingModelFactory{model: model}, Contexts: contexts, Sessions: agentSessions})
	if err != nil {
		t.Fatal(err)
	}
	service, err := codingagent.NewService(codingagent.Dependencies{Sessions: products, Turns: products, Plans: products, Workflows: products, Children: products, AgentSessions: agentSessions, Worktrees: products, Agent: runtime, Tools: emptyToolFactory{}, Prompts: staticPrompt{}, Events: &productEvents{}})
	if err != nil {
		t.Fatal(err)
	}
	session, err := service.CreateSession(context.Background(), codingagent.Session{ID: "coding-p5-cancel", AgentSessionID: "agent-p5-cancel", WorkspaceID: workspace.ID, WorktreeID: worktree.ID, ProviderProfileID: "profile-1", ModelID: "model-1", PermissionMode: codingagent.PermissionAsk})
	if err != nil {
		t.Fatal(err)
	}
	planned, err := service.StartTurn(context.Background(), codingagent.TurnRequest{SessionID: session.ID, Text: "Plan a cancellable child run", Mode: codingagent.TurnModePlan})
	if err != nil || planned.InterruptKind != "plan_approval" {
		t.Fatalf("planned = %#v, %v", planned, err)
	}
	done := make(chan error, 1)
	go func() {
		_, runErr := service.ResumeTurn(context.Background(), codingagent.ResumeTurnRequest{SessionID: session.ID, TurnID: planned.TurnID, InterruptID: planned.InterruptID, Decision: codingagent.ResolutionApproved, GrantScope: codingagent.PermissionGrantOnce, Strategy: codingagent.ExecutionWorkflowMultiSerial})
		done <- runErr
	}()
	select {
	case <-model.started:
	case <-time.After(5 * time.Second):
		t.Fatal("child Agent did not start")
	}
	if err := service.CancelTurn(context.Background(), session.ID); err != nil {
		t.Fatalf("cancel parent: %v", err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled child did not stop")
	}
	children, err := products.ListChildAgents(context.Background(), planned.TurnID)
	if err != nil || len(children) != 1 || children[0].Status != codingagent.ChildAgentCancelled {
		t.Fatalf("children after cancellation = %#v, %v", children, err)
	}
	turn, err := products.LoadTurn(context.Background(), planned.TurnID)
	if err != nil || turn.Status != codingagent.TurnCancelled {
		t.Fatalf("Turn after cancellation = %#v, %v", turn, err)
	}
}

func TestP5FailedChildCanFallBackToMainAgentBeforeOverallSuccess(t *testing.T) {
	root := t.TempDir()
	for _, arguments := range [][]string{{"init", "--quiet"}, {"config", "user.name", "CodePilot Test"}, {"config", "user.email", "test@example.invalid"}, {"commit", "--allow-empty", "--quiet", "-m", "initial"}} {
		command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", arguments, err, output)
		}
	}
	products := codingmemory.NewRepository()
	now := time.Now().UTC()
	workspace := codingagent.Workspace{ID: "workspace-p5-fallback", DisplayName: "p5-fallback", GitCommonDir: filepath.Join(root, ".git"), Trusted: true, CreatedAt: now, UpdatedAt: now}
	worktree := codingagent.Worktree{ID: "worktree-p5-fallback", WorkspaceID: workspace.ID, Root: root, GitDir: workspace.GitCommonDir, CreatedAt: now, LastUsedAt: now}
	if err := products.SaveWorkspace(context.Background(), workspace); err != nil {
		t.Fatal(err)
	}
	if err := products.SaveWorktree(context.Background(), worktree); err != nil {
		t.Fatal(err)
	}
	planArguments, _ := json.Marshal(codingagent.PlanSubmission{
		Goal: "Fall back safely after a child failure.", Scope: codingagent.PlanScope{Included: []string{"internal"}}, Findings: []string{"The main Agent can retry in its parent context."}, Risks: []string{"A failed child cannot count as success."},
		Steps:              []codingagent.PlanStep{{ID: "implement", Goal: "Attempt the bounded implementation.", Files: []string{"internal"}, Validation: []string{"Implementation completes."}, Role: workflow.RoleImplement, FailureAction: workflow.FailureFallbackMain, MaxAttempts: 2}},
		AcceptanceCriteria: []string{"The main Agent validates the fallback result."}, RecommendedStrategy: codingagent.ExecutionWorkflowMultiSerial, WorkspaceRelevant: true, CompletionMode: codingagent.PlanCompletionExecute,
	})
	failedArguments, _ := json.Marshal(map[string]any{
		"status": "failed", "conclusion": "The child could not complete the node.",
		"evidence": []map[string]any{{"source_id": "child", "summary": "The bounded attempt failed."}}, "unresolved": []string{"Implementation remains."},
	})
	model := &sequentialModel{responses: []llm.Message{
		p5ToolCall("fallback-context", "request_workspace_context", json.RawMessage(`{"reason":"The fallback task depends on this workspace."}`)),
		p5ToolCall("fallback-plan", "exit_plan_mode", planArguments),
		p5ToolCall("failed-child-result", "submit_agent_task_result", failedArguments),
		finalAssistant(), finalAssistant(), finalAssistant(),
	}}
	agentSessions := agentsession.NewMemoryRepository()
	contexts, _ := contextmanager.NewManager()
	runtime, err := agent.NewRuntime(agent.Dependencies{Models: sequentialModelFactory{model: model}, Contexts: contexts, Sessions: agentSessions})
	if err != nil {
		t.Fatal(err)
	}
	service, err := codingagent.NewService(codingagent.Dependencies{Sessions: products, Turns: products, Plans: products, Workflows: products, Children: products, AgentSessions: agentSessions, Worktrees: products, Agent: runtime, Tools: emptyToolFactory{}, Prompts: staticPrompt{}, Events: &productEvents{}})
	if err != nil {
		t.Fatal(err)
	}
	session, err := service.CreateSession(context.Background(), codingagent.Session{ID: "coding-p5-fallback", AgentSessionID: "agent-p5-fallback", WorkspaceID: workspace.ID, WorktreeID: worktree.ID, ProviderProfileID: "profile-1", ModelID: "model-1", PermissionMode: codingagent.PermissionAsk})
	if err != nil {
		t.Fatal(err)
	}
	planned, err := service.StartTurn(context.Background(), codingagent.TurnRequest{SessionID: session.ID, Text: "Plan a child with explicit fallback", Mode: codingagent.TurnModePlan})
	if err != nil || planned.InterruptKind != "plan_approval" {
		t.Fatalf("planned = %#v, %v", planned, err)
	}
	completed, err := service.ResumeTurn(context.Background(), codingagent.ResumeTurnRequest{SessionID: session.ID, TurnID: planned.TurnID, InterruptID: planned.InterruptID, Decision: codingagent.ResolutionApproved, GrantScope: codingagent.PermissionGrantOnce, Strategy: codingagent.ExecutionWorkflowMultiSerial})
	if err != nil || completed.Status != string(workflow.StatusCompleted) {
		t.Fatalf("fallback Workflow = %#v, %v", completed, err)
	}
	turn, err := products.LoadTurn(context.Background(), planned.TurnID)
	if err != nil || turn.Status != codingagent.TurnCompleted || len(turn.Runs) != 6 || turn.Runs[2].ChildAgentID == "" || turn.Runs[2].Status != codingagent.RunBindingFailed || turn.Runs[3].ChildAgentID != "" || turn.Runs[3].Status != codingagent.RunBindingCompleted {
		t.Fatalf("fallback Turn = %#v, %v", turn, err)
	}
	children, err := products.ListChildAgents(context.Background(), turn.ID)
	if err != nil || len(children) != 1 || children[0].Status != codingagent.ChildAgentFailed {
		t.Fatalf("failed child = %#v, %v", children, err)
	}
	durable, err := products.LoadWorkflow(context.Background(), workflow.ID(turn.WorkflowID))
	if err != nil || durable.Nodes[0].Attempts != 2 || workflow.NodeExecutor(durable.Nodes[0]) != workflow.ExecutorMain || !strings.HasPrefix(durable.Nodes[0].ResultRef, "run:") {
		t.Fatalf("fallback node = %#v, %v", durable.Nodes[0], err)
	}
}

func requestContainsText(request llm.ChatRequest, target string) bool {
	for _, message := range request.Messages {
		for _, content := range message.Content {
			if content.Type == llm.ContentText && strings.Contains(content.Text, target) {
				return true
			}
		}
	}
	return false
}

func p5ToolCall(id, name string, arguments json.RawMessage) llm.Message {
	return llm.Message{Role: llm.RoleAssistant, Provider: "profile-1", Model: "model-1", StopReason: llm.StopReasonToolUse, Content: []llm.Content{{Type: llm.ContentToolCall, ToolCall: &llm.ToolCall{ID: id, Name: name, Arguments: arguments}}}}
}

func p5ChildResult(id, conclusion string, changes, validation []string) llm.Message {
	arguments, _ := json.Marshal(map[string]any{
		"status": "succeeded", "conclusion": conclusion, "changes": changes,
		"evidence": []map[string]any{{"source_id": "workspace", "summary": conclusion}}, "validation": validation,
	})
	return p5ToolCall(id, "submit_agent_task_result", arguments)
}
