package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/eaglc/codepilot/internal/provider"
	"github.com/eaglc/codepilot/internal/tool"
)

func TestEinoInvokerRunsRegisteredToolAndStreamsFinalText(t *testing.T) {
	t.Parallel()
	fixture := newEinoInvokerFixture(t)
	fixture.source.interrupt = false

	result, err := fixture.invoker.Invoke(context.Background(), fixture.input, fixture.sink)
	if err != nil {
		t.Fatalf("Invoke() error = %v", err)
	}
	if result.Status != InvocationCompleted || result.FinalText != "done" || result.Steps != 2 {
		t.Fatalf("Invoke() result = %+v", result)
	}
	if fixture.source.callCount() != 1 {
		t.Fatalf("tool calls = %d, want 1", fixture.source.callCount())
	}
	if !hasInvocationEvent(fixture.sink.events, InvocationEventToolStarted, "") ||
		!hasInvocationEvent(fixture.sink.events, InvocationEventToolFinished, tool.ResultCompleted) ||
		!hasInvocationEvent(fixture.sink.events, InvocationEventAssistantText, "") {
		t.Fatalf("events = %+v", fixture.sink.events)
	}
	if _, exists, err := fixture.store.Get(context.Background(), fixture.input.CheckpointID); err != nil || exists {
		t.Fatalf("terminal checkpoint exists = %v, error = %v", exists, err)
	}
}

func TestEinoInvokerInterruptsAndResumesApprovedTool(t *testing.T) {
	t.Parallel()
	fixture := newEinoInvokerFixture(t)

	interrupted, err := fixture.invoker.Invoke(context.Background(), fixture.input, fixture.sink)
	if err != nil {
		t.Fatalf("Invoke() error = %v", err)
	}
	if interrupted.Status != InvocationInterrupted || interrupted.Interrupt == nil {
		t.Fatalf("Invoke() result = %+v", interrupted)
	}
	if interrupted.Interrupt.ID == "approval-request-1" || interrupted.Interrupt.Kind != "approval" {
		t.Fatalf("interrupt = %+v", interrupted.Interrupt)
	}
	if !strings.Contains(string(interrupted.Interrupt.Payload), "approval-request-1") {
		t.Fatalf("interrupt payload = %s", interrupted.Interrupt.Payload)
	}
	if fixture.source.callCount() != 1 {
		t.Fatalf("tool calls before resume = %d", fixture.source.callCount())
	}
	if _, exists, err := fixture.store.Get(context.Background(), fixture.input.CheckpointID); err != nil || !exists {
		t.Fatalf("interrupted checkpoint exists = %v, error = %v", exists, err)
	}

	resumeSink := &recordingInvocationSink{}
	completed, err := fixture.invoker.Resume(context.Background(), ResumeInput{
		CheckpointID: fixture.input.CheckpointID,
		InterruptID:  interrupted.Interrupt.ID,
		Response:     InterruptResponse{Kind: InterruptApproved},
	}, resumeSink)
	if err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	if completed.Status != InvocationCompleted || completed.FinalText != "done" || completed.Steps != 2 {
		t.Fatalf("Resume() result = %+v", completed)
	}
	if fixture.source.callCount() != 2 {
		t.Fatalf("tool calls after resume = %d, want 2", fixture.source.callCount())
	}
	if !hasInvocationEvent(resumeSink.events, InvocationEventToolFinished, tool.ResultCompleted) {
		t.Fatalf("resume events = %+v", resumeSink.events)
	}
	if _, exists, err := fixture.store.Get(context.Background(), fixture.input.CheckpointID); err != nil || exists {
		t.Fatalf("completed checkpoint exists = %v, error = %v", exists, err)
	}
}

func TestEinoInvokerRejectsInterruptedToolWithoutReexecution(t *testing.T) {
	t.Parallel()
	fixture := newEinoInvokerFixture(t)
	interrupted, err := fixture.invoker.Invoke(context.Background(), fixture.input, fixture.sink)
	if err != nil {
		t.Fatalf("Invoke() error = %v", err)
	}

	resumeSink := &recordingInvocationSink{}
	completed, err := fixture.invoker.Resume(context.Background(), ResumeInput{
		CheckpointID: fixture.input.CheckpointID,
		InterruptID:  interrupted.Interrupt.ID,
		Response:     InterruptResponse{Kind: InterruptRejected},
	}, resumeSink)
	if err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	if completed.Status != InvocationCompleted || fixture.source.callCount() != 1 {
		t.Fatalf("Resume() result = %+v, tool calls = %d", completed, fixture.source.callCount())
	}
	if !hasInvocationEvent(resumeSink.events, InvocationEventToolFinished, tool.ResultDenied) {
		t.Fatalf("resume events = %+v", resumeSink.events)
	}
}

func TestEinoInvokerCancellationDropsInterruptedCheckpointWithoutReexecution(t *testing.T) {
	t.Parallel()
	fixture := newEinoInvokerFixture(t)
	interrupted, err := fixture.invoker.Invoke(context.Background(), fixture.input, fixture.sink)
	if err != nil {
		t.Fatalf("Invoke() error = %v", err)
	}

	result, err := fixture.invoker.Resume(context.Background(), ResumeInput{
		CheckpointID: fixture.input.CheckpointID,
		InterruptID:  interrupted.Interrupt.ID,
		Response:     InterruptResponse{Kind: InterruptCancelled},
	}, &recordingInvocationSink{})
	if err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	if result.Status != InvocationCancelled || result.TerminationReason != "cancelled" {
		t.Fatalf("Resume() result = %+v", result)
	}
	if fixture.source.callCount() != 1 {
		t.Fatalf("tool calls = %d, want 1", fixture.source.callCount())
	}
	if _, exists, err := fixture.store.Get(context.Background(), fixture.input.CheckpointID); err != nil || exists {
		t.Fatalf("cancelled checkpoint exists = %v, error = %v", exists, err)
	}
}

func TestEinoInvokerRequiresExactResumeTargetAndCloseDropsCheckpoint(t *testing.T) {
	t.Parallel()
	fixture := newEinoInvokerFixture(t)
	interrupted, err := fixture.invoker.Invoke(context.Background(), fixture.input, fixture.sink)
	if err != nil {
		t.Fatalf("Invoke() error = %v", err)
	}
	if _, err := fixture.invoker.Resume(context.Background(), ResumeInput{
		CheckpointID: fixture.input.CheckpointID,
		InterruptID:  "wrong-interrupt",
		Response:     InterruptResponse{Kind: InterruptApproved},
	}, &recordingInvocationSink{}); err == nil {
		t.Fatal("Resume() mismatch error = nil")
	}
	if err := fixture.invoker.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := fixture.invoker.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	if _, exists, err := fixture.store.Get(context.Background(), fixture.input.CheckpointID); err != nil || exists {
		t.Fatalf("checkpoint after Close exists = %v, error = %v", exists, err)
	}
	if interrupted.Interrupt == nil {
		t.Fatal("Invoke() interrupt = nil")
	}
}

func TestEinoInvokerReportsStepAndDurationLimits(t *testing.T) {
	t.Parallel()
	t.Run("steps", func(t *testing.T) {
		fixture := newEinoInvokerFixture(t)
		fixture.source.interrupt = false
		fixture.input.Limits.MaxSteps = 1
		result, err := fixture.invoker.Invoke(context.Background(), fixture.input, fixture.sink)
		if err != nil {
			t.Fatalf("Invoke() error = %v", err)
		}
		if result.Status != InvocationLimitReached || result.TerminationReason != "max-steps" || result.Steps != 1 {
			t.Fatalf("Invoke() result = %+v", result)
		}
	})
	t.Run("duration", func(t *testing.T) {
		store := NewMemoryCheckpointStore()
		factory, err := NewEinoInvokerFactory(EinoInvokerDependencies{
			Models:      &fixedModelFactory{value: blockingToolCallingModel{}},
			Checkpoints: store,
		})
		if err != nil {
			t.Fatalf("NewEinoInvokerFactory() error = %v", err)
		}
		invoker, err := factory.CreateInvoker(context.Background())
		if err != nil {
			t.Fatalf("CreateInvoker() error = %v", err)
		}
		input := validInvocationInput(tool.NewRegistry())
		input.Limits.MaxDuration = 25 * time.Millisecond
		result, err := invoker.Invoke(context.Background(), input, &recordingInvocationSink{})
		if err != nil {
			t.Fatalf("Invoke() error = %v", err)
		}
		if result.Status != InvocationLimitReached || result.TerminationReason != "max-duration" {
			t.Fatalf("Invoke() result = %+v", result)
		}
	})
}

func TestNewEinoInvokerFactoryRejectsNilDependencies(t *testing.T) {
	t.Parallel()
	if _, err := NewEinoInvokerFactory(EinoInvokerDependencies{}); err == nil {
		t.Fatal("NewEinoInvokerFactory() error = nil")
	}
	var typedNil *fixedModelFactory
	if _, err := NewEinoInvokerFactory(EinoInvokerDependencies{Models: typedNil, Checkpoints: NewMemoryCheckpointStore()}); err == nil {
		t.Fatal("NewEinoInvokerFactory() typed nil error = nil")
	}
}

type einoInvokerFixture struct {
	invoker AgentInvoker
	store   *MemoryCheckpointStore
	source  *interruptingRegistryTool
	input   InvocationInput
	sink    *recordingInvocationSink
}

func newEinoInvokerFixture(t *testing.T) *einoInvokerFixture {
	t.Helper()
	store := NewMemoryCheckpointStore()
	modelValue := &scriptedToolCallingModel{state: &scriptedModelState{}}
	factory, err := NewEinoInvokerFactory(EinoInvokerDependencies{
		Models:      &fixedModelFactory{value: modelValue},
		Checkpoints: store,
	})
	if err != nil {
		t.Fatalf("NewEinoInvokerFactory() error = %v", err)
	}
	invoker, err := factory.CreateInvoker(context.Background())
	if err != nil {
		t.Fatalf("CreateInvoker() error = %v", err)
	}
	source := &interruptingRegistryTool{interrupt: true}
	registry := tool.NewRegistry()
	if err := registry.Register(source); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	return &einoInvokerFixture{
		invoker: invoker,
		store:   store,
		source:  source,
		input:   validInvocationInput(registry),
		sink:    &recordingInvocationSink{},
	}
}

type fixedModelFactory struct {
	value model.ToolCallingChatModel
}

func (f *fixedModelFactory) NewChatModel(ctx context.Context, _ provider.ModelRef) (model.ToolCallingChatModel, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return f.value, nil
}

type scriptedModelState struct {
	mu       sync.Mutex
	toolInfo []*schema.ToolInfo
}

type scriptedToolCallingModel struct {
	state *scriptedModelState
}

type blockingToolCallingModel struct{}

func (blockingToolCallingModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return blockingToolCallingModel{}, nil
}

func (blockingToolCallingModel) Generate(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (blockingToolCallingModel) Stream(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (m *scriptedToolCallingModel) WithTools(values []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	m.state.mu.Lock()
	m.state.toolInfo = append([]*schema.ToolInfo(nil), values...)
	m.state.mu.Unlock()
	return m, nil
}

func (m *scriptedToolCallingModel) Generate(ctx context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return scriptedModelResponse(input), nil
}

func (m *scriptedToolCallingModel) Stream(ctx context.Context, input []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	message, err := m.Generate(ctx, input)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

func scriptedModelResponse(input []*schema.Message) *schema.Message {
	for index := len(input) - 1; index >= 0; index-- {
		if input[index] != nil && input[index].Role == schema.Tool {
			return schema.AssistantMessage("done", nil)
		}
	}
	return schema.AssistantMessage("", []schema.ToolCall{{
		ID:   "call-1",
		Type: "function",
		Function: schema.FunctionCall{
			Name:      "approval_probe",
			Arguments: `{"value":"safe"}`,
		},
	}})
}

type interruptingRegistryTool struct {
	calls     atomic.Int32
	interrupt bool
}

func (t *interruptingRegistryTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        "approval_probe",
		Description: "Exercise the provider-neutral approval boundary.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"value":{"type":"string"}},"required":["value"],"additionalProperties":false}`),
	}
}

func (t *interruptingRegistryTool) Invoke(ctx context.Context, _ json.RawMessage) (tool.Result, error) {
	if err := ctx.Err(); err != nil {
		return tool.Result{}, err
	}
	call := t.calls.Add(1)
	if t.interrupt && call == 1 {
		return tool.Result{
			Status:  tool.ResultInterrupted,
			Content: "Waiting for approval.",
			Interrupt: &tool.Interrupt{
				ID:      "approval-request-1",
				Kind:    "approval",
				Payload: json.RawMessage(`{"request_id":"approval-request-1"}`),
			},
		}, nil
	}
	return tool.Result{
		Status:  tool.ResultCompleted,
		Content: `{"ok":true}`,
		Data:    json.RawMessage(`{"ok":true}`),
	}, nil
}

func (t *interruptingRegistryTool) callCount() int {
	return int(t.calls.Load())
}

func hasInvocationEvent(events []InvocationEvent, kind InvocationEventKind, status tool.ResultStatus) bool {
	for _, event := range events {
		if event.Kind != kind {
			continue
		}
		if status == "" || (event.Tool != nil && event.Tool.Status == status) {
			return true
		}
	}
	return false
}
