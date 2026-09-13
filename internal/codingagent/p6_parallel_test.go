package codingagent_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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

func TestP6ParallelReadOnlyWorkflowRunsIndependentNodesConcurrently(t *testing.T) {
	root := t.TempDir()
	for _, arguments := range [][]string{{"init", "--quiet"}, {"config", "user.name", "CodePilot Test"}, {"config", "user.email", "test@example.invalid"}, {"commit", "--allow-empty", "--quiet", "-m", "initial"}} {
		command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", arguments, err, output)
		}
	}
	products := codingmemory.NewRepository()
	now := time.Now().UTC()
	workspace := codingagent.Workspace{ID: "workspace-p6", DisplayName: "p6", GitCommonDir: filepath.Join(root, ".git"), Trusted: true, CreatedAt: now, UpdatedAt: now}
	worktree := codingagent.Worktree{ID: "worktree-p6", WorkspaceID: workspace.ID, Root: root, GitDir: workspace.GitCommonDir, CreatedAt: now, LastUsedAt: now}
	if err := products.SaveWorkspace(context.Background(), workspace); err != nil {
		t.Fatal(err)
	}
	if err := products.SaveWorktree(context.Background(), worktree); err != nil {
		t.Fatal(err)
	}
	planArguments, _ := json.Marshal(codingagent.PlanSubmission{
		Goal:     "Explore two independent areas concurrently and review the evidence.",
		Scope:    codingagent.PlanScope{Included: []string{"internal/api", "internal/ui"}},
		Findings: []string{"The exploration areas have no dependency on each other."},
		Risks:    []string{"Every concurrent node must remain read-only."},
		Steps: []codingagent.PlanStep{
			{ID: "explore-api", Goal: "Explore API behavior.", Files: []string{"internal/api"}, Validation: []string{"API evidence is recorded."}, Role: workflow.RoleExplore, FailureAction: workflow.FailureBlock, MaxAttempts: 1},
			{ID: "explore-ui", Goal: "Explore UI behavior.", Files: []string{"internal/ui"}, Validation: []string{"UI evidence is recorded."}, Role: workflow.RoleExplore, FailureAction: workflow.FailureBlock, MaxAttempts: 1},
		},
		AcceptanceCriteria:  []string{"Both independent findings are reviewed."},
		RecommendedStrategy: codingagent.ExecutionWorkflowMultiParallelReadOnly,
		WorkspaceRelevant:   true, CompletionMode: codingagent.PlanCompletionExecute,
	})
	model := &parallelReadOnlyModel{plan: planArguments, firstWaveReady: make(chan struct{})}
	agentSessions := agentsession.NewMemoryRepository()
	contexts, _ := contextmanager.NewManager()
	runtime, err := agent.NewRuntime(agent.Dependencies{Models: parallelReadOnlyModelFactory{model: model}, Contexts: contexts, Sessions: agentSessions})
	if err != nil {
		t.Fatal(err)
	}
	service, err := codingagent.NewService(codingagent.Dependencies{
		Sessions: products, Turns: products, Plans: products, Workflows: products, Children: products,
		AgentSessions: agentSessions, Worktrees: products, Agent: runtime, Tools: emptyToolFactory{}, Prompts: staticPrompt{}, Events: &parallelProductEvents{},
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := service.CreateSession(context.Background(), codingagent.Session{
		ID: "coding-p6", AgentSessionID: "agent-p6", WorkspaceID: workspace.ID, WorktreeID: worktree.ID,
		ProviderProfileID: "profile-1", ModelID: "model-1", PermissionMode: codingagent.PermissionAsk,
	})
	if err != nil {
		t.Fatal(err)
	}
	planned, err := service.StartTurn(context.Background(), codingagent.TurnRequest{SessionID: session.ID, Text: "Plan parallel read-only exploration", Mode: codingagent.TurnModePlan})
	if err != nil || planned.InterruptKind != "plan_approval" {
		t.Fatalf("planned = %#v, %v", planned, err)
	}
	completed, err := service.ResumeTurn(context.Background(), codingagent.ResumeTurnRequest{
		SessionID: session.ID, TurnID: planned.TurnID, InterruptID: planned.InterruptID,
		Decision: codingagent.ResolutionApproved, GrantScope: codingagent.PermissionGrantOnce, Strategy: codingagent.ExecutionWorkflowMultiParallelReadOnly,
	})
	if err != nil || completed.Status != string(workflow.StatusCompleted) {
		t.Fatalf("parallel Workflow = %#v, %v", completed, err)
	}
	model.mu.Lock()
	maximumActive := model.maximumActive
	firstWaveElapsed := model.firstWaveFinished.Sub(model.firstWaveStarted)
	model.mu.Unlock()
	if maximumActive < 2 {
		t.Fatalf("maximum concurrent first-wave Agents = %d", maximumActive)
	}
	if firstWaveElapsed <= 0 || firstWaveElapsed >= 450*time.Millisecond {
		t.Fatalf("parallel first wave took %s; independent 250ms nodes did not overlap", firstWaveElapsed)
	}
	turn, err := products.LoadTurn(context.Background(), planned.TurnID)
	if err != nil || turn.Status != codingagent.TurnCompleted || turn.Strategy != codingagent.ExecutionWorkflowMultiParallelReadOnly {
		t.Fatalf("parallel Turn = %#v, %v", turn, err)
	}
	durable, err := products.LoadWorkflow(context.Background(), workflow.ID(turn.WorkflowID))
	if err != nil || durable.Status != workflow.StatusCompleted || durable.Strategy != workflow.StrategyMultiAgentParallelReadOnly || durable.Budget.MaxConcurrency != 2 {
		t.Fatalf("parallel durable Workflow = %#v, %v", durable, err)
	}
	children, err := products.ListChildAgents(context.Background(), turn.ID)
	if err != nil || len(children) != 4 {
		t.Fatalf("parallel children = %#v, %v", children, err)
	}
	for _, child := range children {
		if child.Status != codingagent.ChildAgentCompleted || len(child.Task.WritePaths) != 0 || child.Profile == codingagent.CapabilityImplement || child.Profile == codingagent.CapabilityIntegrate {
			t.Fatalf("parallel child escaped read-only policy: %#v", child)
		}
	}
}

func TestP6PlanRunsIndependentReadOnlyExplorationsConcurrentlyAndWaitsForApproval(t *testing.T) {
	root := t.TempDir()
	for _, arguments := range [][]string{{"init", "--quiet"}, {"config", "user.name", "CodePilot Test"}, {"config", "user.email", "test@example.invalid"}, {"commit", "--allow-empty", "--quiet", "-m", "initial"}} {
		command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", arguments, err, output)
		}
	}
	products := codingmemory.NewRepository()
	now := time.Now().UTC()
	workspace := codingagent.Workspace{ID: "workspace-p6-plan", DisplayName: "p6-plan", GitCommonDir: filepath.Join(root, ".git"), Trusted: true, CreatedAt: now, UpdatedAt: now}
	worktree := codingagent.Worktree{ID: "worktree-p6-plan", WorkspaceID: workspace.ID, Root: root, GitDir: workspace.GitCommonDir, CreatedAt: now, LastUsedAt: now}
	if err := products.SaveWorkspace(context.Background(), workspace); err != nil {
		t.Fatal(err)
	}
	if err := products.SaveWorktree(context.Background(), worktree); err != nil {
		t.Fatal(err)
	}
	planArguments, _ := json.Marshal(codingagent.PlanSubmission{
		Goal: "Plan a change using two independent evidence sources.", Scope: codingagent.PlanScope{Included: []string{"internal/api", "internal/ui"}},
		Findings: []string{"Both exploration results support the Plan."}, Risks: []string{"Implementation remains approval-gated."},
		Steps:              []codingagent.PlanStep{{ID: "implement", Goal: "Implement after approval.", Files: []string{"internal"}, Validation: []string{"Run tests."}}},
		AcceptanceCriteria: []string{"The Plan incorporates both evidence sources."}, RecommendedStrategy: codingagent.ExecutionSingle,
		WorkspaceRelevant: true, CompletionMode: codingagent.PlanCompletionExecute,
	})
	model := &parallelPlanModel{plan: planArguments, waveReady: make(chan struct{})}
	agentSessions := agentsession.NewMemoryRepository()
	contexts, _ := contextmanager.NewManager()
	runtime, err := agent.NewRuntime(agent.Dependencies{Models: parallelPlanModelFactory{model: model}, Contexts: contexts, Sessions: agentSessions})
	if err != nil {
		t.Fatal(err)
	}
	service, err := codingagent.NewService(codingagent.Dependencies{
		Sessions: products, Turns: products, Plans: products, Workflows: products, Children: products,
		AgentSessions: agentSessions, Worktrees: products, Agent: runtime, Tools: emptyToolFactory{}, Prompts: staticPrompt{}, Events: &parallelProductEvents{},
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := service.CreateSession(context.Background(), codingagent.Session{
		ID: "coding-p6-plan", AgentSessionID: "agent-p6-plan", WorkspaceID: workspace.ID, WorktreeID: worktree.ID,
		ProviderProfileID: "profile-1", ModelID: "model-1", PermissionMode: codingagent.PermissionAsk,
	})
	if err != nil {
		t.Fatal(err)
	}
	planned, err := service.StartTurn(context.Background(), codingagent.TurnRequest{SessionID: session.ID, Text: "Plan with parallel evidence gathering", Mode: codingagent.TurnModePlan})
	if err != nil || planned.InterruptKind != "plan_approval" {
		t.Fatalf("parallel Plan = %#v, %v", planned, err)
	}
	model.mu.Lock()
	maximumActive := model.maximumActive
	elapsed := model.waveFinished.Sub(model.waveStarted)
	finalParentSawBoth := model.finalParentSawBoth
	model.mu.Unlock()
	if maximumActive < 2 || elapsed <= 0 || elapsed >= 450*time.Millisecond {
		t.Fatalf("parallel Plan wave active=%d elapsed=%s", maximumActive, elapsed)
	}
	if !finalParentSawBoth {
		t.Fatal("parent planner did not receive both structured exploration results")
	}
	turn, err := products.LoadTurn(context.Background(), planned.TurnID)
	if err != nil || turn.Status != codingagent.TurnInterrupted || turn.Phase != codingagent.TurnPhaseAwaitingPlanApproval || turn.Strategy != codingagent.ExecutionSingle || turn.WorkflowID != "" || len(turn.PendingPlanExploreIDs) != 0 {
		t.Fatalf("parallel Plan Turn = %#v, %v", turn, err)
	}
	children, err := products.ListChildAgents(context.Background(), turn.ID)
	if err != nil || len(children) != 2 {
		t.Fatalf("parallel Plan children = %#v, %v", children, err)
	}
	for _, child := range children {
		if child.Status != codingagent.ChildAgentCompleted || child.Profile != codingagent.CapabilityExplore || len(child.Task.WritePaths) != 0 {
			t.Fatalf("parallel Plan child escaped read-only boundary: %#v", child)
		}
	}
}

func TestP6ParallelWorkflowMarksResultsStaleWhenWorktreeDrifts(t *testing.T) {
	root := t.TempDir()
	for _, arguments := range [][]string{{"init", "--quiet"}, {"config", "user.name", "CodePilot Test"}, {"config", "user.email", "test@example.invalid"}, {"commit", "--allow-empty", "--quiet", "-m", "initial"}} {
		command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", arguments, err, output)
		}
	}
	products := codingmemory.NewRepository()
	now := time.Now().UTC()
	workspace := codingagent.Workspace{ID: "workspace-p6-drift", DisplayName: "p6-drift", GitCommonDir: filepath.Join(root, ".git"), Trusted: true, CreatedAt: now, UpdatedAt: now}
	worktree := codingagent.Worktree{ID: "worktree-p6-drift", WorkspaceID: workspace.ID, Root: root, GitDir: workspace.GitCommonDir, CreatedAt: now, LastUsedAt: now}
	if err := products.SaveWorkspace(context.Background(), workspace); err != nil {
		t.Fatal(err)
	}
	if err := products.SaveWorktree(context.Background(), worktree); err != nil {
		t.Fatal(err)
	}
	planArguments, _ := json.Marshal(codingagent.PlanSubmission{
		Goal: "Explore two areas against one stable workspace revision.", Scope: codingagent.PlanScope{Included: []string{"internal/api", "internal/ui"}},
		Findings: []string{"The areas are independent."}, Risks: []string{"Concurrent evidence becomes stale if relevant files change."},
		Steps: []codingagent.PlanStep{
			{ID: "explore-api", Goal: "Explore API behavior.", Files: []string{"internal/api"}, Validation: []string{"API evidence is recorded."}, Role: workflow.RoleExplore, FailureAction: workflow.FailureBlock, MaxAttempts: 1},
			{ID: "explore-ui", Goal: "Explore UI behavior.", Files: []string{"internal/ui"}, Validation: []string{"UI evidence is recorded."}, Role: workflow.RoleExplore, FailureAction: workflow.FailureBlock, MaxAttempts: 1},
		},
		AcceptanceCriteria: []string{"Evidence is current."}, RecommendedStrategy: codingagent.ExecutionWorkflowMultiParallelReadOnly,
		WorkspaceRelevant: true, CompletionMode: codingagent.PlanCompletionExecute,
	})
	model := &parallelReadOnlyModel{plan: planArguments, firstWaveReady: make(chan struct{})}
	agentSessions := agentsession.NewMemoryRepository()
	contexts, _ := contextmanager.NewManager()
	runtime, err := agent.NewRuntime(agent.Dependencies{Models: parallelReadOnlyModelFactory{model: model}, Contexts: contexts, Sessions: agentSessions})
	if err != nil {
		t.Fatal(err)
	}
	service, err := codingagent.NewService(codingagent.Dependencies{
		Sessions: products, Turns: products, Plans: products, Workflows: products, Children: products,
		AgentSessions: agentSessions, Worktrees: products, Agent: runtime, Tools: emptyToolFactory{}, Prompts: staticPrompt{}, Events: &parallelProductEvents{},
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := service.CreateSession(context.Background(), codingagent.Session{
		ID: "coding-p6-drift", AgentSessionID: "agent-p6-drift", WorkspaceID: workspace.ID, WorktreeID: worktree.ID,
		ProviderProfileID: "profile-1", ModelID: "model-1", PermissionMode: codingagent.PermissionAsk,
	})
	if err != nil {
		t.Fatal(err)
	}
	planned, err := service.StartTurn(context.Background(), codingagent.TurnRequest{SessionID: session.ID, Text: "Plan drift-sensitive parallel exploration", Mode: codingagent.TurnModePlan})
	if err != nil || planned.InterruptKind != "plan_approval" {
		t.Fatalf("planned = %#v, %v", planned, err)
	}
	driftWrite := make(chan error, 1)
	go func() {
		<-model.firstWaveReady
		if err := os.MkdirAll(filepath.Join(root, "internal", "api"), 0o755); err != nil {
			driftWrite <- err
			return
		}
		driftWrite <- os.WriteFile(filepath.Join(root, "internal", "api", "user-change.go"), []byte("package api\n"), 0o600)
	}()
	result, err := service.ResumeTurn(context.Background(), codingagent.ResumeTurnRequest{
		SessionID: session.ID, TurnID: planned.TurnID, InterruptID: planned.InterruptID,
		Decision: codingagent.ResolutionApproved, GrantScope: codingagent.PermissionGrantOnce, Strategy: codingagent.ExecutionWorkflowMultiParallelReadOnly,
	})
	if writeErr := <-driftWrite; writeErr != nil {
		t.Fatal(writeErr)
	}
	if err != nil || result.Status != string(workflow.StatusNeedsReplan) {
		t.Fatalf("parallel drift result = %#v, %v", result, err)
	}
	turn, err := products.LoadTurn(context.Background(), planned.TurnID)
	if err != nil || turn.WorkspaceDrift == nil || turn.WorkspaceDrift.Severity != codingagent.WorkspaceDriftMaterial || turn.WorkspaceDrift.Source != codingagent.WorkspaceDriftDuringExecution {
		t.Fatalf("parallel drift Turn = %#v, %v", turn, err)
	}
	durable, err := products.LoadWorkflow(context.Background(), workflow.ID(turn.WorkflowID))
	if err != nil || durable.Status != workflow.StatusNeedsReplan || durable.Nodes[0].Status != workflow.NodeCompleted || durable.Nodes[1].Status != workflow.NodeCompleted {
		t.Fatalf("parallel drift Workflow = %#v, %v", durable, err)
	}
}

func TestP6ParentCancellationBroadcastsToEveryParallelChild(t *testing.T) {
	root := t.TempDir()
	for _, arguments := range [][]string{{"init", "--quiet"}, {"config", "user.name", "CodePilot Test"}, {"config", "user.email", "test@example.invalid"}, {"commit", "--allow-empty", "--quiet", "-m", "initial"}} {
		command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", arguments, err, output)
		}
	}
	products := codingmemory.NewRepository()
	now := time.Now().UTC()
	workspace := codingagent.Workspace{ID: "workspace-p6-cancel", DisplayName: "p6-cancel", GitCommonDir: filepath.Join(root, ".git"), Trusted: true, CreatedAt: now, UpdatedAt: now}
	worktree := codingagent.Worktree{ID: "worktree-p6-cancel", WorkspaceID: workspace.ID, Root: root, GitDir: workspace.GitCommonDir, CreatedAt: now, LastUsedAt: now}
	if err := products.SaveWorkspace(context.Background(), workspace); err != nil {
		t.Fatal(err)
	}
	if err := products.SaveWorktree(context.Background(), worktree); err != nil {
		t.Fatal(err)
	}
	planArguments, _ := json.Marshal(codingagent.PlanSubmission{
		Goal: "Explore two independent areas until cancelled.", Scope: codingagent.PlanScope{Included: []string{"internal/api", "internal/ui"}},
		Findings: []string{"The nodes can run concurrently."}, Risks: []string{"Cancellation must stop both children."},
		Steps: []codingagent.PlanStep{
			{ID: "explore-api", Goal: "Explore API behavior.", Files: []string{"internal/api"}, Validation: []string{"API evidence is recorded."}, Role: workflow.RoleExplore, FailureAction: workflow.FailureBlock, MaxAttempts: 1},
			{ID: "explore-ui", Goal: "Explore UI behavior.", Files: []string{"internal/ui"}, Validation: []string{"UI evidence is recorded."}, Role: workflow.RoleExplore, FailureAction: workflow.FailureBlock, MaxAttempts: 1},
		},
		AcceptanceCriteria: []string{"Both explorations stop on cancellation."}, RecommendedStrategy: codingagent.ExecutionWorkflowMultiParallelReadOnly,
		WorkspaceRelevant: true, CompletionMode: codingagent.PlanCompletionExecute,
	})
	model := &parallelCancelModel{plan: planArguments, bothStarted: make(chan struct{})}
	agentSessions := agentsession.NewMemoryRepository()
	contexts, _ := contextmanager.NewManager()
	runtime, err := agent.NewRuntime(agent.Dependencies{Models: parallelCancelModelFactory{model: model}, Contexts: contexts, Sessions: agentSessions})
	if err != nil {
		t.Fatal(err)
	}
	service, err := codingagent.NewService(codingagent.Dependencies{
		Sessions: products, Turns: products, Plans: products, Workflows: products, Children: products,
		AgentSessions: agentSessions, Worktrees: products, Agent: runtime, Tools: emptyToolFactory{}, Prompts: staticPrompt{}, Events: &parallelProductEvents{},
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := service.CreateSession(context.Background(), codingagent.Session{
		ID: "coding-p6-cancel", AgentSessionID: "agent-p6-cancel", WorkspaceID: workspace.ID, WorktreeID: worktree.ID,
		ProviderProfileID: "profile-1", ModelID: "model-1", PermissionMode: codingagent.PermissionAsk,
	})
	if err != nil {
		t.Fatal(err)
	}
	planned, err := service.StartTurn(context.Background(), codingagent.TurnRequest{SessionID: session.ID, Text: "Plan cancellable parallel exploration", Mode: codingagent.TurnModePlan})
	if err != nil || planned.InterruptKind != "plan_approval" {
		t.Fatalf("planned = %#v, %v", planned, err)
	}
	type resumeOutcome struct {
		result codingagent.TurnResult
		err    error
	}
	resumed := make(chan resumeOutcome, 1)
	go func() {
		result, resumeErr := service.ResumeTurn(context.Background(), codingagent.ResumeTurnRequest{
			SessionID: session.ID, TurnID: planned.TurnID, InterruptID: planned.InterruptID,
			Decision: codingagent.ResolutionApproved, GrantScope: codingagent.PermissionGrantOnce, Strategy: codingagent.ExecutionWorkflowMultiParallelReadOnly,
		})
		resumed <- resumeOutcome{result: result, err: resumeErr}
	}()
	select {
	case <-model.bothStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("parallel children did not both start")
	}
	if err := service.CancelTurn(context.Background(), session.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case outcome := <-resumed:
		if outcome.err != nil || outcome.result.Status != string(workflow.StatusCancelled) {
			t.Fatalf("cancelled parallel Workflow = %#v, %v", outcome.result, outcome.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("parallel cancellation did not finish")
	}
	turn, err := products.LoadTurn(context.Background(), planned.TurnID)
	if err != nil || turn.Status != codingagent.TurnCancelled {
		t.Fatalf("cancelled Turn = %#v, %v", turn, err)
	}
	durable, err := products.LoadWorkflow(context.Background(), workflow.ID(turn.WorkflowID))
	if err != nil || durable.Status != workflow.StatusCancelled {
		t.Fatalf("cancelled Workflow = %#v, %v", durable, err)
	}
	children, err := products.ListChildAgents(context.Background(), turn.ID)
	if err != nil || len(children) != 2 {
		t.Fatalf("cancelled children = %#v, %v", children, err)
	}
	for _, child := range children {
		if child.Status != codingagent.ChildAgentCancelled {
			t.Fatalf("child continued after parent cancellation: %#v", child)
		}
	}
}

type parallelReadOnlyModelFactory struct{ model *parallelReadOnlyModel }

func (f parallelReadOnlyModelFactory) CreateModel(context.Context, llm.ModelRef) (llm.ChatModel, error) {
	return f.model, nil
}

type parallelReadOnlyModel struct {
	mu                sync.Mutex
	plan              json.RawMessage
	parentCalls       int
	active            int
	maximumActive     int
	firstWaveCount    int
	firstWaveReady    chan struct{}
	firstWaveStarted  time.Time
	firstWaveFinished time.Time
}

func (*parallelReadOnlyModel) Complete(context.Context, llm.ChatRequest) (llm.Message, error) {
	return llm.Message{}, nil
}

func (m *parallelReadOnlyModel) Stream(ctx context.Context, request llm.ChatRequest) (llm.Stream, error) {
	goal := delegatedGoal(request)
	if goal == "" {
		m.mu.Lock()
		call := m.parentCalls
		m.parentCalls++
		m.mu.Unlock()
		var response llm.Message
		if call == 0 {
			response = p5ToolCall("p6-context", "request_workspace_context", json.RawMessage(`{"reason":"The independent exploration targets depend on the current workspace."}`))
		} else {
			response = p5ToolCall("p6-plan", "exit_plan_mode", m.plan)
		}
		return &finalStream{events: []llm.StreamEvent{{Kind: llm.StreamResponseFinished, Message: &response}}}, nil
	}
	if strings.HasPrefix(goal, "Explore ") {
		m.mu.Lock()
		if m.firstWaveCount == 0 {
			m.firstWaveStarted = time.Now()
		}
		m.active++
		if m.active > m.maximumActive {
			m.maximumActive = m.active
		}
		m.firstWaveCount++
		if m.firstWaveCount == 2 {
			close(m.firstWaveReady)
		}
		m.mu.Unlock()
		select {
		case <-m.firstWaveReady:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		select {
		case <-time.After(250 * time.Millisecond):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		m.mu.Lock()
		m.active--
		if m.active == 0 {
			m.firstWaveFinished = time.Now()
		}
		m.mu.Unlock()
	}
	response := p5ChildResult("p6-result-"+strings.ReplaceAll(strings.ToLower(goal), " ", "-"), "Completed: "+goal, nil, []string{"Read-only evidence validated."})
	return &finalStream{events: []llm.StreamEvent{{Kind: llm.StreamResponseFinished, Message: &response}}}, nil
}

func delegatedGoal(request llm.ChatRequest) string {
	for _, message := range request.Messages {
		for _, content := range message.Content {
			if content.Type != llm.ContentText || !strings.Contains(content.Text, "\"kind\":\"agent_task_v1\"") {
				continue
			}
			var payload struct {
				Goal string `json:"goal"`
				Task struct {
					Goal string `json:"goal"`
				} `json:"task"`
			}
			start := strings.Index(content.Text, "{")
			if start >= 0 && json.Unmarshal([]byte(content.Text[start:]), &payload) == nil {
				return payload.Task.Goal
			}
		}
	}
	return ""
}

type parallelProductEvents struct{ mu sync.Mutex }

func (e *parallelProductEvents) PublishCodingEvent(context.Context, codingagent.Event) error {
	e.mu.Lock()
	e.mu.Unlock()
	return nil
}

type parallelPlanModelFactory struct{ model *parallelPlanModel }

func (f parallelPlanModelFactory) CreateModel(context.Context, llm.ModelRef) (llm.ChatModel, error) {
	return f.model, nil
}

type parallelPlanModel struct {
	mu                 sync.Mutex
	plan               json.RawMessage
	parentCalls        int
	active             int
	maximumActive      int
	waveCount          int
	waveReady          chan struct{}
	waveStarted        time.Time
	waveFinished       time.Time
	finalParentSawBoth bool
}

type parallelCancelModelFactory struct{ model *parallelCancelModel }

func (f parallelCancelModelFactory) CreateModel(context.Context, llm.ModelRef) (llm.ChatModel, error) {
	return f.model, nil
}

type parallelCancelModel struct {
	mu          sync.Mutex
	plan        json.RawMessage
	parentCalls int
	started     int
	bothStarted chan struct{}
}

func (*parallelCancelModel) Complete(context.Context, llm.ChatRequest) (llm.Message, error) {
	return llm.Message{}, nil
}

func (m *parallelCancelModel) Stream(ctx context.Context, request llm.ChatRequest) (llm.Stream, error) {
	if delegatedGoal(request) == "" {
		m.mu.Lock()
		call := m.parentCalls
		m.parentCalls++
		m.mu.Unlock()
		response := p5ToolCall("p6-cancel-context", "request_workspace_context", json.RawMessage(`{"reason":"The exploration targets depend on this workspace."}`))
		if call != 0 {
			response = p5ToolCall("p6-cancel-plan", "exit_plan_mode", m.plan)
		}
		return &finalStream{events: []llm.StreamEvent{{Kind: llm.StreamResponseFinished, Message: &response}}}, nil
	}
	m.mu.Lock()
	m.started++
	if m.started == 2 {
		close(m.bothStarted)
	}
	m.mu.Unlock()
	<-ctx.Done()
	return nil, ctx.Err()
}

func (*parallelPlanModel) Complete(context.Context, llm.ChatRequest) (llm.Message, error) {
	return llm.Message{}, nil
}

func (m *parallelPlanModel) Stream(ctx context.Context, request llm.ChatRequest) (llm.Stream, error) {
	goal := delegatedGoal(request)
	if goal == "" {
		m.mu.Lock()
		call := m.parentCalls
		m.parentCalls++
		if call == 2 {
			m.finalParentSawBoth = requestContainsText(request, "API evidence is current.") && requestContainsText(request, "UI evidence is current.")
		}
		m.mu.Unlock()
		var response llm.Message
		switch call {
		case 0:
			response = p5ToolCall("p6-plan-context", "request_workspace_context", json.RawMessage(`{"reason":"The Plan depends on two independent workspace areas."}`))
		case 1:
			response = p5ToolCall("p6-plan-delegate", "delegate_plan_explores", json.RawMessage(`{"tasks":[{"goal":"Inspect API evidence.","read_paths":["internal/api"],"acceptance_criteria":["Record API evidence."]},{"goal":"Inspect UI evidence.","read_paths":["internal/ui"],"acceptance_criteria":["Record UI evidence."]}]}`))
		default:
			response = p5ToolCall("p6-plan-submit", "exit_plan_mode", m.plan)
		}
		return &finalStream{events: []llm.StreamEvent{{Kind: llm.StreamResponseFinished, Message: &response}}}, nil
	}
	m.mu.Lock()
	if m.waveCount == 0 {
		m.waveStarted = time.Now()
	}
	m.waveCount++
	m.active++
	if m.active > m.maximumActive {
		m.maximumActive = m.active
	}
	if m.waveCount == 2 {
		close(m.waveReady)
	}
	m.mu.Unlock()
	select {
	case <-m.waveReady:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case <-time.After(250 * time.Millisecond):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	m.mu.Lock()
	m.active--
	if m.active == 0 {
		m.waveFinished = time.Now()
	}
	m.mu.Unlock()
	conclusion := "API evidence is current."
	if strings.Contains(goal, "UI") {
		conclusion = "UI evidence is current."
	}
	response := p5ChildResult("p6-plan-result-"+strings.ToLower(strings.Fields(goal)[1]), conclusion, nil, []string{"Read-only inspection completed."})
	return &finalStream{events: []llm.StreamEvent{{Kind: llm.StreamResponseFinished, Message: &response}}}, nil
}
