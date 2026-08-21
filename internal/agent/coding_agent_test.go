package agent

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eaglc/codepilot/internal/contextmanager"
	"github.com/eaglc/codepilot/internal/session"
	"github.com/eaglc/codepilot/internal/tool"
)

func TestCodingAgentRunTurnResumesPatchAndCheckApprovals(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	scope := testTurnScope(root)
	workspaces := &approvalWorkspace{scope: scope}
	invoker := &approvalScriptedInvoker{}
	navigator := &fakeCodeNavigator{}
	factory, err := NewFactory(Dependencies{
		Workspaces: workspaces,
		Languages:  staticLanguageResolver{profile: testGoProfile()},
		Authorizer: &decisionAuthorizer{decisions: []session.ApprovalDecisionKind{
			session.ApprovalAllowOnce,
			session.ApprovalAllowOnce,
		}},
		Invokers:  staticInvokerFactory{invoker: invoker},
		CodeIntel: navigator,
		Contexts:  newTestContextManager(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	codingAgent, err := factory.CreateCodingAgent(context.Background(), testCodingAgentConfig(root))
	if err != nil {
		t.Fatal(err)
	}
	events := &recordingSessionEvents{}

	result, err := codingAgent.RunTurn(context.Background(), testTurnRequest(scope), events)
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalText != "Bug fixed and checks passed." || result.Steps != 3 {
		t.Fatalf("result = %#v", result)
	}
	if result.CheckSummary.Outcome != session.CheckPassed || len(result.AppliedPatches) != 1 {
		t.Fatalf("evidence = %#v, patches = %#v", result.CheckSummary, result.AppliedPatches)
	}
	if invoker.toolCount != 11 || invoker.resumeCount != 2 {
		t.Fatalf("tools = %d, resumes = %d", invoker.toolCount, invoker.resumeCount)
	}
	if got := events.count(session.EventApprovalRequested); got != 2 {
		t.Fatalf("approval requested events = %d, want 2", got)
	}
	if got := events.count(session.EventApprovalResolved); got != 2 {
		t.Fatalf("approval resolved events = %d, want 2", got)
	}
	if got := events.count(session.EventPatchApplied); got != 1 {
		t.Fatalf("patch events = %d, want 1", got)
	}
	if got := events.diffKinds(); len(got) != 3 || got[0] != session.DiffProposed || got[1] != session.DiffSession || got[2] != session.DiffWorkspace {
		t.Fatalf("diff events = %#v", got)
	}
	if err := codingAgent.Close(); err != nil {
		t.Fatalf("close coding agent: %v", err)
	}
	if navigator.closedWorktree != scope.WorktreeID {
		t.Fatalf("closed worktree = %q, want %q", navigator.closedWorktree, scope.WorktreeID)
	}
}

func TestCodingAgentRunTurnMapsCancelledApprovalToCancelledCheck(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	scope := testTurnScope(root)
	invoker := &approvalScriptedInvoker{}
	factory, err := NewFactory(Dependencies{
		Workspaces: &approvalWorkspace{scope: scope},
		Languages:  staticLanguageResolver{profile: testGoProfile()},
		Authorizer: &decisionAuthorizer{decisions: []session.ApprovalDecisionKind{session.ApprovalCancelled}},
		Invokers:   staticInvokerFactory{invoker: invoker},
		Contexts:   newTestContextManager(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	codingAgent, err := factory.CreateCodingAgent(context.Background(), testCodingAgentConfig(root))
	if err != nil {
		t.Fatal(err)
	}

	result, err := codingAgent.RunTurn(context.Background(), testTurnRequest(scope), &recordingSessionEvents{})
	if err != nil {
		t.Fatal(err)
	}
	if result.CheckSummary.Outcome != session.CheckCancelled || result.TerminationReason != "cancelled" {
		t.Fatalf("result = %#v", result)
	}
	if invoker.resumeCount != 1 || invoker.lastResponse != InterruptCancelled {
		t.Fatalf("resume count = %d, response = %q", invoker.resumeCount, invoker.lastResponse)
	}
}

func TestCodingAgentRunTurnRecordsRejectedCheckWithoutReexecution(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	scope := testTurnScope(root)
	invoker := &approvalScriptedInvoker{}
	workspaces := &approvalWorkspace{scope: scope}
	factory, err := NewFactory(Dependencies{
		Workspaces: workspaces,
		Languages:  staticLanguageResolver{profile: testGoProfile()},
		Authorizer: &decisionAuthorizer{decisions: []session.ApprovalDecisionKind{
			session.ApprovalAllowOnce,
			session.ApprovalDeny,
		}},
		Invokers: staticInvokerFactory{invoker: invoker},
		Contexts: newTestContextManager(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	codingAgent, err := factory.CreateCodingAgent(context.Background(), testCodingAgentConfig(root))
	if err != nil {
		t.Fatal(err)
	}

	result, err := codingAgent.RunTurn(context.Background(), testTurnRequest(scope), &recordingSessionEvents{})
	if err != nil {
		t.Fatal(err)
	}
	if result.CheckSummary.Outcome != session.CheckDenied || len(result.AppliedPatches) != 1 {
		t.Fatalf("result = %#v", result)
	}
	if workspaces.checkCalls != 1 {
		t.Fatalf("check calls = %d, want one pre-approval call", workspaces.checkCalls)
	}
	if invoker.lastResponse != InterruptRejected {
		t.Fatalf("resume response = %q", invoker.lastResponse)
	}
}

func TestCodingAgentRunTurnUsesManagedContext(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	scope := testTurnScope(root)
	invoker := &capturingInvoker{}
	manager, err := contextmanager.NewManager(managedContextStrategy{})
	if err != nil {
		t.Fatal(err)
	}
	factory, err := NewFactory(Dependencies{
		Workspaces: &approvalWorkspace{scope: scope},
		Languages:  staticLanguageResolver{profile: testGoProfile()},
		Authorizer: &decisionAuthorizer{},
		Invokers:   staticInvokerFactory{invoker: invoker},
		Contexts:   manager,
	})
	if err != nil {
		t.Fatal(err)
	}
	codingAgent, err := factory.CreateCodingAgent(context.Background(), testCodingAgentConfig(root))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := codingAgent.RunTurn(context.Background(), testTurnRequest(scope), &recordingSessionEvents{}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(invoker.input.SystemPrompt, "\nmanaged context") {
		t.Fatalf("managed system prompt = %q", invoker.input.SystemPrompt)
	}
	if len(invoker.input.Messages) != 2 || invoker.input.Messages[0].Content != "Earlier context. [managed]" || invoker.input.Messages[1].Content != "Fix the bug." {
		t.Fatalf("managed invocation messages = %#v", invoker.input.Messages)
	}
}

func TestCodingEventAdapterCoalescesAssistantTextAndMapsToolFailure(t *testing.T) {
	scope := testTurnScope(filepath.Clean(t.TempDir()))
	events := &recordingSessionEvents{}
	adapter, err := newCodingEventAdapter(scope, events)
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.PublishInvocationEvent(context.Background(), InvocationEvent{Kind: InvocationEventAssistantText, Text: "hello "}); err != nil {
		t.Fatal(err)
	}
	if err := adapter.PublishInvocationEvent(context.Background(), InvocationEvent{Kind: InvocationEventAssistantText, Text: "world"}); err != nil {
		t.Fatal(err)
	}
	if err := adapter.PublishInvocationEvent(context.Background(), InvocationEvent{
		Kind: InvocationEventToolFinished,
		Tool: &InvocationToolEvent{Name: "read_file", Status: tool.ResultFailed, Summary: "failed"},
	}); err != nil {
		t.Fatal(err)
	}

	values := events.snapshot()
	if len(values) != 2 || values[0].Kind != session.EventAssistantDelta || values[0].Payload.Text.Text != "hello world" || values[1].Kind != session.EventToolFailed {
		t.Fatalf("events = %#v", values)
	}
}

type staticLanguageResolver struct {
	profile LanguageProfile
}

func (r staticLanguageResolver) ResolveLanguage(context.Context, string) (LanguageProfile, error) {
	return r.profile, nil
}

type staticInvokerFactory struct {
	invoker AgentInvoker
}

type managedContextStrategy struct{}

func (managedContextStrategy) Process(_ context.Context, request contextmanager.Request) (contextmanager.Result, error) {
	messages := append([]contextmanager.Message(nil), request.Messages...)
	for index := range messages {
		if !messages[index].Current {
			messages[index].Content += " [managed]"
		}
	}
	return contextmanager.Result{SystemPrompt: request.SystemPrompt + "\nmanaged context", Messages: messages}, nil
}

type capturingInvoker struct {
	input InvocationInput
}

func (i *capturingInvoker) Invoke(_ context.Context, input InvocationInput, _ InvocationEventSink) (InvocationResult, error) {
	i.input = input
	return InvocationResult{Status: InvocationCompleted, FinalText: "done", TerminationReason: "completed"}, nil
}

func (*capturingInvoker) Resume(context.Context, ResumeInput, InvocationEventSink) (InvocationResult, error) {
	return InvocationResult{}, errors.New("unexpected resume")
}

func (*capturingInvoker) Close() error { return nil }

func (f staticInvokerFactory) CreateInvoker(context.Context) (AgentInvoker, error) {
	return f.invoker, nil
}

type decisionAuthorizer struct {
	mu        sync.Mutex
	decisions []session.ApprovalDecisionKind
}

func (*decisionAuthorizer) Authorize(context.Context, session.PermissionMode, session.Action) (session.Authorization, error) {
	return session.Authorization{}, errors.New("unexpected authorization call")
}

func (a *decisionAuthorizer) WaitDecision(context.Context, session.ApprovalRequestID) (session.ApprovalDecision, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.decisions) == 0 {
		return session.ApprovalDecision{}, errors.New("no scripted decision")
	}
	kind := a.decisions[0]
	a.decisions = a.decisions[1:]
	return session.ApprovalDecision{Kind: kind, DecidedAt: time.Now().UTC()}, nil
}

func (*decisionAuthorizer) Resolve(context.Context, session.ApprovalResolution) error {
	return nil
}

func (*decisionAuthorizer) ClearSession(context.Context, session.SessionID) error {
	return nil
}

type approvalScriptedInvoker struct {
	registry     *tool.Registry
	toolCount    int
	resumeCount  int
	lastResponse InterruptResponseKind
}

func (i *approvalScriptedInvoker) Invoke(ctx context.Context, input InvocationInput, events InvocationEventSink) (InvocationResult, error) {
	i.registry = input.Tools
	i.toolCount = len(input.Tools.Definitions())
	return i.invokeInterruptedTool(ctx, events, "apply_patch", json.RawMessage(`{"patch":"diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n@@ -1 +1 @@\n-old\n+new\n","intent":"fix bug"}`), "runtime-patch", 1)
}

func (i *approvalScriptedInvoker) Resume(ctx context.Context, input ResumeInput, events InvocationEventSink) (InvocationResult, error) {
	i.resumeCount++
	i.lastResponse = input.Response.Kind
	if input.Response.Kind == InterruptCancelled {
		return InvocationResult{Status: InvocationCancelled, Steps: 1, TerminationReason: "cancelled"}, nil
	}
	if input.Response.Kind == InterruptRejected {
		return InvocationResult{Status: InvocationCompleted, FinalText: "The check was not run.", Steps: 3, TerminationReason: "completed"}, nil
	}
	if input.Response.Kind != InterruptApproved {
		return InvocationResult{}, errors.New("unexpected resume response")
	}
	if i.resumeCount == 1 {
		if _, err := i.invokeTool(ctx, events, "apply_patch", json.RawMessage(`{"patch":"diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n@@ -1 +1 @@\n-old\n+new\n","intent":"fix bug"}`)); err != nil {
			return InvocationResult{}, err
		}
		return i.invokeInterruptedTool(ctx, events, "run_checks", json.RawMessage(`{"plan_id":"go-test-all"}`), "runtime-check", 2)
	}
	if _, err := i.invokeTool(ctx, events, "run_checks", json.RawMessage(`{"plan_id":"go-test-all"}`)); err != nil {
		return InvocationResult{}, err
	}
	return InvocationResult{Status: InvocationCompleted, FinalText: "Bug fixed and checks passed.", Steps: 3, TerminationReason: "completed"}, nil
}

func (*approvalScriptedInvoker) Close() error {
	return nil
}

func (i *approvalScriptedInvoker) invokeInterruptedTool(ctx context.Context, events InvocationEventSink, name string, arguments json.RawMessage, runtimeID string, steps int) (InvocationResult, error) {
	result, err := i.invokeTool(ctx, events, name, arguments)
	if err != nil {
		return InvocationResult{}, err
	}
	if result.Status != tool.ResultInterrupted || result.Interrupt == nil {
		return InvocationResult{}, errors.New("scripted tool did not interrupt")
	}
	interrupt := &InvocationInterrupt{ID: runtimeID, Kind: result.Interrupt.Kind, Payload: append(json.RawMessage(nil), result.Interrupt.Payload...)}
	if err := events.PublishInvocationEvent(ctx, InvocationEvent{Kind: InvocationEventInterrupted, Interrupt: interrupt}); err != nil {
		return InvocationResult{}, err
	}
	return InvocationResult{Status: InvocationInterrupted, Steps: steps, TerminationReason: "interrupted", Interrupt: interrupt}, nil
}

func (i *approvalScriptedInvoker) invokeTool(ctx context.Context, events InvocationEventSink, name string, arguments json.RawMessage) (tool.Result, error) {
	value, exists := i.registry.Lookup(name)
	if !exists {
		return tool.Result{}, errors.New("scripted tool is unavailable")
	}
	if err := events.PublishInvocationEvent(ctx, InvocationEvent{Kind: InvocationEventToolStarted, Tool: &InvocationToolEvent{Name: name}}); err != nil {
		return tool.Result{}, err
	}
	result, err := value.Invoke(ctx, arguments)
	if err != nil {
		return tool.Result{}, err
	}
	if err := events.PublishInvocationEvent(ctx, InvocationEvent{Kind: InvocationEventToolFinished, Tool: &InvocationToolEvent{Name: name, Status: result.Status, Summary: result.Content}}); err != nil {
		return tool.Result{}, err
	}
	return result, nil
}

type approvalWorkspace struct {
	scope      session.TurnScope
	patchCalls int
	checkCalls int
}

func (*approvalWorkspace) ListFiles(context.Context, ListFilesRequest) (ListFilesResult, error) {
	return ListFilesResult{}, nil
}

func (*approvalWorkspace) SearchCode(context.Context, SearchCodeRequest) (SearchCodeResult, error) {
	return SearchCodeResult{}, nil
}

func (*approvalWorkspace) ReadFile(context.Context, ReadFileRequest) (ReadFileResult, error) {
	return ReadFileResult{}, nil
}

func (*approvalWorkspace) GitStatus(context.Context, GitStatusRequest) (GitStatusResult, error) {
	return GitStatusResult{}, nil
}

func (*approvalWorkspace) ReadDiff(context.Context, ReadDiffRequest) (session.DiffResult, error) {
	return session.DiffResult{}, nil
}

func (w *approvalWorkspace) ApplyPatch(context.Context, ApplyPatchRequest) (ApplyPatchResult, error) {
	w.patchCalls++
	proposed := session.DiffResult{Kind: session.DiffProposed, Text: "diff", Files: []session.DiffFile{{Path: "main.go"}}}
	if w.patchCalls == 1 {
		request := session.ApprovalRequest{
			ID: "approval-patch", SessionID: w.scope.SessionID, TurnID: w.scope.TurnID, CreatedAt: time.Now().UTC(),
			Action: session.Action{
				Kind: session.ActionApplyPatch, Summary: "Apply fix", Patch: &session.PatchAction{Patch: "diff", Files: []string{"main.go"}},
			},
		}
		return ApplyPatchResult{ProposedDiff: proposed}, &session.ApprovalRequiredError{Request: request}
	}
	return ApplyPatchResult{
		Applied: true, ProposedDiff: proposed,
		PatchRecord: session.PatchRecord{
			ID: "patch-1", SessionID: w.scope.SessionID, TurnID: w.scope.TurnID, Patch: "diff", AppliedAt: time.Now().UTC(),
			Files: []session.PatchedFile{{Path: "main.go", BeforeHash: "before", AfterHash: "after"}},
		},
	}, nil
}

func (w *approvalWorkspace) RunChecks(context.Context, RunChecksRequest) (RunChecksResult, error) {
	w.checkCalls++
	if w.checkCalls == 1 {
		request := session.ApprovalRequest{
			ID: "approval-check", SessionID: w.scope.SessionID, TurnID: w.scope.TurnID, CreatedAt: time.Now().UTC(),
			Action: session.Action{
				Kind: session.ActionRunCheck, Summary: "Run tests", Command: &session.CommandAction{Program: "go", Args: []string{"test", "./..."}, Timeout: time.Minute},
			},
		}
		return RunChecksResult{PlanID: "go-test-all", Outcome: session.CheckNotRun}, &session.ApprovalRequiredError{Request: request}
	}
	return RunChecksResult{PlanID: "go-test-all", Outcome: session.CheckPassed, Summary: "The approved project check passed."}, nil
}

type recordingSessionEvents struct {
	mu     sync.Mutex
	events []session.Event
}

func (s *recordingSessionEvents) Publish(_ context.Context, event session.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, event)
	return nil
}

func (s *recordingSessionEvents) snapshot() []session.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]session.Event(nil), s.events...)
}

func (s *recordingSessionEvents) count(kind session.EventKind) int {
	count := 0
	for _, event := range s.snapshot() {
		if event.Kind == kind {
			count++
		}
	}
	return count
}

func (s *recordingSessionEvents) diffKinds() []session.DiffKind {
	values := make([]session.DiffKind, 0)
	for _, event := range s.snapshot() {
		if event.Kind == session.EventDiffChanged && event.Payload.Diff != nil {
			values = append(values, event.Payload.Diff.Kind)
		}
	}
	return values
}

func testGoProfile() LanguageProfile {
	return LanguageProfile{
		ID:         LanguageGo,
		PromptHint: "Use idiomatic Go.",
		CheckPlans: []CheckPlan{{
			ID: "go-test-all", Description: "Run all tests.",
			Command: CheckCommand{ID: "go-test-all", Program: "go", Args: []string{"test", "./..."}, Timeout: time.Minute, MaxOutputBytes: 1 << 20},
		}},
	}
}

func testCodingAgentConfig(root string) session.CodingAgentConfig {
	return session.CodingAgentConfig{
		SessionID: "session-1", WorkspaceID: "workspace-1", WorktreeID: "worktree-1", WorktreeRoot: root,
		ProviderProfileID: "provider-1", ModelID: "model-1", Limits: testRunLimits(),
	}
}

func testTurnScope(root string) session.TurnScope {
	config := testCodingAgentConfig(root)
	return session.TurnScope{
		TurnID: "turn-1", SessionID: config.SessionID, WorkspaceID: config.WorkspaceID, WorktreeID: config.WorktreeID,
		WorktreeRoot: config.WorktreeRoot, ProviderProfileID: config.ProviderProfileID, ModelID: config.ModelID,
		PermissionMode: session.PermissionAsk, Limits: config.Limits,
	}
}

func testTurnRequest(scope session.TurnScope) session.TurnRequest {
	return session.TurnRequest{
		Scope: scope,
		History: []session.Message{{
			ID: "message-1", SessionID: scope.SessionID, TurnID: "turn-old", Role: session.RoleUser, Content: "Earlier context.",
		}},
		UserMessage: session.Message{
			ID: "message-2", SessionID: scope.SessionID, TurnID: scope.TurnID, Role: session.RoleUser, Content: "Fix the bug.",
		},
	}
}

func testRunLimits() session.RunLimits {
	return session.RunLimits{
		MaxSteps: 20, MaxTurnDuration: time.Minute, CommandTimeout: time.Minute,
		ToolResultMaxBytes: 1 << 20, CommandOutputMaxBytes: 1 << 20,
	}
}

func newTestContextManager(t *testing.T) *contextmanager.Manager {
	t.Helper()
	manager, err := contextmanager.NewManager(contextmanager.NopStrategy{})
	if err != nil {
		t.Fatal(err)
	}
	return manager
}
