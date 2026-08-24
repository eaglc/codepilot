package agent

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	agentsession "github.com/eaglc/codepilot/internal/agent/session"
	"github.com/eaglc/codepilot/internal/contextmanager"
	"github.com/eaglc/codepilot/internal/llm"
	"github.com/eaglc/codepilot/internal/tool"
)

// ContextProcessor is the context boundary consumed by Runtime.
type ContextProcessor interface {
	Process(ctx context.Context, request contextmanager.Request) (contextmanager.Result, error)
}

// IDGenerator creates collision-resistant IDs with a semantic prefix.
type IDGenerator interface {
	Next(prefix string) (string, error)
}

// RetryWaiter provides cancellable backoff and is injectable for deterministic tests.
type RetryWaiter interface {
	Wait(ctx context.Context, delay time.Duration) error
}

// DataPolicy sanitizes model/tool data before it becomes durable or observable.
// Product layers may inject stricter rules without teaching Agent about paths,
// credentials, or business-specific Tool schemas.
type DataPolicy interface {
	SanitizeMessage(message llm.Message) llm.Message
	SanitizeToolArguments(toolName string, arguments json.RawMessage) json.RawMessage
	SanitizeToolResult(toolName string, result tool.Result) tool.Result
	SanitizeText(value string) string
}

// TextStreamSanitizer preserves a data policy while making safe text prefixes
// observable before the provider finishes the whole response.
type TextStreamSanitizer interface {
	Write(value string) string
	Flush() string
}

// StreamingDataPolicy optionally supplies a stateful sanitizer for model text
// split across provider events. Policies without one retain the conservative
// whole-response buffering behavior.
type StreamingDataPolicy interface {
	NewTextStreamSanitizer() TextStreamSanitizer
}

// Dependencies contains only generic capabilities required by Runtime.
type Dependencies struct {
	Models      llm.ModelFactory
	Contexts    ContextProcessor
	Sessions    agentsession.Repository
	IDs         IDGenerator
	RetryWaiter RetryWaiter
	DataPolicy  DataPolicy
}

// Runtime executes provider-neutral Agent runs.
type Runtime struct {
	models      llm.ModelFactory
	contexts    ContextProcessor
	sessions    agentsession.Repository
	ids         IDGenerator
	retryWaiter RetryWaiter
	dataPolicy  DataPolicy
}

// NewRuntime validates and creates a generic Agent runtime.
func NewRuntime(deps Dependencies) (*Runtime, error) {
	if deps.Models == nil || deps.Contexts == nil || deps.Sessions == nil {
		return nil, errors.New("create agent runtime: model, context, and session dependencies are required")
	}
	if deps.IDs == nil {
		deps.IDs = randomIDGenerator{}
	}
	if deps.RetryWaiter == nil {
		deps.RetryWaiter = timerRetryWaiter{}
	}
	if deps.DataPolicy == nil {
		deps.DataPolicy = identityDataPolicy{}
	}
	return &Runtime{models: deps.Models, contexts: deps.Contexts, sessions: deps.Sessions, ids: deps.IDs, retryWaiter: deps.RetryWaiter, dataPolicy: deps.DataPolicy}, nil
}

// RunLimits bounds model steps and total elapsed runtime.
type RunLimits struct {
	MaxSteps             int
	MaxDuration          time.Duration
	MaxModelAttempts     int
	InitialRetryDelay    time.Duration
	MaxRetryDelay        time.Duration
	MaxTotalTokens       int
	MaxOutputTokens      int
	MaxCost              float64
	MaxToolCalls         int
	MaxRepeatedToolCalls int
	MaxOutputBytes       int
	MaxNoProgressSteps   int
}

// RunRequest starts one user-triggered run on an existing Agent session lane.
type RunRequest struct {
	SessionID        agentsession.ID
	Lane             agentsession.Lane
	RunID            agentsession.RunID
	UserEntryID      agentsession.EntryID
	SystemPrompt     string
	Model            llm.ModelRef
	UserMessage      llm.Message
	UntrustedContext []llm.Message
	Tools            *tool.Registry
	Limits           RunLimits
}

// RunStatus describes how a generic Agent run returned to its caller.
type RunStatus string

const (
	RunCompleted    RunStatus = "completed"
	RunInterrupted  RunStatus = "interrupted"
	RunLimitReached RunStatus = "limit_reached"
	RunAborted      RunStatus = "aborted"
	RunFailed       RunStatus = "failed"
)

// RunResult contains terminal Agent facts without Coding-specific classification.
type RunResult struct {
	RunID        agentsession.RunID
	Status       RunStatus
	FinalMessage *llm.Message
	Steps        int
	Reason       string
	Interrupt    *tool.Interrupt
}

// ResumeRequest supplies the product-resolved result for one durable interrupt.
// It does not add another user message or create another operation.
type ResumeRequest struct {
	SessionID        agentsession.ID
	Lane             agentsession.Lane
	RunID            agentsession.RunID
	InterruptID      string
	Resolution       tool.Result
	SystemPrompt     string
	Model            llm.ModelRef
	UntrustedContext []llm.Message
	Tools            *tool.Registry
	Limits           RunLimits
}

// Run executes model steps and centrally journals all tool activity.
func (r *Runtime) Run(ctx context.Context, request RunRequest, sink EventSink) (RunResult, error) {
	if r == nil {
		return RunResult{}, errors.New("run agent: runtime is nil")
	}
	request, err := r.normalizeRequest(request)
	if err != nil {
		return RunResult{}, err
	}
	if sink == nil {
		sink = NopEventSink{}
	}
	runCtx, cancel := context.WithTimeout(ctx, request.Limits.MaxDuration)
	defer cancel()
	dispatcher := &eventDispatcher{runtime: r, sink: sink, sessionID: request.SessionID, runID: request.RunID}

	snapshot, err := r.sessions.Load(runCtx, request.SessionID)
	if err != nil {
		return RunResult{}, fmt.Errorf("run agent: load session: %w", err)
	}
	sourceLeaf, err := laneLeaf(snapshot, request.Lane)
	if err != nil {
		return RunResult{}, err
	}
	if err := r.appendRecord(runCtx, request, agentsession.Record{
		Type: agentsession.RecordOperationStarted, RunID: request.RunID,
		Operation: &agentsession.OperationData{Intent: agentsession.OperationRun, SourceLeafID: sourceLeaf},
	}); err != nil {
		return RunResult{}, err
	}
	durableUser := r.dataPolicy.SanitizeMessage(request.UserMessage)
	if err := durableUser.Validate(); err != nil {
		return r.failRun(runCtx, request, dispatcher, 0, "sanitize_user_message", err)
	}
	if _, err := r.sessions.AppendEntry(runCtx, request.SessionID, request.Lane, agentsession.Entry{
		ID: request.UserEntryID, RunID: request.RunID, Type: agentsession.EntryMessage, Message: messagePointer(durableUser),
	}); err != nil {
		return r.failRun(runCtx, request, dispatcher, 0, "append_user_message", err)
	}
	if err := dispatcher.publish(runCtx, Event{Kind: EventRunStarted}); err != nil {
		return r.failRun(runCtx, request, dispatcher, 0, "publish_run_started", err)
	}

	model, err := r.models.CreateModel(runCtx, request.Model)
	if err != nil {
		return r.failRun(runCtx, request, dispatcher, 0, "create_model", err)
	}
	return r.runSteps(runCtx, request, dispatcher, model, 1)
}

// Resume resolves one durable tool interrupt and continues the same Agent run.
func (r *Runtime) Resume(ctx context.Context, resume ResumeRequest, sink EventSink) (RunResult, error) {
	if r == nil {
		return RunResult{}, errors.New("resume agent: runtime is nil")
	}
	request, err := r.normalizeResumeRequest(resume)
	if err != nil {
		return RunResult{}, err
	}
	if sink == nil {
		sink = NopEventSink{}
	}
	runCtx, cancel := context.WithTimeout(ctx, request.Limits.MaxDuration)
	defer cancel()
	dispatcher := &eventDispatcher{runtime: r, sink: sink, sessionID: request.SessionID, runID: request.RunID}
	snapshot, err := r.sessions.Load(runCtx, request.SessionID)
	if err != nil {
		return RunResult{}, fmt.Errorf("resume agent: load session: %w", err)
	}
	pendingInterrupt, pendingTool, toolAlreadyFinished, err := findResumeState(snapshot, request.RunID, resume.InterruptID)
	if err != nil {
		return RunResult{}, err
	}
	resolution := resume.Resolution.Clone()
	if !toolAlreadyFinished {
		if pendingToolResultExists(snapshot, pendingTool.ResultEntryID) {
			pending := agentsession.PendingTool{
				RunID: request.RunID, Lane: request.Lane, AssistantEntryID: pendingTool.AssistantEntryID,
				ToolIndex: pendingTool.ToolIndex, ToolCallID: pendingTool.ToolCallID, ToolName: pendingTool.ToolName,
				IdempotencyKey: pendingTool.IdempotencyKey, ResultEntryID: pendingTool.ResultEntryID, ReplayPolicy: pendingTool.ReplayPolicy,
				ResultEntryPresent: true,
			}
			durableResult, resultErr := recoveredResultEntry(snapshot, pending)
			if resultErr != nil {
				return RunResult{}, resultErr
			}
			if err := r.persistRecoveredToolFinish(runCtx, request, dispatcher, pendingTool, durableResult); err != nil {
				return RunResult{}, err
			}
			toolAlreadyFinished = true
		} else {
			idempotencyKey := pendingTool.IdempotencyKey
			if pendingTool.ReplayPolicy == string(tool.ReplayIdempotent) && idempotencyKey == "" {
				return RunResult{}, fmt.Errorf("resume agent Tool %q: original idempotency key is missing", pendingTool.ToolName)
			}
			if idempotencyKey == "" {
				idempotencyKey = string(request.RunID) + ":" + pendingTool.ToolCallID
			}
			progress := &toolProgressSink{dispatcher: dispatcher, callID: pendingTool.ToolCallID, name: pendingTool.ToolName}
			resolution, err = request.Tools.Resume(runCtx, tool.Call{
				ID: pendingTool.ToolCallID, Name: pendingTool.ToolName, Arguments: pendingTool.EffectiveArgs,
				IdempotencyKey: idempotencyKey,
			}, tool.Interrupt{ID: pendingInterrupt.InterruptID, Kind: pendingInterrupt.Kind, Payload: pendingInterrupt.Payload}, resolution, progress)
			if err != nil {
				if runCtx.Err() != nil {
					return r.failRun(runCtx, request, dispatcher, completedStepCount(snapshot, request.RunID), "resume_interrupted_tool", runCtx.Err())
				}
				resolution = tool.Result{Status: tool.ResultFailed, Content: []llm.Content{{Type: llm.ContentText, Text: "The approved tool action could not be completed safely."}}}
			}
		}
	}
	if err := dispatcher.publish(runCtx, Event{Kind: EventRunResumed, Interrupt: &InterruptEvent{ID: pendingInterrupt.InterruptID, Kind: pendingInterrupt.Kind, Decision: string(resolution.Status)}}); err != nil {
		return r.failRun(runCtx, request, dispatcher, completedStepCount(snapshot, request.RunID), "publish_run_resumed", err)
	}
	if !toolAlreadyFinished {
		if err := r.persistToolResult(runCtx, request, dispatcher, pendingTool, resolution); err != nil {
			return r.failRun(runCtx, request, dispatcher, completedStepCount(snapshot, request.RunID), "persist_resolved_tool", err)
		}
	}
	if err := r.appendRecord(runCtx, request, agentsession.Record{
		Type: agentsession.RecordInterruptResolved, RunID: request.RunID,
		Interrupt: &agentsession.InterruptData{InterruptID: pendingInterrupt.InterruptID, Kind: pendingInterrupt.Kind, ToolCallID: pendingInterrupt.ToolCallID, Decision: string(resolution.Status), Payload: append(json.RawMessage(nil), resolution.Details...)},
	}); err != nil {
		return RunResult{}, fmt.Errorf("resume agent: persist interrupt resolution: %w", err)
	}
	assistant, err := assistantMessage(snapshot, pendingTool.AssistantEntryID)
	if err != nil {
		return r.failRun(runCtx, request, dispatcher, completedStepCount(snapshot, request.RunID), "load_interrupted_assistant", err)
	}
	calls := assistant.ToolCalls()
	if pendingTool.ToolIndex < 0 || pendingTool.ToolIndex >= len(calls) || calls[pendingTool.ToolIndex].ID != pendingTool.ToolCallID {
		return r.failRun(runCtx, request, dispatcher, completedStepCount(snapshot, request.RunID), "validate_interrupted_tool", errors.New("durable tool index does not match assistant message"))
	}
	for index := pendingTool.ToolIndex + 1; index < len(calls); index++ {
		if reason, budgetErr := r.toolBudgetReason(runCtx, request, calls[index]); budgetErr != nil {
			return r.failRun(runCtx, request, dispatcher, completedStepCount(snapshot, request.RunID), "inspect_tool_budget_after_resume", budgetErr)
		} else if reason != "" {
			if err := r.cancelToolCalls(runCtx, request, dispatcher, pendingTool.AssistantEntryID, calls, index, reason); err != nil {
				return r.failRun(runCtx, request, dispatcher, completedStepCount(snapshot, request.RunID), "cancel_budgeted_tools_after_resume", err)
			}
			steps := completedStepCount(snapshot, request.RunID)
			if err := r.finishRun(runCtx, request, dispatcher, steps, RunLimitReached, reason); err != nil {
				return RunResult{}, err
			}
			return RunResult{RunID: request.RunID, Status: RunLimitReached, Steps: steps, Reason: reason}, nil
		}
		_, interrupted, err := r.executeTool(runCtx, request, dispatcher, pendingTool.AssistantEntryID, index, calls[index])
		if err != nil {
			return r.failRun(runCtx, request, dispatcher, completedStepCount(snapshot, request.RunID), "execute_tool_after_resume", err)
		}
		if interrupted != nil {
			if err := dispatcher.publish(runCtx, Event{Kind: EventRunInterrupted, Interrupt: &InterruptEvent{ID: interrupted.ID, Kind: interrupted.Kind}}); err != nil {
				return r.failRun(runCtx, request, dispatcher, completedStepCount(snapshot, request.RunID), "publish_run_interrupted", err)
			}
			return RunResult{RunID: request.RunID, Status: RunInterrupted, Steps: completedStepCount(snapshot, request.RunID), Reason: "tool_interrupted", Interrupt: interrupted}, nil
		}
	}
	steps := completedStepCount(snapshot, request.RunID)
	if steps >= request.Limits.MaxSteps {
		if err := r.finishRun(runCtx, request, dispatcher, steps, RunLimitReached, "max_steps"); err != nil {
			return RunResult{}, err
		}
		return RunResult{RunID: request.RunID, Status: RunLimitReached, Steps: steps, Reason: "max_steps"}, nil
	}
	model, err := r.models.CreateModel(runCtx, request.Model)
	if err != nil {
		return r.failRun(runCtx, request, dispatcher, steps, "create_model_after_resume", err)
	}
	return r.runSteps(runCtx, request, dispatcher, model, steps+1)
}

func (r *Runtime) runSteps(ctx context.Context, request RunRequest, dispatcher *eventDispatcher, model llm.ChatModel, firstStep int) (RunResult, error) {
	for step := firstStep; step <= request.Limits.MaxSteps; step++ {
		if reason, err := r.noProgressReason(ctx, request); err != nil {
			return r.failRun(ctx, request, dispatcher, step-1, "inspect_run_progress", err)
		} else if reason != "" {
			if err := r.finishRun(ctx, request, dispatcher, step-1, RunLimitReached, reason); err != nil {
				return RunResult{}, err
			}
			return RunResult{RunID: request.RunID, Status: RunLimitReached, Steps: step - 1, Reason: reason}, nil
		}
		if reason, err := r.runBudgetReason(ctx, request); err != nil {
			return r.failRun(ctx, request, dispatcher, step-1, "inspect_run_budget", err)
		} else if reason != "" {
			if err := r.finishRun(ctx, request, dispatcher, step-1, RunLimitReached, reason); err != nil {
				return RunResult{}, err
			}
			return RunResult{RunID: request.RunID, Status: RunLimitReached, Steps: step - 1, Reason: reason}, nil
		}
		if err := r.appendRecord(ctx, request, agentsession.Record{
			Type: agentsession.RecordStepStarted, RunID: request.RunID, Step: &agentsession.StepData{Attempt: step},
		}); err != nil {
			return r.failRun(ctx, request, dispatcher, step-1, "record_step_started", err)
		}
		if err := dispatcher.publish(ctx, Event{Kind: EventStepStarted, Step: &StepEvent{Number: step}}); err != nil {
			return r.failRun(ctx, request, dispatcher, step-1, "publish_step_started", err)
		}

		contextResult, err := r.buildContext(ctx, request)
		if err != nil {
			return r.failRun(ctx, request, dispatcher, step-1, "build_context", err)
		}
		if err := r.persistSummaries(ctx, request, contextResult.Summaries, dispatcher); err != nil {
			return r.failRun(ctx, request, dispatcher, step-1, "persist_compaction", err)
		}
		safeContext, err := r.sanitizeContextMessages(contextResult.Messages)
		if err != nil {
			return r.failRun(ctx, request, dispatcher, step-1, "sanitize_model_context", err)
		}
		chatRequest := llm.ChatRequest{
			Model: request.Model, SystemPrompt: r.dataPolicy.SanitizeText(contextResult.SystemPrompt), Messages: unwrapMessages(safeContext), Tools: request.Tools.Definitions(),
		}
		assistant, err := r.streamAssistantWithRetry(ctx, request, model, chatRequest, dispatcher)
		if err != nil {
			return r.failRun(ctx, request, dispatcher, step-1, "model_step", err)
		}
		assistantEntryID, err := r.nextID("entry")
		if err != nil {
			return r.failRun(ctx, request, dispatcher, step-1, "generate_assistant_entry_id", err)
		}
		durableAssistant := r.dataPolicy.SanitizeMessage(assistant)
		if err := durableAssistant.Validate(); err != nil {
			return r.failRun(ctx, request, dispatcher, step-1, "sanitize_assistant_message", err)
		}
		assistantEntry, err := r.sessions.AppendEntry(ctx, request.SessionID, request.Lane, agentsession.Entry{
			ID: agentsession.EntryID(assistantEntryID), RunID: request.RunID, Type: agentsession.EntryMessage, Message: messagePointer(durableAssistant),
		})
		if err != nil {
			return r.failRun(ctx, request, dispatcher, step-1, "append_assistant_message", err)
		}
		if assistant.Usage != nil {
			usage := *assistant.Usage
			if err := r.appendRecord(ctx, request, agentsession.Record{Type: agentsession.RecordUsage, RunID: request.RunID, Usage: &usage}); err != nil {
				return r.failRun(ctx, request, dispatcher, step-1, "record_usage", err)
			}
		}
		if err := r.appendRecord(ctx, request, agentsession.Record{
			Type: agentsession.RecordStepFinished, RunID: request.RunID,
			Step: &agentsession.StepData{Attempt: step, AssistantEntryID: assistantEntry.ID, StopReason: string(assistant.StopReason)},
		}); err != nil {
			return r.failRun(ctx, request, dispatcher, step, "record_step_finished", err)
		}
		if err := dispatcher.publish(ctx, Event{Kind: EventStepFinished, Step: &StepEvent{Number: step, AssistantEntryID: string(assistantEntry.ID), StopReason: string(assistant.StopReason)}}); err != nil {
			return r.failRun(ctx, request, dispatcher, step, "publish_step_finished", err)
		}

		calls := assistant.ToolCalls()
		if reason, err := r.runBudgetReason(ctx, request); err != nil {
			return r.failRun(ctx, request, dispatcher, step, "inspect_run_budget", err)
		} else if reason != "" {
			if err := r.cancelToolCalls(ctx, request, dispatcher, assistantEntry.ID, calls, 0, reason); err != nil {
				return r.failRun(ctx, request, dispatcher, step, "cancel_budgeted_tools", err)
			}
			if err := r.finishRun(ctx, request, dispatcher, step, RunLimitReached, reason); err != nil {
				return RunResult{}, err
			}
			message := durableAssistant.Clone()
			return RunResult{RunID: request.RunID, Status: RunLimitReached, FinalMessage: &message, Steps: step, Reason: reason}, nil
		}
		if len(calls) == 0 {
			if err := r.finishRun(ctx, request, dispatcher, step, RunCompleted, "model_completed"); err != nil {
				return RunResult{}, err
			}
			message := durableAssistant.Clone()
			return RunResult{RunID: request.RunID, Status: RunCompleted, FinalMessage: &message, Steps: step, Reason: "model_completed"}, nil
		}
		for index, call := range calls {
			if reason, err := r.toolBudgetReason(ctx, request, call); err != nil {
				return r.failRun(ctx, request, dispatcher, step, "inspect_tool_budget", err)
			} else if reason != "" {
				if err := r.cancelToolCalls(ctx, request, dispatcher, assistantEntry.ID, calls, index, reason); err != nil {
					return r.failRun(ctx, request, dispatcher, step, "cancel_budgeted_tools", err)
				}
				if err := r.finishRun(ctx, request, dispatcher, step, RunLimitReached, reason); err != nil {
					return RunResult{}, err
				}
				return RunResult{RunID: request.RunID, Status: RunLimitReached, Steps: step, Reason: reason}, nil
			}
			result, interrupted, err := r.executeTool(ctx, request, dispatcher, assistantEntry.ID, index, call)
			if err != nil {
				return r.failRun(ctx, request, dispatcher, step, "execute_tool", err)
			}
			if interrupted != nil {
				if err := dispatcher.publish(ctx, Event{Kind: EventRunInterrupted, Interrupt: &InterruptEvent{ID: interrupted.ID, Kind: interrupted.Kind}}); err != nil {
					return r.failRun(ctx, request, dispatcher, step, "publish_run_interrupted", err)
				}
				return RunResult{RunID: request.RunID, Status: RunInterrupted, Steps: step, Reason: "tool_interrupted", Interrupt: interrupted}, nil
			}
			_ = result
		}
	}
	if err := r.finishRun(ctx, request, dispatcher, request.Limits.MaxSteps, RunLimitReached, "max_steps"); err != nil {
		return RunResult{}, err
	}
	return RunResult{RunID: request.RunID, Status: RunLimitReached, Steps: request.Limits.MaxSteps, Reason: "max_steps"}, nil
}

func (r *Runtime) normalizeRequest(request RunRequest) (RunRequest, error) {
	if request.SessionID == "" {
		return RunRequest{}, errors.New("run agent: session id is required")
	}
	if request.Lane == "" {
		request.Lane = agentsession.MainLane
	}
	if request.RunID == "" {
		id, err := r.nextID("run")
		if err != nil {
			return RunRequest{}, err
		}
		request.RunID = agentsession.RunID(id)
	}
	if request.UserEntryID == "" {
		id, err := r.nextID("entry")
		if err != nil {
			return RunRequest{}, err
		}
		request.UserEntryID = agentsession.EntryID(id)
	}
	if request.UserMessage.Role != llm.RoleUser {
		return RunRequest{}, errors.New("run agent: current message must have user role")
	}
	if err := request.UserMessage.Validate(); err != nil {
		return RunRequest{}, fmt.Errorf("run agent: %w", err)
	}
	if err := request.Model.Validate(); err != nil {
		return RunRequest{}, fmt.Errorf("run agent: %w", err)
	}
	request.UntrustedContext = cloneLLMMessages(request.UntrustedContext)
	if err := validateUntrustedContext(request.UntrustedContext); err != nil {
		return RunRequest{}, err
	}
	if request.Tools == nil {
		registry, err := tool.NewRegistry()
		if err != nil {
			return RunRequest{}, err
		}
		request.Tools = registry
	}
	if request.Limits.MaxSteps <= 0 {
		request.Limits.MaxSteps = 32
	}
	if request.Limits.MaxDuration <= 0 {
		request.Limits.MaxDuration = 30 * time.Minute
	}
	request.Limits = normalizeRetryLimits(request.Limits)
	return request, nil
}

func validateUntrustedContext(messages []llm.Message) error {
	if len(messages) > 16 {
		return errors.New("run agent: untrusted context exceeds its message limit")
	}
	for index, message := range messages {
		if message.Role != llm.RoleUser {
			return fmt.Errorf("run agent: untrusted context message %d must use user role", index)
		}
		if err := message.Validate(); err != nil {
			return fmt.Errorf("run agent: untrusted context message %d: %w", index, err)
		}
	}
	return nil
}

func cloneLLMMessages(messages []llm.Message) []llm.Message {
	clones := make([]llm.Message, len(messages))
	for index := range messages {
		clones[index] = messages[index].Clone()
	}
	return clones
}

func (r *Runtime) normalizeResumeRequest(resume ResumeRequest) (RunRequest, error) {
	if resume.SessionID == "" || resume.RunID == "" || strings.TrimSpace(resume.InterruptID) == "" {
		return RunRequest{}, errors.New("resume agent: session, run, and interrupt ids are required")
	}
	if err := resume.Model.Validate(); err != nil {
		return RunRequest{}, fmt.Errorf("resume agent: %w", err)
	}
	if err := resume.Resolution.Validate(); err != nil {
		return RunRequest{}, fmt.Errorf("resume agent: %w", err)
	}
	if resume.Resolution.Status == tool.ResultInterrupted {
		return RunRequest{}, errors.New("resume agent: resolution cannot request another interrupt")
	}
	request := RunRequest{
		SessionID: resume.SessionID, Lane: resume.Lane, RunID: resume.RunID,
		SystemPrompt: resume.SystemPrompt, Model: resume.Model, UntrustedContext: cloneLLMMessages(resume.UntrustedContext), Tools: resume.Tools, Limits: resume.Limits,
	}
	if request.Lane == "" {
		request.Lane = agentsession.MainLane
	}
	if request.Tools == nil {
		registry, err := tool.NewRegistry()
		if err != nil {
			return RunRequest{}, err
		}
		request.Tools = registry
	}
	if request.Limits.MaxSteps <= 0 {
		request.Limits.MaxSteps = 32
	}
	if request.Limits.MaxDuration <= 0 {
		request.Limits.MaxDuration = 30 * time.Minute
	}
	request.Limits = normalizeRetryLimits(request.Limits)
	if err := validateUntrustedContext(request.UntrustedContext); err != nil {
		return RunRequest{}, err
	}
	return request, nil
}

func normalizeRetryLimits(limits RunLimits) RunLimits {
	if limits.MaxModelAttempts <= 0 {
		limits.MaxModelAttempts = 3
	}
	if limits.InitialRetryDelay <= 0 {
		limits.InitialRetryDelay = 250 * time.Millisecond
	}
	if limits.MaxRetryDelay <= 0 {
		limits.MaxRetryDelay = 2 * time.Second
	}
	if limits.MaxRetryDelay < limits.InitialRetryDelay {
		limits.MaxRetryDelay = limits.InitialRetryDelay
	}
	if limits.MaxTotalTokens <= 0 {
		limits.MaxTotalTokens = 2_000_000
	}
	if limits.MaxOutputTokens <= 0 {
		limits.MaxOutputTokens = 256_000
	}
	if limits.MaxCost <= 0 {
		limits.MaxCost = 50
	}
	if limits.MaxToolCalls <= 0 {
		limits.MaxToolCalls = 128
	}
	if limits.MaxRepeatedToolCalls <= 0 {
		limits.MaxRepeatedToolCalls = 4
	}
	if limits.MaxOutputBytes <= 0 {
		limits.MaxOutputBytes = 8 << 20
	}
	if limits.MaxNoProgressSteps <= 0 {
		limits.MaxNoProgressSteps = 6
	}
	return limits
}

func (r *Runtime) buildContext(ctx context.Context, request RunRequest) (contextmanager.Result, error) {
	snapshot, err := r.sessions.Load(ctx, request.SessionID)
	if err != nil {
		return contextmanager.Result{}, err
	}
	entries, err := agentsession.BranchEntries(snapshot, request.Lane)
	if err != nil {
		return contextmanager.Result{}, err
	}
	var messages []contextmanager.Message
	for _, entry := range entries {
		if entry.Type != agentsession.EntryMessage || entry.Message == nil {
			continue
		}
		messages = append(messages, contextmanager.Message{EntryID: string(entry.ID), TurnID: string(entry.RunID), Message: entry.Message.Clone()})
	}
	if len(messages) == 0 {
		return contextmanager.Result{}, errors.New("build agent context: branch has no messages")
	}
	messages[len(messages)-1].Current = true
	if len(request.UntrustedContext) != 0 {
		current := messages[len(messages)-1]
		messages = messages[:len(messages)-1]
		for index, message := range request.UntrustedContext {
			messages = append(messages, contextmanager.Message{
				EntryID: fmt.Sprintf("untrusted-context:%s:%d", request.RunID, index+1), TurnID: string(request.RunID), Message: message.Clone(),
			})
		}
		messages = append(messages, current)
	}
	var budget contextmanager.Budget
	if catalog, ok := r.models.(llm.ModelCatalog); ok {
		if model, describeErr := catalog.DescribeModel(ctx, request.Model); describeErr == nil {
			budget = contextmanager.BudgetForModel(model)
		}
	}
	return r.contexts.Process(ctx, contextmanager.Request{
		Scope:        contextmanager.Scope{SessionID: string(request.SessionID), RunID: string(request.RunID), Model: request.Model},
		SystemPrompt: request.SystemPrompt, Messages: messages, Tools: request.Tools.Definitions(), Budget: budget,
	})
}

func (r *Runtime) streamAssistantWithRetry(ctx context.Context, run RunRequest, model llm.ChatModel, request llm.ChatRequest, dispatcher *eventDispatcher) (llm.Message, error) {
	delay := run.Limits.InitialRetryDelay
	for attempt := 1; attempt <= run.Limits.MaxModelAttempts; attempt++ {
		message, observed, err := r.streamAssistant(ctx, model, request, dispatcher)
		if err == nil {
			return message, nil
		}
		reason, retryable := retryErrorInfo(err)
		if !retryable || observed || attempt == run.Limits.MaxModelAttempts {
			return llm.Message{}, err
		}
		if err := dispatcher.publish(ctx, Event{Kind: EventRetryScheduled, Retry: &RetryEvent{Attempt: attempt + 1, Delay: delay, Reason: reason}}); err != nil {
			return llm.Message{}, err
		}
		if err := r.retryWaiter.Wait(ctx, delay); err != nil {
			return llm.Message{}, err
		}
		if delay < run.Limits.MaxRetryDelay {
			delay *= 2
			if delay > run.Limits.MaxRetryDelay {
				delay = run.Limits.MaxRetryDelay
			}
		}
	}
	return llm.Message{}, errors.New("run model step: retry loop exhausted")
}

func (r *Runtime) streamAssistant(ctx context.Context, model llm.ChatModel, request llm.ChatRequest, dispatcher *eventDispatcher) (llm.Message, bool, error) {
	if err := request.Validate(); err != nil {
		return llm.Message{}, false, err
	}
	stream, err := model.Stream(ctx, request)
	if err != nil {
		return llm.Message{}, false, err
	}
	defer stream.Close()
	thinkingActive := false
	observed := false
	textSanitizer := newTextStreamSanitizer(r.dataPolicy)
	publishText := func(delta string) error {
		if delta == "" {
			return nil
		}
		return dispatcher.publish(ctx, Event{Kind: EventAssistantTextDelta, Assistant: &AssistantEvent{Text: delta}})
	}
	publishBufferedText := func() error {
		return publishText(textSanitizer.Flush())
	}
	for {
		event, err := stream.Recv()
		if err != nil {
			if publishErr := publishBufferedText(); publishErr != nil {
				return llm.Message{}, observed, publishErr
			}
			if errors.Is(err, io.EOF) {
				return llm.Message{}, observed, errors.New("run model step: stream ended without a terminal event")
			}
			return llm.Message{}, observed, err
		}
		if err := event.Validate(); err != nil {
			return llm.Message{}, observed, err
		}
		switch event.Kind {
		case llm.StreamTextDelta:
			observed = true
			if err := publishText(textSanitizer.Write(event.Delta)); err != nil {
				return llm.Message{}, observed, err
			}
		case llm.StreamThinkingDelta:
			observed = true
			if !thinkingActive {
				thinkingActive = true
				if err := dispatcher.publish(ctx, Event{Kind: EventAssistantThinkingChanged, Assistant: &AssistantEvent{ThinkingActive: true}}); err != nil {
					return llm.Message{}, observed, err
				}
			}
		case llm.StreamResponseFailed:
			if err := publishBufferedText(); err != nil {
				return llm.Message{}, observed, err
			}
			return llm.Message{}, observed, &llm.ResponseError{Code: event.ErrorCode, Message: event.ErrorMessage}
		case llm.StreamResponseFinished:
			if err := publishBufferedText(); err != nil {
				return llm.Message{}, observed, err
			}
			if thinkingActive {
				if err := dispatcher.publish(ctx, Event{Kind: EventAssistantThinkingChanged, Assistant: &AssistantEvent{ThinkingActive: false}}); err != nil {
					return llm.Message{}, observed, err
				}
			}
			message := event.Message.Clone()
			if message.Timestamp.IsZero() {
				message.Timestamp = time.Now().UTC()
			}
			return message, observed, nil
		}
	}
}

type retryableError interface {
	Temporary() bool
	RetryReason() string
}

func retryErrorInfo(err error) (string, bool) {
	var transient retryableError
	if !errors.As(err, &transient) || !transient.Temporary() {
		return "", false
	}
	reason := strings.TrimSpace(transient.RetryReason())
	if reason == "" || len(reason) > 128 {
		reason = "transient_model_error"
	}
	return reason, true
}

type timerRetryWaiter struct{}

func (timerRetryWaiter) Wait(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type runBudgetState struct {
	totalTokens  int
	outputTokens int
	cost         float64
	toolCalls    int
	outputBytes  int
	repeated     map[string]int
}

func (r *Runtime) runBudgetReason(ctx context.Context, request RunRequest) (string, error) {
	state, err := r.loadRunBudget(ctx, request)
	if err != nil {
		return "", err
	}
	switch {
	case state.totalTokens >= request.Limits.MaxTotalTokens:
		return "max_total_tokens", nil
	case state.outputTokens >= request.Limits.MaxOutputTokens:
		return "max_output_tokens", nil
	case state.cost >= request.Limits.MaxCost:
		return "max_cost", nil
	case state.outputBytes >= request.Limits.MaxOutputBytes:
		return "max_output_bytes", nil
	default:
		return "", nil
	}
}

func (r *Runtime) toolBudgetReason(ctx context.Context, request RunRequest, call llm.ToolCall) (string, error) {
	state, err := r.loadRunBudget(ctx, request)
	if err != nil {
		return "", err
	}
	if state.toolCalls >= request.Limits.MaxToolCalls {
		return "max_tool_calls", nil
	}
	if state.repeated[toolCallSignature(call.Name, call.Arguments)] >= request.Limits.MaxRepeatedToolCalls {
		return "max_repeated_tool_calls", nil
	}
	return "", nil
}

func (r *Runtime) loadRunBudget(ctx context.Context, request RunRequest) (runBudgetState, error) {
	snapshot, err := r.sessions.Load(ctx, request.SessionID)
	if err != nil {
		return runBudgetState{}, err
	}
	state := runBudgetState{repeated: make(map[string]int)}
	for _, record := range snapshot.Records {
		if record.RunID != request.RunID {
			continue
		}
		switch record.Type {
		case agentsession.RecordUsage:
			if record.Usage == nil {
				continue
			}
			total := record.Usage.TotalTokens
			if total <= 0 {
				total = record.Usage.InputTokens + record.Usage.OutputTokens
			}
			state.totalTokens += total
			state.outputTokens += record.Usage.OutputTokens
			state.cost += record.Usage.Cost
		case agentsession.RecordToolStarted:
			if record.Tool == nil {
				continue
			}
			state.toolCalls++
			state.repeated[toolCallSignature(record.Tool.ToolName, record.Tool.EffectiveArgs)]++
		}
	}
	for _, entry := range snapshot.Entries {
		if entry.RunID == request.RunID && entry.Message != nil && entry.Message.Role == llm.RoleAssistant {
			state.outputBytes += assistantOutputBytes(*entry.Message)
		}
	}
	return state, nil
}

func (r *Runtime) noProgressReason(ctx context.Context, request RunRequest) (string, error) {
	snapshot, err := r.sessions.Load(ctx, request.SessionID)
	if err != nil {
		return "", err
	}
	entries := make(map[agentsession.EntryID]llm.Message)
	for _, entry := range snapshot.Entries {
		if entry.RunID == request.RunID && entry.Message != nil && entry.Message.Role == llm.RoleAssistant {
			entries[entry.ID] = entry.Message.Clone()
		}
	}
	finishedTools := make(map[agentsession.EntryID][]agentsession.ToolData)
	for _, record := range snapshot.Records {
		if record.RunID == request.RunID && record.Type == agentsession.RecordToolFinished && record.Tool != nil {
			finishedTools[record.Tool.AssistantEntryID] = append(finishedTools[record.Tool.AssistantEntryID], *record.Tool)
		}
	}
	type progressStep struct {
		fingerprint string
		allErrors   bool
	}
	var steps []progressStep
	for _, record := range snapshot.Records {
		if record.RunID != request.RunID || record.Type != agentsession.RecordStepFinished || record.Step == nil {
			continue
		}
		assistant, found := entries[record.Step.AssistantEntryID]
		if !found {
			continue
		}
		calls := assistant.ToolCalls()
		results := finishedTools[record.Step.AssistantEntryID]
		if len(calls) == 0 || len(results) < len(calls) {
			continue
		}
		byIndex := make(map[int]agentsession.ToolData, len(results))
		for _, result := range results {
			byIndex[result.ToolIndex] = result
		}
		var fingerprint strings.Builder
		allErrors := true
		complete := true
		for index, call := range calls {
			result, found := byIndex[index]
			if !found || result.ToolCallID != call.ID {
				complete = false
				break
			}
			fingerprint.WriteString(toolCallSignature(call.Name, call.Arguments))
			fingerprint.WriteByte(0)
			fingerprint.WriteString(result.Status)
			fingerprint.WriteByte(0)
			fingerprint.WriteString(result.Summary)
			fingerprint.WriteByte('\n')
			allErrors = allErrors && result.IsError
		}
		if complete {
			digest := sha256.Sum256([]byte(fingerprint.String()))
			steps = append(steps, progressStep{fingerprint: fmt.Sprintf("%x", digest[:]), allErrors: allErrors})
		}
	}
	if len(steps) < request.Limits.MaxNoProgressSteps {
		return "", nil
	}
	recent := steps[len(steps)-request.Limits.MaxNoProgressSteps:]
	allErrors := true
	repeated := true
	for index := range recent {
		allErrors = allErrors && recent[index].allErrors
		if index > 0 && recent[index].fingerprint != recent[0].fingerprint {
			repeated = false
		}
	}
	if allErrors {
		return "no_progress_tool_errors", nil
	}
	if repeated {
		return "no_progress_repeated_step", nil
	}
	return "", nil
}

func assistantOutputBytes(message llm.Message) int {
	total := 0
	for _, content := range message.Content {
		total += len(content.Text)
		if content.ToolCall != nil {
			total += len(content.ToolCall.Name) + len(content.ToolCall.Arguments)
		}
	}
	return total
}

func toolCallSignature(name string, arguments json.RawMessage) string {
	canonical := append([]byte(nil), arguments...)
	var value any
	if json.Unmarshal(arguments, &value) == nil {
		if encoded, err := json.Marshal(value); err == nil {
			canonical = encoded
		}
	}
	digest := sha256.Sum256(append(append([]byte(name), 0), canonical...))
	return fmt.Sprintf("%x", digest[:])
}

func (r *Runtime) cancelToolCalls(ctx context.Context, request RunRequest, dispatcher *eventDispatcher, assistantEntryID agentsession.EntryID, calls []llm.ToolCall, start int, reason string) error {
	for index := start; index < len(calls); index++ {
		if err := r.cancelToolCall(ctx, request, dispatcher, assistantEntryID, index, calls[index], reason); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runtime) cancelToolCall(ctx context.Context, request RunRequest, dispatcher *eventDispatcher, assistantEntryID agentsession.EntryID, index int, call llm.ToolCall, reason string) error {
	entryID, err := r.nextID("entry")
	if err != nil {
		return err
	}
	pending := &agentsession.ToolData{
		AssistantEntryID: assistantEntryID, ToolIndex: index, ToolCallID: call.ID, ToolName: call.Name,
		EffectiveArgs: r.dataPolicy.SanitizeToolArguments(call.Name, call.Arguments), IdempotencyKey: string(request.RunID) + ":" + call.ID,
		ResultEntryID: agentsession.EntryID(entryID), ReplayPolicy: string(tool.ReplayNever),
	}
	if err := r.appendRecord(ctx, request, agentsession.Record{Type: agentsession.RecordToolStarted, RunID: request.RunID, Tool: pending}); err != nil {
		return err
	}
	if err := dispatcher.publish(ctx, Event{Kind: EventToolStarted, Tool: &ToolEvent{CallID: call.ID, Name: call.Name, Status: "cancelled", Summary: reason}}); err != nil {
		return err
	}
	result := tool.Result{Status: tool.ResultCancelled, Content: []llm.Content{{Type: llm.ContentText, Text: "The tool was not executed because the Agent run reached its " + reason + " budget."}}}
	return r.persistToolResult(ctx, request, dispatcher, pending, result)
}

func (r *Runtime) executeTool(ctx context.Context, request RunRequest, dispatcher *eventDispatcher, assistantEntryID agentsession.EntryID, index int, call llm.ToolCall) (tool.Result, *tool.Interrupt, error) {
	resultEntryIDValue, err := r.nextID("entry")
	if err != nil {
		return tool.Result{}, nil, err
	}
	resultEntryID := agentsession.EntryID(resultEntryIDValue)
	executable, found := request.Tools.Lookup(call.Name)
	replayPolicy := tool.ReplayNever
	if found {
		replayPolicy = executable.ReplayPolicy()
	}
	durableArguments := r.dataPolicy.SanitizeToolArguments(call.Name, call.Arguments)
	if len(durableArguments) == 0 || !json.Valid(durableArguments) {
		return tool.Result{}, nil, errors.New("sanitize tool arguments: policy returned invalid JSON")
	}
	if err := r.appendRecord(ctx, request, agentsession.Record{
		Type: agentsession.RecordToolStarted, RunID: request.RunID,
		Tool: &agentsession.ToolData{AssistantEntryID: assistantEntryID, ToolIndex: index, ToolCallID: call.ID, ToolName: call.Name, EffectiveArgs: durableArguments, IdempotencyKey: string(request.RunID) + ":" + call.ID, ResultEntryID: resultEntryID, ReplayPolicy: string(replayPolicy)},
	}); err != nil {
		return tool.Result{}, nil, err
	}
	if err := dispatcher.publish(ctx, Event{Kind: EventToolStarted, Tool: &ToolEvent{CallID: call.ID, Name: call.Name, Status: "running"}}); err != nil {
		return tool.Result{}, nil, err
	}

	var result tool.Result
	if !found {
		result = tool.Result{Status: tool.ResultInvalid, Content: []llm.Content{{Type: llm.ContentText, Text: "The requested tool is not registered for this run."}}}
	} else {
		progress := &toolProgressSink{dispatcher: dispatcher, callID: call.ID, name: call.Name}
		result, err = request.Tools.Execute(ctx, tool.Call{ID: call.ID, Name: call.Name, Arguments: call.Arguments, IdempotencyKey: string(request.RunID) + ":" + call.ID}, progress)
		if err != nil {
			if ctx.Err() != nil {
				return tool.Result{}, nil, ctx.Err()
			}
			result = tool.Result{Status: tool.ResultFailed, Content: []llm.Content{{Type: llm.ContentText, Text: "The tool failed because its execution capability was unavailable."}}}
		}
	}
	if result.Status == tool.ResultInterrupted {
		interrupt := *result.Interrupt
		interrupt.Payload = append(json.RawMessage(nil), result.Interrupt.Payload...)
		if err := r.appendRecord(ctx, request, agentsession.Record{
			Type: agentsession.RecordInterruptRequested, RunID: request.RunID,
			Interrupt: &agentsession.InterruptData{InterruptID: interrupt.ID, Kind: interrupt.Kind, ToolCallID: call.ID, Payload: append(json.RawMessage(nil), interrupt.Payload...)},
		}); err != nil {
			return tool.Result{}, nil, err
		}
		return result, &interrupt, nil
	}
	pending := &agentsession.ToolData{AssistantEntryID: assistantEntryID, ToolIndex: index, ToolCallID: call.ID, ToolName: call.Name, IdempotencyKey: string(request.RunID) + ":" + call.ID, ResultEntryID: resultEntryID, ReplayPolicy: string(replayPolicy)}
	if err := r.persistToolResult(ctx, request, dispatcher, pending, result); err != nil {
		return tool.Result{}, nil, err
	}
	return result, nil, nil
}

func (r *Runtime) persistToolResult(ctx context.Context, request RunRequest, dispatcher *eventDispatcher, pending *agentsession.ToolData, result tool.Result) error {
	result = r.dataPolicy.SanitizeToolResult(pending.ToolName, result)
	if err := result.Validate(); err != nil {
		return fmt.Errorf("sanitize tool result: %w", err)
	}
	message := llm.Message{
		Role: llm.RoleTool, ToolCallID: pending.ToolCallID, ToolName: pending.ToolName, IsError: result.IsError(), Content: cloneContent(result.Content), Details: append(json.RawMessage(nil), result.Details...), Timestamp: time.Now().UTC(),
	}
	if _, err := r.sessions.AppendEntry(ctx, request.SessionID, request.Lane, agentsession.Entry{ID: pending.ResultEntryID, RunID: request.RunID, Type: agentsession.EntryMessage, Message: &message}); err != nil {
		return err
	}
	summary := toolResultSummary(result)
	if err := r.appendRecord(ctx, request, agentsession.Record{
		Type: agentsession.RecordToolFinished, RunID: request.RunID,
		Tool: &agentsession.ToolData{AssistantEntryID: pending.AssistantEntryID, ToolIndex: pending.ToolIndex, ToolCallID: pending.ToolCallID, ToolName: pending.ToolName, IdempotencyKey: pending.IdempotencyKey, ResultEntryID: pending.ResultEntryID, ReplayPolicy: pending.ReplayPolicy, Status: string(result.Status), IsError: result.IsError(), Summary: summary},
	}); err != nil {
		return err
	}
	return dispatcher.publish(ctx, Event{Kind: EventToolFinished, Tool: &ToolEvent{CallID: pending.ToolCallID, Name: pending.ToolName, Status: string(result.Status), Summary: summary, Details: append(json.RawMessage(nil), result.Details...)}})
}

func (r *Runtime) persistSummaries(ctx context.Context, request RunRequest, summaries []contextmanager.Summary, dispatcher *eventDispatcher) error {
	if len(summaries) == 0 {
		return nil
	}
	snapshot, err := r.sessions.Load(ctx, request.SessionID)
	if err != nil {
		return err
	}
	existing := make(map[string]struct{})
	for _, entry := range snapshot.Entries {
		if entry.Compaction != nil {
			existing[entry.Compaction.SourceDigest+"\x00"+entry.Compaction.StrategyVersion] = struct{}{}
		}
	}
	for _, summary := range summaries {
		identity := summary.SourceDigest + "\x00" + summary.StrategyVersion
		if _, found := existing[identity]; found {
			continue
		}
		summary.Text = strings.TrimSpace(r.dataPolicy.SanitizeText(summary.Text))
		if summary.Text == "" || contextmanager.ValidateSummaryFacts(summary.Text, summary.Facts) != nil {
			return errors.New("persist context summary: data policy removed required summary facts")
		}
		if err := dispatcher.publish(ctx, Event{Kind: EventCompactionStarted, Compaction: &CompactionEvent{SourceDigest: summary.SourceDigest, FromEntryID: summary.CoversFromEntryID, ToEntryID: summary.CoversToEntryID}}); err != nil {
			return err
		}
		entryID, err := r.nextID("entry")
		if err != nil {
			return err
		}
		_, err = r.sessions.AppendEntry(ctx, request.SessionID, request.Lane, agentsession.Entry{
			ID: agentsession.EntryID(entryID), RunID: request.RunID, Type: agentsession.EntryCompaction,
			Compaction: &agentsession.Compaction{Summary: summary.Text, CoversFromEntryID: agentsession.EntryID(summary.CoversFromEntryID), CoversToEntryID: agentsession.EntryID(summary.CoversToEntryID), SourceDigest: summary.SourceDigest, Strategy: summary.Strategy, StrategyVersion: summary.StrategyVersion, SummaryModel: summary.Model, Usage: summary.Usage},
		})
		if err != nil {
			return err
		}
		existing[identity] = struct{}{}
		if err := dispatcher.publish(ctx, Event{Kind: EventCompactionFinished, Compaction: &CompactionEvent{SourceDigest: summary.SourceDigest, FromEntryID: summary.CoversFromEntryID, ToEntryID: summary.CoversToEntryID}}); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runtime) sanitizeContextMessages(messages []contextmanager.Message) ([]contextmanager.Message, error) {
	safe := make([]contextmanager.Message, len(messages))
	for index := range messages {
		safe[index] = messages[index]
		safe[index].Message = r.dataPolicy.SanitizeMessage(messages[index].Message)
		if err := safe[index].Message.Validate(); err != nil {
			return nil, fmt.Errorf("sanitize context message %d: %w", index, err)
		}
	}
	return safe, nil
}

func (r *Runtime) finishRun(ctx context.Context, request RunRequest, dispatcher *eventDispatcher, steps int, status RunStatus, reason string) error {
	outcome := string(status)
	if err := r.appendRecord(context.WithoutCancel(ctx), request, agentsession.Record{Type: agentsession.RecordOperationFinished, RunID: request.RunID, Operation: &agentsession.OperationData{Outcome: outcome}}); err != nil {
		return err
	}
	return dispatcher.publish(context.WithoutCancel(ctx), Event{Kind: EventRunFinished, Terminal: &TerminalEvent{Status: outcome, Reason: reason, Steps: steps}})
}

func (r *Runtime) failRun(ctx context.Context, request RunRequest, dispatcher *eventDispatcher, steps int, operation string, cause error) (RunResult, error) {
	status := RunFailed
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) || ctx.Err() != nil {
		status = RunAborted
	}
	finishCtx := context.WithoutCancel(ctx)
	safeCause := r.safeError(cause)
	recordErr := r.appendRecord(finishCtx, request, agentsession.Record{Type: agentsession.RecordOperationFinished, RunID: request.RunID, Operation: &agentsession.OperationData{Outcome: string(status), ErrorCode: operation, ErrorMessage: safeCause.Error()}})
	eventErr := dispatcher.publish(finishCtx, Event{Kind: EventRunFailed, Terminal: &TerminalEvent{Status: string(status), Reason: operation, Steps: steps}})
	return RunResult{RunID: request.RunID, Status: status, Steps: steps, Reason: operation}, errors.Join(safeCause, recordErr, eventErr)
}

func (r *Runtime) appendRecord(ctx context.Context, request RunRequest, record agentsession.Record) error {
	id, err := r.nextID("record")
	if err != nil {
		return err
	}
	record.ID = agentsession.RecordID(id)
	_, err = r.sessions.AppendRecord(ctx, request.SessionID, request.Lane, record)
	return err
}

func (r *Runtime) nextID(prefix string) (string, error) {
	value, err := r.ids.Next(prefix)
	if err != nil {
		return "", fmt.Errorf("generate %s id: %w", prefix, err)
	}
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("generate %s id: generator returned empty value", prefix)
	}
	return value, nil
}

type randomIDGenerator struct{}

func (randomIDGenerator) Next(prefix string) (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(value), nil
}

type eventDispatcher struct {
	runtime   *Runtime
	sink      EventSink
	sessionID agentsession.ID
	runID     agentsession.RunID
	sequence  uint64
}

func (d *eventDispatcher) publish(ctx context.Context, event Event) error {
	id, err := d.runtime.nextID("event")
	if err != nil {
		return err
	}
	d.sequence++
	event.ID = id
	event.Sequence = d.sequence
	event.SessionID = d.sessionID
	event.RunID = d.runID
	event.Timestamp = time.Now().UTC()
	return d.sink.PublishAgentEvent(ctx, event)
}

type toolProgressSink struct {
	dispatcher *eventDispatcher
	callID     string
	name       string
}

func (s *toolProgressSink) PublishToolProgress(ctx context.Context, progress tool.Progress) error {
	policy := s.dispatcher.runtime.dataPolicy
	return s.dispatcher.publish(ctx, Event{Kind: EventToolProgress, Tool: &ToolEvent{
		CallID: s.callID, Name: s.name, Status: "running", Summary: policy.SanitizeText(progress.Summary),
		Details: policy.SanitizeToolArguments(s.name, progress.Details),
	}})
}

type identityDataPolicy struct{}

func (identityDataPolicy) SanitizeMessage(message llm.Message) llm.Message { return message.Clone() }
func (identityDataPolicy) SanitizeToolArguments(_ string, arguments json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), arguments...)
}
func (identityDataPolicy) SanitizeToolResult(_ string, result tool.Result) tool.Result {
	return result.Clone()
}
func (identityDataPolicy) SanitizeText(value string) string { return value }

func (identityDataPolicy) NewTextStreamSanitizer() TextStreamSanitizer {
	return identityTextStreamSanitizer{}
}

type identityTextStreamSanitizer struct{}

func (identityTextStreamSanitizer) Write(value string) string { return value }
func (identityTextStreamSanitizer) Flush() string             { return "" }

type bufferedTextStreamSanitizer struct {
	policy DataPolicy
	text   strings.Builder
}

func newTextStreamSanitizer(policy DataPolicy) TextStreamSanitizer {
	if streaming, ok := policy.(StreamingDataPolicy); ok {
		if sanitizer := streaming.NewTextStreamSanitizer(); sanitizer != nil {
			return sanitizer
		}
	}
	return &bufferedTextStreamSanitizer{policy: policy}
}

func (s *bufferedTextStreamSanitizer) Write(value string) string {
	s.text.WriteString(value)
	return ""
}

func (s *bufferedTextStreamSanitizer) Flush() string {
	value := s.policy.SanitizeText(s.text.String())
	s.text.Reset()
	return value
}

type policyError struct {
	message string
	cause   error
}

func (e policyError) Error() string { return e.message }
func (e policyError) Unwrap() error { return e.cause }

func (r *Runtime) safeError(cause error) error {
	if cause == nil {
		return errors.New("the operation failed")
	}
	message := strings.TrimSpace(r.dataPolicy.SanitizeText(cause.Error()))
	if message == "" {
		message = "the operation failed without a safe diagnostic"
	}
	if message == cause.Error() {
		return cause
	}
	return policyError{message: message, cause: cause}
}

func laneLeaf(snapshot agentsession.Snapshot, lane agentsession.Lane) (agentsession.EntryID, error) {
	for _, pointer := range snapshot.Lanes {
		if pointer.Lane == lane {
			return pointer.LeafID, nil
		}
	}
	return "", fmt.Errorf("run agent: lane %q not found", lane)
}

func findResumeState(snapshot agentsession.Snapshot, runID agentsession.RunID, interruptID string) (*agentsession.InterruptData, *agentsession.ToolData, bool, error) {
	recovery := agentsession.AnalyzeRecovery(snapshot)
	runPending := false
	for _, pending := range recovery.PendingRuns {
		if pending == runID {
			runPending = true
			break
		}
	}
	if !runPending {
		return nil, nil, false, fmt.Errorf("resume agent run %q: operation is not pending", runID)
	}
	var interrupt *agentsession.InterruptData
	for _, pending := range recovery.PendingInterrupts {
		if pending.RunID == runID && pending.InterruptID == interruptID {
			interrupt = &agentsession.InterruptData{InterruptID: pending.InterruptID, Kind: pending.Kind, ToolCallID: pending.ToolCallID, Payload: append(json.RawMessage(nil), pending.Payload...)}
			break
		}
	}
	if interrupt == nil {
		return nil, nil, false, fmt.Errorf("resume agent run %q: interrupt %q is not pending", runID, interruptID)
	}
	var pendingTool *agentsession.ToolData
	toolFinished := false
	for index := len(snapshot.Records) - 1; index >= 0; index-- {
		record := snapshot.Records[index]
		if record.RunID != runID || record.Tool == nil || record.Tool.ToolCallID != interrupt.ToolCallID {
			continue
		}
		if record.Type == agentsession.RecordToolFinished {
			toolFinished = true
		}
		if record.Type == agentsession.RecordToolStarted {
			value := *record.Tool
			value.EffectiveArgs = append(json.RawMessage(nil), record.Tool.EffectiveArgs...)
			pendingTool = &value
			break
		}
	}
	if pendingTool == nil {
		return nil, nil, false, fmt.Errorf("resume agent run %q: interrupted tool %q has no durable start", runID, interrupt.ToolCallID)
	}
	return interrupt, pendingTool, toolFinished, nil
}

func assistantMessage(snapshot agentsession.Snapshot, entryID agentsession.EntryID) (llm.Message, error) {
	for _, entry := range snapshot.Entries {
		if entry.ID == entryID && entry.Type == agentsession.EntryMessage && entry.Message != nil && entry.Message.Role == llm.RoleAssistant {
			return entry.Message.Clone(), nil
		}
	}
	return llm.Message{}, fmt.Errorf("assistant entry %q was not found", entryID)
}

func pendingToolResultExists(snapshot agentsession.Snapshot, entryID agentsession.EntryID) bool {
	for _, entry := range snapshot.Entries {
		if entry.ID == entryID && entry.Type == agentsession.EntryMessage && entry.Message != nil && entry.Message.Role == llm.RoleTool {
			return true
		}
	}
	return false
}

func completedStepCount(snapshot agentsession.Snapshot, runID agentsession.RunID) int {
	steps := 0
	for _, record := range snapshot.Records {
		if record.RunID == runID && record.Type == agentsession.RecordStepFinished && record.Step != nil && record.Step.Attempt > steps {
			steps = record.Step.Attempt
		}
	}
	return steps
}

func unwrapMessages(messages []contextmanager.Message) []llm.Message {
	values := make([]llm.Message, len(messages))
	for index, message := range messages {
		values[index] = message.Message.Clone()
	}
	return values
}

func messagePointer(message llm.Message) *llm.Message {
	clone := message.Clone()
	return &clone
}

func cloneContent(content []llm.Content) []llm.Content {
	clones := make([]llm.Content, len(content))
	for index, item := range content {
		clones[index] = item.Clone()
	}
	return clones
}

func toolResultSummary(result tool.Result) string {
	for _, content := range result.Content {
		if content.Type == llm.ContentText {
			const limit = 512
			if len(content.Text) <= limit {
				return content.Text
			}
			return content.Text[:limit] + "...[truncated]"
		}
	}
	return string(result.Status)
}
