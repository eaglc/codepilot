package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	agentsession "github.com/eaglc/codepilot/internal/agent/session"
	"github.com/eaglc/codepilot/internal/contextmanager"
	"github.com/eaglc/codepilot/internal/llm"
	"github.com/eaglc/codepilot/internal/tool"
)

type sequenceIDs struct{ next int }

func (g *sequenceIDs) Next(prefix string) (string, error) {
	g.next++
	return prefix + "-" + string(rune('a'+g.next-1)), nil
}

type fakeModelFactory struct{ model *fakeModel }

func (f fakeModelFactory) CreateModel(context.Context, llm.ModelRef) (llm.ChatModel, error) {
	return f.model, nil
}

type fakeModel struct {
	responses []llm.Message
	requests  []llm.ChatRequest
}

type summaryUsageStrategy struct {
	usage llm.Usage
	err   error
}

type eventCompactionStrategy struct{}

func (eventCompactionStrategy) Process(ctx context.Context, request contextmanager.Request) (contextmanager.Result, error) {
	boundary := contextmanager.CompactionBoundary{
		SourceDigest: strings.Repeat("a", 64), FromEntryID: request.Messages[0].EntryID, ToEntryID: request.Messages[0].EntryID,
	}
	if request.OnCompactionStarted == nil {
		return contextmanager.Result{}, errors.New("compaction start callback is missing")
	}
	if err := request.OnCompactionStarted(ctx, boundary); err != nil {
		return contextmanager.Result{}, err
	}
	return contextmanager.Result{
		SystemPrompt: request.SystemPrompt, Messages: request.Messages,
		Summaries: []contextmanager.Summary{{
			Text: "durable summary", CoversFromEntryID: boundary.FromEntryID, CoversToEntryID: boundary.ToEntryID,
			SourceDigest: boundary.SourceDigest, Strategy: "test-summary", StrategyVersion: "v1",
		}},
	}, nil
}

func (s summaryUsageStrategy) Process(_ context.Context, request contextmanager.Request) (contextmanager.Result, error) {
	return contextmanager.Result{
		SystemPrompt: request.SystemPrompt,
		Messages:     request.Messages,
		SummaryUsage: []llm.Usage{s.usage},
	}, s.err
}

func (m *fakeModel) Complete(context.Context, llm.ChatRequest) (llm.Message, error) {
	return llm.Message{}, nil
}

func (m *fakeModel) Stream(_ context.Context, request llm.ChatRequest) (llm.Stream, error) {
	m.requests = append(m.requests, request)
	response := m.responses[0]
	m.responses = m.responses[1:]
	events := []llm.StreamEvent{}
	for _, content := range response.Content {
		if content.Type == llm.ContentText {
			events = append(events, llm.StreamEvent{Kind: llm.StreamTextDelta, Delta: content.Text})
		} else if content.Type == llm.ContentThinking {
			events = append(events, llm.StreamEvent{Kind: llm.StreamThinkingDelta, Delta: content.Text})
		}
	}
	events = append(events, llm.StreamEvent{Kind: llm.StreamResponseFinished, Message: &response})
	return &fakeStream{events: events}, nil
}

type fakeStream struct{ events []llm.StreamEvent }

func (s *fakeStream) Recv() (llm.StreamEvent, error) {
	if len(s.events) == 0 {
		return llm.StreamEvent{}, io.EOF
	}
	event := s.events[0]
	s.events = s.events[1:]
	return event, nil
}

func (*fakeStream) Close() error { return nil }

type readTool struct{ calls int }

func (*readTool) Definition() llm.ToolDefinition {
	return llm.ToolDefinition{Name: "read_file", Description: "read a file", InputSchema: json.RawMessage(`{"type":"object"}`)}
}

type failingReadTool struct{ calls int }

func (*failingReadTool) Definition() llm.ToolDefinition {
	return llm.ToolDefinition{Name: "read_file", Description: "fail to read a file", InputSchema: json.RawMessage(`{"type":"object"}`)}
}
func (*failingReadTool) ReplayPolicy() tool.ReplayPolicy { return tool.ReplaySafe }
func (t *failingReadTool) Execute(context.Context, tool.Call, tool.ProgressSink) (tool.Result, error) {
	t.calls++
	return tool.Result{Status: tool.ResultFailed, Content: []llm.Content{{Type: llm.ContentText, Text: "read failed"}}}, nil
}

type interruptTool struct {
	calls   int
	resumes int
}

type handoffTool struct{ resumes int }

func (*handoffTool) Definition() llm.ToolDefinition {
	return llm.ToolDefinition{Name: "handoff", Description: "control handoff", InputSchema: json.RawMessage(`{"type":"object"}`)}
}
func (*handoffTool) ReplayPolicy() tool.ReplayPolicy { return tool.ReplayNever }
func (*handoffTool) Execute(context.Context, tool.Call, tool.ProgressSink) (tool.Result, error) {
	return tool.Result{Status: tool.ResultInterrupted, Content: []llm.Content{{Type: llm.ContentText, Text: "waiting"}}, Interrupt: &tool.Interrupt{ID: "handoff-1", Kind: "control"}}, nil
}
func (t *handoffTool) Resume(_ context.Context, _ tool.Call, _ tool.Interrupt, resolution tool.Result, _ tool.ProgressSink) (tool.Result, error) {
	t.resumes++
	return resolution, nil
}
func (*handoffTool) ControlPolicy() tool.ControlPolicy {
	return tool.ControlPolicy{Exclusive: true, HandoffAfterResolution: true}
}

func (*interruptTool) Definition() llm.ToolDefinition {
	return llm.ToolDefinition{Name: "request_approval", Description: "request approval", InputSchema: json.RawMessage(`{"type":"object"}`)}
}

func (*interruptTool) ReplayPolicy() tool.ReplayPolicy { return tool.ReplayNever }

func (t *interruptTool) Execute(context.Context, tool.Call, tool.ProgressSink) (tool.Result, error) {
	t.calls++
	return tool.Result{
		Status: tool.ResultInterrupted, Content: []llm.Content{{Type: llm.ContentText, Text: "approval required"}},
		Interrupt: &tool.Interrupt{ID: "approval-1", Kind: "approval", Payload: json.RawMessage(`{"summary":"edit file"}`)},
	}, nil
}

func (t *interruptTool) Resume(_ context.Context, _ tool.Call, _ tool.Interrupt, resolution tool.Result, _ tool.ProgressSink) (tool.Result, error) {
	t.resumes++
	if resolution.Status != tool.ResultCompleted {
		return resolution, nil
	}
	return tool.Result{
		Status: tool.ResultCompleted, Content: []llm.Content{{Type: llm.ContentText, Text: "approved action executed"}},
		Details: json.RawMessage(`{"executed":true}`),
	}, nil
}

func (*readTool) ReplayPolicy() tool.ReplayPolicy { return tool.ReplaySafe }

func (t *readTool) Execute(context.Context, tool.Call, tool.ProgressSink) (tool.Result, error) {
	t.calls++
	return tool.Result{Status: tool.ResultCompleted, Content: []llm.Content{{Type: llm.ContentText, Text: "package main"}}}, nil
}

type idempotentTool struct {
	calls int
	keys  []string
}

func (*idempotentTool) Definition() llm.ToolDefinition {
	return llm.ToolDefinition{Name: "idempotent_write", Description: "idempotent test Tool", InputSchema: json.RawMessage(`{"type":"object"}`)}
}
func (*idempotentTool) ReplayPolicy() tool.ReplayPolicy { return tool.ReplayIdempotent }
func (t *idempotentTool) Execute(_ context.Context, call tool.Call, _ tool.ProgressSink) (tool.Result, error) {
	t.calls++
	t.keys = append(t.keys, call.IdempotencyKey)
	return tool.Result{Status: tool.ResultCompleted, Content: []llm.Content{{Type: llm.ContentText, Text: "idempotent result"}}}, nil
}

type eventCollector struct{ events []Event }

func (c *eventCollector) PublishAgentEvent(_ context.Context, event Event) error {
	c.events = append(c.events, event)
	return nil
}

type compactionEventCollector struct {
	events             []Event
	repository         agentsession.Repository
	finishSawPersisted bool
}

func (c *compactionEventCollector) PublishAgentEvent(ctx context.Context, event Event) error {
	c.events = append(c.events, event)
	if event.Kind != EventCompactionFinished {
		return nil
	}
	snapshot, err := c.repository.Load(ctx, event.SessionID)
	if err != nil {
		return err
	}
	for _, entry := range snapshot.Entries {
		if entry.Type == agentsession.EntryCompaction && entry.Compaction != nil && entry.Compaction.SourceDigest == event.Compaction.SourceDigest {
			c.finishSawPersisted = true
			break
		}
	}
	return nil
}

type scriptedStreamModel struct {
	streams  [][]llm.StreamEvent
	requests []llm.ChatRequest
}

func (*scriptedStreamModel) Complete(context.Context, llm.ChatRequest) (llm.Message, error) {
	return llm.Message{}, nil
}

func (m *scriptedStreamModel) Stream(_ context.Context, request llm.ChatRequest) (llm.Stream, error) {
	m.requests = append(m.requests, request)
	events := append([]llm.StreamEvent(nil), m.streams[0]...)
	m.streams = m.streams[1:]
	return &fakeStream{events: events}, nil
}

type scriptedStreamFactory struct{ model *scriptedStreamModel }

func (f scriptedStreamFactory) CreateModel(context.Context, llm.ModelRef) (llm.ChatModel, error) {
	return f.model, nil
}

type testRedactionPolicy struct{}

func (testRedactionPolicy) SanitizeText(value string) string {
	return strings.ReplaceAll(value, "top-secret", "[safe]")
}

func (p testRedactionPolicy) SanitizeToolArguments(_ string, value json.RawMessage) json.RawMessage {
	return json.RawMessage(p.SanitizeText(string(value)))
}

func (p testRedactionPolicy) SanitizeMessage(message llm.Message) llm.Message {
	message = message.Clone()
	for index := range message.Content {
		message.Content[index].Text = p.SanitizeText(message.Content[index].Text)
		if message.Content[index].ToolCall != nil {
			message.Content[index].ToolCall.Arguments = p.SanitizeToolArguments(message.Content[index].ToolCall.Name, message.Content[index].ToolCall.Arguments)
		}
	}
	message.Details = p.SanitizeToolArguments(message.ToolName, message.Details)
	return message
}

func (p testRedactionPolicy) SanitizeToolResult(name string, result tool.Result) tool.Result {
	result = result.Clone()
	for index := range result.Content {
		result.Content[index].Text = p.SanitizeText(result.Content[index].Text)
	}
	result.Details = p.SanitizeToolArguments(name, result.Details)
	return result
}

func TestStreamAssistantPublishesProviderTextDeltasBeforeFinish(t *testing.T) {
	response := llm.Message{
		Role: llm.RoleAssistant, Provider: "test", Model: "model", StopReason: llm.StopReasonStop,
		Content: []llm.Content{{Type: llm.ContentText, Text: "hello world"}},
	}
	model := &scriptedStreamModel{streams: [][]llm.StreamEvent{{
		{Kind: llm.StreamTextDelta, Delta: "hello "},
		{Kind: llm.StreamTextDelta, Delta: "world"},
		{Kind: llm.StreamResponseFinished, Message: &response},
	}}}
	runtime := &Runtime{ids: &sequenceIDs{}, dataPolicy: identityDataPolicy{}}
	events := &eventCollector{}
	dispatcher := &eventDispatcher{runtime: runtime, sink: events, sessionID: "session", runID: "run"}
	message, observed, err := runtime.streamAssistant(context.Background(), model, llm.ChatRequest{
		Model: llm.ModelRef{Provider: "test", Model: "model"},
	}, dispatcher)
	if err != nil || !observed || message.Content[0].Text != "hello world" {
		t.Fatalf("stream result=%#v observed=%v err=%v", message, observed, err)
	}
	var deltas []string
	for _, event := range events.events {
		if event.Kind == EventAssistantTextDelta && event.Assistant != nil {
			deltas = append(deltas, event.Assistant.Text)
		}
	}
	if len(deltas) != 2 || deltas[0] != "hello " || deltas[1] != "world" {
		t.Fatalf("assistant deltas = %#v", deltas)
	}
}

type secretResultTool struct{ arguments json.RawMessage }

func (*secretResultTool) Definition() llm.ToolDefinition {
	return llm.ToolDefinition{Name: "secret_tool", Description: "exercise the data policy", InputSchema: json.RawMessage(`{"type":"object"}`)}
}

func (*secretResultTool) ReplayPolicy() tool.ReplayPolicy { return tool.ReplaySafe }

func (t *secretResultTool) Execute(ctx context.Context, call tool.Call, sink tool.ProgressSink) (tool.Result, error) {
	t.arguments = append(json.RawMessage(nil), call.Arguments...)
	if sink != nil {
		_ = sink.PublishToolProgress(ctx, tool.Progress{Summary: "using top-secret", Details: json.RawMessage(`{"token":"top-secret"}`)})
	}
	return tool.Result{
		Status: tool.ResultCompleted, Content: []llm.Content{{Type: llm.ContentText, Text: "result top-secret"}},
		Details: json.RawMessage(`{"token":"top-secret"}`),
	}, nil
}

type recordingRetryWaiter struct{ delays []time.Duration }

func (w *recordingRetryWaiter) Wait(ctx context.Context, delay time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	w.delays = append(w.delays, delay)
	return nil
}

type retryModel struct {
	calls        int
	failures     int
	partialFirst bool
}

func (*retryModel) Complete(context.Context, llm.ChatRequest) (llm.Message, error) {
	return llm.Message{}, nil
}

func (m *retryModel) Stream(context.Context, llm.ChatRequest) (llm.Stream, error) {
	m.calls++
	if m.calls <= m.failures {
		events := []llm.StreamEvent{{Kind: llm.StreamResponseFailed, ErrorCode: "rate_limited", ErrorMessage: "Provider rate limit was reached."}}
		if m.partialFirst {
			events = append([]llm.StreamEvent{{Kind: llm.StreamTextDelta, Delta: "partial"}}, events...)
		}
		return &fakeStream{events: events}, nil
	}
	response := llm.Message{Role: llm.RoleAssistant, Provider: "test", Model: "model", StopReason: llm.StopReasonStop, Content: []llm.Content{{Type: llm.ContentText, Text: "done"}}}
	return &fakeStream{events: []llm.StreamEvent{{Kind: llm.StreamResponseFinished, Message: &response}}}, nil
}

type retryModelFactory struct{ model *retryModel }

func (f retryModelFactory) CreateModel(context.Context, llm.ModelRef) (llm.ChatModel, error) {
	return f.model, nil
}

func TestRuntimeRetriesOnlyUnobservedTransientModelFailures(t *testing.T) {
	repository := agentsession.NewMemoryRepository()
	if err := repository.Create(context.Background(), agentsession.Metadata{ID: "session-retry"}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	manager, _ := contextmanager.NewManager()
	model := &retryModel{failures: 2}
	waits := &recordingRetryWaiter{}
	runtime, err := NewRuntime(Dependencies{Models: retryModelFactory{model: model}, Contexts: manager, Sessions: repository, IDs: &sequenceIDs{}, RetryWaiter: waits})
	if err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	events := &eventCollector{}
	result, err := runtime.Run(context.Background(), RunRequest{
		SessionID: "session-retry", RunID: "run-retry", UserEntryID: "user-retry", Model: llm.ModelRef{Provider: "test", Model: "model"},
		UserMessage: llm.Message{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentText, Text: "retry"}}},
		Limits:      RunLimits{MaxSteps: 2, MaxModelAttempts: 3, InitialRetryDelay: 10 * time.Millisecond, MaxRetryDelay: 15 * time.Millisecond},
	}, events)
	if err != nil || result.Status != RunCompleted || model.calls != 3 {
		t.Fatalf("retry result=%#v calls=%d err=%v", result, model.calls, err)
	}
	if len(waits.delays) != 2 || waits.delays[0] != 10*time.Millisecond || waits.delays[1] != 15*time.Millisecond {
		t.Fatalf("retry delays = %#v", waits.delays)
	}
	var retries int
	for _, event := range events.events {
		if event.Kind == EventRetryScheduled {
			retries++
			if event.Retry == nil || event.Retry.Reason != "rate_limited" || event.Retry.Attempt != retries+1 {
				t.Fatalf("retry event = %#v", event)
			}
		}
	}
	if retries != 2 {
		t.Fatalf("retry events = %#v", events.events)
	}
	snapshot, _ := repository.Load(context.Background(), "session-retry")
	var starts, finishes int
	for _, record := range snapshot.Records {
		if record.Type == agentsession.RecordStepStarted {
			starts++
		}
		if record.Type == agentsession.RecordStepFinished {
			finishes++
		}
	}
	if starts != 1 || finishes != 1 {
		t.Fatalf("logical step was duplicated: starts=%d finishes=%d", starts, finishes)
	}
}

func TestRuntimeDoesNotRetryAfterVisibleStreamDelta(t *testing.T) {
	repository := agentsession.NewMemoryRepository()
	if err := repository.Create(context.Background(), agentsession.Metadata{ID: "session-partial"}); err != nil {
		t.Fatal(err)
	}
	manager, _ := contextmanager.NewManager()
	model := &retryModel{failures: 1, partialFirst: true}
	waits := &recordingRetryWaiter{}
	runtime, _ := NewRuntime(Dependencies{Models: retryModelFactory{model: model}, Contexts: manager, Sessions: repository, IDs: &sequenceIDs{}, RetryWaiter: waits})
	events := &eventCollector{}
	_, err := runtime.Run(context.Background(), RunRequest{
		SessionID: "session-partial", RunID: "run-partial", UserEntryID: "user-partial", Model: llm.ModelRef{Provider: "test", Model: "model"},
		UserMessage: llm.Message{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentText, Text: "partial"}}}, Limits: RunLimits{MaxModelAttempts: 3},
	}, events)
	if err == nil || model.calls != 1 || len(waits.delays) != 0 {
		t.Fatalf("partial stream was retried: calls=%d waits=%#v err=%v", model.calls, waits.delays, err)
	}
	for _, event := range events.events {
		if event.Kind == EventRetryScheduled {
			t.Fatalf("partial stream emitted retry event: %#v", events.events)
		}
	}
}

func TestRuntimeStopsRepeatedToolLoopAndPersistsCancelledResult(t *testing.T) {
	repository := agentsession.NewMemoryRepository()
	if err := repository.Create(context.Background(), agentsession.Metadata{ID: "session-loop"}); err != nil {
		t.Fatal(err)
	}
	manager, _ := contextmanager.NewManager()
	model := &fakeModel{responses: []llm.Message{
		toolCallingMessage("call-1", json.RawMessage(`{"path":"main.go"}`)),
		toolCallingMessage("call-2", json.RawMessage(`{ "path" : "main.go" }`)),
		toolCallingMessage("call-3", json.RawMessage(`{"path":"main.go"}`)),
	}}
	runtime, _ := NewRuntime(Dependencies{Models: fakeModelFactory{model: model}, Contexts: manager, Sessions: repository, IDs: &sequenceIDs{}})
	executable := &readTool{}
	registry, _ := tool.NewRegistry(executable)
	result, err := runtime.Run(context.Background(), RunRequest{
		SessionID: "session-loop", RunID: "run-loop", UserEntryID: "user-loop", Model: llm.ModelRef{Provider: "test", Model: "model"},
		UserMessage: llm.Message{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentText, Text: "loop"}}}, Tools: registry,
		Limits: RunLimits{MaxSteps: 10, MaxRepeatedToolCalls: 2},
	}, &eventCollector{})
	if err != nil || result.Status != RunLimitReached || result.Reason != "max_repeated_tool_calls" || executable.calls != 2 {
		t.Fatalf("loop result=%#v executions=%d err=%v", result, executable.calls, err)
	}
	snapshot, _ := repository.Load(context.Background(), "session-loop")
	var lastTool *llm.Message
	for _, entry := range snapshot.Entries {
		if entry.Message != nil && entry.Message.Role == llm.RoleTool {
			message := entry.Message.Clone()
			lastTool = &message
		}
	}
	if lastTool == nil || !lastTool.IsError || !strings.Contains(lastTool.Content[0].Text, "max_repeated_tool_calls") {
		t.Fatalf("cancelled tool result = %#v", lastTool)
	}
	if len(agentsession.AnalyzeRecovery(snapshot).PendingRuns) != 0 {
		t.Fatalf("budget stop left a pending run: %#v", agentsession.AnalyzeRecovery(snapshot))
	}
}

func TestRuntimeEnforcesDurableUsageAndOutputBudgets(t *testing.T) {
	tests := []struct {
		name   string
		usage  *llm.Usage
		text   string
		limits RunLimits
		reason string
	}{
		{name: "total tokens", usage: &llm.Usage{TotalTokens: 100, InputTokens: 80, OutputTokens: 20}, text: "done", limits: RunLimits{MaxTotalTokens: 100}, reason: "max_total_tokens"},
		{name: "output tokens", usage: &llm.Usage{TotalTokens: 100, InputTokens: 50, OutputTokens: 50}, text: "done", limits: RunLimits{MaxTotalTokens: 1000, MaxOutputTokens: 50}, reason: "max_output_tokens"},
		{name: "cost", usage: &llm.Usage{TotalTokens: 10, OutputTokens: 2, Cost: 1.5}, text: "done", limits: RunLimits{MaxTotalTokens: 1000, MaxOutputTokens: 1000, MaxCost: 1}, reason: "max_cost"},
		{name: "output bytes", text: strings.Repeat("x", 80), limits: RunLimits{MaxTotalTokens: 1000, MaxOutputTokens: 1000, MaxCost: 10, MaxOutputBytes: 64}, reason: "max_output_bytes"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := agentsession.NewMemoryRepository()
			_ = repository.Create(context.Background(), agentsession.Metadata{ID: "session-budget"})
			manager, _ := contextmanager.NewManager()
			response := llm.Message{Role: llm.RoleAssistant, Provider: "test", Model: "model", StopReason: llm.StopReasonStop, Usage: test.usage, Content: []llm.Content{{Type: llm.ContentText, Text: test.text}}}
			runtime, _ := NewRuntime(Dependencies{Models: fakeModelFactory{model: &fakeModel{responses: []llm.Message{response}}}, Contexts: manager, Sessions: repository, IDs: &sequenceIDs{}})
			result, err := runtime.Run(context.Background(), RunRequest{
				SessionID: "session-budget", RunID: "run-budget", UserEntryID: "user-budget", Model: llm.ModelRef{Provider: "test", Model: "model"},
				UserMessage: llm.Message{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentText, Text: "budget"}}}, Limits: test.limits,
			}, &eventCollector{})
			if err != nil || result.Status != RunLimitReached || result.Reason != test.reason || result.FinalMessage == nil {
				t.Fatalf("budget result=%#v err=%v", result, err)
			}
		})
	}
}

func TestRuntimeChargesSummaryUsageBeforePrimaryModelCall(t *testing.T) {
	repository := agentsession.NewMemoryRepository()
	if err := repository.Create(context.Background(), agentsession.Metadata{ID: "session-summary-budget"}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	manager, err := contextmanager.NewManager(summaryUsageStrategy{usage: llm.Usage{InputTokens: 80, OutputTokens: 20, TotalTokens: 100, Cost: 0.5}})
	if err != nil {
		t.Fatalf("create context manager: %v", err)
	}
	model := &fakeModel{}
	runtime, err := NewRuntime(Dependencies{Models: fakeModelFactory{model: model}, Contexts: manager, Sessions: repository, IDs: &sequenceIDs{}})
	if err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	result, err := runtime.Run(context.Background(), RunRequest{
		SessionID: "session-summary-budget", RunID: "run-summary-budget", UserEntryID: "user-summary-budget",
		Model:       llm.ModelRef{Provider: "test", Model: "model"},
		UserMessage: llm.Message{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentText, Text: "budget"}}},
		Limits:      RunLimits{MaxTotalTokens: 100, MaxOutputTokens: 1000, MaxCost: 10},
	}, &eventCollector{})
	if err != nil {
		t.Fatalf("run agent: %v", err)
	}
	if result.Status != RunLimitReached || result.Reason != "max_total_tokens" || len(model.requests) != 0 {
		t.Fatalf("summary budget result=%#v model requests=%d", result, len(model.requests))
	}
	snapshot, err := repository.Load(context.Background(), "session-summary-budget")
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	var recorded bool
	for _, record := range snapshot.Records {
		if record.Type == agentsession.RecordUsage && record.Usage != nil && record.Usage.TotalTokens == 100 {
			recorded = true
		}
	}
	if !recorded {
		t.Fatalf("summary usage was not durably recorded: %#v", snapshot.Records)
	}
}

func TestRuntimeRecordsSummaryUsageWhenContextBuildFails(t *testing.T) {
	repository := agentsession.NewMemoryRepository()
	if err := repository.Create(context.Background(), agentsession.Metadata{ID: "session-summary-failure"}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	manager, err := contextmanager.NewManager(summaryUsageStrategy{
		usage: llm.Usage{InputTokens: 40, OutputTokens: 10, TotalTokens: 50, Cost: 0.25},
		err:   errors.New("context does not fit after summary"),
	})
	if err != nil {
		t.Fatalf("create context manager: %v", err)
	}
	model := &fakeModel{}
	runtime, err := NewRuntime(Dependencies{Models: fakeModelFactory{model: model}, Contexts: manager, Sessions: repository, IDs: &sequenceIDs{}})
	if err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	result, runErr := runtime.Run(context.Background(), RunRequest{
		SessionID: "session-summary-failure", RunID: "run-summary-failure", UserEntryID: "user-summary-failure",
		Model:       llm.ModelRef{Provider: "test", Model: "model"},
		UserMessage: llm.Message{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentText, Text: "too large"}}},
	}, &eventCollector{})
	if runErr == nil || result.Status != RunFailed || len(model.requests) != 0 {
		t.Fatalf("failed summary result=%#v model requests=%d err=%v", result, len(model.requests), runErr)
	}
	snapshot, err := repository.Load(context.Background(), "session-summary-failure")
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	var recorded bool
	for _, record := range snapshot.Records {
		if record.Type == agentsession.RecordUsage && record.Usage != nil && record.Usage.TotalTokens == 50 {
			recorded = true
		}
	}
	if !recorded {
		t.Fatalf("failed summary usage was not durably recorded: %#v", snapshot.Records)
	}
}

func TestRuntimeEnforcesTotalToolCallBudget(t *testing.T) {
	repository := agentsession.NewMemoryRepository()
	_ = repository.Create(context.Background(), agentsession.Metadata{ID: "session-tools"})
	manager, _ := contextmanager.NewManager()
	model := &fakeModel{responses: []llm.Message{
		toolCallingMessage("call-1", json.RawMessage(`{"path":"one.go"}`)),
		toolCallingMessage("call-2", json.RawMessage(`{"path":"two.go"}`)),
	}}
	runtime, _ := NewRuntime(Dependencies{Models: fakeModelFactory{model: model}, Contexts: manager, Sessions: repository, IDs: &sequenceIDs{}})
	executable := &readTool{}
	registry, _ := tool.NewRegistry(executable)
	result, err := runtime.Run(context.Background(), RunRequest{
		SessionID: "session-tools", RunID: "run-tools", UserEntryID: "user-tools", Model: llm.ModelRef{Provider: "test", Model: "model"},
		UserMessage: llm.Message{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentText, Text: "tools"}}}, Tools: registry, Limits: RunLimits{MaxToolCalls: 1, MaxRepeatedToolCalls: 10},
	}, &eventCollector{})
	if err != nil || result.Status != RunLimitReached || result.Reason != "max_tool_calls" || executable.calls != 1 {
		t.Fatalf("tool budget result=%#v calls=%d err=%v", result, executable.calls, err)
	}
}

func TestRuntimeFinishesCompactionOnlyAfterSummaryIsPersisted(t *testing.T) {
	repository := agentsession.NewMemoryRepository()
	if err := repository.Create(context.Background(), agentsession.Metadata{ID: "session-compaction-events"}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	manager, err := contextmanager.NewManager(eventCompactionStrategy{})
	if err != nil {
		t.Fatalf("create context manager: %v", err)
	}
	model := &fakeModel{responses: []llm.Message{{
		Role: llm.RoleAssistant, Provider: "test", Model: "model", StopReason: llm.StopReasonStop,
		Content: []llm.Content{{Type: llm.ContentText, Text: "done"}},
	}}}
	runtime, err := NewRuntime(Dependencies{
		Models: fakeModelFactory{model: model}, Contexts: manager, Sessions: repository, IDs: &sequenceIDs{},
	})
	if err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	registry, err := tool.NewRegistry(&readTool{})
	if err != nil {
		t.Fatalf("create tools: %v", err)
	}
	events := &compactionEventCollector{repository: repository}
	result, err := runtime.Run(context.Background(), RunRequest{
		SessionID: "session-compaction-events", RunID: "run-compaction-events", UserEntryID: "user-compaction-events",
		Model:       llm.ModelRef{Provider: "test", Model: "model"},
		UserMessage: llm.Message{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentText, Text: "compact"}}},
		Tools:       registry, Limits: RunLimits{MaxSteps: 1},
	}, events)
	if err != nil || result.Status != RunCompleted {
		t.Fatalf("run agent: result=%#v err=%v", result, err)
	}
	started, finished := -1, -1
	for index, event := range events.events {
		switch event.Kind {
		case EventCompactionStarted:
			started = index
		case EventCompactionFinished:
			finished = index
		}
	}
	if started < 0 || finished <= started || !events.finishSawPersisted {
		t.Fatalf("compaction events=%#v finish_saw_persisted=%v", events.events, events.finishSawPersisted)
	}
}

func TestRuntimeDetectsNoProgressRepeatedStepsAndErrorSteps(t *testing.T) {
	tests := []struct {
		name       string
		arguments  []json.RawMessage
		executable tool.Tool
		reason     string
	}{
		{name: "repeated fingerprint", arguments: []json.RawMessage{json.RawMessage(`{"path":"same.go"}`), json.RawMessage(`{"path":"same.go"}`)}, executable: &readTool{}, reason: "no_progress_repeated_step"},
		{name: "consecutive errors", arguments: []json.RawMessage{json.RawMessage(`{"path":"one.go"}`), json.RawMessage(`{"path":"two.go"}`)}, executable: &failingReadTool{}, reason: "no_progress_tool_errors"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := agentsession.NewMemoryRepository()
			_ = repository.Create(context.Background(), agentsession.Metadata{ID: "session-progress"})
			manager, _ := contextmanager.NewManager()
			model := &fakeModel{responses: []llm.Message{toolCallingMessage("call-1", test.arguments[0]), toolCallingMessage("call-2", test.arguments[1])}}
			runtime, _ := NewRuntime(Dependencies{Models: fakeModelFactory{model: model}, Contexts: manager, Sessions: repository, IDs: &sequenceIDs{}})
			registry, _ := tool.NewRegistry(test.executable)
			result, err := runtime.Run(context.Background(), RunRequest{
				SessionID: "session-progress", RunID: "run-progress", UserEntryID: "user-progress", Model: llm.ModelRef{Provider: "test", Model: "model"},
				UserMessage: llm.Message{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentText, Text: "progress"}}}, Tools: registry,
				Limits: RunLimits{MaxSteps: 10, MaxRepeatedToolCalls: 10, MaxNoProgressSteps: 2},
			}, &eventCollector{})
			if err != nil || result.Status != RunLimitReached || result.Reason != test.reason || result.Steps != 2 || len(model.requests) != 2 {
				t.Fatalf("progress result=%#v model requests=%d err=%v", result, len(model.requests), err)
			}
		})
	}
}

func toolCallingMessage(callID string, arguments json.RawMessage) llm.Message {
	return llm.Message{Role: llm.RoleAssistant, Provider: "test", Model: "model", StopReason: llm.StopReasonToolUse, Content: []llm.Content{{Type: llm.ContentToolCall, ToolCall: &llm.ToolCall{ID: callID, Name: "read_file", Arguments: arguments}}}}
}

func TestRuntimeCentrallyPersistsToolActivity(t *testing.T) {
	repository := agentsession.NewMemoryRepository()
	if err := repository.Create(context.Background(), agentsession.Metadata{ID: "session-1"}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	manager, err := contextmanager.NewManager()
	if err != nil {
		t.Fatalf("create context manager: %v", err)
	}
	model := &fakeModel{responses: []llm.Message{
		{Role: llm.RoleAssistant, Provider: "test", Model: "model", StopReason: llm.StopReasonToolUse, Content: []llm.Content{{Type: llm.ContentToolCall, ToolCall: &llm.ToolCall{ID: "call-1", Name: "read_file", Arguments: json.RawMessage(`{"path":"main.go"}`)}}}},
		{Role: llm.RoleAssistant, Provider: "test", Model: "model", StopReason: llm.StopReasonStop, Usage: &llm.Usage{InputTokens: 20, OutputTokens: 5, TotalTokens: 25}, Content: []llm.Content{{Type: llm.ContentThinking, Text: "checking"}, {Type: llm.ContentText, Text: "done"}}},
	}}
	runtime, err := NewRuntime(Dependencies{Models: fakeModelFactory{model: model}, Contexts: manager, Sessions: repository, IDs: &sequenceIDs{}})
	if err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	executable := &readTool{}
	registry, err := tool.NewRegistry(executable)
	if err != nil {
		t.Fatalf("create registry: %v", err)
	}
	events := &eventCollector{}
	result, err := runtime.Run(context.Background(), RunRequest{
		SessionID: "session-1", RunID: "run-1", UserEntryID: "user-1", SystemPrompt: "system",
		Model:       llm.ModelRef{Provider: "test", Model: "model"},
		UserMessage: llm.Message{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentText, Text: "inspect"}}},
		Tools:       registry, Limits: RunLimits{MaxSteps: 4},
	}, events)
	if err != nil {
		t.Fatalf("run agent: %v", err)
	}
	if result.Status != RunCompleted || result.Steps != 2 || executable.calls != 1 {
		t.Fatalf("result = %#v, tool calls = %d", result, executable.calls)
	}
	snapshot, err := repository.Load(context.Background(), "session-1")
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	var roles []llm.Role
	var toolStarted, toolFinished, usageRecorded bool
	for _, entry := range snapshot.Entries {
		if entry.Message != nil {
			roles = append(roles, entry.Message.Role)
		}
	}
	for _, record := range snapshot.Records {
		toolStarted = toolStarted || record.Type == agentsession.RecordToolStarted
		toolFinished = toolFinished || record.Type == agentsession.RecordToolFinished
		usageRecorded = usageRecorded || record.Type == agentsession.RecordUsage
	}
	wantRoles := []llm.Role{llm.RoleUser, llm.RoleAssistant, llm.RoleTool, llm.RoleAssistant}
	if len(roles) != len(wantRoles) {
		t.Fatalf("message roles = %#v", roles)
	}
	for index := range roles {
		if roles[index] != wantRoles[index] {
			t.Fatalf("message roles = %#v", roles)
		}
	}
	if !toolStarted || !toolFinished || !usageRecorded {
		t.Fatalf("tool records missing: %#v", snapshot.Records)
	}
	if len(model.requests) != 2 || len(model.requests[1].Messages) != 3 || model.requests[1].Messages[2].Role != llm.RoleTool {
		t.Fatalf("second model request = %#v", model.requests)
	}
	var sawToolStart, sawToolFinish, sawThinkingStart, sawThinkingFinish bool
	for _, event := range events.events {
		sawToolStart = sawToolStart || event.Kind == EventToolStarted
		sawToolFinish = sawToolFinish || event.Kind == EventToolFinished
		if event.Kind == EventAssistantThinkingChanged && event.Assistant != nil {
			sawThinkingStart = sawThinkingStart || event.Assistant.ThinkingActive
			sawThinkingFinish = sawThinkingFinish || !event.Assistant.ThinkingActive
		}
	}
	if !sawToolStart || !sawToolFinish || !sawThinkingStart || !sawThinkingFinish {
		t.Fatalf("agent events = %#v", events.events)
	}
}

func TestLatestBranchCompactionRestoresAuthoritativeSummaryAndFacts(t *testing.T) {
	digest := strings.Repeat("a", 64)
	entries := []agentsession.Entry{
		{ID: "u1", Sequence: 1, Type: agentsession.EntryMessage, Message: &llm.Message{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentText, Text: "inspect"}}}},
		{ID: "tool", Sequence: 2, ParentID: "u1", Type: agentsession.EntryMessage, Message: &llm.Message{Role: llm.RoleTool, ToolCallID: "call", ToolName: "read_file", Content: []llm.Content{{Type: llm.ContentText, Text: "stored sha256:" + digest}}}},
		{ID: "summary", Sequence: 3, ParentID: "tool", Type: agentsession.EntryCompaction, Compaction: &agentsession.Compaction{
			Summary: "read_file stored sha256:" + digest, CoversFromEntryID: "u1", CoversToEntryID: "tool", SourceDigest: "source", Strategy: "rolling-summary", StrategyVersion: "v4",
		}},
		{ID: "current", Sequence: 4, ParentID: "summary", Type: agentsession.EntryMessage, Message: &llm.Message{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentText, Text: "continue"}}}},
	}
	summary, coveredThrough := latestBranchCompaction(entries)
	if summary == nil || coveredThrough != 1 || summary.CoversFromEntryID != "u1" || summary.CoversToEntryID != "tool" {
		t.Fatalf("summary=%#v coveredThrough=%d", summary, coveredThrough)
	}
	if len(summary.Facts) != 2 {
		t.Fatalf("facts were not recovered from authoritative source entries: %#v", summary.Facts)
	}
}

func TestRuntimeDataPolicyProtectsJournalContextEventsAndSplitStreams(t *testing.T) {
	repository := agentsession.NewMemoryRepository()
	if err := repository.Create(context.Background(), agentsession.Metadata{ID: "session-policy"}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	manager, err := contextmanager.NewManager()
	if err != nil {
		t.Fatalf("create context manager: %v", err)
	}
	toolResponse := llm.Message{
		Role: llm.RoleAssistant, Provider: "test", Model: "model", StopReason: llm.StopReasonToolUse,
		Content: []llm.Content{{Type: llm.ContentToolCall, ToolCall: &llm.ToolCall{ID: "call-secret", Name: "secret_tool", Arguments: json.RawMessage(`{"token":"top-secret"}`)}}},
	}
	finalResponse := llm.Message{
		Role: llm.RoleAssistant, Provider: "test", Model: "model", StopReason: llm.StopReasonStop,
		Content: []llm.Content{{Type: llm.ContentText, Text: "TOKEN=top-secret"}},
	}
	model := &scriptedStreamModel{streams: [][]llm.StreamEvent{
		{{Kind: llm.StreamResponseFinished, Message: &toolResponse}},
		{
			{Kind: llm.StreamTextDelta, Delta: "TOKEN=top-"},
			{Kind: llm.StreamTextDelta, Delta: "secret"},
			{Kind: llm.StreamResponseFinished, Message: &finalResponse},
		},
	}}
	runtime, err := NewRuntime(Dependencies{
		Models: scriptedStreamFactory{model: model}, Contexts: manager, Sessions: repository, IDs: &sequenceIDs{}, DataPolicy: testRedactionPolicy{},
	})
	if err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	executable := &secretResultTool{}
	registry, err := tool.NewRegistry(executable)
	if err != nil {
		t.Fatalf("create registry: %v", err)
	}
	events := &eventCollector{}
	result, err := runtime.Run(context.Background(), RunRequest{
		SessionID: "session-policy", RunID: "run-policy", UserEntryID: "user-policy", Model: llm.ModelRef{Provider: "test", Model: "model"},
		UserMessage:      llm.Message{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentText, Text: "inspect top-secret"}}},
		UntrustedContext: []llm.Message{{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentText, Text: "repository-guidance top-secret"}}}},
		Tools:            registry, Limits: RunLimits{MaxSteps: 4},
	}, events)
	if err != nil || result.Status != RunCompleted {
		t.Fatalf("run agent: result=%#v err=%v", result, err)
	}
	if !strings.Contains(string(executable.arguments), "top-secret") {
		t.Fatalf("tool did not receive original model arguments: %s", executable.arguments)
	}
	snapshot, err := repository.Load(context.Background(), "session-policy")
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	assertNoSecret := func(name string, value any) {
		t.Helper()
		encoded, encodeErr := json.Marshal(value)
		if encodeErr != nil {
			t.Fatalf("encode %s: %v", name, encodeErr)
		}
		if strings.Contains(string(encoded), "top-secret") || !strings.Contains(string(encoded), "[safe]") {
			t.Fatalf("%s crossed the data-policy boundary unsafely: %s", name, encoded)
		}
	}
	assertNoSecret("journal", snapshot)
	assertNoSecret("model requests", model.requests)
	assertNoSecret("agent events", events.events)
	assertNoSecret("run result", result)
	encodedSnapshot, _ := json.Marshal(snapshot)
	encodedRequests, _ := json.Marshal(model.requests)
	if strings.Contains(string(encodedSnapshot), "repository-guidance") || !strings.Contains(string(encodedRequests), "repository-guidance [safe]") {
		t.Fatalf("untrusted context persistence boundary: snapshot=%s requests=%s", encodedSnapshot, encodedRequests)
	}
}

func TestRuntimeDataPolicyRedactsModelFailureDiagnostics(t *testing.T) {
	repository := agentsession.NewMemoryRepository()
	_ = repository.Create(context.Background(), agentsession.Metadata{ID: "session-policy-error"})
	manager, _ := contextmanager.NewManager()
	model := &scriptedStreamModel{streams: [][]llm.StreamEvent{{{
		Kind: llm.StreamResponseFailed, ErrorCode: "invalid_request", ErrorMessage: "provider exposed top-secret",
	}}}}
	runtime, _ := NewRuntime(Dependencies{
		Models: scriptedStreamFactory{model: model}, Contexts: manager, Sessions: repository, IDs: &sequenceIDs{}, DataPolicy: testRedactionPolicy{},
	})
	events := &eventCollector{}
	_, err := runtime.Run(context.Background(), RunRequest{
		SessionID: "session-policy-error", RunID: "run-policy-error", UserEntryID: "user-policy-error", Model: llm.ModelRef{Provider: "test", Model: "model"},
		UserMessage: llm.Message{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentText, Text: "fail"}}}, Limits: RunLimits{MaxModelAttempts: 1},
	}, events)
	if err == nil || strings.Contains(err.Error(), "top-secret") || !strings.Contains(err.Error(), "[safe]") {
		t.Fatalf("unsafe returned error: %v", err)
	}
	snapshot, _ := repository.Load(context.Background(), "session-policy-error")
	for name, value := range map[string]any{"journal": snapshot, "events": events.events} {
		encoded, encodeErr := json.Marshal(value)
		if encodeErr != nil || strings.Contains(string(encoded), "top-secret") {
			t.Fatalf("unsafe %s diagnostics: %s (encode err=%v)", name, encoded, encodeErr)
		}
		if name == "journal" && !strings.Contains(string(encoded), "[safe]") {
			t.Fatalf("journal lost the safe failure diagnostic: %s", encoded)
		}
	}
}

func TestRuntimePersistsInterruptAndResumesSameRun(t *testing.T) {
	repository := agentsession.NewMemoryRepository()
	if err := repository.Create(context.Background(), agentsession.Metadata{ID: "session-resume"}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	manager, err := contextmanager.NewManager()
	if err != nil {
		t.Fatalf("create context manager: %v", err)
	}
	model := &fakeModel{responses: []llm.Message{
		{Role: llm.RoleAssistant, Provider: "test", Model: "model", StopReason: llm.StopReasonToolUse, Content: []llm.Content{{Type: llm.ContentToolCall, ToolCall: &llm.ToolCall{ID: "call-approval", Name: "request_approval", Arguments: json.RawMessage(`{"path":"main.go"}`)}}}},
		{Role: llm.RoleAssistant, Provider: "test", Model: "model", StopReason: llm.StopReasonStop, Content: []llm.Content{{Type: llm.ContentText, Text: "approved and done"}}},
	}}
	runtime, err := NewRuntime(Dependencies{Models: fakeModelFactory{model: model}, Contexts: manager, Sessions: repository, IDs: &sequenceIDs{}})
	if err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	executable := &interruptTool{}
	registry, err := tool.NewRegistry(executable)
	if err != nil {
		t.Fatalf("create registry: %v", err)
	}
	request := RunRequest{
		SessionID: "session-resume", RunID: "run-resume", UserEntryID: "user-resume", SystemPrompt: "system",
		Model:       llm.ModelRef{Provider: "test", Model: "model"},
		UserMessage: llm.Message{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentText, Text: "edit"}}},
		Tools:       registry, Limits: RunLimits{MaxSteps: 4},
	}
	first, err := runtime.Run(context.Background(), request, &eventCollector{})
	if err != nil {
		t.Fatalf("run agent: %v", err)
	}
	if first.Status != RunInterrupted || first.Interrupt == nil || first.Interrupt.ID != "approval-1" || executable.calls != 1 {
		t.Fatalf("interrupted result = %#v, calls = %d", first, executable.calls)
	}
	interruptedSnapshot, err := repository.Load(context.Background(), request.SessionID)
	if err != nil {
		t.Fatalf("load interrupted session: %v", err)
	}
	recovery := agentsession.AnalyzeRecovery(interruptedSnapshot)
	if len(recovery.PendingRuns) != 1 || len(recovery.PendingTools) != 1 || len(recovery.PendingInterrupts) != 1 {
		t.Fatalf("recovery state = %#v", recovery)
	}
	for _, entry := range interruptedSnapshot.Entries {
		if entry.Message != nil && entry.Message.Role == llm.RoleTool {
			t.Fatal("an interrupted tool must not persist a fabricated tool result")
		}
	}
	events := &eventCollector{}
	resumed, err := runtime.Resume(context.Background(), ResumeRequest{
		SessionID: request.SessionID, RunID: request.RunID, InterruptID: "approval-1", SystemPrompt: request.SystemPrompt,
		Model: request.Model, Tools: registry, Limits: request.Limits,
		Resolution: tool.Result{Status: tool.ResultCompleted, Content: []llm.Content{{Type: llm.ContentText, Text: "approved"}}, Details: json.RawMessage(`{"decision":"allow"}`)},
	}, events)
	if err != nil {
		t.Fatalf("resume agent: %v", err)
	}
	if resumed.Status != RunCompleted || resumed.Steps != 2 || executable.calls != 1 || executable.resumes != 1 {
		t.Fatalf("resumed result = %#v, calls = %d, resumes = %d", resumed, executable.calls, executable.resumes)
	}
	finalSnapshot, err := repository.Load(context.Background(), request.SessionID)
	if err != nil {
		t.Fatalf("load resumed session: %v", err)
	}
	finalRecovery := agentsession.AnalyzeRecovery(finalSnapshot)
	if len(finalRecovery.PendingRuns) != 0 || len(finalRecovery.PendingTools) != 0 || len(finalRecovery.PendingInterrupts) != 0 {
		t.Fatalf("final recovery state = %#v", finalRecovery)
	}
	var roles []llm.Role
	for _, entry := range finalSnapshot.Entries {
		if entry.Message != nil {
			roles = append(roles, entry.Message.Role)
		}
	}
	want := []llm.Role{llm.RoleUser, llm.RoleAssistant, llm.RoleTool, llm.RoleAssistant}
	if len(roles) != len(want) {
		t.Fatalf("roles = %#v", roles)
	}
	for index := range want {
		if roles[index] != want[index] {
			t.Fatalf("roles = %#v", roles)
		}
	}
	var sawResumed bool
	for _, event := range events.events {
		sawResumed = sawResumed || event.Kind == EventRunResumed
	}
	if !sawResumed {
		t.Fatalf("resume events = %#v", events.events)
	}
}

func TestRuntimeAutomaticallyReplaysSafeToolThenWaitsBeforeContinuingModel(t *testing.T) {
	repository, runtime, registry, executable := recoveryRuntime(t, &readTool{}, []llm.Message{{
		Role: llm.RoleAssistant, Provider: "test", Model: "model", StopReason: llm.StopReasonStop,
		Content: []llm.Content{{Type: llm.ContentText, Text: "recovered"}},
	}})
	seedPendingToolRun(t, repository, "read_file", "safe", "run-safe:call-safe", false)
	plan := mustRecoveryPlan(t, repository)
	if len(plan.Actions) != 1 || plan.Actions[0].Kind != agentsession.RecoveryRetryTool || !plan.Actions[0].Automatic {
		t.Fatalf("initial plan = %#v", plan)
	}
	result, err := runtime.Recover(context.Background(), RecoverRequest{
		SessionID: "session-recovery", RunID: "run-recovery", ActionID: plan.Actions[0].ID,
		Automatic: true, ContinueRun: false, SystemPrompt: "system", Model: llm.ModelRef{Provider: "test", Model: "model"}, Tools: registry,
	}, &eventCollector{})
	if err != nil {
		t.Fatalf("recover safe Tool: %v", err)
	}
	if result.Status != RunInterrupted || result.Reason != "recovery_checkpoint" || executable.(*readTool).calls != 1 {
		t.Fatalf("checkpoint result = %#v, Tool calls = %d", result, executable.(*readTool).calls)
	}
	plan = mustRecoveryPlan(t, repository)
	if len(plan.Actions) != 1 || plan.Actions[0].Kind != agentsession.RecoveryContinueRun || plan.Actions[0].Automatic {
		t.Fatalf("post-Tool plan = %#v", plan)
	}
	result, err = runtime.Recover(context.Background(), RecoverRequest{
		SessionID: "session-recovery", RunID: "run-recovery", ActionID: plan.Actions[0].ID,
		Decision: agentsession.RecoveryRetry, ContinueRun: true, SystemPrompt: "system",
		Model: llm.ModelRef{Provider: "test", Model: "model"}, Tools: registry,
	}, &eventCollector{})
	if err != nil {
		t.Fatalf("continue recovered run: %v", err)
	}
	if result.Status != RunCompleted || result.Steps != 2 || len(mustRecoveryPlan(t, repository).Actions) != 0 {
		t.Fatalf("continued result = %#v, final plan = %#v", result, mustRecoveryPlan(t, repository))
	}
}

func TestRuntimeIdempotentRecoveryReusesExactDurableKey(t *testing.T) {
	executable := &idempotentTool{}
	repository, runtime, registry, _ := recoveryRuntime(t, executable, nil)
	seedPendingToolRun(t, repository, "idempotent_write", "idempotent", "original-idempotency-key", false)
	plan := mustRecoveryPlan(t, repository)
	_, err := runtime.Recover(context.Background(), RecoverRequest{
		SessionID: "session-recovery", RunID: "run-recovery", ActionID: plan.Actions[0].ID,
		Automatic: true, SystemPrompt: "system", Model: llm.ModelRef{Provider: "test", Model: "model"}, Tools: registry,
	}, &eventCollector{})
	if err != nil {
		t.Fatalf("recover idempotent Tool: %v", err)
	}
	if executable.calls != 1 || len(executable.keys) != 1 || executable.keys[0] != "original-idempotency-key" {
		t.Fatalf("idempotent calls = %d, keys = %#v", executable.calls, executable.keys)
	}
}

func TestRuntimeReconcilesDurableToolResultWithoutReexecution(t *testing.T) {
	repository, runtime, registry, executable := recoveryRuntime(t, &readTool{}, nil)
	seedPendingToolRun(t, repository, "read_file", "safe", "run-recovery:call-recovery", true)
	plan := mustRecoveryPlan(t, repository)
	if plan.Actions[0].Kind != agentsession.RecoveryReconcileTool {
		t.Fatalf("plan = %#v", plan)
	}
	_, err := runtime.Recover(context.Background(), RecoverRequest{
		SessionID: "session-recovery", RunID: "run-recovery", ActionID: plan.Actions[0].ID,
		Automatic: true, SystemPrompt: "system", Model: llm.ModelRef{Provider: "test", Model: "model"}, Tools: registry,
	}, &eventCollector{})
	if err != nil {
		t.Fatalf("reconcile Tool result: %v", err)
	}
	if executable.(*readTool).calls != 0 {
		t.Fatalf("reconciliation re-executed Tool %d times", executable.(*readTool).calls)
	}
	plan = mustRecoveryPlan(t, repository)
	if len(plan.Actions) != 1 || plan.Actions[0].Kind != agentsession.RecoveryContinueRun {
		t.Fatalf("reconciled plan = %#v", plan)
	}
}

func TestRuntimeNeverReplayRequiresExplicitDecisionAndCanAbandonTurn(t *testing.T) {
	repository, runtime, registry, _ := recoveryRuntime(t, &interruptTool{}, nil)
	seedPendingToolRun(t, repository, "request_approval", "never", "run-recovery:call-recovery", false)
	plan := mustRecoveryPlan(t, repository)
	if plan.Actions[0].Kind != agentsession.RecoveryDecideTool || plan.Actions[0].Automatic {
		t.Fatalf("plan = %#v", plan)
	}
	if _, err := runtime.Recover(context.Background(), RecoverRequest{
		SessionID: "session-recovery", RunID: "run-recovery", ActionID: plan.Actions[0].ID,
		Automatic: true, SystemPrompt: "system", Model: llm.ModelRef{Provider: "test", Model: "model"}, Tools: registry,
	}, &eventCollector{}); err == nil {
		t.Fatal("ReplayNever action was automatically executed")
	}
	result, err := runtime.Recover(context.Background(), RecoverRequest{
		SessionID: "session-recovery", RunID: "run-recovery", ActionID: plan.Actions[0].ID,
		Decision: agentsession.RecoveryAbandonRun, SystemPrompt: "system",
		Model: llm.ModelRef{Provider: "test", Model: "model"}, Tools: registry,
	}, &eventCollector{})
	if err != nil || result.Status != RunAborted {
		t.Fatalf("abandon result = %#v, err = %v", result, err)
	}
	if len(mustRecoveryPlan(t, repository).Actions) != 0 {
		t.Fatalf("abandoned plan = %#v", mustRecoveryPlan(t, repository))
	}
}

func recoveryRuntime(t *testing.T, executable tool.Tool, responses []llm.Message) (*agentsession.MemoryRepository, *Runtime, *tool.Registry, tool.Tool) {
	t.Helper()
	repository := agentsession.NewMemoryRepository()
	if err := repository.Create(context.Background(), agentsession.Metadata{ID: "session-recovery"}); err != nil {
		t.Fatalf("create recovery session: %v", err)
	}
	manager, err := contextmanager.NewManager()
	if err != nil {
		t.Fatalf("create context manager: %v", err)
	}
	runtime, err := NewRuntime(Dependencies{
		Models: fakeModelFactory{model: &fakeModel{responses: responses}}, Contexts: manager, Sessions: repository, IDs: &sequenceIDs{},
	})
	if err != nil {
		t.Fatalf("create recovery runtime: %v", err)
	}
	registry, err := tool.NewRegistry(executable)
	if err != nil {
		t.Fatalf("create recovery registry: %v", err)
	}
	return repository, runtime, registry, executable
}

func seedPendingToolRun(t *testing.T, repository agentsession.Repository, toolName, replayPolicy, idempotencyKey string, resultEntry bool) {
	t.Helper()
	ctx := context.Background()
	appendRecoveryRecord(t, repository, agentsession.Record{ID: "record-operation", Type: agentsession.RecordOperationStarted, RunID: "run-recovery", Operation: &agentsession.OperationData{Intent: agentsession.OperationRun}})
	appendRecoveryEntry(t, repository, agentsession.Entry{ID: "user-recovery", RunID: "run-recovery", Type: agentsession.EntryMessage, Message: &llm.Message{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentText, Text: "recover"}}}})
	appendRecoveryRecord(t, repository, agentsession.Record{ID: "record-step-start", Type: agentsession.RecordStepStarted, RunID: "run-recovery", Step: &agentsession.StepData{Attempt: 1}})
	appendRecoveryEntry(t, repository, agentsession.Entry{ID: "assistant-recovery", RunID: "run-recovery", Type: agentsession.EntryMessage, Message: &llm.Message{
		Role: llm.RoleAssistant, StopReason: llm.StopReasonToolUse,
		Content: []llm.Content{{Type: llm.ContentToolCall, ToolCall: &llm.ToolCall{ID: "call-recovery", Name: toolName, Arguments: json.RawMessage(`{"value":1}`)}}},
	}})
	appendRecoveryRecord(t, repository, agentsession.Record{ID: "record-step-finish", Type: agentsession.RecordStepFinished, RunID: "run-recovery", Step: &agentsession.StepData{Attempt: 1, AssistantEntryID: "assistant-recovery", StopReason: string(llm.StopReasonToolUse)}})
	appendRecoveryRecord(t, repository, agentsession.Record{ID: "record-tool-start", Type: agentsession.RecordToolStarted, RunID: "run-recovery", Tool: &agentsession.ToolData{
		AssistantEntryID: "assistant-recovery", ToolCallID: "call-recovery", ToolName: toolName,
		EffectiveArgs: json.RawMessage(`{"value":1}`), IdempotencyKey: idempotencyKey,
		ResultEntryID: "result-recovery", ReplayPolicy: replayPolicy,
	}})
	if resultEntry {
		appendRecoveryEntry(t, repository, agentsession.Entry{ID: "result-recovery", RunID: "run-recovery", Type: agentsession.EntryMessage, Message: &llm.Message{
			Role: llm.RoleTool, ToolCallID: "call-recovery", ToolName: toolName, Content: []llm.Content{{Type: llm.ContentText, Text: "already durable"}},
		}})
	}
	_ = ctx
}

func appendRecoveryEntry(t *testing.T, repository agentsession.Repository, entry agentsession.Entry) {
	t.Helper()
	if _, err := repository.AppendEntry(context.Background(), "session-recovery", agentsession.MainLane, entry); err != nil {
		t.Fatalf("append recovery entry %q: %v", entry.ID, err)
	}
}

func appendRecoveryRecord(t *testing.T, repository agentsession.Repository, record agentsession.Record) {
	t.Helper()
	if _, err := repository.AppendRecord(context.Background(), "session-recovery", agentsession.MainLane, record); err != nil {
		t.Fatalf("append recovery record %q: %v", record.ID, err)
	}
}

func TestRuntimeRejectsExclusiveControlMixedWithOtherCalls(t *testing.T) {
	repository := agentsession.NewMemoryRepository()
	if err := repository.Create(context.Background(), agentsession.Metadata{ID: "session-exclusive"}); err != nil {
		t.Fatal(err)
	}
	manager, _ := contextmanager.NewManager()
	model := &fakeModel{responses: []llm.Message{{Role: llm.RoleAssistant, StopReason: llm.StopReasonToolUse, Content: []llm.Content{
		{Type: llm.ContentToolCall, ToolCall: &llm.ToolCall{ID: "control", Name: "handoff", Arguments: json.RawMessage(`{}`)}},
		{Type: llm.ContentToolCall, ToolCall: &llm.ToolCall{ID: "read", Name: "read_file", Arguments: json.RawMessage(`{}`)}},
	}}}}
	handoff := &handoffTool{}
	read := &readTool{}
	registry, _ := tool.NewRegistry(handoff, read)
	runtime, _ := NewRuntime(Dependencies{Models: fakeModelFactory{model: model}, Contexts: manager, Sessions: repository, IDs: &sequenceIDs{}})
	result, err := runtime.Run(context.Background(), RunRequest{SessionID: "session-exclusive", RunID: "run-exclusive", UserEntryID: "user-exclusive", Model: llm.ModelRef{Provider: "test", Model: "model"}, UserMessage: llm.Message{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentText, Text: "control"}}}, Tools: registry}, &eventCollector{})
	if err == nil || result.Status != RunFailed || handoff.resumes != 0 || read.calls != 0 {
		t.Fatalf("mixed control result=%#v err=%v handoff=%d read=%d", result, err, handoff.resumes, read.calls)
	}
}

func TestRuntimeControlResolutionHandsOffWithoutAnotherModelStep(t *testing.T) {
	repository := agentsession.NewMemoryRepository()
	if err := repository.Create(context.Background(), agentsession.Metadata{ID: "session-handoff"}); err != nil {
		t.Fatal(err)
	}
	manager, _ := contextmanager.NewManager()
	model := &fakeModel{responses: []llm.Message{{Role: llm.RoleAssistant, StopReason: llm.StopReasonToolUse, Content: []llm.Content{{Type: llm.ContentToolCall, ToolCall: &llm.ToolCall{ID: "control", Name: "handoff", Arguments: json.RawMessage(`{}`)}}}}}}
	handoff := &handoffTool{}
	registry, _ := tool.NewRegistry(handoff)
	runtime, _ := NewRuntime(Dependencies{Models: fakeModelFactory{model: model}, Contexts: manager, Sessions: repository, IDs: &sequenceIDs{}})
	first, err := runtime.Run(context.Background(), RunRequest{SessionID: "session-handoff", RunID: "run-handoff", UserEntryID: "user-handoff", Model: llm.ModelRef{Provider: "test", Model: "model"}, UserMessage: llm.Message{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentText, Text: "control"}}}, Tools: registry}, &eventCollector{})
	if err != nil || first.Status != RunInterrupted {
		t.Fatalf("initial handoff result=%#v err=%v", first, err)
	}
	resumed, err := runtime.Resume(context.Background(), ResumeRequest{SessionID: "session-handoff", RunID: "run-handoff", InterruptID: "handoff-1", Resolution: tool.Result{Status: tool.ResultCompleted, Content: []llm.Content{{Type: llm.ContentText, Text: "approved"}}}, Model: llm.ModelRef{Provider: "test", Model: "model"}, Tools: registry}, &eventCollector{})
	if err != nil || resumed.Status != RunHandedOff || handoff.resumes != 1 || len(model.requests) != 1 {
		t.Fatalf("resumed handoff result=%#v err=%v resumes=%d model_requests=%d", resumed, err, handoff.resumes, len(model.requests))
	}
}

func TestRuntimeContinueDoesNotAppendSyntheticUserMessage(t *testing.T) {
	repository := agentsession.NewMemoryRepository()
	if err := repository.Create(context.Background(), agentsession.Metadata{ID: "session-continue"}); err != nil {
		t.Fatal(err)
	}
	manager, _ := contextmanager.NewManager()
	model := &fakeModel{responses: []llm.Message{
		{Role: llm.RoleAssistant, StopReason: llm.StopReasonStop, Content: []llm.Content{{Type: llm.ContentText, Text: "first"}}},
		{Role: llm.RoleAssistant, StopReason: llm.StopReasonStop, Content: []llm.Content{{Type: llm.ContentText, Text: "second"}}},
	}}
	runtime, _ := NewRuntime(Dependencies{Models: fakeModelFactory{model: model}, Contexts: manager, Sessions: repository, IDs: &sequenceIDs{}})
	_, err := runtime.Run(context.Background(), RunRequest{SessionID: "session-continue", RunID: "run-1", UserEntryID: "user-1", Model: llm.ModelRef{Provider: "test", Model: "model"}, UserMessage: llm.Message{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentText, Text: "request"}}}}, &eventCollector{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.Continue(context.Background(), ContinueRequest{SessionID: "session-continue", RunID: "run-2", Model: llm.ModelRef{Provider: "test", Model: "model"}}, &eventCollector{})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, _ := repository.Load(context.Background(), "session-continue")
	var users int
	for _, entry := range snapshot.Entries {
		if entry.Message != nil && entry.Message.Role == llm.RoleUser {
			users++
		}
	}
	if users != 1 {
		t.Fatalf("synthetic user messages = %d, entries=%#v", users, snapshot.Entries)
	}
}

func mustRecoveryPlan(t *testing.T, repository agentsession.Repository) agentsession.RecoveryPlan {
	t.Helper()
	snapshot, err := repository.Load(context.Background(), "session-recovery")
	if err != nil {
		t.Fatalf("load recovery plan: %v", err)
	}
	return agentsession.BuildRecoveryPlan(snapshot)
}
