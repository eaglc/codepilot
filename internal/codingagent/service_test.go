package codingagent_test

import (
	"context"
	"encoding/json"
	"io"
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

type sequentialModel struct{ responses []llm.Message }

func (m *sequentialModel) Complete(context.Context, llm.ChatRequest) (llm.Message, error) {
	return llm.Message{}, nil
}

func (m *sequentialModel) Stream(context.Context, llm.ChatRequest) (llm.Stream, error) {
	response := m.responses[0]
	m.responses = m.responses[1:]
	return &finalStream{events: []llm.StreamEvent{{Kind: llm.StreamResponseFinished, Message: &response}}}, nil
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
		Sessions: productStore, AgentSessions: agentSessions, Worktrees: productStore,
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
		Sessions: productStore, AgentSessions: agentSessions, Worktrees: productStore,
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
		Sessions: productStore, AgentSessions: agentSessions, Worktrees: productStore,
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
		Sessions: productStore, AgentSessions: agentSessions, Worktrees: productStore,
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
		Sessions: productStore, AgentSessions: agentSessions, Worktrees: productStore,
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
