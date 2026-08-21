package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/adk"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/eaglc/codepilot/internal/tool"
	einojsonschema "github.com/eino-contrib/jsonschema"
)

const (
	maxInvocationSteps       = 100
	maxInvocationDuration    = 2 * time.Hour
	maxInvocationIDBytes     = 256
	maxSystemPromptBytes     = 512 << 10
	maxInvocationMessageSize = 1 << 20
	maxInvocationMessages    = 2_000
	maxInvocationHistorySize = 8 << 20
	maxToolEventSummaryBytes = 512
)

var (
	_ AgentInvokerFactory    = (*EinoInvokerFactory)(nil)
	_ AgentInvoker           = (*EinoInvoker)(nil)
	_ einotool.InvokableTool = (*einoRegistryTool)(nil)
	_ adk.CheckPointStore    = (*MemoryCheckpointStore)(nil)
	_ adk.CheckPointDeleter  = (*MemoryCheckpointStore)(nil)
)

// EinoInvokerDependencies contains the version-specific Eino boundaries.
type EinoInvokerDependencies struct {
	Models      ModelFactory
	Checkpoints CheckpointStore
}

// EinoInvokerFactory creates independent invokers over shared external ports.
type EinoInvokerFactory struct {
	models      ModelFactory
	checkpoints CheckpointStore
}

// NewEinoInvokerFactory validates and captures Eino adapter dependencies.
func NewEinoInvokerFactory(deps EinoInvokerDependencies) (*EinoInvokerFactory, error) {
	if isNilDependency(deps.Models) {
		return nil, errors.New("create Eino invoker factory: model factory is required")
	}
	if isNilDependency(deps.Checkpoints) {
		return nil, errors.New("create Eino invoker factory: checkpoint store is required")
	}
	return &EinoInvokerFactory{models: deps.Models, checkpoints: deps.Checkpoints}, nil
}

// CreateInvoker creates one stateful invoker for exclusive CodingAgent use.
func (f *EinoInvokerFactory) CreateInvoker(ctx context.Context) (AgentInvoker, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f == nil || isNilDependency(f.models) || isNilDependency(f.checkpoints) {
		return nil, errors.New("create Eino invoker: factory is unavailable")
	}
	return &EinoInvoker{models: f.models, checkpoints: f.checkpoints}, nil
}

// EinoInvoker owns the live Runner and tool objects required to resume one
// interrupted invocation. Checkpoints intentionally do not serialize tools.
type EinoInvoker struct {
	mu           sync.Mutex
	models       ModelFactory
	checkpoints  CheckpointStore
	runner       *adk.Runner
	relay        *invocationEventRelay
	checkpointID string
	interruptID  string
	limits       InvocationLimits
	steps        int
	finalText    string
	active       bool
	activeDone   chan struct{}
	activeCancel context.CancelFunc
	closed       bool
}

// Invoke starts a new Eino ChatModelAgent run with a per-turn tool registry.
func (i *EinoInvoker) Invoke(ctx context.Context, input InvocationInput, events InvocationEventSink) (InvocationResult, error) {
	if err := validateInvocationInput(input, events); err != nil {
		return InvocationResult{}, err
	}
	runCtx, cancel := context.WithTimeout(ctx, input.Limits.MaxDuration)
	relay := newInvocationEventRelay(events)
	if err := i.beginInvocation(cancel, relay, input.Limits, ""); err != nil {
		cancel()
		return InvocationResult{}, err
	}

	result := InvocationResult{}
	defer func() {
		cancel()
		i.finishInvocation(result, input.CheckpointID)
	}()

	chatModel, err := i.models.NewChatModel(runCtx, input.Model)
	if err != nil {
		return result, fmt.Errorf("invoke Eino agent: create model: %w", err)
	}
	if isNilDependency(chatModel) {
		return result, errors.New("invoke Eino agent: model factory returned nil")
	}
	einoTools, err := newEinoRegistryTools(input.Tools, relay)
	if err != nil {
		return result, err
	}
	agentValue, err := adk.NewChatModelAgent(runCtx, &adk.ChatModelAgentConfig{
		Name:          "codepilot",
		Description:   "Run a bounded provider-neutral coding tool loop.",
		Instruction:   input.SystemPrompt,
		Model:         chatModel,
		MaxIterations: input.Limits.MaxSteps,
		ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{
			Tools: einoTools,
		}},
	})
	if err != nil {
		return result, fmt.Errorf("invoke Eino agent: create chat agent: %w", err)
	}
	runner := adk.NewRunner(runCtx, adk.RunnerConfig{
		Agent:           agentValue,
		EnableStreaming: true,
		CheckPointStore: i.checkpoints,
	})
	i.captureRunner(runner, input.CheckpointID)
	iterator := runner.Run(runCtx, invocationMessages(input.Messages), adk.WithCheckPointID(input.CheckpointID))
	result, err = consumeEinoEvents(runCtx, ctx, iterator, relay, einoEventState{})
	return result, err
}

// Resume continues the exact interrupted Runner retained by this invoker.
func (i *EinoInvoker) Resume(ctx context.Context, input ResumeInput, events InvocationEventSink) (InvocationResult, error) {
	if err := validateResumeInput(input, events); err != nil {
		return InvocationResult{}, err
	}
	runner, relay, limits, progress, err := i.resumeState(input)
	if err != nil {
		return InvocationResult{}, err
	}
	relay.setSink(events)
	runCtx, cancel := context.WithTimeout(ctx, limits.MaxDuration)
	if err := i.beginInvocation(cancel, relay, limits, input.InterruptID); err != nil {
		cancel()
		return InvocationResult{}, err
	}

	result := InvocationResult{}
	defer func() {
		cancel()
		i.finishInvocation(result, input.CheckpointID)
	}()
	// Cancellation is a terminal lifecycle signal, not a tool result the model
	// may continue from. Finishing here also removes the retained checkpoint.
	if input.Response.Kind == InterruptCancelled {
		result = InvocationResult{
			Status: InvocationCancelled, FinalText: progress.finalText, Steps: progress.steps,
			TerminationReason: "cancelled",
		}
		return result, nil
	}

	iterator, err := runner.ResumeWithParams(runCtx, input.CheckpointID, &adk.ResumeParams{
		Targets: map[string]any{input.InterruptID: input.Response},
	})
	if err != nil {
		return result, fmt.Errorf("resume Eino agent: %w", err)
	}
	result, err = consumeEinoEvents(runCtx, ctx, iterator, relay, progress)
	return result, err
}

// Close cancels any active run, discards resumable state, and is idempotent.
func (i *EinoInvoker) Close() error {
	if i == nil {
		return nil
	}
	i.mu.Lock()
	if i.closed {
		done := i.activeDone
		i.mu.Unlock()
		if done != nil {
			<-done
		}
		return nil
	}
	i.closed = true
	cancel := i.activeCancel
	done := i.activeDone
	checkpointID := i.checkpointID
	if cancel != nil {
		cancel()
	}
	i.mu.Unlock()
	if done != nil {
		<-done
	}
	if checkpointID != "" {
		return deleteCheckpoint(context.Background(), i.checkpoints, checkpointID)
	}
	return nil
}

func (i *EinoInvoker) beginInvocation(cancel context.CancelFunc, relay *invocationEventRelay, limits InvocationLimits, expectedInterruptID string) error {
	if i == nil {
		return errors.New("run Eino agent: invoker is nil")
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.closed {
		return errors.New("run Eino agent: invoker is closed")
	}
	if i.active {
		return errors.New("run Eino agent: another invocation is active")
	}
	if expectedInterruptID == "" && i.interruptID != "" {
		return errors.New("run Eino agent: interrupted invocation must be resumed or closed")
	}
	if expectedInterruptID != "" && i.interruptID != expectedInterruptID {
		return errors.New("resume Eino agent: interrupt no longer matches retained state")
	}
	i.active = true
	i.activeDone = make(chan struct{})
	i.activeCancel = cancel
	i.relay = relay
	i.limits = limits
	return nil
}

func (i *EinoInvoker) captureRunner(runner *adk.Runner, checkpointID string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.runner = runner
	i.checkpointID = checkpointID
}

// finishInvocation retains Runner state only for a valid interrupt. Every
// terminal path removes the checkpoint so it cannot be replayed later.
func (i *EinoInvoker) finishInvocation(result InvocationResult, checkpointID string) {
	keep := result.Status == InvocationInterrupted && result.Interrupt != nil
	i.mu.Lock()
	if i.closed {
		keep = false
	}
	if keep {
		i.interruptID = result.Interrupt.ID
		i.steps = result.Steps
		i.finalText = result.FinalText
	} else {
		i.runner = nil
		i.relay = nil
		i.checkpointID = ""
		i.interruptID = ""
		i.limits = InvocationLimits{}
		i.steps = 0
		i.finalText = ""
	}
	i.active = false
	i.activeCancel = nil
	done := i.activeDone
	i.activeDone = nil
	if done != nil {
		close(done)
	}
	i.mu.Unlock()
	if !keep && checkpointID != "" {
		_ = deleteCheckpoint(context.Background(), i.checkpoints, checkpointID)
	}
}

func (i *EinoInvoker) resumeState(input ResumeInput) (*adk.Runner, *invocationEventRelay, InvocationLimits, einoEventState, error) {
	if i == nil {
		return nil, nil, InvocationLimits{}, einoEventState{}, errors.New("resume Eino agent: invoker is nil")
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.closed {
		return nil, nil, InvocationLimits{}, einoEventState{}, errors.New("resume Eino agent: invoker is closed")
	}
	if i.active {
		return nil, nil, InvocationLimits{}, einoEventState{}, errors.New("resume Eino agent: another invocation is active")
	}
	if i.runner == nil || i.relay == nil || i.interruptID == "" {
		return nil, nil, InvocationLimits{}, einoEventState{}, errors.New("resume Eino agent: no interrupted invocation is retained")
	}
	if i.checkpointID != input.CheckpointID || i.interruptID != input.InterruptID {
		return nil, nil, InvocationLimits{}, einoEventState{}, errors.New("resume Eino agent: checkpoint or interrupt does not match")
	}
	return i.runner, i.relay, i.limits, einoEventState{steps: i.steps, finalText: i.finalText}, nil
}

type invocationEventRelay struct {
	mu        sync.RWMutex
	publishMu sync.Mutex
	sink      InvocationEventSink
}

func newInvocationEventRelay(sink InvocationEventSink) *invocationEventRelay {
	return &invocationEventRelay{sink: sink}
}

func (r *invocationEventRelay) setSink(sink InvocationEventSink) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sink = sink
}

func (r *invocationEventRelay) publish(ctx context.Context, event InvocationEvent) error {
	r.publishMu.Lock()
	defer r.publishMu.Unlock()
	r.mu.RLock()
	sink := r.sink
	r.mu.RUnlock()
	if isNilDependency(sink) {
		return errors.New("publish invocation event: sink is unavailable")
	}
	return sink.PublishInvocationEvent(ctx, event)
}

// einoRegistryTool translates one provider-neutral Tool into Eino's invokable
// contract while retaining only the transient interrupt needed for resume.
type einoRegistryTool struct {
	name    string
	source  tool.Tool
	info    *schema.ToolInfo
	relay   *invocationEventRelay
	mu      sync.Mutex
	pending string
}

func newEinoRegistryTools(registry *tool.Registry, relay *invocationEventRelay) ([]einotool.BaseTool, error) {
	definitions := registry.Definitions()
	values := make([]einotool.BaseTool, 0, len(definitions))
	for _, definition := range definitions {
		source, exists := registry.Lookup(definition.Name)
		if !exists {
			return nil, fmt.Errorf("create Eino tools: registry entry %q disappeared", definition.Name)
		}
		var parameters einojsonschema.Schema
		if err := json.Unmarshal(definition.InputSchema, &parameters); err != nil {
			return nil, fmt.Errorf("create Eino tool %q: decode schema: %w", definition.Name, err)
		}
		values = append(values, &einoRegistryTool{
			name:   definition.Name,
			source: source,
			info: &schema.ToolInfo{
				Name:        definition.Name,
				Desc:        definition.Description,
				ParamsOneOf: schema.NewParamsOneOfByJSONSchema(&parameters),
			},
			relay: relay,
		})
	}
	return values, nil
}

func (t *einoRegistryTool) Info(context.Context) (*schema.ToolInfo, error) {
	encoded, err := json.Marshal(t.info)
	if err != nil {
		return nil, err
	}
	var copied schema.ToolInfo
	if err := json.Unmarshal(encoded, &copied); err != nil {
		return nil, err
	}
	return &copied, nil
}

func (t *einoRegistryTool) InvokableRun(ctx context.Context, arguments string, _ ...einotool.Option) (string, error) {
	if err := t.relay.publish(ctx, InvocationEvent{Kind: InvocationEventToolStarted, Tool: &InvocationToolEvent{Name: t.name}}); err != nil {
		return "", err
	}
	wasInterrupted, _, _ := einotool.GetInterruptState[any](ctx)
	if wasInterrupted {
		isTarget, hasResponse, response := einotool.GetResumeContext[InterruptResponse](ctx)
		if !isTarget {
			return t.reinterrupt(ctx)
		}
		if !hasResponse || !validInterruptResponse(response.Kind) {
			return "", errors.New("resume Eino tool: valid interrupt response is required")
		}
		switch response.Kind {
		case InterruptRejected:
			t.clearPending()
			return t.complete(ctx, tool.Result{Status: tool.ResultDenied, Content: "The user rejected this operation."})
		case InterruptCancelled:
			t.clearPending()
			return t.complete(ctx, tool.Result{Status: tool.ResultCancelled, Content: "The user cancelled this operation."})
		case InterruptApproved:
		}
	}

	result, err := t.source.Invoke(ctx, json.RawMessage(arguments))
	if err != nil {
		reportErr := t.relay.publish(ctx, InvocationEvent{Kind: InvocationEventToolFinished, Tool: &InvocationToolEvent{
			Name: t.name, Status: tool.ResultFailed, Summary: "Tool execution failed.",
		}})
		return "", errors.Join(err, reportErr)
	}
	if !validToolResultStatus(result.Status) {
		return "", errors.New("invoke Eino tool: tool returned an invalid status")
	}
	if result.Status == tool.ResultInterrupted {
		if result.Interrupt == nil || strings.TrimSpace(result.Interrupt.Kind) == "" {
			return "", errors.New("invoke Eino tool: interrupted result is missing metadata")
		}
		info, encodeErr := json.Marshal(einoToolInterruptInfo{
			Kind:    result.Interrupt.Kind,
			Payload: append(json.RawMessage(nil), result.Interrupt.Payload...),
		})
		if encodeErr != nil {
			return "", fmt.Errorf("invoke Eino tool: encode interrupt: %w", encodeErr)
		}
		t.setPending(string(info))
		if err := t.publishFinished(ctx, result); err != nil {
			return "", err
		}
		return "", einotool.Interrupt(ctx, string(info))
	}
	t.clearPending()
	return t.complete(ctx, result)
}

func (t *einoRegistryTool) reinterrupt(ctx context.Context) (string, error) {
	t.mu.Lock()
	info := t.pending
	t.mu.Unlock()
	if info == "" {
		return "", errors.New("resume Eino tool: interrupt metadata is unavailable")
	}
	result := tool.Result{Status: tool.ResultInterrupted, Content: "The tool is still waiting for external input."}
	if err := t.publishFinished(ctx, result); err != nil {
		return "", err
	}
	return "", einotool.Interrupt(ctx, info)
}

func (t *einoRegistryTool) complete(ctx context.Context, result tool.Result) (string, error) {
	if err := t.publishFinished(ctx, result); err != nil {
		return "", err
	}
	return encodeToolResult(result)
}

func (t *einoRegistryTool) publishFinished(ctx context.Context, result tool.Result) error {
	return t.relay.publish(ctx, InvocationEvent{Kind: InvocationEventToolFinished, Tool: &InvocationToolEvent{
		Name: t.name, Status: result.Status, Summary: boundedInvocationText(result.Content, maxToolEventSummaryBytes),
	}})
}

func (t *einoRegistryTool) setPending(value string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pending = value
}

func (t *einoRegistryTool) clearPending() {
	t.setPending("")
}

type einoToolInterruptInfo struct {
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

type einoToolResultEnvelope struct {
	Status  tool.ResultStatus `json:"status"`
	Result  json.RawMessage   `json:"result,omitempty"`
	Content string            `json:"content,omitempty"`
}

func encodeToolResult(result tool.Result) (string, error) {
	envelope := einoToolResultEnvelope{Status: result.Status}
	if len(result.Data) > 0 && json.Valid(result.Data) {
		envelope.Result = append(json.RawMessage(nil), result.Data...)
	} else {
		envelope.Content = result.Content
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return "", fmt.Errorf("encode Eino tool result: %w", err)
	}
	return string(encoded), nil
}

type einoEventState struct {
	finalText string
	steps     int
}

func consumeEinoEvents(runCtx context.Context, parentCtx context.Context, iterator *adk.AsyncIterator[*adk.AgentEvent], relay *invocationEventRelay, state einoEventState) (InvocationResult, error) {
	for {
		event, exists := iterator.Next()
		if !exists {
			break
		}
		if event == nil {
			return InvocationResult{}, errors.New("run Eino agent: received nil event")
		}
		if event.Err != nil {
			return invocationErrorResult(runCtx, parentCtx, state, event.Err)
		}
		if event.Action != nil && event.Action.Interrupted != nil {
			interrupt, err := decodeInvocationInterrupt(event.Action.Interrupted)
			if err != nil {
				return InvocationResult{}, err
			}
			if err := relay.publish(runCtx, InvocationEvent{Kind: InvocationEventInterrupted, Interrupt: interrupt}); err != nil {
				return InvocationResult{}, err
			}
			return InvocationResult{
				Status: InvocationInterrupted, FinalText: state.finalText, Steps: state.steps,
				TerminationReason: "interrupted", Interrupt: interrupt,
			}, nil
		}
		if event.Output != nil && event.Output.MessageOutput != nil {
			if err := state.consumeMessage(runCtx, event.Output.MessageOutput, relay); err != nil {
				return InvocationResult{}, err
			}
		}
	}
	if parentCtx.Err() != nil {
		return InvocationResult{Status: InvocationCancelled, FinalText: state.finalText, Steps: state.steps, TerminationReason: "cancelled"}, parentCtx.Err()
	}
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		return InvocationResult{Status: InvocationLimitReached, FinalText: state.finalText, Steps: state.steps, TerminationReason: "max-duration"}, nil
	}
	return InvocationResult{Status: InvocationCompleted, FinalText: state.finalText, Steps: state.steps, TerminationReason: "completed"}, nil
}

func (s *einoEventState) consumeMessage(ctx context.Context, variant *adk.MessageVariant, relay *invocationEventRelay) error {
	if variant.Role != schema.Assistant {
		if variant.IsStreaming && variant.MessageStream != nil {
			defer variant.MessageStream.Close()
			for {
				_, err := variant.MessageStream.Recv()
				if errors.Is(err, io.EOF) {
					return nil
				}
				if err != nil {
					return err
				}
			}
		}
		return nil
	}
	s.steps++
	var text strings.Builder
	if variant.IsStreaming {
		if variant.MessageStream == nil {
			return errors.New("run Eino agent: assistant stream is nil")
		}
		defer variant.MessageStream.Close()
		for {
			message, err := variant.MessageStream.Recv()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return err
			}
			if message != nil && message.Content != "" {
				text.WriteString(message.Content)
				if err := relay.publish(ctx, InvocationEvent{Kind: InvocationEventAssistantText, Text: message.Content}); err != nil {
					return err
				}
			}
		}
	} else if variant.Message != nil && variant.Message.Content != "" {
		text.WriteString(variant.Message.Content)
		if err := relay.publish(ctx, InvocationEvent{Kind: InvocationEventAssistantText, Text: variant.Message.Content}); err != nil {
			return err
		}
	}
	if text.Len() > 0 {
		s.finalText = text.String()
	}
	return nil
}

func invocationErrorResult(runCtx context.Context, parentCtx context.Context, state einoEventState, err error) (InvocationResult, error) {
	if parentCtx.Err() != nil {
		return InvocationResult{Status: InvocationCancelled, FinalText: state.finalText, Steps: state.steps, TerminationReason: "cancelled"}, parentCtx.Err()
	}
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		return InvocationResult{Status: InvocationLimitReached, FinalText: state.finalText, Steps: state.steps, TerminationReason: "max-duration"}, nil
	}
	if errors.Is(err, adk.ErrExceedMaxIterations) {
		return InvocationResult{Status: InvocationLimitReached, FinalText: state.finalText, Steps: state.steps, TerminationReason: "max-steps"}, nil
	}
	return InvocationResult{}, fmt.Errorf("run Eino agent: %w", err)
}

func decodeInvocationInterrupt(info *adk.InterruptInfo) (*InvocationInterrupt, error) {
	var root *adk.InterruptCtx
	for _, value := range info.InterruptContexts {
		if value != nil && value.IsRootCause {
			if root != nil {
				return nil, errors.New("run Eino agent: multiple root-cause interrupts are unsupported")
			}
			root = value
		}
	}
	if root == nil || strings.TrimSpace(root.ID) == "" {
		return nil, errors.New("run Eino agent: interrupt has no root cause")
	}
	encoded, ok := root.Info.(string)
	if !ok {
		return nil, errors.New("run Eino agent: interrupt payload has an unexpected type")
	}
	var decoded einoToolInterruptInfo
	if err := json.Unmarshal([]byte(encoded), &decoded); err != nil || strings.TrimSpace(decoded.Kind) == "" {
		return nil, errors.New("run Eino agent: interrupt payload is invalid")
	}
	return &InvocationInterrupt{ID: root.ID, Kind: decoded.Kind, Payload: append(json.RawMessage(nil), decoded.Payload...)}, nil
}

func invocationMessages(values []InvocationMessage) []*schema.Message {
	messages := make([]*schema.Message, 0, len(values))
	for _, value := range values {
		switch value.Role {
		case InvocationRoleUser:
			messages = append(messages, schema.UserMessage(value.Content))
		case InvocationRoleAssistant:
			messages = append(messages, schema.AssistantMessage(value.Content, nil))
		}
	}
	return messages
}

func validateInvocationInput(input InvocationInput, events InvocationEventSink) error {
	if strings.TrimSpace(input.ID) == "" || len(input.ID) > maxInvocationIDBytes || strings.TrimSpace(input.CheckpointID) == "" || len(input.CheckpointID) > maxInvocationIDBytes {
		return errors.New("invoke Eino agent: invocation and checkpoint IDs are required and bounded")
	}
	if !validInvocationIdentifier(input.Model.Provider) || !validInvocationIdentifier(input.Model.Model) {
		return errors.New("invoke Eino agent: provider and model are required")
	}
	if len(input.SystemPrompt) > maxSystemPromptBytes {
		return errors.New("invoke Eino agent: system prompt exceeds its size limit")
	}
	if input.Tools == nil {
		return errors.New("invoke Eino agent: tool registry is required")
	}
	if isNilDependency(events) {
		return errors.New("invoke Eino agent: event sink is required")
	}
	if input.Limits.MaxSteps <= 0 || input.Limits.MaxSteps > maxInvocationSteps || input.Limits.MaxDuration <= 0 || input.Limits.MaxDuration > maxInvocationDuration {
		return errors.New("invoke Eino agent: invocation limits are invalid")
	}
	if len(input.Messages) == 0 || len(input.Messages) > maxInvocationMessages {
		return errors.New("invoke Eino agent: conversation message count is invalid")
	}
	totalMessageBytes := 0
	for _, message := range input.Messages {
		if (message.Role != InvocationRoleUser && message.Role != InvocationRoleAssistant) || strings.TrimSpace(message.Content) == "" || len(message.Content) > maxInvocationMessageSize {
			return errors.New("invoke Eino agent: conversation contains an invalid message")
		}
		totalMessageBytes += len(message.Content)
		if totalMessageBytes > maxInvocationHistorySize {
			return errors.New("invoke Eino agent: conversation exceeds its total size limit")
		}
	}
	return nil
}

func validateResumeInput(input ResumeInput, events InvocationEventSink) error {
	if strings.TrimSpace(input.CheckpointID) == "" || len(input.CheckpointID) > maxInvocationIDBytes || strings.TrimSpace(input.InterruptID) == "" || len(input.InterruptID) > maxInvocationIDBytes {
		return errors.New("resume Eino agent: checkpoint and interrupt IDs are required and bounded")
	}
	if !validInterruptResponse(input.Response.Kind) {
		return errors.New("resume Eino agent: interrupt response is invalid")
	}
	if isNilDependency(events) {
		return errors.New("resume Eino agent: event sink is required")
	}
	return nil
}

func validInterruptResponse(kind InterruptResponseKind) bool {
	switch kind {
	case InterruptApproved, InterruptRejected, InterruptCancelled:
		return true
	default:
		return false
	}
}

func validToolResultStatus(status tool.ResultStatus) bool {
	switch status {
	case tool.ResultCompleted, tool.ResultDenied, tool.ResultInvalid, tool.ResultFailed, tool.ResultCancelled, tool.ResultInterrupted:
		return true
	default:
		return false
	}
}

func validInvocationIdentifier(value string) bool {
	return strings.TrimSpace(value) == value && value != "" && len(value) <= maxInvocationIDBytes
}

func boundedInvocationText(value string, limit int) string {
	return truncateToolText(strings.TrimSpace(value), limit)
}

func deleteCheckpoint(ctx context.Context, store CheckpointStore, checkpointID string) error {
	deleter, ok := store.(interface {
		Delete(context.Context, string) error
	})
	if !ok {
		return nil
	}
	return deleter.Delete(ctx, checkpointID)
}

func isNilDependency(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
