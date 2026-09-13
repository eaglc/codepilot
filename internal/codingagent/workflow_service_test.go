package codingagent_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	"github.com/eaglc/codepilot/internal/workflow"
)

type failWorkflowEventRepository struct {
	workflow.Repository
	failEvent workflow.EventType
	failOnce  bool
}

type workflowBlockingModelFactory struct{ model *workflowBlockingModel }

func (f workflowBlockingModelFactory) CreateModel(context.Context, llm.ModelRef) (llm.ChatModel, error) {
	return f.model, nil
}

type workflowBlockingModel struct {
	responses []llm.Message
	started   chan struct{}
}

type workflowFailOnceModelFactory struct{ model *workflowFailOnceModel }

func (f workflowFailOnceModelFactory) CreateModel(context.Context, llm.ModelRef) (llm.ChatModel, error) {
	return f.model, nil
}

type workflowFailOnceModel struct {
	responses []llm.Message
	failed    bool
	calls     int
}

func (*workflowFailOnceModel) Complete(context.Context, llm.ChatRequest) (llm.Message, error) {
	return llm.Message{}, nil
}

func (m *workflowFailOnceModel) Stream(_ context.Context, _ llm.ChatRequest) (llm.Stream, error) {
	m.calls++
	if len(m.responses) != 0 {
		response := m.responses[0]
		m.responses = m.responses[1:]
		return &finalStream{events: []llm.StreamEvent{{Kind: llm.StreamResponseFinished, Message: &response}}}, nil
	}
	if !m.failed {
		m.failed = true
		return nil, errors.New("injected node model failure")
	}
	response := finalAssistant()
	return &finalStream{events: []llm.StreamEvent{{Kind: llm.StreamResponseFinished, Message: &response}}}, nil
}

func (*workflowBlockingModel) Complete(context.Context, llm.ChatRequest) (llm.Message, error) {
	return llm.Message{}, nil
}

func (m *workflowBlockingModel) Stream(ctx context.Context, _ llm.ChatRequest) (llm.Stream, error) {
	if len(m.responses) != 0 {
		response := m.responses[0]
		m.responses = m.responses[1:]
		return &finalStream{events: []llm.StreamEvent{{Kind: llm.StreamResponseFinished, Message: &response}}}, nil
	}
	select {
	case m.started <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func (r *failWorkflowEventRepository) AppendWorkflowEvent(ctx context.Context, id workflow.ID, expectedRevision uint64, event workflow.Event) (workflow.Workflow, error) {
	if r.failOnce && event.Type == r.failEvent {
		r.failOnce = false
		return workflow.Workflow{}, fmt.Errorf("injected %s write gap", event.Type)
	}
	return r.Repository.AppendWorkflowEvent(ctx, id, expectedRevision, event)
}

func TestSingleAgentWorkflowExecutesThreeNodesInOneProductTurn(t *testing.T) {
	root := t.TempDir()
	for _, arguments := range [][]string{{"init", "--quiet"}, {"config", "user.name", "CodePilot Test"}, {"config", "user.email", "test@example.invalid"}, {"commit", "--allow-empty", "--quiet", "-m", "initial"}} {
		command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", arguments, err, output)
		}
	}
	products := codingmemory.NewRepository()
	now := time.Now().UTC()
	workspace := codingagent.Workspace{ID: "workspace-workflow", DisplayName: "workflow", GitCommonDir: filepath.Join(root, ".git"), Trusted: true, CreatedAt: now, UpdatedAt: now}
	worktree := codingagent.Worktree{ID: "worktree-workflow", WorkspaceID: workspace.ID, Root: root, GitDir: workspace.GitCommonDir, CreatedAt: now, LastUsedAt: now}
	if err := products.SaveWorkspace(context.Background(), workspace); err != nil {
		t.Fatal(err)
	}
	if err := products.SaveWorktree(context.Background(), worktree); err != nil {
		t.Fatal(err)
	}
	planArguments, _ := json.Marshal(codingagent.PlanSubmission{
		Goal: "Execute a durable three-node Workflow.", Scope: codingagent.PlanScope{Included: []string{"serial Workflow execution"}},
		Findings: []string{"Product Turn continuation is available."}, Risks: []string{"Node completion must not be replayed."},
		Steps: []codingagent.PlanStep{
			{ID: "implement", Goal: "Implement the approved change.", Validation: []string{"Implementation completes."}, Role: workflow.RoleImplement, FailureAction: workflow.FailureRetry, MaxAttempts: 2},
			{ID: "validate", Goal: "Validate the approved change.", DependsOn: []string{"implement"}, Validation: []string{"Validation passes."}, Role: workflow.RoleValidate, FailureAction: workflow.FailureBlock, MaxAttempts: 1},
		},
		AcceptanceCriteria: []string{"The combined result is validated."}, RecommendedStrategy: codingagent.ExecutionWorkflowSingle,
		WorkspaceRelevant: true, CompletionMode: codingagent.PlanCompletionExecute,
	})
	workspaceCall := llm.Message{Role: llm.RoleAssistant, Provider: "profile-1", Model: "model-1", StopReason: llm.StopReasonToolUse, Content: []llm.Content{{Type: llm.ContentToolCall, ToolCall: &llm.ToolCall{ID: "workflow-context", Name: "request_workspace_context", Arguments: json.RawMessage(`{"reason":"The executable Plan depends on this worktree."}`)}}}}
	planCall := llm.Message{Role: llm.RoleAssistant, Provider: "profile-1", Model: "model-1", StopReason: llm.StopReasonToolUse, Content: []llm.Content{{Type: llm.ContentToolCall, ToolCall: &llm.ToolCall{ID: "workflow-plan", Name: "exit_plan_mode", Arguments: planArguments}}}}
	model := &sequentialModel{responses: []llm.Message{workspaceCall, planCall, finalAssistant(), finalAssistant(), finalAssistant()}}
	agentSessions := agentsession.NewMemoryRepository()
	contexts, _ := contextmanager.NewManager()
	runtime, err := agent.NewRuntime(agent.Dependencies{Models: sequentialModelFactory{model: model}, Contexts: contexts, Sessions: agentSessions})
	if err != nil {
		t.Fatal(err)
	}
	events := &productEvents{}
	service, err := codingagent.NewService(codingagent.Dependencies{
		Sessions: products, Turns: products, Plans: products, Workflows: products, AgentSessions: agentSessions, Worktrees: products,
		Agent: runtime, Tools: emptyToolFactory{}, Prompts: staticPrompt{}, Events: events,
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := service.CreateSession(context.Background(), codingagent.Session{
		ID: "coding-workflow", AgentSessionID: "agent-workflow", WorkspaceID: workspace.ID, WorktreeID: worktree.ID,
		ProviderProfileID: "profile-1", ModelID: "model-1", PermissionMode: codingagent.PermissionAsk,
	})
	if err != nil {
		t.Fatal(err)
	}
	planned, err := service.StartTurn(context.Background(), codingagent.TurnRequest{SessionID: session.ID, Text: "Plan and execute a serial Workflow", Mode: codingagent.TurnModePlan})
	if err != nil || planned.InterruptKind != "plan_approval" {
		t.Fatalf("planned = %#v, %v", planned, err)
	}
	disabledFeatures := codingagent.DefaultFeatureFlags()
	disabledFeatures.Workflows = false
	disabled, err := codingagent.NewService(codingagent.Dependencies{
		Sessions: products, Turns: products, Plans: products, Workflows: products, AgentSessions: agentSessions, Worktrees: products,
		Agent: runtime, Tools: emptyToolFactory{}, Prompts: staticPrompt{}, Events: &productEvents{}, Features: &disabledFeatures,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := disabled.ResumeTurn(context.Background(), codingagent.ResumeTurnRequest{
		SessionID: session.ID, TurnID: planned.TurnID, InterruptID: planned.InterruptID,
		Decision: codingagent.ResolutionApproved, GrantScope: codingagent.PermissionGrantOnce, Strategy: codingagent.ExecutionWorkflowSingle,
	}); err == nil {
		t.Fatal("disabled Workflow feature created a new Workflow")
	}
	unstarted, err := products.LoadTurn(context.Background(), planned.TurnID)
	if err != nil || unstarted.WorkflowID != "" || unstarted.Phase != codingagent.TurnPhaseAwaitingPlanApproval {
		t.Fatalf("disabled Workflow mutated the awaiting Turn = %#v, %v", unstarted, err)
	}
	executed, err := service.ResumeTurn(context.Background(), codingagent.ResumeTurnRequest{
		SessionID: session.ID, TurnID: planned.TurnID, InterruptID: planned.InterruptID,
		Decision: codingagent.ResolutionApproved, GrantScope: codingagent.PermissionGrantOnce, Strategy: codingagent.ExecutionWorkflowSingle,
	})
	if err != nil || executed.Status != string(workflow.StatusCompleted) {
		t.Fatalf("executed = %#v, %v", executed, err)
	}
	turn, err := products.LoadTurn(context.Background(), planned.TurnID)
	if err != nil || turn.Status != codingagent.TurnCompleted || turn.Strategy != codingagent.ExecutionWorkflowSingle || turn.WorkflowID == "" || len(turn.Runs) != 5 {
		t.Fatalf("Workflow Turn = %#v, %v", turn, err)
	}
	durable, err := products.LoadWorkflow(context.Background(), workflow.ID(turn.WorkflowID))
	if err != nil || durable.Status != workflow.StatusCompleted || len(durable.Nodes) != 3 {
		t.Fatalf("durable Workflow = %#v, %v", durable, err)
	}
	for _, node := range durable.Nodes {
		if node.Status != workflow.NodeCompleted || node.ResultRef == "" {
			t.Fatalf("Workflow node = %#v", node)
		}
	}
	snapshot, err := service.Snapshot(context.Background(), session.ID)
	if err != nil || snapshot.ActiveWorkflow == nil || snapshot.ActiveWorkflow.Status != string(workflow.StatusCompleted) || snapshot.ActiveWorkflow.CompletedNodes != 3 || snapshot.ActiveWorkflow.Revision != durable.Revision || snapshot.ActiveWorkflow.UsedRuns != 3 || snapshot.ActiveWorkflow.UsedAgentSteps != 3 {
		t.Fatalf("Workflow snapshot = %#v, %v", snapshot.ActiveWorkflow, err)
	}
	if snapshot.Metrics.Workflow.Turns != 1 || snapshot.Metrics.Workflow.CompletedTurns != 1 || snapshot.Metrics.Workflow.NodeRuns != 3 || snapshot.Metrics.Workflow.Steps != 3 || snapshot.Metrics.Steps != 5 {
		t.Fatalf("Workflow metrics = %#v; Turn steps=%d", snapshot.Metrics.Workflow, snapshot.Metrics.Steps)
	}
	nodeStarted, workflowCompleted := 0, false
	for _, event := range events.values {
		if event.Kind == codingagent.EventWorkflowNodeStarted && event.NodeID != "" && event.Payload.Workflow != nil {
			nodeStarted++
		}
		if event.Kind == codingagent.EventWorkflowCompleted {
			workflowCompleted = true
		}
	}
	if nodeStarted != 3 || !workflowCompleted {
		t.Fatalf("Workflow events = %#v", events.values)
	}
	if len(model.requests) != 5 {
		t.Fatalf("model requests = %d", len(model.requests))
	}
	userMessages := 0
	for _, entry := range mustAgentSnapshot(t, agentSessions, session.AgentSessionID).Entries {
		if entry.Message.Role == llm.RoleUser {
			userMessages++
		}
	}
	if userMessages != 1 {
		t.Fatalf("user transcript entries = %d", userMessages)
	}
}

func TestWorkflowRecoveryDoesNotRepeatCompletedNodeAcrossEventWriteGap(t *testing.T) {
	runWorkflowEventWriteGapRecovery(t, workflow.EventNodeCompleted, 3)
}

func TestWorkflowRecoveryStartsNodeAfterNodeStartedEventWriteGap(t *testing.T) {
	runWorkflowEventWriteGapRecovery(t, workflow.EventNodeStarted, 2)
}

func TestWorkflowRecoveryFinishesAfterWorkflowTerminalEventWriteGap(t *testing.T) {
	runWorkflowEventWriteGapRecovery(t, workflow.EventWorkflowCompleted, 5)
}

func runWorkflowEventWriteGapRecovery(t *testing.T, failEvent workflow.EventType, requestsBeforeRestart int) {
	t.Helper()
	root := t.TempDir()
	for _, arguments := range [][]string{{"init", "--quiet"}, {"config", "user.name", "CodePilot Test"}, {"config", "user.email", "test@example.invalid"}, {"commit", "--allow-empty", "--quiet", "-m", "initial"}} {
		command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", arguments, err, output)
		}
	}
	products := codingmemory.NewRepository()
	now := time.Now().UTC()
	workspace := codingagent.Workspace{ID: "workspace-workflow-gap", DisplayName: "workflow-gap", GitCommonDir: filepath.Join(root, ".git"), Trusted: true, CreatedAt: now, UpdatedAt: now}
	worktree := codingagent.Worktree{ID: "worktree-workflow-gap", WorkspaceID: workspace.ID, Root: root, GitDir: workspace.GitCommonDir, CreatedAt: now, LastUsedAt: now}
	if err := products.SaveWorkspace(context.Background(), workspace); err != nil {
		t.Fatal(err)
	}
	if err := products.SaveWorktree(context.Background(), worktree); err != nil {
		t.Fatal(err)
	}
	planArguments, _ := json.Marshal(codingagent.PlanSubmission{
		Goal: "Recover a durable serial Workflow.", Scope: codingagent.PlanScope{Included: []string{"Workflow recovery"}},
		Findings: []string{"Run completion and Workflow events are separate durable boundaries."}, Risks: []string{"A completed node must not execute twice."},
		Steps: []codingagent.PlanStep{
			{ID: "implement", Goal: "Implement once.", Validation: []string{"Implementation completes once."}, Role: workflow.RoleImplement, FailureAction: workflow.FailureTerminate, MaxAttempts: 1},
			{ID: "validate", Goal: "Validate after recovery.", DependsOn: []string{"implement"}, Validation: []string{"Validation passes."}, Role: workflow.RoleValidate, FailureAction: workflow.FailureBlock, MaxAttempts: 1},
		},
		AcceptanceCriteria: []string{"Recovery preserves exactly-once node completion."}, RecommendedStrategy: codingagent.ExecutionWorkflowSingle,
		WorkspaceRelevant: true, CompletionMode: codingagent.PlanCompletionExecute,
	})
	workspaceCall := llm.Message{Role: llm.RoleAssistant, Provider: "profile-1", Model: "model-1", StopReason: llm.StopReasonToolUse, Content: []llm.Content{{Type: llm.ContentToolCall, ToolCall: &llm.ToolCall{ID: "gap-context", Name: "request_workspace_context", Arguments: json.RawMessage(`{"reason":"The recovery Plan depends on this worktree."}`)}}}}
	planCall := llm.Message{Role: llm.RoleAssistant, Provider: "profile-1", Model: "model-1", StopReason: llm.StopReasonToolUse, Content: []llm.Content{{Type: llm.ContentToolCall, ToolCall: &llm.ToolCall{ID: "gap-plan", Name: "exit_plan_mode", Arguments: planArguments}}}}
	model := &sequentialModel{responses: []llm.Message{workspaceCall, planCall, finalAssistant(), finalAssistant(), finalAssistant()}}
	agentSessions := agentsession.NewMemoryRepository()
	contexts, _ := contextmanager.NewManager()
	runtime, err := agent.NewRuntime(agent.Dependencies{Models: sequentialModelFactory{model: model}, Contexts: contexts, Sessions: agentSessions})
	if err != nil {
		t.Fatal(err)
	}
	failingWorkflows := &failWorkflowEventRepository{Repository: products, failEvent: failEvent, failOnce: true}
	first, err := codingagent.NewService(codingagent.Dependencies{
		Sessions: products, Turns: products, Plans: products, Workflows: failingWorkflows, AgentSessions: agentSessions, Worktrees: products,
		Agent: runtime, Tools: emptyToolFactory{}, Prompts: staticPrompt{}, Events: &productEvents{},
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := first.CreateSession(context.Background(), codingagent.Session{
		ID: "coding-workflow-gap", AgentSessionID: "agent-workflow-gap", WorkspaceID: workspace.ID, WorktreeID: worktree.ID,
		ProviderProfileID: "profile-1", ModelID: "model-1", PermissionMode: codingagent.PermissionAsk,
	})
	if err != nil {
		t.Fatal(err)
	}
	planned, err := first.StartTurn(context.Background(), codingagent.TurnRequest{SessionID: session.ID, Text: "Plan Workflow recovery", Mode: codingagent.TurnModePlan})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.ResumeTurn(context.Background(), codingagent.ResumeTurnRequest{
		SessionID: session.ID, TurnID: planned.TurnID, InterruptID: planned.InterruptID,
		Decision: codingagent.ResolutionApproved, GrantScope: codingagent.PermissionGrantOnce, Strategy: codingagent.ExecutionWorkflowSingle,
	}); err == nil {
		t.Fatal("Workflow node completion gap was not injected")
	}
	if len(model.requests) != requestsBeforeRestart {
		t.Fatalf("model requests before restart = %d", len(model.requests))
	}
	gapTurn, err := products.LoadTurn(context.Background(), planned.TurnID)
	wantRunStatus := codingagent.RunBindingCompleted
	if failEvent == workflow.EventNodeStarted {
		wantRunStatus = codingagent.RunBindingHandedOff
	}
	if err != nil || gapTurn.Status != codingagent.TurnRunning || gapTurn.Runs[len(gapTurn.Runs)-1].Status != wantRunStatus {
		t.Fatalf("Turn at completion gap = %#v, %v", gapTurn, err)
	}
	recovered, err := codingagent.NewService(codingagent.Dependencies{
		Sessions: products, Turns: products, Plans: products, Workflows: products, AgentSessions: agentSessions, Worktrees: products,
		Agent: runtime, Tools: emptyToolFactory{}, Prompts: staticPrompt{}, Events: &productEvents{},
		Features: &codingagent.FeatureFlags{ProductTurns: true, PlanMode: true, PlanSuggestions: true, Workflows: false},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := recovered.RecoverAutomatically(context.Background(), session.ID); err != nil {
		t.Fatalf("RecoverAutomatically: %v", err)
	}
	finalTurn, err := products.LoadTurn(context.Background(), planned.TurnID)
	if err != nil || finalTurn.Status != codingagent.TurnCompleted {
		t.Fatalf("recovered Turn = %#v, %v", finalTurn, err)
	}
	finalWorkflow, err := products.LoadWorkflow(context.Background(), workflow.ID(finalTurn.WorkflowID))
	if err != nil || finalWorkflow.Status != workflow.StatusCompleted || len(finalWorkflow.Nodes) != 3 {
		t.Fatalf("recovered Workflow = %#v, %v", finalWorkflow, err)
	}
	if finalWorkflow.Nodes[0].Attempts != 1 || len(model.requests) != 5 {
		t.Fatalf("completed node replayed: attempts=%d model requests=%d", finalWorkflow.Nodes[0].Attempts, len(model.requests))
	}
}

func TestWorkflowRecoveryRecordsFailedNodeAfterNodeFailedEventWriteGap(t *testing.T) {
	root := t.TempDir()
	for _, arguments := range [][]string{{"init", "--quiet"}, {"config", "user.name", "CodePilot Test"}, {"config", "user.email", "test@example.invalid"}, {"commit", "--allow-empty", "--quiet", "-m", "initial"}} {
		command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", arguments, err, output)
		}
	}
	products := codingmemory.NewRepository()
	now := time.Now().UTC()
	workspace := codingagent.Workspace{ID: "workspace-workflow-failed-gap", DisplayName: "failed-gap", GitCommonDir: filepath.Join(root, ".git"), Trusted: true, CreatedAt: now, UpdatedAt: now}
	worktree := codingagent.Worktree{ID: "worktree-workflow-failed-gap", WorkspaceID: workspace.ID, Root: root, GitDir: workspace.GitCommonDir, CreatedAt: now, LastUsedAt: now}
	if err := products.SaveWorkspace(context.Background(), workspace); err != nil {
		t.Fatal(err)
	}
	if err := products.SaveWorktree(context.Background(), worktree); err != nil {
		t.Fatal(err)
	}
	planArguments, _ := json.Marshal(codingagent.PlanSubmission{
		Goal: "Recover a failed Workflow node.", Scope: codingagent.PlanScope{Included: []string{"Workflow failure recovery"}},
		Findings: []string{"Run failure and Workflow failure events are separate durable boundaries."}, Risks: []string{"The failed node must not rerun."},
		Steps:              []codingagent.PlanStep{{ID: "implement", Goal: "Fail once.", Files: []string{"internal"}, Validation: []string{"Failure is recorded once."}, Role: workflow.RoleImplement, FailureAction: workflow.FailureTerminate, MaxAttempts: 1}},
		AcceptanceCriteria: []string{"Recovery terminates the Workflow without replay."}, RecommendedStrategy: codingagent.ExecutionWorkflowSingle,
		WorkspaceRelevant: true, CompletionMode: codingagent.PlanCompletionExecute,
	})
	workspaceCall := llm.Message{Role: llm.RoleAssistant, Provider: "profile-1", Model: "model-1", StopReason: llm.StopReasonToolUse, Content: []llm.Content{{Type: llm.ContentToolCall, ToolCall: &llm.ToolCall{ID: "failed-gap-context", Name: "request_workspace_context", Arguments: json.RawMessage(`{"reason":"The failure recovery Plan depends on this worktree."}`)}}}}
	planCall := llm.Message{Role: llm.RoleAssistant, Provider: "profile-1", Model: "model-1", StopReason: llm.StopReasonToolUse, Content: []llm.Content{{Type: llm.ContentToolCall, ToolCall: &llm.ToolCall{ID: "failed-gap-plan", Name: "exit_plan_mode", Arguments: planArguments}}}}
	model := &workflowFailOnceModel{responses: []llm.Message{workspaceCall, planCall}}
	agentSessions := agentsession.NewMemoryRepository()
	contexts, _ := contextmanager.NewManager()
	runtime, err := agent.NewRuntime(agent.Dependencies{Models: workflowFailOnceModelFactory{model: model}, Contexts: contexts, Sessions: agentSessions})
	if err != nil {
		t.Fatal(err)
	}
	failingWorkflows := &failWorkflowEventRepository{Repository: products, failEvent: workflow.EventNodeFailed, failOnce: true}
	first, err := codingagent.NewService(codingagent.Dependencies{
		Sessions: products, Turns: products, Plans: products, Workflows: failingWorkflows, AgentSessions: agentSessions, Worktrees: products,
		Agent: runtime, Tools: emptyToolFactory{}, Prompts: staticPrompt{}, Events: &productEvents{},
		Limits: agent.RunLimits{MaxSteps: 8, MaxDuration: time.Minute, MaxModelAttempts: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := first.CreateSession(context.Background(), codingagent.Session{
		ID: "coding-workflow-failed-gap", AgentSessionID: "agent-workflow-failed-gap", WorkspaceID: workspace.ID, WorktreeID: worktree.ID,
		ProviderProfileID: "profile-1", ModelID: "model-1", PermissionMode: codingagent.PermissionAsk,
	})
	if err != nil {
		t.Fatal(err)
	}
	planned, err := first.StartTurn(context.Background(), codingagent.TurnRequest{SessionID: session.ID, Text: "Plan failed node recovery", Mode: codingagent.TurnModePlan})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.ResumeTurn(context.Background(), codingagent.ResumeTurnRequest{
		SessionID: session.ID, TurnID: planned.TurnID, InterruptID: planned.InterruptID,
		Decision: codingagent.ResolutionApproved, GrantScope: codingagent.PermissionGrantOnce, Strategy: codingagent.ExecutionWorkflowSingle,
	}); err == nil {
		t.Fatal("Workflow node failure event gap was not injected")
	}
	if model.calls != 3 {
		t.Fatalf("model calls before restart = %d", model.calls)
	}
	gapTurn, err := products.LoadTurn(context.Background(), planned.TurnID)
	if err != nil || gapTurn.Status != codingagent.TurnRunning || gapTurn.Runs[len(gapTurn.Runs)-1].Status != codingagent.RunBindingFailed {
		t.Fatalf("Turn at failed-node gap = %#v, %v", gapTurn, err)
	}
	recovered, err := codingagent.NewService(codingagent.Dependencies{
		Sessions: products, Turns: products, Plans: products, Workflows: products, AgentSessions: agentSessions, Worktrees: products,
		Agent: runtime, Tools: emptyToolFactory{}, Prompts: staticPrompt{}, Events: &productEvents{},
		Limits: agent.RunLimits{MaxSteps: 8, MaxDuration: time.Minute, MaxModelAttempts: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := recovered.RecoverAutomatically(context.Background(), session.ID); err != nil {
		t.Fatal(err)
	}
	finalTurn, err := products.LoadTurn(context.Background(), planned.TurnID)
	if err != nil || finalTurn.Status != codingagent.TurnFailed {
		t.Fatalf("recovered failed Turn = %#v, %v", finalTurn, err)
	}
	finalWorkflow, err := products.LoadWorkflow(context.Background(), workflow.ID(finalTurn.WorkflowID))
	if err != nil || finalWorkflow.Status != workflow.StatusFailed || finalWorkflow.Nodes[0].Status != workflow.NodeFailed || finalWorkflow.Nodes[0].Attempts != 1 || model.calls != 3 {
		t.Fatalf("recovered failed Workflow = %#v, model calls=%d, %v", finalWorkflow, model.calls, err)
	}
}

func TestActiveWorkflowCancellationStopsRunAndCancelsUnstartedNodes(t *testing.T) {
	root := t.TempDir()
	for _, arguments := range [][]string{{"init", "--quiet"}, {"config", "user.name", "CodePilot Test"}, {"config", "user.email", "test@example.invalid"}, {"commit", "--allow-empty", "--quiet", "-m", "initial"}} {
		command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", arguments, err, output)
		}
	}
	products := codingmemory.NewRepository()
	now := time.Now().UTC()
	workspace := codingagent.Workspace{ID: "workspace-workflow-cancel", DisplayName: "workflow-cancel", GitCommonDir: filepath.Join(root, ".git"), Trusted: true, CreatedAt: now, UpdatedAt: now}
	worktree := codingagent.Worktree{ID: "worktree-workflow-cancel", WorkspaceID: workspace.ID, Root: root, GitDir: workspace.GitCommonDir, CreatedAt: now, LastUsedAt: now}
	if err := products.SaveWorkspace(context.Background(), workspace); err != nil {
		t.Fatal(err)
	}
	if err := products.SaveWorktree(context.Background(), worktree); err != nil {
		t.Fatal(err)
	}
	planArguments, _ := json.Marshal(codingagent.PlanSubmission{
		Goal: "Cancel a durable serial Workflow.", Scope: codingagent.PlanScope{Included: []string{"Workflow cancellation"}},
		Findings: []string{"The task has multiple ordered nodes."}, Risks: []string{"Cancellation must stop the active node."},
		Steps: []codingagent.PlanStep{
			{ID: "implement", Goal: "Wait for cancellation.", Files: []string{"internal"}, Validation: []string{"The operation is cancellable."}, Role: workflow.RoleImplement, FailureAction: workflow.FailureTerminate, MaxAttempts: 1},
			{ID: "validate", Goal: "Must not start.", DependsOn: []string{"implement"}, Files: []string{"internal"}, Validation: []string{"This node remains unstarted."}, Role: workflow.RoleValidate, FailureAction: workflow.FailureBlock, MaxAttempts: 1},
		},
		AcceptanceCriteria: []string{"Cancellation is durable."}, RecommendedStrategy: codingagent.ExecutionWorkflowSingle,
		WorkspaceRelevant: true, CompletionMode: codingagent.PlanCompletionExecute,
	})
	workspaceCall := llm.Message{Role: llm.RoleAssistant, Provider: "profile-1", Model: "model-1", StopReason: llm.StopReasonToolUse, Content: []llm.Content{{Type: llm.ContentToolCall, ToolCall: &llm.ToolCall{ID: "cancel-context", Name: "request_workspace_context", Arguments: json.RawMessage(`{"reason":"The cancellation Plan depends on this worktree."}`)}}}}
	planCall := llm.Message{Role: llm.RoleAssistant, Provider: "profile-1", Model: "model-1", StopReason: llm.StopReasonToolUse, Content: []llm.Content{{Type: llm.ContentToolCall, ToolCall: &llm.ToolCall{ID: "cancel-plan", Name: "exit_plan_mode", Arguments: planArguments}}}}
	model := &workflowBlockingModel{responses: []llm.Message{workspaceCall, planCall}, started: make(chan struct{}, 1)}
	agentSessions := agentsession.NewMemoryRepository()
	contexts, _ := contextmanager.NewManager()
	runtime, err := agent.NewRuntime(agent.Dependencies{Models: workflowBlockingModelFactory{model: model}, Contexts: contexts, Sessions: agentSessions})
	if err != nil {
		t.Fatal(err)
	}
	events := &productEvents{}
	service, err := codingagent.NewService(codingagent.Dependencies{
		Sessions: products, Turns: products, Plans: products, Workflows: products, AgentSessions: agentSessions, Worktrees: products,
		Agent: runtime, Tools: emptyToolFactory{}, Prompts: staticPrompt{}, Events: events,
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := service.CreateSession(context.Background(), codingagent.Session{
		ID: "coding-workflow-cancel", AgentSessionID: "agent-workflow-cancel", WorkspaceID: workspace.ID, WorktreeID: worktree.ID,
		ProviderProfileID: "profile-1", ModelID: "model-1", PermissionMode: codingagent.PermissionAsk,
	})
	if err != nil {
		t.Fatal(err)
	}
	planned, err := service.StartTurn(context.Background(), codingagent.TurnRequest{SessionID: session.ID, Text: "Plan a cancellable Workflow", Mode: codingagent.TurnModePlan})
	if err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		result codingagent.TurnResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, resumeErr := service.ResumeTurn(context.Background(), codingagent.ResumeTurnRequest{
			SessionID: session.ID, TurnID: planned.TurnID, InterruptID: planned.InterruptID,
			Decision: codingagent.ResolutionApproved, GrantScope: codingagent.PermissionGrantOnce, Strategy: codingagent.ExecutionWorkflowSingle,
		})
		done <- outcome{result: result, err: resumeErr}
	}()
	select {
	case <-model.started:
	case <-time.After(2 * time.Second):
		t.Fatal("Workflow node did not start")
	}
	if err := service.CancelTurn(context.Background(), session.ID); err != nil {
		t.Fatal(err)
	}
	var completed outcome
	select {
	case completed = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled Workflow did not return")
	}
	if completed.result.Status != string(workflow.StatusCancelled) || completed.err != nil {
		t.Fatalf("cancelled result = %#v, %v", completed.result, completed.err)
	}
	turn, err := products.LoadTurn(context.Background(), planned.TurnID)
	if err != nil || turn.Status != codingagent.TurnCancelled {
		t.Fatalf("cancelled Turn = %#v, %v", turn, err)
	}
	durable, err := products.LoadWorkflow(context.Background(), workflow.ID(turn.WorkflowID))
	if err != nil || durable.Status != workflow.StatusCancelled {
		t.Fatalf("cancelled Workflow = %#v, %v", durable, err)
	}
	for _, node := range durable.Nodes {
		if node.Status != workflow.NodeCancelled {
			t.Fatalf("node was not cancelled: %#v", node)
		}
	}
	found := false
	for _, event := range events.values {
		found = found || event.Kind == codingagent.EventWorkflowCancelled
	}
	if !found {
		t.Fatalf("Workflow cancellation event missing: %#v", events.values)
	}
}

func TestWorkflowServiceAppliesEveryNodeFailurePolicy(t *testing.T) {
	tests := []struct {
		name           string
		action         workflow.FailureAction
		maxAttempts    int
		wantWorkflow   workflow.Status
		wantNode       workflow.NodeStatus
		wantAttempts   int
		wantRetryEvent bool
	}{
		{name: "retry", action: workflow.FailureRetry, maxAttempts: 2, wantWorkflow: workflow.StatusCompleted, wantNode: workflow.NodeCompleted, wantAttempts: 2, wantRetryEvent: true},
		{name: "block", action: workflow.FailureBlock, maxAttempts: 1, wantWorkflow: workflow.StatusBlocked, wantNode: workflow.NodeBlocked, wantAttempts: 1},
		{name: "replan", action: workflow.FailureReplan, maxAttempts: 1, wantWorkflow: workflow.StatusNeedsReplan, wantNode: workflow.NodeFailed, wantAttempts: 1},
		{name: "terminate", action: workflow.FailureTerminate, maxAttempts: 1, wantWorkflow: workflow.StatusFailed, wantNode: workflow.NodeFailed, wantAttempts: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			for _, arguments := range [][]string{{"init", "--quiet"}, {"config", "user.name", "CodePilot Test"}, {"config", "user.email", "test@example.invalid"}, {"commit", "--allow-empty", "--quiet", "-m", "initial"}} {
				command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
				if output, err := command.CombinedOutput(); err != nil {
					t.Fatalf("git %v: %v\n%s", arguments, err, output)
				}
			}
			products := codingmemory.NewRepository()
			now := time.Now().UTC()
			workspaceID := codingagent.WorkspaceID("workspace-policy-" + test.name)
			worktreeID := codingagent.WorktreeID("worktree-policy-" + test.name)
			workspace := codingagent.Workspace{ID: workspaceID, DisplayName: test.name, GitCommonDir: filepath.Join(root, ".git"), Trusted: true, CreatedAt: now, UpdatedAt: now}
			worktree := codingagent.Worktree{ID: worktreeID, WorkspaceID: workspace.ID, Root: root, GitDir: workspace.GitCommonDir, CreatedAt: now, LastUsedAt: now}
			if err := products.SaveWorkspace(context.Background(), workspace); err != nil {
				t.Fatal(err)
			}
			if err := products.SaveWorktree(context.Background(), worktree); err != nil {
				t.Fatal(err)
			}
			planArguments, _ := json.Marshal(codingagent.PlanSubmission{
				Goal: "Exercise the " + test.name + " failure policy.", Scope: codingagent.PlanScope{Included: []string{"Workflow policy"}},
				Findings: []string{"The node failure is deterministic."}, Risks: []string{"The failure must not be skipped."},
				Steps:              []codingagent.PlanStep{{ID: "implement", Goal: "Run the failing node.", Files: []string{"internal"}, Validation: []string{"Apply the declared failure policy."}, Role: workflow.RoleImplement, FailureAction: test.action, MaxAttempts: test.maxAttempts}},
				AcceptanceCriteria: []string{"The policy reaches a durable state."}, RecommendedStrategy: codingagent.ExecutionWorkflowSingle,
				WorkspaceRelevant: true, CompletionMode: codingagent.PlanCompletionExecute,
			})
			workspaceCall := llm.Message{Role: llm.RoleAssistant, Provider: "profile-1", Model: "model-1", StopReason: llm.StopReasonToolUse, Content: []llm.Content{{Type: llm.ContentToolCall, ToolCall: &llm.ToolCall{ID: "policy-context", Name: "request_workspace_context", Arguments: json.RawMessage(`{"reason":"The policy Plan depends on this worktree."}`)}}}}
			planCall := llm.Message{Role: llm.RoleAssistant, Provider: "profile-1", Model: "model-1", StopReason: llm.StopReasonToolUse, Content: []llm.Content{{Type: llm.ContentToolCall, ToolCall: &llm.ToolCall{ID: "policy-plan", Name: "exit_plan_mode", Arguments: planArguments}}}}
			model := &workflowFailOnceModel{responses: []llm.Message{workspaceCall, planCall}}
			agentSessions := agentsession.NewMemoryRepository()
			contexts, _ := contextmanager.NewManager()
			runtime, err := agent.NewRuntime(agent.Dependencies{Models: workflowFailOnceModelFactory{model: model}, Contexts: contexts, Sessions: agentSessions})
			if err != nil {
				t.Fatal(err)
			}
			events := &productEvents{}
			service, err := codingagent.NewService(codingagent.Dependencies{
				Sessions: products, Turns: products, Plans: products, Workflows: products, AgentSessions: agentSessions, Worktrees: products,
				Agent: runtime, Tools: emptyToolFactory{}, Prompts: staticPrompt{}, Events: events,
				Limits: agent.RunLimits{MaxSteps: 8, MaxDuration: time.Minute, MaxModelAttempts: 1},
			})
			if err != nil {
				t.Fatal(err)
			}
			session, err := service.CreateSession(context.Background(), codingagent.Session{
				ID: codingagent.SessionID("coding-policy-" + test.name), AgentSessionID: agentsession.ID("agent-policy-" + test.name), WorkspaceID: workspace.ID, WorktreeID: worktree.ID,
				ProviderProfileID: "profile-1", ModelID: "model-1", PermissionMode: codingagent.PermissionAsk,
			})
			if err != nil {
				t.Fatal(err)
			}
			planned, err := service.StartTurn(context.Background(), codingagent.TurnRequest{SessionID: session.ID, Text: "Plan the failure policy", Mode: codingagent.TurnModePlan})
			if err != nil {
				t.Fatal(err)
			}
			result, err := service.ResumeTurn(context.Background(), codingagent.ResumeTurnRequest{
				SessionID: session.ID, TurnID: planned.TurnID, InterruptID: planned.InterruptID,
				Decision: codingagent.ResolutionApproved, GrantScope: codingagent.PermissionGrantOnce, Strategy: codingagent.ExecutionWorkflowSingle,
			})
			if err != nil || result.Status != string(test.wantWorkflow) {
				t.Fatalf("policy result = %#v, %v", result, err)
			}
			turn, err := products.LoadTurn(context.Background(), planned.TurnID)
			if err != nil {
				t.Fatal(err)
			}
			durable, err := products.LoadWorkflow(context.Background(), workflow.ID(turn.WorkflowID))
			if err != nil || durable.Status != test.wantWorkflow || durable.Nodes[0].Status != test.wantNode || durable.Nodes[0].Attempts != test.wantAttempts {
				t.Fatalf("policy Workflow = %#v, %v", durable, err)
			}
			foundRetry := false
			for _, event := range events.values {
				foundRetry = foundRetry || event.Kind == codingagent.EventWorkflowNodeRetrying
			}
			if foundRetry != test.wantRetryEvent {
				t.Fatalf("retry event=%v events=%#v", foundRetry, events.values)
			}
		})
	}
}

func mustAgentSnapshot(t *testing.T, repository agentsession.Repository, id agentsession.ID) agentsession.Snapshot {
	t.Helper()
	value, err := repository.Load(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return value
}
