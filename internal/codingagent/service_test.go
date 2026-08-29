package codingagent_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
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
)

type finalModelFactory struct{}

func (finalModelFactory) CreateModel(context.Context, llm.ModelRef) (llm.ChatModel, error) {
	return finalModel{}, nil
}

type finalModel struct{}

func (finalModel) Complete(context.Context, llm.ChatRequest) (llm.Message, error) {
	return finalAssistant(), nil
}

func (finalModel) Stream(context.Context, llm.ChatRequest) (llm.Stream, error) {
	message := finalAssistant()
	return &finalStream{events: []llm.StreamEvent{{Kind: llm.StreamTextDelta, Delta: "done"}, {Kind: llm.StreamResponseFinished, Message: &message}}}, nil
}

func finalAssistant() llm.Message {
	return llm.Message{Role: llm.RoleAssistant, Provider: "profile-1", Model: "model-1", StopReason: llm.StopReasonStop, Content: []llm.Content{{Type: llm.ContentText, Text: "done"}}}
}

type blockingModelFactory struct{ started chan struct{} }

func (f blockingModelFactory) CreateModel(context.Context, llm.ModelRef) (llm.ChatModel, error) {
	return blockingModel{started: f.started}, nil
}

type blockingModel struct{ started chan struct{} }

func (blockingModel) Complete(context.Context, llm.ChatRequest) (llm.Message, error) {
	return llm.Message{}, nil
}

func (m blockingModel) Stream(ctx context.Context, _ llm.ChatRequest) (llm.Stream, error) {
	select {
	case m.started <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

type finalStream struct{ events []llm.StreamEvent }

func (s *finalStream) Recv() (llm.StreamEvent, error) {
	if len(s.events) == 0 {
		return llm.StreamEvent{}, io.EOF
	}
	event := s.events[0]
	s.events = s.events[1:]
	return event, nil
}

func (*finalStream) Close() error { return nil }

type emptyToolFactory struct{}

func (emptyToolFactory) CreateTools(context.Context, codingagent.ToolScope) (*tool.Registry, error) {
	return tool.NewRegistry()
}

type failTerminalTurnRepository struct {
	codingagent.TurnRepository
	failOnce bool
}

func (r *failTerminalTurnRepository) SaveTurn(ctx context.Context, turn codingagent.Turn, expectedRevision uint64) error {
	if r.failOnce && len(turn.Runs) != 0 {
		status := turn.Runs[len(turn.Runs)-1].Status
		if status == codingagent.RunBindingHandedOff || status == codingagent.RunBindingCancelled || status == codingagent.RunBindingCompleted || status == codingagent.RunBindingFailed {
			r.failOnce = false
			return errors.New("injected terminal Product Turn write failure")
		}
	}
	return r.TurnRepository.SaveTurn(ctx, turn, expectedRevision)
}

type staticPrompt struct{}

func (staticPrompt) BuildSystemPrompt(context.Context, codingagent.PromptScope) (string, error) {
	return "coding system prompt", nil
}

type productEvents struct{ values []codingagent.Event }

func (e *productEvents) PublishCodingEvent(_ context.Context, event codingagent.Event) error {
	e.values = append(e.values, event)
	return nil
}

type sequentialModelFactory struct{ model *sequentialModel }

func (f sequentialModelFactory) CreateModel(context.Context, llm.ModelRef) (llm.ChatModel, error) {
	return f.model, nil
}

type sequentialModel struct {
	responses []llm.Message
	requests  []llm.ChatRequest
}

func (m *sequentialModel) Complete(context.Context, llm.ChatRequest) (llm.Message, error) {
	return llm.Message{}, nil
}

func (m *sequentialModel) Stream(_ context.Context, request llm.ChatRequest) (llm.Stream, error) {
	m.requests = append(m.requests, request)
	response := m.responses[0]
	m.responses = m.responses[1:]
	return &finalStream{events: []llm.StreamEvent{{Kind: llm.StreamResponseFinished, Message: &response}}}, nil
}

func TestServiceAgentSuggestedPlanRequiresConsentAndExecutesInOriginalTurn(t *testing.T) {
	root := t.TempDir()
	for _, arguments := range [][]string{{"init", "--quiet"}, {"config", "user.name", "CodePilot Test"}, {"config", "user.email", "test@example.invalid"}, {"commit", "--allow-empty", "--quiet", "-m", "initial"}} {
		command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", arguments, err, output)
		}
	}
	products := codingmemory.NewRepository()
	now := time.Now().UTC()
	workspace := codingagent.Workspace{ID: "workspace-suggested-plan", DisplayName: "suggested-plan", GitCommonDir: filepath.Join(root, ".git"), Trusted: true, CreatedAt: now, UpdatedAt: now}
	worktree := codingagent.Worktree{ID: "worktree-suggested-plan", WorkspaceID: workspace.ID, Root: root, GitDir: workspace.GitCommonDir, CreatedAt: now, LastUsedAt: now}
	if err := products.SaveWorkspace(context.Background(), workspace); err != nil {
		t.Fatal(err)
	}
	if err := products.SaveWorktree(context.Background(), worktree); err != nil {
		t.Fatal(err)
	}
	entryArguments := json.RawMessage(`{"reason_code":"cross_module_change","summary":"This change spans the coordinator, persistence, and UI, so reviewing the sequence first will reduce rework."}`)
	workspaceArguments := json.RawMessage(`{"reason":"The requested cross-module code change depends on the current implementation."}`)
	planArguments, _ := json.Marshal(codingagent.PlanSubmission{
		Goal: "Implement Agent-suggested Plan entry.", Scope: codingagent.PlanScope{Included: []string{"internal/codingagent", "internal/ui"}},
		Findings: []string{"The Product Turn can continue across capability profiles."}, Risks: []string{"Entry approval must not grant write permission."},
		Steps: []codingagent.PlanStep{
			{ID: "coordinator", Goal: "Add the trusted entry transition.", Files: []string{"internal/codingagent/service.go"}, Validation: []string{"Run coordinator tests."}},
			{ID: "ui", Goal: "Show the user decision.", DependsOn: []string{"coordinator"}, Files: []string{"internal/ui/approval_picker.go"}, Validation: []string{"Run UI tests."}},
		},
		AcceptanceCriteria: []string{"The user controls entry into Plan mode."}, RecommendedStrategy: codingagent.ExecutionSingle,
		WorkspaceRelevant: true, CompletionMode: codingagent.PlanCompletionExecute,
	})
	responses := []llm.Message{
		{Role: llm.RoleAssistant, Provider: "profile-1", Model: "model-1", StopReason: llm.StopReasonToolUse, Content: []llm.Content{{Type: llm.ContentToolCall, ToolCall: &llm.ToolCall{ID: "enter-plan", Name: "enter_plan_mode", Arguments: entryArguments}}}},
		{Role: llm.RoleAssistant, Provider: "profile-1", Model: "model-1", StopReason: llm.StopReasonToolUse, Content: []llm.Content{{Type: llm.ContentToolCall, ToolCall: &llm.ToolCall{ID: "workspace-context", Name: "request_workspace_context", Arguments: workspaceArguments}}}},
		{Role: llm.RoleAssistant, Provider: "profile-1", Model: "model-1", StopReason: llm.StopReasonToolUse, Content: []llm.Content{{Type: llm.ContentToolCall, ToolCall: &llm.ToolCall{ID: "exit-plan", Name: "exit_plan_mode", Arguments: planArguments}}}},
		finalAssistant(),
	}
	model := &sequentialModel{responses: responses}
	agentSessions := agentsession.NewMemoryRepository()
	contexts, _ := contextmanager.NewManager()
	runtime, err := agent.NewRuntime(agent.Dependencies{Models: sequentialModelFactory{model: model}, Contexts: contexts, Sessions: agentSessions})
	if err != nil {
		t.Fatal(err)
	}
	events := &productEvents{}
	service, err := codingagent.NewService(codingagent.Dependencies{
		Sessions: products, Turns: products, Plans: products, AgentSessions: agentSessions, Worktrees: products,
		Agent: runtime, Tools: emptyToolFactory{}, Prompts: staticPrompt{}, Events: events,
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := service.CreateSession(context.Background(), codingagent.Session{
		ID: "coding-suggested-plan", AgentSessionID: "agent-suggested-plan", WorkspaceID: workspace.ID, WorktreeID: worktree.ID,
		ProviderProfileID: "profile-1", ModelID: "model-1", PermissionMode: codingagent.PermissionAsk,
	})
	if err != nil {
		t.Fatal(err)
	}
	waiting, err := service.StartTurn(context.Background(), codingagent.TurnRequest{SessionID: session.ID, Text: "Refactor the plan workflow across product layers"})
	if err != nil || waiting.InterruptKind != "plan_entry_approval" {
		t.Fatalf("Plan entry suggestion = %#v, %v", waiting, err)
	}
	snapshot, err := service.Snapshot(context.Background(), session.ID)
	if err != nil || !snapshot.PendingPlanEntryApproval || snapshot.ActiveTurn == nil || snapshot.ActiveTurn.Phase != codingagent.TurnPhaseAwaitingPlanEntryApproval || len(snapshot.PendingInterrupts) != 1 || snapshot.PendingInterrupts[0].PlanEntryReason != codingagent.PlanEntryCrossModuleChange {
		t.Fatalf("Plan entry snapshot = %#v, %v", snapshot, err)
	}
	if len(model.requests) != 1 || !requestHasTool(model.requests[0], "enter_plan_mode") {
		t.Fatalf("Direct request tools = %#v", model.requests)
	}
	if _, err := service.ResumeTurn(context.Background(), codingagent.ResumeTurnRequest{
		SessionID: session.ID, TurnID: waiting.TurnID, InterruptID: waiting.InterruptID,
		Decision: codingagent.ResolutionApproved, GrantScope: codingagent.PermissionGrantSession,
	}); err == nil {
		t.Fatal("Plan entry approval created a session permission grant")
	}
	planned, err := service.ResumeTurn(context.Background(), codingagent.ResumeTurnRequest{
		SessionID: session.ID, TurnID: waiting.TurnID, InterruptID: waiting.InterruptID,
		Decision: codingagent.ResolutionApproved, GrantScope: codingagent.PermissionGrantOnce,
	})
	if err != nil || planned.InterruptKind != "plan_approval" {
		t.Fatalf("approved Plan entry = %#v, %v", planned, err)
	}
	if len(model.requests) != 3 || requestHasTool(model.requests[1], "enter_plan_mode") || !requestHasTool(model.requests[1], "request_workspace_context") || !requestHasTool(model.requests[2], "exit_plan_mode") || requestHasTool(model.requests[2], "apply_patch") {
		t.Fatalf("Plan request tools = %#v", model.requests)
	}
	executed, err := service.ResumeTurn(context.Background(), codingagent.ResumeTurnRequest{
		SessionID: session.ID, TurnID: waiting.TurnID, InterruptID: planned.InterruptID,
		Decision: codingagent.ResolutionApproved, GrantScope: codingagent.PermissionGrantOnce,
	})
	if err != nil || executed.Status != string(agent.RunCompleted) || executed.Response != "done" {
		t.Fatalf("approved suggested Plan execution = %#v, %v", executed, err)
	}
	turn, err := products.LoadTurn(context.Background(), waiting.TurnID)
	if err != nil || turn.Phase != codingagent.TurnPhaseExecuting || turn.Status != codingagent.TurnCompleted || len(turn.Runs) != 4 {
		t.Fatalf("suggested Plan Product Turn = %#v, %v", turn, err)
	}
	durable, err := agentSessions.Load(context.Background(), session.AgentSessionID)
	if err != nil {
		t.Fatal(err)
	}
	userMessages := 0
	for _, entry := range durable.Entries {
		if entry.Message != nil && entry.Message.Role == llm.RoleUser {
			userMessages++
		}
	}
	if userMessages != 1 {
		t.Fatalf("user messages = %d, want one original request", userMessages)
	}
	var suggested, approved, started bool
	for _, event := range events.values {
		suggested = suggested || event.Kind == codingagent.EventPlanEntrySuggested
		approved = approved || event.Kind == codingagent.EventPlanEntryApproved
		started = started || event.Kind == codingagent.EventPlanStarted
	}
	if !suggested || !approved || !started {
		t.Fatalf("Plan entry events = %#v", events.values)
	}
}

func TestServiceDeclinedPlanSuggestionContinuesDirectAndDoesNotLoop(t *testing.T) {
	products := codingmemory.NewRepository()
	seedMemoryWorktree(t, products, "workspace-plan-declined", "worktree-plan-declined")
	entryArguments := json.RawMessage(`{"reason_code":"material_ambiguity","summary":"The requested behavior has two materially different compatibility outcomes."}`)
	entryCall := func(id string) llm.Message {
		return llm.Message{Role: llm.RoleAssistant, Provider: "profile-1", Model: "model-1", StopReason: llm.StopReasonToolUse, Content: []llm.Content{{Type: llm.ContentToolCall, ToolCall: &llm.ToolCall{ID: id, Name: "enter_plan_mode", Arguments: entryArguments}}}}
	}
	model := &sequentialModel{responses: []llm.Message{entryCall("enter-first"), entryCall("enter-repeat"), finalAssistant()}}
	agentSessions := agentsession.NewMemoryRepository()
	contexts, _ := contextmanager.NewManager()
	runtime, _ := agent.NewRuntime(agent.Dependencies{Models: sequentialModelFactory{model: model}, Contexts: contexts, Sessions: agentSessions})
	service, err := codingagent.NewService(codingagent.Dependencies{
		Sessions: products, Turns: products, Plans: products, AgentSessions: agentSessions, Worktrees: products,
		Agent: runtime, Tools: emptyToolFactory{}, Prompts: staticPrompt{}, Events: &productEvents{},
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := service.CreateSession(context.Background(), codingagent.Session{
		ID: "coding-plan-declined", AgentSessionID: "agent-plan-declined", WorkspaceID: "workspace-plan-declined", WorktreeID: "worktree-plan-declined",
		ProviderProfileID: "profile-1", ModelID: "model-1", PermissionMode: codingagent.PermissionAsk,
	})
	if err != nil {
		t.Fatal(err)
	}
	waiting, err := service.StartTurn(context.Background(), codingagent.TurnRequest{SessionID: session.ID, Text: "Make the requested compatibility change"})
	if err != nil || waiting.InterruptKind != "plan_entry_approval" {
		t.Fatalf("Plan entry suggestion = %#v, %v", waiting, err)
	}
	completed, err := service.ResumeTurn(context.Background(), codingagent.ResumeTurnRequest{
		SessionID: session.ID, TurnID: waiting.TurnID, InterruptID: waiting.InterruptID,
		Decision: codingagent.ResolutionDenied, GrantScope: codingagent.PermissionGrantOnce,
	})
	if err != nil || completed.Status != string(agent.RunCompleted) || completed.Response != "done" {
		t.Fatalf("declined Plan entry = %#v, %v", completed, err)
	}
	turn, err := products.LoadTurn(context.Background(), waiting.TurnID)
	if err != nil || turn.Phase != codingagent.TurnPhaseDirect || turn.Status != codingagent.TurnCompleted || len(turn.Runs) != 1 || len(turn.DeclinedPlanReasons) != 1 || turn.DeclinedPlanReasons[0] != codingagent.PlanEntryMaterialAmbiguity {
		t.Fatalf("declined Direct Turn = %#v, %v", turn, err)
	}
	snapshot, err := service.Snapshot(context.Background(), session.ID)
	if err != nil || len(snapshot.PendingInterrupts) != 0 || snapshot.PendingPlanEntryApproval {
		t.Fatalf("declined Plan snapshot = %#v, %v", snapshot, err)
	}
}

func TestRecoveryContinuesDurablyDeclinedPlanSuggestion(t *testing.T) {
	products := codingmemory.NewRepository()
	seedMemoryWorktree(t, products, "workspace-plan-decline-recovery", "worktree-plan-decline-recovery")
	entryArguments := json.RawMessage(`{"reason_code":"complex_validation","summary":"The validation sequence spans several failure boundaries."}`)
	model := &sequentialModel{responses: []llm.Message{
		{Role: llm.RoleAssistant, Provider: "profile-1", Model: "model-1", StopReason: llm.StopReasonToolUse, Content: []llm.Content{{Type: llm.ContentToolCall, ToolCall: &llm.ToolCall{ID: "enter-plan-recovery", Name: "enter_plan_mode", Arguments: entryArguments}}}},
		finalAssistant(),
	}}
	agentSessions := agentsession.NewMemoryRepository()
	newService := func(t *testing.T) *codingagent.Service {
		t.Helper()
		contexts, _ := contextmanager.NewManager()
		runtime, err := agent.NewRuntime(agent.Dependencies{Models: sequentialModelFactory{model: model}, Contexts: contexts, Sessions: agentSessions})
		if err != nil {
			t.Fatal(err)
		}
		service, err := codingagent.NewService(codingagent.Dependencies{
			Sessions: products, Turns: products, Plans: products, AgentSessions: agentSessions, Worktrees: products,
			Agent: runtime, Tools: emptyToolFactory{}, Prompts: staticPrompt{}, Events: &productEvents{},
		})
		if err != nil {
			t.Fatal(err)
		}
		return service
	}
	service := newService(t)
	session, err := service.CreateSession(context.Background(), codingagent.Session{
		ID: "coding-plan-decline-recovery", AgentSessionID: "agent-plan-decline-recovery", WorkspaceID: "workspace-plan-decline-recovery", WorktreeID: "worktree-plan-decline-recovery",
		ProviderProfileID: "profile-1", ModelID: "model-1", PermissionMode: codingagent.PermissionAsk,
	})
	if err != nil {
		t.Fatal(err)
	}
	waiting, err := service.StartTurn(context.Background(), codingagent.TurnRequest{SessionID: session.ID, Text: "Make the validation change"})
	if err != nil || waiting.InterruptKind != "plan_entry_approval" {
		t.Fatalf("Plan entry suggestion = %#v, %v", waiting, err)
	}
	restarted := newService(t)
	snapshot, err := restarted.Snapshot(context.Background(), session.ID)
	if err != nil || !snapshot.PendingPlanEntryApproval || snapshot.ActiveTurn == nil || snapshot.ActiveTurn.Phase != codingagent.TurnPhaseAwaitingPlanEntryApproval || len(snapshot.PendingInterrupts) != 1 || snapshot.PendingInterrupts[0].InterruptID != waiting.InterruptID {
		t.Fatalf("restarted Plan entry decision = %#v, %v", snapshot, err)
	}

	// Simulate a process exit after the Product Turn recorded the decline but
	// before the generic Agent journal recorded and applied the same decision.
	turn, err := products.LoadTurn(context.Background(), waiting.TurnID)
	if err != nil {
		t.Fatal(err)
	}
	expected := turn.Revision
	turn.Phase = codingagent.TurnPhaseDirect
	turn.DeclinedPlanReasons = append(turn.DeclinedPlanReasons, codingagent.PlanEntryComplexValidation)
	turn.UpdatedAt = time.Now().UTC()
	turn.Revision++
	if err := products.SaveTurn(context.Background(), turn, expected); err != nil {
		t.Fatal(err)
	}

	completed, err := newService(t).ResumeTurn(context.Background(), codingagent.ResumeTurnRequest{
		SessionID: session.ID, TurnID: waiting.TurnID, InterruptID: waiting.InterruptID,
		Decision: codingagent.ResolutionDenied, GrantScope: codingagent.PermissionGrantOnce,
	})
	if err != nil || completed.Status != string(agent.RunCompleted) || completed.Response != "done" {
		t.Fatalf("recover declined Plan entry = %#v, %v", completed, err)
	}
	turn, err = products.LoadTurn(context.Background(), waiting.TurnID)
	if err != nil || turn.Phase != codingagent.TurnPhaseDirect || turn.Status != codingagent.TurnCompleted || len(turn.DeclinedPlanReasons) != 1 {
		t.Fatalf("recovered declined Product Turn = %#v, %v", turn, err)
	}
}

func TestServicePlanSuggestionCanReturnForDifferentNewRisk(t *testing.T) {
	products := codingmemory.NewRepository()
	seedMemoryWorktree(t, products, "workspace-plan-new-risk", "worktree-plan-new-risk")
	entryCall := func(id string, reason codingagent.PlanEntryReasonCode, summary string) llm.Message {
		arguments, _ := json.Marshal(map[string]string{"reason_code": string(reason), "summary": summary})
		return llm.Message{Role: llm.RoleAssistant, Provider: "profile-1", Model: "model-1", StopReason: llm.StopReasonToolUse, Content: []llm.Content{{Type: llm.ContentToolCall, ToolCall: &llm.ToolCall{ID: id, Name: "enter_plan_mode", Arguments: arguments}}}}
	}
	model := &sequentialModel{responses: []llm.Message{
		entryCall("entry-ambiguity", codingagent.PlanEntryMaterialAmbiguity, "The initial request has two materially different outcomes."),
		entryCall("entry-security", codingagent.PlanEntrySecurityPermissions, "New evidence shows this also changes credential access boundaries."),
	}}
	agentSessions := agentsession.NewMemoryRepository()
	contexts, _ := contextmanager.NewManager()
	runtime, _ := agent.NewRuntime(agent.Dependencies{Models: sequentialModelFactory{model: model}, Contexts: contexts, Sessions: agentSessions})
	service, err := codingagent.NewService(codingagent.Dependencies{
		Sessions: products, Turns: products, Plans: products, AgentSessions: agentSessions, Worktrees: products,
		Agent: runtime, Tools: emptyToolFactory{}, Prompts: staticPrompt{}, Events: &productEvents{},
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := service.CreateSession(context.Background(), codingagent.Session{
		ID: "coding-plan-new-risk", AgentSessionID: "agent-plan-new-risk", WorkspaceID: "workspace-plan-new-risk", WorktreeID: "worktree-plan-new-risk",
		ProviderProfileID: "profile-1", ModelID: "model-1", PermissionMode: codingagent.PermissionAsk,
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.StartTurn(context.Background(), codingagent.TurnRequest{SessionID: session.ID, Text: "Make the requested change"})
	if err != nil || first.InterruptKind != "plan_entry_approval" {
		t.Fatalf("initial suggestion = %#v, %v", first, err)
	}
	second, err := service.ResumeTurn(context.Background(), codingagent.ResumeTurnRequest{
		SessionID: session.ID, TurnID: first.TurnID, InterruptID: first.InterruptID,
		Decision: codingagent.ResolutionDenied, GrantScope: codingagent.PermissionGrantOnce,
	})
	if err != nil || second.InterruptKind != "plan_entry_approval" || second.InterruptID == first.InterruptID {
		t.Fatalf("new-risk suggestion = %#v, %v", second, err)
	}
	turn, err := products.LoadTurn(context.Background(), first.TurnID)
	if err != nil || turn.PlanEntrySuggestion == nil || turn.PlanEntrySuggestion.ReasonCode != codingagent.PlanEntrySecurityPermissions || len(turn.DeclinedPlanReasons) != 1 || turn.DeclinedPlanReasons[0] != codingagent.PlanEntryMaterialAmbiguity {
		t.Fatalf("new-risk Product Turn = %#v, %v", turn, err)
	}
}

func TestServiceCancelledPlanSuggestionCancelsOriginalTask(t *testing.T) {
	products := codingmemory.NewRepository()
	seedMemoryWorktree(t, products, "workspace-plan-entry-cancel", "worktree-plan-entry-cancel")
	model := &sequentialModel{responses: []llm.Message{{
		Role: llm.RoleAssistant, Provider: "profile-1", Model: "model-1", StopReason: llm.StopReasonToolUse,
		Content: []llm.Content{{Type: llm.ContentToolCall, ToolCall: &llm.ToolCall{
			ID: "enter-cancel", Name: "enter_plan_mode", Arguments: json.RawMessage(`{"reason_code":"high_rollback_cost","summary":"The requested migration has a high rollback cost."}`),
		}}},
	}}}
	agentSessions := agentsession.NewMemoryRepository()
	contexts, _ := contextmanager.NewManager()
	runtime, _ := agent.NewRuntime(agent.Dependencies{Models: sequentialModelFactory{model: model}, Contexts: contexts, Sessions: agentSessions})
	service, err := codingagent.NewService(codingagent.Dependencies{
		Sessions: products, Turns: products, Plans: products, AgentSessions: agentSessions, Worktrees: products,
		Agent: runtime, Tools: emptyToolFactory{}, Prompts: staticPrompt{}, Events: &productEvents{},
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := service.CreateSession(context.Background(), codingagent.Session{
		ID: "coding-plan-entry-cancel", AgentSessionID: "agent-plan-entry-cancel", WorkspaceID: "workspace-plan-entry-cancel", WorktreeID: "worktree-plan-entry-cancel",
		ProviderProfileID: "profile-1", ModelID: "model-1", PermissionMode: codingagent.PermissionAsk,
	})
	if err != nil {
		t.Fatal(err)
	}
	waiting, err := service.StartTurn(context.Background(), codingagent.TurnRequest{SessionID: session.ID, Text: "Perform the risky migration"})
	if err != nil || waiting.InterruptKind != "plan_entry_approval" {
		t.Fatalf("Plan entry suggestion = %#v, %v", waiting, err)
	}
	cancelled, err := service.ResumeTurn(context.Background(), codingagent.ResumeTurnRequest{
		SessionID: session.ID, TurnID: waiting.TurnID, InterruptID: waiting.InterruptID,
		Decision: codingagent.ResolutionCancelled, GrantScope: codingagent.PermissionGrantOnce,
	})
	if err != nil || cancelled.Status != string(agent.RunAborted) {
		t.Fatalf("cancelled Plan entry = %#v, %v", cancelled, err)
	}
	turn, err := products.LoadTurn(context.Background(), waiting.TurnID)
	if err != nil || turn.Status != codingagent.TurnCancelled || len(turn.Runs) != 1 {
		t.Fatalf("cancelled Product Turn = %#v, %v", turn, err)
	}
}

func TestRecoveryClosesApprovedPlanEntryHandoffBeforeStartingPlanning(t *testing.T) {
	products := codingmemory.NewRepository()
	seedMemoryWorktree(t, products, "workspace-plan-entry-recovery", "worktree-plan-entry-recovery")
	planArguments, _ := json.Marshal(codingagent.PlanSubmission{
		Goal: "Provide a reviewed rollout outline.", Scope: codingagent.PlanScope{Included: []string{"Rollout outline"}},
		Findings: []string{"The user approved entering Plan mode."}, Risks: []string{"Keep the outline reversible."},
		Steps:              []codingagent.PlanStep{{ID: "outline", Goal: "Describe the rollout sequence.", Validation: []string{"Review the sequence."}}},
		AcceptanceCriteria: []string{"The outline is actionable."}, RecommendedStrategy: codingagent.ExecutionSingle,
		WorkspaceRelevant: false, CompletionMode: codingagent.PlanCompletionDeliverable,
	})
	model := &sequentialModel{responses: []llm.Message{
		{Role: llm.RoleAssistant, Provider: "profile-1", Model: "model-1", StopReason: llm.StopReasonToolUse, Content: []llm.Content{{Type: llm.ContentToolCall, ToolCall: &llm.ToolCall{ID: "entry-recovery", Name: "enter_plan_mode", Arguments: json.RawMessage(`{"reason_code":"ordered_dependencies","summary":"The rollout has important ordering dependencies."}`)}}}},
		{Role: llm.RoleAssistant, Provider: "profile-1", Model: "model-1", StopReason: llm.StopReasonToolUse, Content: []llm.Content{{Type: llm.ContentToolCall, ToolCall: &llm.ToolCall{ID: "plan-after-recovery", Name: "exit_plan_mode", Arguments: planArguments}}}},
	}}
	agentSessions := agentsession.NewMemoryRepository()
	newRuntime := func(t *testing.T) *agent.Runtime {
		t.Helper()
		contexts, _ := contextmanager.NewManager()
		runtime, err := agent.NewRuntime(agent.Dependencies{Models: sequentialModelFactory{model: model}, Contexts: contexts, Sessions: agentSessions})
		if err != nil {
			t.Fatal(err)
		}
		return runtime
	}
	failingTurns := &failTerminalTurnRepository{TurnRepository: products, failOnce: true}
	first, err := codingagent.NewService(codingagent.Dependencies{
		Sessions: products, Turns: failingTurns, Plans: products, AgentSessions: agentSessions, Worktrees: products,
		Agent: newRuntime(t), Tools: emptyToolFactory{}, Prompts: staticPrompt{}, Events: &productEvents{},
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := first.CreateSession(context.Background(), codingagent.Session{
		ID: "coding-plan-entry-recovery", AgentSessionID: "agent-plan-entry-recovery", WorkspaceID: "workspace-plan-entry-recovery", WorktreeID: "worktree-plan-entry-recovery",
		ProviderProfileID: "profile-1", ModelID: "model-1", PermissionMode: codingagent.PermissionAsk,
	})
	if err != nil {
		t.Fatal(err)
	}
	waiting, err := first.StartTurn(context.Background(), codingagent.TurnRequest{SessionID: session.ID, Text: "Prepare the rollout"})
	if err != nil || waiting.InterruptKind != "plan_entry_approval" {
		t.Fatalf("Plan entry suggestion = %#v, %v", waiting, err)
	}
	if _, err := first.ResumeTurn(context.Background(), codingagent.ResumeTurnRequest{
		SessionID: session.ID, TurnID: waiting.TurnID, InterruptID: waiting.InterruptID,
		Decision: codingagent.ResolutionApproved, GrantScope: codingagent.PermissionGrantOnce,
	}); err == nil {
		t.Fatal("injected Product Turn handoff write did not fail")
	}
	recovered, err := codingagent.NewService(codingagent.Dependencies{
		Sessions: products, Turns: products, Plans: products, AgentSessions: agentSessions, Worktrees: products,
		Agent: newRuntime(t), Tools: emptyToolFactory{}, Prompts: staticPrompt{}, Events: &productEvents{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if completed, err := recovered.RecoverAutomatically(context.Background(), session.ID); err != nil || completed < 2 {
		t.Fatalf("recover approved Plan entry = %d, %v", completed, err)
	}
	snapshot, err := recovered.Snapshot(context.Background(), session.ID)
	if err != nil || snapshot.ActiveTurn == nil || snapshot.ActiveTurn.Phase != codingagent.TurnPhaseAwaitingPlanApproval || !snapshot.PendingPlanApproval || snapshot.PendingPlanEntryApproval || len(snapshot.PendingInterrupts) != 1 || snapshot.PendingInterrupts[0].Kind != "plan_approval" {
		t.Fatalf("recovered Planning snapshot = %#v, %v", snapshot, err)
	}
	turn, err := products.LoadTurn(context.Background(), waiting.TurnID)
	if err != nil || len(turn.Runs) != 2 || turn.Runs[0].Status != codingagent.RunBindingHandedOff || turn.Runs[1].Status != codingagent.RunBindingInterrupted {
		t.Fatalf("recovered Plan entry Turn = %#v, %v", turn, err)
	}
}

func TestRecoveryDoesNotExecuteCancelledPlanApprovalHandoff(t *testing.T) {
	products := codingmemory.NewRepository()
	seedMemoryWorktree(t, products, "workspace-plan-cancel-recovery", "worktree-plan-cancel-recovery")
	planArguments, _ := json.Marshal(codingagent.PlanSubmission{
		Goal: "Produce a cancellable outline.", Scope: codingagent.PlanScope{Included: []string{"Outline"}},
		Findings: []string{"Cancellation must survive restart."}, Risks: []string{"A cancelled Plan must not execute."},
		Steps:              []codingagent.PlanStep{{ID: "outline", Goal: "Draft the outline.", Validation: []string{"Review it."}}},
		AcceptanceCriteria: []string{"Cancellation is terminal."}, RecommendedStrategy: codingagent.ExecutionSingle,
		WorkspaceRelevant: false, CompletionMode: codingagent.PlanCompletionDeliverable,
	})
	model := &sequentialModel{responses: []llm.Message{
		{
			Role:       llm.RoleAssistant,
			Provider:   "profile-1",
			Model:      "model-1",
			StopReason: llm.StopReasonToolUse,
			Content: []llm.Content{
				{Type: llm.ContentToolCall, ToolCall: &llm.ToolCall{ID: "cancel-plan", Name: "exit_plan_mode", Arguments: planArguments}},
			},
		},
	}}
	agentSessions := agentsession.NewMemoryRepository()
	newRuntime := func(t *testing.T) *agent.Runtime {
		t.Helper()
		contexts, _ := contextmanager.NewManager()
		runtime, err := agent.NewRuntime(agent.Dependencies{Models: sequentialModelFactory{model: model}, Contexts: contexts, Sessions: agentSessions})
		if err != nil {
			t.Fatal(err)
		}
		return runtime
	}
	failingTurns := &failTerminalTurnRepository{TurnRepository: products, failOnce: true}
	first, err := codingagent.NewService(codingagent.Dependencies{
		Sessions: products, Turns: failingTurns, Plans: products, AgentSessions: agentSessions, Worktrees: products,
		Agent: newRuntime(t), Tools: emptyToolFactory{}, Prompts: staticPrompt{}, Events: &productEvents{},
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := first.CreateSession(context.Background(), codingagent.Session{
		ID: "coding-plan-cancel-recovery", AgentSessionID: "agent-plan-cancel-recovery", WorkspaceID: "workspace-plan-cancel-recovery", WorktreeID: "worktree-plan-cancel-recovery",
		ProviderProfileID: "profile-1", ModelID: "model-1", PermissionMode: codingagent.PermissionAsk,
	})
	if err != nil {
		t.Fatal(err)
	}
	waiting, err := first.StartTurn(context.Background(), codingagent.TurnRequest{SessionID: session.ID, Text: "Create a cancellable Plan", Mode: codingagent.TurnModePlan})
	if err != nil || waiting.InterruptKind != "plan_approval" {
		t.Fatalf("Plan approval = %#v, %v", waiting, err)
	}
	if _, err := first.ResumeTurn(context.Background(), codingagent.ResumeTurnRequest{
		SessionID: session.ID, TurnID: waiting.TurnID, InterruptID: waiting.InterruptID,
		Decision: codingagent.ResolutionCancelled, GrantScope: codingagent.PermissionGrantOnce,
	}); err == nil {
		t.Fatal("injected cancelled Product Turn write did not fail")
	}
	recovered, err := codingagent.NewService(codingagent.Dependencies{
		Sessions: products, Turns: products, Plans: products, AgentSessions: agentSessions, Worktrees: products,
		Agent: newRuntime(t), Tools: emptyToolFactory{}, Prompts: staticPrompt{}, Events: &productEvents{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if completed, err := recovered.RecoverAutomatically(context.Background(), session.ID); err != nil || completed != 1 {
		t.Fatalf("recover cancelled Plan = %d, %v", completed, err)
	}
	turn, err := products.LoadTurn(context.Background(), waiting.TurnID)
	if err != nil || turn.Status != codingagent.TurnCancelled || len(turn.Runs) != 1 {
		t.Fatalf("recovered cancelled Plan Turn = %#v, %v", turn, err)
	}
	if len(model.responses) != 0 {
		t.Fatalf("cancelled Plan unexpectedly called the model %d more time(s)", len(model.responses))
	}
}

func TestServicePlanSuggestionFeatureFlagKeepsExplicitPlanAvailable(t *testing.T) {
	products := codingmemory.NewRepository()
	seedMemoryWorktree(t, products, "workspace-plan-suggestion-disabled", "worktree-plan-suggestion-disabled")
	model := &sequentialModel{responses: []llm.Message{finalAssistant(), finalAssistant()}}
	agentSessions := agentsession.NewMemoryRepository()
	contexts, _ := contextmanager.NewManager()
	runtime, _ := agent.NewRuntime(agent.Dependencies{Models: sequentialModelFactory{model: model}, Contexts: contexts, Sessions: agentSessions})
	features := codingagent.DefaultFeatureFlags()
	features.PlanSuggestions = false
	service, err := codingagent.NewService(codingagent.Dependencies{
		Sessions: products, Turns: products, Plans: products, AgentSessions: agentSessions, Worktrees: products,
		Agent: runtime, Tools: emptyToolFactory{}, Prompts: staticPrompt{}, Events: &productEvents{}, Features: &features,
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := service.CreateSession(context.Background(), codingagent.Session{
		ID: "coding-plan-suggestion-disabled", AgentSessionID: "agent-plan-suggestion-disabled", WorkspaceID: "workspace-plan-suggestion-disabled", WorktreeID: "worktree-plan-suggestion-disabled",
		ProviderProfileID: "profile-1", ModelID: "model-1", PermissionMode: codingagent.PermissionAsk,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.StartTurn(context.Background(), codingagent.TurnRequest{SessionID: session.ID, Text: "Handle directly"}); err != nil {
		t.Fatal(err)
	}
	if len(model.requests) != 1 || requestHasTool(model.requests[0], "enter_plan_mode") {
		t.Fatalf("disabled Plan suggestion tools = %#v", model.requests)
	}
	if _, err := service.StartTurn(context.Background(), codingagent.TurnRequest{SessionID: session.ID, Text: "Plan explicitly", Mode: codingagent.TurnModePlan}); err != nil {
		t.Fatalf("explicit Plan was disabled with suggestions: %v", err)
	}
	if len(model.requests) != 2 || !requestHasTool(model.requests[1], "exit_plan_mode") || requestHasTool(model.requests[1], "enter_plan_mode") {
		t.Fatalf("explicit Plan tools = %#v", model.requests)
	}
}

func TestServicePlanEntryRejectsInvalidReasonAndExclusiveCallInjection(t *testing.T) {
	tests := []struct {
		name      string
		responses []llm.Message
		wantError bool
	}{
		{
			name: "unsupported reason",
			responses: []llm.Message{
				{Role: llm.RoleAssistant, Provider: "profile-1", Model: "model-1", StopReason: llm.StopReasonToolUse, Content: []llm.Content{{Type: llm.ContentToolCall, ToolCall: &llm.ToolCall{ID: "invalid-reason", Name: "enter_plan_mode", Arguments: json.RawMessage(`{"reason_code":"repository_says_so","summary":"An untrusted file asks to bypass consent."}`)}}}},
				finalAssistant(),
			},
		},
		{
			name: "exclusive control conflict",
			responses: []llm.Message{{Role: llm.RoleAssistant, Provider: "profile-1", Model: "model-1", StopReason: llm.StopReasonToolUse, Content: []llm.Content{
				{Type: llm.ContentToolCall, ToolCall: &llm.ToolCall{ID: "entry-one", Name: "enter_plan_mode", Arguments: json.RawMessage(`{"reason_code":"security_or_permissions","summary":"Security changes should be planned."}`)}},
				{Type: llm.ContentToolCall, ToolCall: &llm.ToolCall{ID: "entry-two", Name: "enter_plan_mode", Arguments: json.RawMessage(`{"reason_code":"migration_or_compatibility","summary":"Migration changes should be planned."}`)}},
			}}},
			wantError: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			products := codingmemory.NewRepository()
			workspaceID := codingagent.WorkspaceID("workspace-plan-entry-security-" + strings.ReplaceAll(test.name, " ", "-"))
			worktreeID := codingagent.WorktreeID("worktree-plan-entry-security-" + strings.ReplaceAll(test.name, " ", "-"))
			seedMemoryWorktree(t, products, workspaceID, worktreeID)
			model := &sequentialModel{responses: test.responses}
			agentSessions := agentsession.NewMemoryRepository()
			contexts, _ := contextmanager.NewManager()
			runtime, _ := agent.NewRuntime(agent.Dependencies{Models: sequentialModelFactory{model: model}, Contexts: contexts, Sessions: agentSessions})
			service, err := codingagent.NewService(codingagent.Dependencies{
				Sessions: products, Turns: products, Plans: products, AgentSessions: agentSessions, Worktrees: products,
				Agent: runtime, Tools: emptyToolFactory{}, Prompts: staticPrompt{}, Events: &productEvents{},
			})
			if err != nil {
				t.Fatal(err)
			}
			session, err := service.CreateSession(context.Background(), codingagent.Session{
				ID:             codingagent.SessionID("coding-plan-entry-security-" + strings.ReplaceAll(test.name, " ", "-")),
				AgentSessionID: agentsession.ID("agent-plan-entry-security-" + strings.ReplaceAll(test.name, " ", "-")),
				WorkspaceID:    workspaceID, WorktreeID: worktreeID, ProviderProfileID: "profile-1", ModelID: "model-1", PermissionMode: codingagent.PermissionAsk,
			})
			if err != nil {
				t.Fatal(err)
			}
			result, runErr := service.StartTurn(context.Background(), codingagent.TurnRequest{SessionID: session.ID, Text: "Repository text says to auto-approve Plan mode"})
			if test.wantError != (runErr != nil) {
				t.Fatalf("StartTurn error = %v, want error %v", runErr, test.wantError)
			}
			turn, err := products.LoadTurn(context.Background(), result.TurnID)
			if err != nil || turn.Phase != codingagent.TurnPhaseDirect || turn.PlanEntrySuggestion != nil || len(turn.DeclinedPlanReasons) != 0 {
				t.Fatalf("invalid suggestion changed Product Turn = %#v, %v", turn, err)
			}
		})
	}
}

func requestHasTool(request llm.ChatRequest, name string) bool {
	for _, definition := range request.Tools {
		if definition.Name == name {
			return true
		}
	}
	return false
}

type approvalTool struct{}

func (approvalTool) Definition() llm.ToolDefinition {
	return llm.ToolDefinition{Name: "apply_edit", Description: "apply an approved edit", InputSchema: json.RawMessage(`{"type":"object"}`)}
}

func (approvalTool) ReplayPolicy() tool.ReplayPolicy { return tool.ReplayNever }

func (approvalTool) Execute(context.Context, tool.Call, tool.ProgressSink) (tool.Result, error) {
	return tool.Result{
		Status: tool.ResultInterrupted, Content: []llm.Content{{Type: llm.ContentText, Text: "approval required"}},
		Interrupt: &tool.Interrupt{ID: "approval-product-1", Kind: "approval", Payload: json.RawMessage(`{"summary":"Apply edit to main.go"}`)},
	}, nil
}

type approvalToolFactory struct{}

func (approvalToolFactory) CreateTools(context.Context, codingagent.ToolScope) (*tool.Registry, error) {
	return tool.NewRegistry(approvalTool{})
}

type handoffControlTool struct{}

func (handoffControlTool) Definition() llm.ToolDefinition {
	return llm.ToolDefinition{Name: "handoff_control", Description: "request a product control transition", InputSchema: json.RawMessage(`{"type":"object"}`)}
}

func (handoffControlTool) ReplayPolicy() tool.ReplayPolicy { return tool.ReplayNever }

func (handoffControlTool) ControlPolicy() tool.ControlPolicy {
	return tool.ControlPolicy{Exclusive: true, HandoffAfterResolution: true}
}

func (handoffControlTool) Execute(context.Context, tool.Call, tool.ProgressSink) (tool.Result, error) {
	return tool.Result{
		Status: tool.ResultInterrupted, Content: []llm.Content{{Type: llm.ContentText, Text: "control approval required"}},
		Interrupt: &tool.Interrupt{ID: "control-product-1", Kind: "control"},
	}, nil
}

func (handoffControlTool) Resume(_ context.Context, _ tool.Call, _ tool.Interrupt, resolution tool.Result, _ tool.ProgressSink) (tool.Result, error) {
	return resolution, nil
}

type handoffToolFactory struct{}

func (handoffToolFactory) CreateTools(context.Context, codingagent.ToolScope) (*tool.Registry, error) {
	return tool.NewRegistry(handoffControlTool{})
}

type grantAwarePatchTool struct {
	owner   *grantAwareToolFactory
	granted bool
}

func (*grantAwarePatchTool) Definition() llm.ToolDefinition {
	return llm.ToolDefinition{Name: "apply_patch", Description: "test permission-aware patch", InputSchema: json.RawMessage(`{"type":"object"}`)}
}
func (*grantAwarePatchTool) ReplayPolicy() tool.ReplayPolicy { return tool.ReplayNever }
func (t *grantAwarePatchTool) Execute(context.Context, tool.Call, tool.ProgressSink) (tool.Result, error) {
	if t.granted {
		t.owner.automatic++
		return tool.Result{Status: tool.ResultCompleted, Content: []llm.Content{{Type: llm.ContentText, Text: "session grant applied"}}}, nil
	}
	return tool.Result{
		Status: tool.ResultInterrupted, Content: []llm.Content{{Type: llm.ContentText, Text: "approval required"}},
		Interrupt: &tool.Interrupt{ID: "approval-grant-1", Kind: "approval", Payload: json.RawMessage(`{"kind":"coding_patch_approval_v1","version":1,"summary":"Edit main.go","patch":"safe fixture","files":["main.go"],"digest":"fixture-digest"}`)},
	}, nil
}

type grantAwareToolFactory struct {
	scopes    []codingagent.ToolScope
	automatic int
}

func (f *grantAwareToolFactory) CreateTools(_ context.Context, scope codingagent.ToolScope) (*tool.Registry, error) {
	f.scopes = append(f.scopes, scope)
	granted := codingagent.PermissionGranted(scope.PermissionGrants, codingagent.PermissionRequest{
		ToolName: "apply_patch", Action: codingagent.PermissionActionModify, Paths: []string{"main.go"},
	}, time.Now().UTC())
	return tool.NewRegistry(&grantAwarePatchTool{owner: f, granted: granted})
}

type recoveryReadTool struct{ calls int }

func (*recoveryReadTool) Definition() llm.ToolDefinition {
	return llm.ToolDefinition{Name: "recovery_read", Description: "read during recovery", InputSchema: json.RawMessage(`{"type":"object"}`)}
}
func (*recoveryReadTool) ReplayPolicy() tool.ReplayPolicy { return tool.ReplaySafe }
func (t *recoveryReadTool) Execute(context.Context, tool.Call, tool.ProgressSink) (tool.Result, error) {
	t.calls++
	return tool.Result{Status: tool.ResultCompleted, Content: []llm.Content{{Type: llm.ContentText, Text: "recovered read"}}}, nil
}

type recoveryToolFactory struct{ executable *recoveryReadTool }

func (f recoveryToolFactory) CreateTools(context.Context, codingagent.ToolScope) (*tool.Registry, error) {
	return tool.NewRegistry(f.executable)
}

type gapAgentRunner struct {
	runs          int
	continuations int
}

func (r *gapAgentRunner) Run(_ context.Context, request agent.RunRequest, _ agent.EventSink) (agent.RunResult, error) {
	r.runs++
	message := finalAssistant()
	return agent.RunResult{RunID: request.RunID, Status: agent.RunCompleted, FinalMessage: &message}, nil
}

func (*gapAgentRunner) Resume(context.Context, agent.ResumeRequest, agent.EventSink) (agent.RunResult, error) {
	return agent.RunResult{}, nil
}

func (*gapAgentRunner) Recover(context.Context, agent.RecoverRequest, agent.EventSink) (agent.RunResult, error) {
	return agent.RunResult{}, nil
}

func (r *gapAgentRunner) Continue(_ context.Context, request agent.ContinueRequest, _ agent.EventSink) (agent.RunResult, error) {
	r.continuations++
	message := finalAssistant()
	return agent.RunResult{RunID: request.RunID, Status: agent.RunCompleted, FinalMessage: &message}, nil
}

func TestServiceComposesGenericAgentIntoProductSnapshotAndEvents(t *testing.T) {
	productStore := codingmemory.NewRepository()
	seedMemoryWorktree(t, productStore, "workspace-1", "worktree-1")
	agentSessions := agentsession.NewMemoryRepository()
	contexts, err := contextmanager.NewManager()
	if err != nil {
		t.Fatalf("create context manager: %v", err)
	}
	runtime, err := agent.NewRuntime(agent.Dependencies{Models: finalModelFactory{}, Contexts: contexts, Sessions: agentSessions})
	if err != nil {
		t.Fatalf("create Agent runtime: %v", err)
	}
	events := &productEvents{}
	service, err := codingagent.NewService(codingagent.Dependencies{
		Sessions: productStore, Turns: productStore, AgentSessions: agentSessions, Worktrees: productStore,
		Agent: runtime, Tools: emptyToolFactory{}, Prompts: staticPrompt{}, Events: events,
	})
	if err != nil {
		t.Fatalf("create Coding Agent service: %v", err)
	}
	created, err := service.CreateSession(context.Background(), codingagent.Session{
		ID: "coding-1", AgentSessionID: "agent-1", WorkspaceID: "workspace-1", WorktreeID: "worktree-1",
		ProviderProfileID: "profile-1", ModelID: "model-1", PermissionMode: codingagent.PermissionAsk,
	})
	if err != nil {
		t.Fatalf("create Coding session: %v", err)
	}
	result, err := service.StartTurn(context.Background(), codingagent.TurnRequest{SessionID: created.ID, Text: "inspect"})
	if err != nil {
		t.Fatalf("start turn: %v", err)
	}
	if result.Status != string(agent.RunCompleted) || result.Response != "done" {
		t.Fatalf("turn result = %#v", result)
	}
	snapshot, err := service.Snapshot(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("load product snapshot: %v", err)
	}
	if snapshot.RuntimeState != codingagent.RuntimeIdle || len(snapshot.Transcript) != 2 || snapshot.Transcript[0].Role != codingagent.TranscriptRoleUser || snapshot.Transcript[1].Role != codingagent.TranscriptRoleAssistant {
		t.Fatalf("product snapshot = %#v", snapshot)
	}
	var sawDelta, sawCompleted bool
	for _, event := range events.values {
		sawDelta = sawDelta || event.Kind == codingagent.EventAssistantOutputDelta
		sawCompleted = sawCompleted || event.Kind == codingagent.EventTurnCompleted
	}
	if !sawDelta || !sawCompleted {
		t.Fatalf("product events = %#v", events.values)
	}
}

func TestServiceExplicitPlanPersistsApprovalAndExecutesInOneProductTurn(t *testing.T) {
	root := t.TempDir()
	for _, arguments := range [][]string{{"init", "--quiet"}, {"config", "user.name", "CodePilot Test"}, {"config", "user.email", "test@example.invalid"}, {"commit", "--allow-empty", "--quiet", "-m", "initial"}} {
		command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", arguments, err, output)
		}
	}
	products := codingmemory.NewRepository()
	now := time.Now().UTC()
	workspace := codingagent.Workspace{ID: "workspace-plan", DisplayName: "plan", GitCommonDir: filepath.Join(root, ".git"), Trusted: true, CreatedAt: now, UpdatedAt: now}
	worktree := codingagent.Worktree{ID: "worktree-plan", WorkspaceID: workspace.ID, Root: root, GitDir: workspace.GitCommonDir, CreatedAt: now, LastUsedAt: now}
	if err := products.SaveWorkspace(context.Background(), workspace); err != nil {
		t.Fatal(err)
	}
	if err := products.SaveWorktree(context.Background(), worktree); err != nil {
		t.Fatal(err)
	}
	agentSessions := agentsession.NewMemoryRepository()
	contexts, _ := contextmanager.NewManager()
	planArguments, _ := json.Marshal(codingagent.PlanSubmission{
		Goal: "Add explicit Plan mode.", Scope: codingagent.PlanScope{Included: []string{"internal/codingagent"}, Excluded: []string{"docs"}},
		Findings: []string{"Product Turns already support continuation."}, Risks: []string{"Plan approval must not grant writes."},
		Steps:              []codingagent.PlanStep{{ID: "implement", Goal: "Implement the approved change.", Files: []string{"internal/codingagent/plan.go"}, Validation: []string{"Run unit tests."}}},
		AcceptanceCriteria: []string{"The approved Plan executes in the same Product Turn."}, RecommendedStrategy: codingagent.ExecutionSingle,
		WorkspaceRelevant: true, CompletionMode: codingagent.PlanCompletionExecute,
	})
	planCall := llm.Message{
		Role: llm.RoleAssistant, Provider: "profile-1", Model: "model-1", StopReason: llm.StopReasonToolUse,
		Content: []llm.Content{{Type: llm.ContentToolCall, ToolCall: &llm.ToolCall{ID: "exit-plan-1", Name: "exit_plan_mode", Arguments: planArguments}}},
	}
	workspaceCall := llm.Message{
		Role: llm.RoleAssistant, Provider: "profile-1", Model: "model-1", StopReason: llm.StopReasonToolUse,
		Content: []llm.Content{{Type: llm.ContentToolCall, ToolCall: &llm.ToolCall{ID: "workspace-context-1", Name: "request_workspace_context", Arguments: json.RawMessage(`{"reason":"The requested code change depends on the current implementation."}`)}}},
	}
	var revisedSubmission codingagent.PlanSubmission
	if err := json.Unmarshal(planArguments, &revisedSubmission); err != nil {
		t.Fatal(err)
	}
	revisedSubmission.Goal = "Add explicit, compact Plan mode."
	revisedArguments, _ := json.Marshal(revisedSubmission)
	revisedPlanCall := llm.Message{
		Role: llm.RoleAssistant, Provider: "profile-1", Model: "model-1", StopReason: llm.StopReasonToolUse,
		Content: []llm.Content{{Type: llm.ContentToolCall, ToolCall: &llm.ToolCall{ID: "exit-plan-2", Name: "exit_plan_mode", Arguments: revisedArguments}}},
	}
	model := &sequentialModel{responses: []llm.Message{workspaceCall, planCall, revisedPlanCall, finalAssistant()}}
	runtime, err := agent.NewRuntime(agent.Dependencies{Models: sequentialModelFactory{model: model}, Contexts: contexts, Sessions: agentSessions})
	if err != nil {
		t.Fatal(err)
	}
	service, err := codingagent.NewService(codingagent.Dependencies{
		Sessions: products, Turns: products, Plans: products, AgentSessions: agentSessions, Worktrees: products,
		Agent: runtime, Tools: emptyToolFactory{}, Prompts: staticPrompt{}, Events: &productEvents{},
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := service.CreateSession(context.Background(), codingagent.Session{
		ID: "coding-plan", AgentSessionID: "agent-plan", WorkspaceID: workspace.ID, WorktreeID: worktree.ID,
		ProviderProfileID: "profile-1", ModelID: "model-1", PermissionMode: codingagent.PermissionAsk,
	})
	if err != nil {
		t.Fatal(err)
	}
	planned, err := service.StartTurn(context.Background(), codingagent.TurnRequest{SessionID: session.ID, Text: "Plan this change", Mode: codingagent.TurnModePlan})
	if err != nil || planned.Status != string(agent.RunInterrupted) || planned.InterruptKind != "plan_approval" {
		t.Fatalf("Plan start = %#v, %v", planned, err)
	}
	statusCommand := exec.Command("git", "-C", root, "status", "--porcelain")
	if output, statusErr := statusCommand.Output(); statusErr != nil || len(output) != 0 {
		t.Fatalf("read-only Planning changed the worktree: status=%q err=%v", output, statusErr)
	}
	snapshot, err := service.Snapshot(context.Background(), session.ID)
	if err != nil || snapshot.ActivePlan == nil || !snapshot.PendingPlanApproval || snapshot.ActivePlan.Version != 1 {
		t.Fatalf("Plan snapshot = %#v, %v", snapshot, err)
	}
	if snapshot.ActiveTurn == nil || snapshot.ActiveTurn.Phase != codingagent.TurnPhaseAwaitingPlanApproval || snapshot.ActiveTurn.RunCount != 2 {
		t.Fatalf("Plan Turn snapshot = %#v", snapshot.ActiveTurn)
	}
	revised, err := service.ResumeTurn(context.Background(), codingagent.ResumeTurnRequest{
		SessionID: session.ID, TurnID: planned.TurnID, InterruptID: planned.InterruptID,
		Decision: codingagent.ResolutionDenied, GrantScope: codingagent.PermissionGrantOnce, Message: "Keep the UI compact.",
	})
	if err != nil || revised.Status != string(agent.RunInterrupted) || revised.InterruptID == planned.InterruptID {
		t.Fatalf("Plan revision = %#v, %v", revised, err)
	}
	snapshot, err = service.Snapshot(context.Background(), session.ID)
	if err != nil || snapshot.ActivePlan == nil || snapshot.ActivePlan.Version != 2 || len(snapshot.PlanHistory) != 2 {
		t.Fatalf("revised Plan snapshot = %#v, %v", snapshot, err)
	}
	if _, err := service.ResumeTurn(context.Background(), codingagent.ResumeTurnRequest{
		SessionID: session.ID, TurnID: planned.TurnID, InterruptID: planned.InterruptID,
		Decision: codingagent.ResolutionApproved, GrantScope: codingagent.PermissionGrantOnce,
	}); err == nil {
		t.Fatal("old Plan approval applied to a revised Plan")
	}
	executed, err := service.ResumeTurn(context.Background(), codingagent.ResumeTurnRequest{
		SessionID: session.ID, TurnID: planned.TurnID, InterruptID: revised.InterruptID,
		Decision: codingagent.ResolutionApproved, GrantScope: codingagent.PermissionGrantOnce,
	})
	if err != nil || executed.Status != string(agent.RunCompleted) || executed.Response != "done" {
		t.Fatalf("Plan approval = %#v, %v", executed, err)
	}
	turn, err := products.LoadTurn(context.Background(), planned.TurnID)
	if err != nil || turn.Phase != codingagent.TurnPhaseExecuting || turn.Status != codingagent.TurnCompleted || len(turn.Runs) != 3 || turn.Runs[0].Status != codingagent.RunBindingHandedOff || turn.Runs[1].Status != codingagent.RunBindingHandedOff {
		t.Fatalf("executed Product Turn = %#v, %v", turn, err)
	}
	entries, err := agentSessions.Load(context.Background(), session.AgentSessionID)
	if err != nil {
		t.Fatal(err)
	}
	userMessages := 0
	for _, entry := range entries.Entries {
		if entry.Message != nil && entry.Message.Role == llm.RoleUser {
			userMessages++
		}
	}
	if userMessages != 1 {
		t.Fatalf("user messages = %d, want one original request", userMessages)
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("Plan flow changed worktree availability: %v", err)
	}
}

func TestServicePlanClarificationAndDeliverableFinishWithoutExecutionRun(t *testing.T) {
	products := codingmemory.NewRepository()
	seedMemoryWorktree(t, products, "workspace-general-plan", "worktree-general-plan")
	agentSessions := agentsession.NewMemoryRepository()
	contexts, _ := contextmanager.NewManager()
	clarificationArguments, _ := json.Marshal(codingagent.ClarificationPrompt{Questions: []codingagent.ClarificationRequest{
		{
			ID: "audience", Header: "Audience", Question: "Who is the primary audience?",
			SelectionMode: codingagent.ClarificationSelectionSingle,
			Options: []codingagent.ClarificationOption{
				{ID: "existing-users", Label: "Existing users", Description: "Emphasize changed behavior."},
				{ID: "new-users", Label: "New users", Description: "Emphasize orientation and context."},
			},
		},
		{
			ID: "format", Header: "Format", Question: "Which delivery format should be used?",
			SelectionMode: codingagent.ClarificationSelectionSingle,
			Options: []codingagent.ClarificationOption{
				{ID: "concise", Label: "Concise brief", Description: "Optimize for quick review.", Recommended: true},
				{ID: "detailed", Label: "Detailed guide", Description: "Include more explanation.", Recommended: true},
			},
		},
	}})
	clarificationCall := llm.Message{
		Role: llm.RoleAssistant, Provider: "profile-1", Model: "model-1", StopReason: llm.StopReasonToolUse,
		Content: []llm.Content{{Type: llm.ContentToolCall, ToolCall: &llm.ToolCall{ID: "clarify-1", Name: "request_user_input", Arguments: clarificationArguments}}},
	}
	planArguments, _ := json.Marshal(codingagent.PlanSubmission{
		Goal: "Plan a product announcement brief.", Scope: codingagent.PlanScope{Included: []string{"A concise user-facing brief"}},
		Findings: []string{"The primary audience is existing users."}, Risks: []string{"The brief may omit necessary context."},
		Steps:              []codingagent.PlanStep{{ID: "draft-brief", Goal: "Draft the announcement structure.", Validation: []string{"Keep it concise and audience-specific."}}},
		AcceptanceCriteria: []string{"The brief reflects the selected audience and format."}, RecommendedStrategy: codingagent.ExecutionSingle,
		WorkspaceRelevant: false, CompletionMode: codingagent.PlanCompletionDeliverable,
	})
	planCall := llm.Message{
		Role: llm.RoleAssistant, Provider: "profile-1", Model: "model-1", StopReason: llm.StopReasonToolUse,
		Content: []llm.Content{{Type: llm.ContentToolCall, ToolCall: &llm.ToolCall{ID: "exit-general-plan", Name: "exit_plan_mode", Arguments: planArguments}}},
	}
	model := &sequentialModel{responses: []llm.Message{clarificationCall, planCall}}
	runtime, err := agent.NewRuntime(agent.Dependencies{Models: sequentialModelFactory{model: model}, Contexts: contexts, Sessions: agentSessions})
	if err != nil {
		t.Fatal(err)
	}
	service, err := codingagent.NewService(codingagent.Dependencies{
		Sessions: products, Turns: products, Plans: products, AgentSessions: agentSessions, Worktrees: products,
		Agent: runtime, Tools: emptyToolFactory{}, Prompts: staticPrompt{}, Events: &productEvents{},
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := service.CreateSession(context.Background(), codingagent.Session{
		ID: "coding-general-plan", AgentSessionID: "agent-general-plan", WorkspaceID: "workspace-general-plan", WorktreeID: "worktree-general-plan",
		ProviderProfileID: "profile-1", ModelID: "model-1", PermissionMode: codingagent.PermissionAsk,
	})
	if err != nil {
		t.Fatal(err)
	}
	waiting, err := service.StartTurn(context.Background(), codingagent.TurnRequest{SessionID: session.ID, Text: "Plan a product announcement", Mode: codingagent.TurnModePlan})
	if err != nil || waiting.InterruptKind != "clarification" {
		t.Fatalf("clarification start = %#v, %v", waiting, err)
	}
	snapshot, err := service.Snapshot(context.Background(), session.ID)
	if err != nil || len(snapshot.PendingInterrupts) != 1 || snapshot.PendingInterrupts[0].Clarification == nil {
		t.Fatalf("clarification snapshot = %#v, %v", snapshot.PendingInterrupts, err)
	}
	questions := snapshot.PendingInterrupts[0].Clarification.Questions
	if questions[0].Options[0].Recommended || !questions[1].Options[0].Recommended || questions[1].Options[1].Recommended {
		t.Fatalf("clarification recommendations were not safely normalized: %#v", questions)
	}
	details, err := codingagent.EncodeClarificationAnswers(*snapshot.PendingInterrupts[0].Clarification, []codingagent.ClarificationAnswer{
		{QuestionID: "audience", OptionID: "existing-users"},
		{QuestionID: "format", OptionID: "concise"},
	})
	if err != nil {
		t.Fatal(err)
	}
	planned, err := service.ResumeTurn(context.Background(), codingagent.ResumeTurnRequest{
		SessionID: session.ID, TurnID: waiting.TurnID, InterruptID: waiting.InterruptID,
		Decision: codingagent.ResolutionApproved, GrantScope: codingagent.PermissionGrantOnce, Details: details,
	})
	if err != nil || planned.InterruptKind != "plan_approval" {
		t.Fatalf("clarification answer = %#v, %v", planned, err)
	}
	snapshot, err = service.Snapshot(context.Background(), session.ID)
	if err != nil || snapshot.ActivePlan == nil || snapshot.ActivePlan.WorkspaceRelevant || snapshot.ActivePlan.CompletionMode != codingagent.PlanCompletionDeliverable {
		t.Fatalf("deliverable Plan snapshot = %#v, %v", snapshot.ActivePlan, err)
	}
	finished, err := service.ResumeTurn(context.Background(), codingagent.ResumeTurnRequest{
		SessionID: session.ID, TurnID: waiting.TurnID, InterruptID: planned.InterruptID,
		Decision: codingagent.ResolutionApproved, GrantScope: codingagent.PermissionGrantOnce,
	})
	if err != nil || finished.Status != string(agent.RunCompleted) {
		t.Fatalf("accept deliverable Plan = %#v, %v", finished, err)
	}
	turn, err := products.LoadTurn(context.Background(), waiting.TurnID)
	if err != nil || turn.Status != codingagent.TurnCompleted || len(turn.Runs) != 1 || turn.Runs[0].Status != codingagent.RunBindingCompleted {
		t.Fatalf("deliverable Product Turn = %#v, %v", turn, err)
	}
	if len(model.responses) != 0 {
		t.Fatalf("unused model responses = %d", len(model.responses))
	}
}

func TestServiceProductTurnFeatureFlagRestoresLegacyDirectPath(t *testing.T) {
	productStore := codingmemory.NewRepository()
	seedMemoryWorktree(t, productStore, "workspace-legacy", "worktree-legacy")
	agentSessions := agentsession.NewMemoryRepository()
	contexts, _ := contextmanager.NewManager()
	runtime, _ := agent.NewRuntime(agent.Dependencies{Models: finalModelFactory{}, Contexts: contexts, Sessions: agentSessions})
	features := codingagent.DefaultFeatureFlags()
	features.ProductTurns = false
	service, err := codingagent.NewService(codingagent.Dependencies{
		Sessions: productStore, AgentSessions: agentSessions, Worktrees: productStore,
		Agent: runtime, Tools: emptyToolFactory{}, Prompts: staticPrompt{}, Events: &productEvents{}, Features: &features,
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := service.CreateSession(context.Background(), codingagent.Session{
		ID: "coding-legacy", AgentSessionID: "agent-legacy", WorkspaceID: "workspace-legacy", WorktreeID: "worktree-legacy",
		ProviderProfileID: "profile", ModelID: "model", PermissionMode: codingagent.PermissionAsk,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.StartTurn(context.Background(), codingagent.TurnRequest{SessionID: session.ID, Text: "inspect"})
	if err != nil {
		t.Fatal(err)
	}
	if result.TurnID == "" || result.TurnID != codingagent.TurnID(result.RunID) {
		t.Fatalf("legacy identities = %#v", result)
	}
	turns, err := productStore.ListTurns(context.Background(), session.ID)
	if err != nil || len(turns) != 0 {
		t.Fatalf("disabled Product Turns persisted data: %#v, %v", turns, err)
	}
	snapshot, err := service.Snapshot(context.Background(), session.ID)
	if err != nil || snapshot.ActiveTurn != nil || len(snapshot.Transcript) != 2 {
		t.Fatalf("legacy snapshot = %#v, %v", snapshot, err)
	}
}

func TestServicePlanFeatureFlagRejectsNewPlanButKeepsDirectPath(t *testing.T) {
	products := codingmemory.NewRepository()
	seedMemoryWorktree(t, products, "workspace-plan-disabled", "worktree-plan-disabled")
	agentSessions := agentsession.NewMemoryRepository()
	contexts, _ := contextmanager.NewManager()
	runtime, err := agent.NewRuntime(agent.Dependencies{Models: finalModelFactory{}, Contexts: contexts, Sessions: agentSessions})
	if err != nil {
		t.Fatal(err)
	}
	features := codingagent.DefaultFeatureFlags()
	features.PlanMode = false
	service, err := codingagent.NewService(codingagent.Dependencies{
		Sessions: products, Turns: products, Plans: products, AgentSessions: agentSessions, Worktrees: products,
		Agent: runtime, Tools: emptyToolFactory{}, Prompts: staticPrompt{}, Events: &productEvents{}, Features: &features,
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := service.CreateSession(context.Background(), codingagent.Session{
		ID: "coding-plan-disabled", AgentSessionID: "agent-plan-disabled", WorkspaceID: "workspace-plan-disabled", WorktreeID: "worktree-plan-disabled",
		ProviderProfileID: "profile", ModelID: "model", PermissionMode: codingagent.PermissionAsk,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.StartTurn(context.Background(), codingagent.TurnRequest{SessionID: session.ID, Text: "plan this", Mode: codingagent.TurnModePlan}); err == nil {
		t.Fatal("disabled Plan feature accepted a new Plan Turn")
	}
	if result, err := service.StartTurn(context.Background(), codingagent.TurnRequest{SessionID: session.ID, Text: "answer directly"}); err != nil || result.Status != string(agent.RunCompleted) {
		t.Fatalf("Direct path after disabling Plan = %#v, %v", result, err)
	}
}

func TestServiceContinuesTwoRunsInOneProductTurnWithoutSyntheticUserMessage(t *testing.T) {
	productStore := codingmemory.NewRepository()
	seedMemoryWorktree(t, productStore, "workspace-handoff", "worktree-handoff")
	agentSessions := agentsession.NewMemoryRepository()
	contexts, _ := contextmanager.NewManager()
	model := &sequentialModel{responses: []llm.Message{
		{Role: llm.RoleAssistant, StopReason: llm.StopReasonToolUse, Content: []llm.Content{{Type: llm.ContentToolCall, ToolCall: &llm.ToolCall{ID: "control-call", Name: "handoff_control", Arguments: json.RawMessage(`{}`)}}}},
		{Role: llm.RoleAssistant, StopReason: llm.StopReasonStop, Content: []llm.Content{{Type: llm.ContentText, Text: "continued"}}},
	}}
	runtime, err := agent.NewRuntime(agent.Dependencies{Models: sequentialModelFactory{model: model}, Contexts: contexts, Sessions: agentSessions})
	if err != nil {
		t.Fatal(err)
	}
	events := &productEvents{}
	service, err := codingagent.NewService(codingagent.Dependencies{
		Sessions: productStore, Turns: productStore, AgentSessions: agentSessions, Worktrees: productStore,
		Agent: runtime, Tools: handoffToolFactory{}, Prompts: staticPrompt{}, Events: events,
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := service.CreateSession(context.Background(), codingagent.Session{
		ID: "coding-handoff", AgentSessionID: "agent-handoff", WorkspaceID: "workspace-handoff", WorktreeID: "worktree-handoff",
		ProviderProfileID: "profile", ModelID: "model", PermissionMode: codingagent.PermissionAsk,
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.StartTurn(context.Background(), codingagent.TurnRequest{SessionID: session.ID, Text: "one request"})
	if err != nil || first.Status != string(agent.RunInterrupted) || first.TurnID == codingagent.TurnID(first.RunID) {
		t.Fatalf("first Run = %#v, %v", first, err)
	}
	handoff, err := service.ResumeTurn(context.Background(), codingagent.ResumeTurnRequest{
		SessionID: session.ID, TurnID: first.TurnID, InterruptID: first.InterruptID, Decision: codingagent.ResolutionApproved,
	})
	if err != nil || handoff.Status != string(agent.RunHandedOff) || handoff.RunID != first.RunID {
		t.Fatalf("handoff = %#v, %v", handoff, err)
	}
	continued, err := service.ContinueTurn(context.Background(), session.ID, first.TurnID)
	if err != nil || continued.Status != string(agent.RunCompleted) || continued.RunID == first.RunID {
		t.Fatalf("continued Run = %#v, %v", continued, err)
	}
	turn, err := productStore.LoadTurn(context.Background(), first.TurnID)
	if err != nil || turn.Status != codingagent.TurnCompleted || len(turn.Runs) != 2 || turn.Runs[0].Status != codingagent.RunBindingHandedOff || turn.Runs[1].Status != codingagent.RunBindingCompleted {
		t.Fatalf("Product Turn = %#v, %v", turn, err)
	}
	durable, err := agentSessions.Load(context.Background(), session.AgentSessionID)
	if err != nil {
		t.Fatal(err)
	}
	userMessages := 0
	for _, entry := range durable.Entries {
		if entry.Message != nil && entry.Message.Role == llm.RoleUser {
			userMessages++
		}
	}
	if userMessages != 1 {
		t.Fatalf("user messages = %d, entries=%#v", userMessages, durable.Entries)
	}
	snapshot, err := service.Snapshot(context.Background(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Metrics.LatestTurnID != first.TurnID || snapshot.Metrics.LatestRunID != continued.RunID || snapshot.Metrics.LatestPhase != codingagent.TurnPhaseDirect || snapshot.Metrics.LatestProfile != codingagent.CapabilityDirect || snapshot.Metrics.LatestTurnStatus != codingagent.TurnCompleted {
		t.Fatalf("phase-aware metrics = %#v", snapshot.Metrics)
	}
	for _, item := range snapshot.Transcript {
		if item.TurnID != first.TurnID {
			t.Fatalf("transcript Turn binding = %#v", snapshot.Transcript)
		}
	}
	for _, event := range events.values {
		if event.TurnID != first.TurnID || (event.RunID != first.RunID && event.RunID != continued.RunID) {
			t.Fatalf("event identity = %#v", event)
		}
	}
}

func TestRecoveryCoordinatorClosesProductTurnCrashGapsExactlyOnce(t *testing.T) {
	newFixture := func(t *testing.T, suffix string) (*codingagent.Service, *codingmemory.Repository, agentsession.Repository, *gapAgentRunner, codingagent.Session) {
		t.Helper()
		products := codingmemory.NewRepository()
		seedMemoryWorktree(t, products, codingagent.WorkspaceID("workspace-"+suffix), codingagent.WorktreeID("worktree-"+suffix))
		agentSessions := agentsession.NewMemoryRepository()
		runner := &gapAgentRunner{}
		service, err := codingagent.NewService(codingagent.Dependencies{
			Sessions: products, Turns: products, AgentSessions: agentSessions, Worktrees: products,
			Agent: runner, Tools: emptyToolFactory{}, Prompts: staticPrompt{}, Events: &productEvents{},
		})
		if err != nil {
			t.Fatal(err)
		}
		session, err := service.CreateSession(context.Background(), codingagent.Session{
			ID: codingagent.SessionID("coding-" + suffix), AgentSessionID: agentsession.ID("agent-" + suffix),
			WorkspaceID: codingagent.WorkspaceID("workspace-" + suffix), WorktreeID: codingagent.WorktreeID("worktree-" + suffix),
			ProviderProfileID: "profile", ModelID: "model", PermissionMode: codingagent.PermissionAsk,
		})
		if err != nil {
			t.Fatal(err)
		}
		return service, products, agentSessions, runner, session
	}
	createPending := func(t *testing.T, products *codingmemory.Repository, session codingagent.Session, suffix string) codingagent.Turn {
		t.Helper()
		now := time.Now().UTC()
		turn := codingagent.Turn{
			ID: codingagent.TurnID("turn-" + suffix), SessionID: session.ID, RequestText: "recover request",
			Phase: codingagent.TurnPhaseDirect, Status: codingagent.TurnPending, Strategy: codingagent.ExecutionSingle, Revision: 1,
			Runs:      []codingagent.RunBinding{{RunID: agentsession.RunID("run-" + suffix), UserEntryID: agentsession.EntryID("entry-" + suffix), Phase: codingagent.TurnPhaseDirect, Profile: codingagent.CapabilityDirect, Status: codingagent.RunBindingPending}},
			CreatedAt: now, UpdatedAt: now,
		}
		if err := products.CreateTurn(context.Background(), turn); err != nil {
			t.Fatal(err)
		}
		return turn
	}
	markRunning := func(t *testing.T, products *codingmemory.Repository, turn codingagent.Turn) codingagent.Turn {
		t.Helper()
		turn.Runs[0].Status = codingagent.RunBindingRunning
		turn.Runs[0].StartedAt = turn.CreatedAt
		turn.Status = codingagent.TurnRunning
		turn.Revision = 2
		if err := products.SaveTurn(context.Background(), turn, 1); err != nil {
			t.Fatal(err)
		}
		return turn
	}

	for _, boundary := range []string{"created", "bound"} {
		t.Run(boundary, func(t *testing.T) {
			service, products, _, runner, session := newFixture(t, boundary)
			turn := createPending(t, products, session, boundary)
			if boundary == "bound" {
				turn = markRunning(t, products, turn)
			}
			completed, err := service.RecoverAutomatically(context.Background(), session.ID)
			if err != nil || completed != 1 || runner.runs != 1 {
				t.Fatalf("first recovery = %d runs=%d err=%v", completed, runner.runs, err)
			}
			completed, err = service.RecoverAutomatically(context.Background(), session.ID)
			if err != nil || completed != 0 || runner.runs != 1 {
				t.Fatalf("repeat recovery = %d runs=%d err=%v", completed, runner.runs, err)
			}
			loaded, err := products.LoadTurn(context.Background(), turn.ID)
			if err != nil || loaded.Status != codingagent.TurnCompleted {
				t.Fatalf("recovered Turn = %#v, %v", loaded, err)
			}
		})
	}

	t.Run("agent terminal before Product Turn terminal", func(t *testing.T) {
		service, products, agentSessions, runner, session := newFixture(t, "terminal")
		turn := markRunning(t, products, createPending(t, products, session, "terminal"))
		appendAgentRecord(t, agentSessions, session.AgentSessionID, agentsession.Record{ID: "started", Type: agentsession.RecordOperationStarted, RunID: turn.Runs[0].RunID, Operation: &agentsession.OperationData{Intent: agentsession.OperationRun}})
		appendAgentRecord(t, agentSessions, session.AgentSessionID, agentsession.Record{ID: "finished", Type: agentsession.RecordOperationFinished, RunID: turn.Runs[0].RunID, Operation: &agentsession.OperationData{Intent: agentsession.OperationRun, Outcome: string(agent.RunCompleted)}})
		completed, err := service.RecoverAutomatically(context.Background(), session.ID)
		if err != nil || completed != 1 || runner.runs != 0 {
			t.Fatalf("terminal reconciliation = %d runs=%d err=%v", completed, runner.runs, err)
		}
		loaded, err := products.LoadTurn(context.Background(), turn.ID)
		if err != nil || loaded.Status != codingagent.TurnCompleted || loaded.Runs[0].Status != codingagent.RunBindingCompleted {
			t.Fatalf("terminal Turn = %#v, %v", loaded, err)
		}
	})
}

func TestRecoveryCoordinatorReplaysPendingContinuationWithoutSyntheticUserEntry(t *testing.T) {
	products := codingmemory.NewRepository()
	seedMemoryWorktree(t, products, "workspace-continuation", "worktree-continuation")
	agentSessions := agentsession.NewMemoryRepository()
	runner := &gapAgentRunner{}
	service, err := codingagent.NewService(codingagent.Dependencies{
		Sessions: products, Turns: products, AgentSessions: agentSessions, Worktrees: products,
		Agent: runner, Tools: emptyToolFactory{}, Prompts: staticPrompt{}, Events: &productEvents{},
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := service.CreateSession(context.Background(), codingagent.Session{
		ID: "coding-continuation", AgentSessionID: "agent-continuation", WorkspaceID: "workspace-continuation", WorktreeID: "worktree-continuation",
		ProviderProfileID: "profile", ModelID: "model", PermissionMode: codingagent.PermissionAsk,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	turn := codingagent.Turn{
		ID: "turn-continuation", SessionID: session.ID, RequestText: "one original request", Phase: codingagent.TurnPhaseDirect,
		Status: codingagent.TurnPending, Strategy: codingagent.ExecutionSingle, Revision: 1,
		Runs:      []codingagent.RunBinding{{RunID: "run-first", UserEntryID: "entry-first", Phase: codingagent.TurnPhaseDirect, Profile: codingagent.CapabilityDirect, Status: codingagent.RunBindingPending}},
		CreatedAt: now, UpdatedAt: now,
	}
	if err := products.CreateTurn(context.Background(), turn); err != nil {
		t.Fatal(err)
	}
	turn.Runs[0].Status, turn.Runs[0].StartedAt = codingagent.RunBindingRunning, now
	turn.Status, turn.Revision = codingagent.TurnRunning, 2
	if err := products.SaveTurn(context.Background(), turn, 1); err != nil {
		t.Fatal(err)
	}
	turn.Runs[0].Status, turn.Runs[0].FinishedAt = codingagent.RunBindingHandedOff, now.Add(time.Second)
	turn.UpdatedAt, turn.Revision = now.Add(time.Second), 3
	if err := products.SaveTurn(context.Background(), turn, 2); err != nil {
		t.Fatal(err)
	}
	turn.Runs = append(turn.Runs, codingagent.RunBinding{RunID: "run-continuation", Phase: codingagent.TurnPhaseDirect, Profile: codingagent.CapabilityDirect, Status: codingagent.RunBindingPending})
	turn.UpdatedAt, turn.Revision = now.Add(2*time.Second), 4
	if err := products.SaveTurn(context.Background(), turn, 3); err != nil {
		t.Fatal(err)
	}
	completed, err := service.RecoverAutomatically(context.Background(), session.ID)
	if err != nil || completed != 1 || runner.runs != 0 || runner.continuations != 1 {
		t.Fatalf("continuation recovery completed=%d runs=%d continuations=%d err=%v", completed, runner.runs, runner.continuations, err)
	}
	recovered, err := products.LoadTurn(context.Background(), turn.ID)
	if err != nil || recovered.Status != codingagent.TurnCompleted || len(recovered.Runs) != 2 || recovered.Runs[1].UserEntryID != "" {
		t.Fatalf("recovered continuation = %#v, %v", recovered, err)
	}
}

func TestServiceCancelTurnOwnsCancellationAndAgentPersistsAbortedTerminal(t *testing.T) {
	productStore := codingmemory.NewRepository()
	seedMemoryWorktree(t, productStore, "workspace-cancel", "worktree-cancel")
	agentSessions := agentsession.NewMemoryRepository()
	contexts, _ := contextmanager.NewManager()
	started := make(chan struct{}, 1)
	runtime, err := agent.NewRuntime(agent.Dependencies{Models: blockingModelFactory{started: started}, Contexts: contexts, Sessions: agentSessions})
	if err != nil {
		t.Fatal(err)
	}
	events := &productEvents{}
	service, err := codingagent.NewService(codingagent.Dependencies{
		Sessions: productStore, Turns: productStore, AgentSessions: agentSessions, Worktrees: productStore,
		Agent: runtime, Tools: emptyToolFactory{}, Prompts: staticPrompt{}, Events: events,
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.CreateSession(context.Background(), codingagent.Session{
		ID: "coding-cancel", AgentSessionID: "agent-cancel", WorkspaceID: "workspace-cancel", WorktreeID: "worktree-cancel",
		ProviderProfileID: "profile-1", ModelID: "model-1", PermissionMode: codingagent.PermissionAsk,
	})
	if err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		result codingagent.TurnResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := service.StartTurn(context.Background(), codingagent.TurnRequest{SessionID: created.ID, Text: "wait"})
		done <- outcome{result: result, err: err}
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("model did not start")
	}
	if err := service.CancelTurn(context.Background(), created.ID); err != nil {
		t.Fatalf("cancel turn: %v", err)
	}
	if err := service.CancelTurn(context.Background(), created.ID); err != nil {
		t.Fatalf("repeat cancel turn: %v", err)
	}
	var completed outcome
	select {
	case completed = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled turn did not return")
	}
	if completed.result.Status != string(agent.RunAborted) || completed.err == nil {
		t.Fatalf("cancelled outcome = %#v err=%v", completed.result, completed.err)
	}
	durable, err := agentSessions.Load(context.Background(), created.AgentSessionID)
	if err != nil {
		t.Fatal(err)
	}
	foundTerminal := false
	for _, record := range durable.Records {
		if record.Type == agentsession.RecordOperationFinished && record.Operation != nil && record.Operation.Outcome == string(agent.RunAborted) {
			foundTerminal = true
		}
	}
	if !foundTerminal {
		t.Fatalf("aborted terminal was not persisted: %#v", durable.Records)
	}
	foundCancelledEvent := false
	for _, event := range events.values {
		foundCancelledEvent = foundCancelledEvent || event.Kind == codingagent.EventTurnCancelled
	}
	if !foundCancelledEvent {
		t.Fatalf("cancelled event was not projected: %#v", events.values)
	}
}

func TestRecoveryCoordinatorReplaysOnlySafeToolAndProjectsManualContinuation(t *testing.T) {
	productStore := codingmemory.NewRepository()
	seedMemoryWorktree(t, productStore, "workspace-recovery", "worktree-recovery")
	agentSessions := agentsession.NewMemoryRepository()
	contexts, err := contextmanager.NewManager()
	if err != nil {
		t.Fatalf("create context manager: %v", err)
	}
	runtime, err := agent.NewRuntime(agent.Dependencies{Models: finalModelFactory{}, Contexts: contexts, Sessions: agentSessions})
	if err != nil {
		t.Fatalf("create Agent runtime: %v", err)
	}
	executable := &recoveryReadTool{}
	service, err := codingagent.NewService(codingagent.Dependencies{
		Sessions: productStore, Turns: productStore, AgentSessions: agentSessions, Worktrees: productStore,
		Agent: runtime, Tools: recoveryToolFactory{executable: executable}, Prompts: staticPrompt{}, Events: &productEvents{},
	})
	if err != nil {
		t.Fatalf("create Coding Agent service: %v", err)
	}
	created, err := service.CreateSession(context.Background(), codingagent.Session{
		ID: "coding-recovery", AgentSessionID: "agent-recovery", WorkspaceID: "workspace-recovery", WorktreeID: "worktree-recovery",
		ProviderProfileID: "profile-1", ModelID: "model-1", PermissionMode: codingagent.PermissionAsk,
	})
	if err != nil {
		t.Fatalf("create Coding recovery session: %v", err)
	}
	appendAgentRecord(t, agentSessions, created.AgentSessionID, agentsession.Record{ID: "operation", Type: agentsession.RecordOperationStarted, RunID: "turn-recovery", Operation: &agentsession.OperationData{Intent: agentsession.OperationRun}})
	appendAgentEntry(t, agentSessions, created.AgentSessionID, agentsession.Entry{ID: "user", RunID: "turn-recovery", Type: agentsession.EntryMessage, Message: &llm.Message{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentText, Text: "recover"}}}})
	appendAgentRecord(t, agentSessions, created.AgentSessionID, agentsession.Record{ID: "step-start", Type: agentsession.RecordStepStarted, RunID: "turn-recovery", Step: &agentsession.StepData{Attempt: 1}})
	appendAgentEntry(t, agentSessions, created.AgentSessionID, agentsession.Entry{ID: "assistant", RunID: "turn-recovery", Type: agentsession.EntryMessage, Message: &llm.Message{
		Role: llm.RoleAssistant, StopReason: llm.StopReasonToolUse,
		Content: []llm.Content{{Type: llm.ContentToolCall, ToolCall: &llm.ToolCall{ID: "call-recovery", Name: "recovery_read", Arguments: json.RawMessage(`{"path":"secret-path"}`)}}},
	}})
	appendAgentRecord(t, agentSessions, created.AgentSessionID, agentsession.Record{ID: "step-finish", Type: agentsession.RecordStepFinished, RunID: "turn-recovery", Step: &agentsession.StepData{Attempt: 1, AssistantEntryID: "assistant", StopReason: string(llm.StopReasonToolUse)}})
	appendAgentRecord(t, agentSessions, created.AgentSessionID, agentsession.Record{ID: "tool-start", Type: agentsession.RecordToolStarted, RunID: "turn-recovery", Tool: &agentsession.ToolData{
		AssistantEntryID: "assistant", ToolCallID: "call-recovery", ToolName: "recovery_read",
		EffectiveArgs: json.RawMessage(`{"path":"secret-path"}`), IdempotencyKey: "private-idempotency-key",
		ResultEntryID: "result", ReplayPolicy: string(tool.ReplaySafe),
	}})

	completed, err := service.RecoverAutomatically(context.Background(), created.ID)
	if err != nil || completed != 1 || executable.calls != 1 {
		t.Fatalf("automatic recovery completed = %d, Tool calls = %d, err = %v", completed, executable.calls, err)
	}
	snapshot, err := service.Snapshot(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("snapshot recovery: %v", err)
	}
	if len(snapshot.RecoveryActions) != 1 || snapshot.RecoveryActions[0].Kind != "continue_run" || snapshot.RecoveryActions[0].Automatic {
		t.Fatalf("product RecoveryPlan = %#v", snapshot.RecoveryActions)
	}
	projected := snapshot.RecoveryActions[0]
	if projected.ToolName != "" || strings.Contains(projected.Summary, "secret-path") || strings.Contains(projected.Summary, "private-idempotency-key") {
		t.Fatalf("private recovery material crossed product boundary: %#v", projected)
	}
	result, err := service.RecoverTurn(context.Background(), codingagent.RecoverTurnRequest{
		SessionID: created.ID, TurnID: projected.TurnID, ActionID: projected.ID, Decision: codingagent.RecoveryRetry,
	})
	if err != nil || result.Status != "completed" {
		t.Fatalf("continue recovered turn = %#v, err = %v", result, err)
	}
	final, err := service.Snapshot(context.Background(), created.ID)
	if err != nil || len(final.RecoveryActions) != 0 || final.RuntimeState != codingagent.RuntimeIdle {
		t.Fatalf("final recovery snapshot = %#v, err = %v", final, err)
	}
}

func appendAgentEntry(t *testing.T, repository agentsession.Repository, sessionID agentsession.ID, entry agentsession.Entry) {
	t.Helper()
	if _, err := repository.AppendEntry(context.Background(), sessionID, agentsession.MainLane, entry); err != nil {
		t.Fatalf("append Agent entry %q: %v", entry.ID, err)
	}
}

func appendAgentRecord(t *testing.T, repository agentsession.Repository, sessionID agentsession.ID, record agentsession.Record) {
	t.Helper()
	if _, err := repository.AppendRecord(context.Background(), sessionID, agentsession.MainLane, record); err != nil {
		t.Fatalf("append Agent record %q: %v", record.ID, err)
	}
}

func TestServiceProjectsAndResolvesInterruptWithoutExposingToolResult(t *testing.T) {
	productStore := codingmemory.NewRepository()
	seedMemoryWorktree(t, productStore, "workspace-resume", "worktree-resume")
	agentSessions := agentsession.NewMemoryRepository()
	contexts, err := contextmanager.NewManager()
	if err != nil {
		t.Fatalf("create context manager: %v", err)
	}
	model := &sequentialModel{responses: []llm.Message{
		{Role: llm.RoleAssistant, Provider: "profile-1", Model: "model-1", StopReason: llm.StopReasonToolUse, Content: []llm.Content{{Type: llm.ContentToolCall, ToolCall: &llm.ToolCall{ID: "call-edit", Name: "apply_edit", Arguments: json.RawMessage(`{"path":"main.go"}`)}}}},
		{Role: llm.RoleAssistant, Provider: "profile-1", Model: "model-1", StopReason: llm.StopReasonStop, Content: []llm.Content{{Type: llm.ContentText, Text: "edit approved"}}},
	}}
	runtime, err := agent.NewRuntime(agent.Dependencies{Models: sequentialModelFactory{model: model}, Contexts: contexts, Sessions: agentSessions})
	if err != nil {
		t.Fatalf("create Agent runtime: %v", err)
	}
	service, err := codingagent.NewService(codingagent.Dependencies{
		Sessions: productStore, Turns: productStore, AgentSessions: agentSessions, Worktrees: productStore,
		Agent: runtime, Tools: approvalToolFactory{}, Prompts: staticPrompt{}, Events: &productEvents{},
	})
	if err != nil {
		t.Fatalf("create Coding Agent service: %v", err)
	}
	created, err := service.CreateSession(context.Background(), codingagent.Session{
		ID: "coding-resume", AgentSessionID: "agent-resume", WorkspaceID: "workspace-resume", WorktreeID: "worktree-resume",
		ProviderProfileID: "profile-1", ModelID: "model-1", PermissionMode: codingagent.PermissionAsk,
	})
	if err != nil {
		t.Fatalf("create Coding session: %v", err)
	}
	interrupted, err := service.StartTurn(context.Background(), codingagent.TurnRequest{SessionID: created.ID, Text: "edit main.go"})
	if err != nil {
		t.Fatalf("start turn: %v", err)
	}
	if interrupted.Status != string(agent.RunInterrupted) || interrupted.InterruptID != "approval-product-1" || interrupted.InterruptKind != "approval" {
		t.Fatalf("interrupted product result = %#v", interrupted)
	}
	snapshot, err := service.Snapshot(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("snapshot interrupted turn: %v", err)
	}
	if snapshot.RuntimeState != codingagent.RuntimeAwaitingApproval || len(snapshot.PendingInterrupts) != 1 || snapshot.PendingInterrupts[0].Summary != "Apply edit to main.go" {
		t.Fatalf("interrupted product snapshot = %#v", snapshot)
	}
	completed, err := service.ResumeTurn(context.Background(), codingagent.ResumeTurnRequest{
		SessionID: created.ID, TurnID: interrupted.TurnID, InterruptID: interrupted.InterruptID,
		Decision: codingagent.ResolutionApproved, Details: json.RawMessage(`{"decision":"allow"}`),
	})
	if err != nil {
		t.Fatalf("resume turn: %v", err)
	}
	if completed.Status != string(agent.RunCompleted) || completed.Response != "edit approved" || completed.TurnID != interrupted.TurnID {
		t.Fatalf("completed product result = %#v", completed)
	}
	snapshot, err = service.Snapshot(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("snapshot resumed turn: %v", err)
	}
	if snapshot.RuntimeState != codingagent.RuntimeIdle || len(snapshot.PendingInterrupts) != 0 {
		t.Fatalf("resumed product snapshot = %#v", snapshot)
	}
}

func TestServiceSessionGrantIsDerivedFromPendingJournalPersistedAndReused(t *testing.T) {
	ctx := context.Background()
	productStore := codingmemory.NewRepository()
	seedMemoryWorktree(t, productStore, "workspace-grant", "worktree-grant")
	agentSessions := agentsession.NewMemoryRepository()
	contexts, err := contextmanager.NewManager()
	if err != nil {
		t.Fatalf("create context manager: %v", err)
	}
	model := &sequentialModel{responses: []llm.Message{
		{Role: llm.RoleAssistant, Provider: "profile", Model: "model", StopReason: llm.StopReasonToolUse, Content: []llm.Content{{Type: llm.ContentToolCall, ToolCall: &llm.ToolCall{ID: "call-one", Name: "apply_patch", Arguments: json.RawMessage(`{"path":"main.go","token":"top-secret"}`)}}}},
		{Role: llm.RoleAssistant, Provider: "profile", Model: "model", StopReason: llm.StopReasonToolUse, Content: []llm.Content{{Type: llm.ContentToolCall, ToolCall: &llm.ToolCall{ID: "call-two", Name: "apply_patch", Arguments: json.RawMessage(`{"path":"main.go"}`)}}}},
		{Role: llm.RoleAssistant, Provider: "profile", Model: "model", StopReason: llm.StopReasonStop, Content: []llm.Content{{Type: llm.ContentText, Text: "done"}}},
	}}
	runtime, err := agent.NewRuntime(agent.Dependencies{Models: sequentialModelFactory{model: model}, Contexts: contexts, Sessions: agentSessions})
	if err != nil {
		t.Fatalf("create Agent runtime: %v", err)
	}
	factory := &grantAwareToolFactory{}
	service, err := codingagent.NewService(codingagent.Dependencies{
		Sessions: productStore, Turns: productStore, AgentSessions: agentSessions, Worktrees: productStore,
		Agent: runtime, Tools: factory, Prompts: staticPrompt{}, Events: &productEvents{},
	})
	if err != nil {
		t.Fatalf("create service: %v", err)
	}
	created, err := service.CreateSession(ctx, codingagent.Session{
		ID: "coding-grant", AgentSessionID: "agent-grant", WorkspaceID: "workspace-grant", WorktreeID: "worktree-grant",
		ProviderProfileID: "profile", ModelID: "model", PermissionMode: codingagent.PermissionAsk,
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	interrupted, err := service.StartTurn(ctx, codingagent.TurnRequest{SessionID: created.ID, Text: "edit twice"})
	if err != nil || interrupted.InterruptID != "approval-grant-1" {
		t.Fatalf("start interrupted turn: result=%#v err=%v", interrupted, err)
	}
	completed, err := service.ResumeTurn(ctx, codingagent.ResumeTurnRequest{
		SessionID: created.ID, TurnID: interrupted.TurnID, InterruptID: interrupted.InterruptID,
		Decision: codingagent.ResolutionApproved, GrantScope: codingagent.PermissionGrantSession,
	})
	if err != nil || completed.Status != string(agent.RunCompleted) || completed.Response != "done" {
		t.Fatalf("resume with session grant: result=%#v err=%v", completed, err)
	}
	stored, err := productStore.LoadSession(ctx, created.ID)
	if err != nil || len(stored.PermissionGrants) != 1 {
		t.Fatalf("stored grants: session=%#v err=%v", stored, err)
	}
	grant := stored.PermissionGrants[0]
	if grant.ToolName != "apply_patch" || grant.Action != codingagent.PermissionActionModify || strings.Join(grant.Paths, ",") != "main.go" || grant.Scope != codingagent.PermissionGrantSession || !grant.ExpiresAt.After(grant.CreatedAt) {
		t.Fatalf("stored grant = %#v", grant)
	}
	encoded, _ := json.Marshal(grant)
	if strings.Contains(string(encoded), "top-secret") || factory.automatic != 1 || len(factory.scopes) != 2 {
		t.Fatalf("grant leaked args or was not reused: grant=%s automatic=%d scopes=%d", encoded, factory.automatic, len(factory.scopes))
	}
}
