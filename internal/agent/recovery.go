package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	agentsession "github.com/eaglc/codepilot/internal/agent/session"
	"github.com/eaglc/codepilot/internal/llm"
	"github.com/eaglc/codepilot/internal/tool"
)

// RecoverRequest resolves exactly one action from a freshly rebuilt durable
// RecoveryPlan. Automatic callers stop at the next journal boundary so product
// startup never silently continues a model conversation.
type RecoverRequest struct {
	SessionID        agentsession.ID
	Lane             agentsession.Lane
	RunID            agentsession.RunID
	ActionID         string
	Decision         agentsession.RecoveryDecision
	Automatic        bool
	ContinueRun      bool
	SystemPrompt     string
	Model            llm.ModelRef
	UntrustedContext []llm.Message
	Tools            *tool.Registry
	Limits           RunLimits
}

// Recover applies one typed crash-recovery action. The plan is rebuilt and the
// action identity is checked immediately before any Tool or journal mutation.
func (r *Runtime) Recover(ctx context.Context, recover RecoverRequest, sink EventSink) (RunResult, error) {
	if r == nil {
		return RunResult{}, errors.New("recover agent: runtime is nil")
	}
	request, err := r.normalizeRecoverRequest(recover)
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
		return RunResult{}, fmt.Errorf("recover agent: load session: %w", err)
	}
	action, err := recoveryAction(snapshot, request.RunID, recover.ActionID)
	if err != nil {
		return RunResult{}, err
	}
	decision, err := recoveryDecision(action, recover.Decision, recover.Automatic)
	if err != nil {
		return RunResult{}, err
	}
	if err := dispatcher.publish(runCtx, Event{Kind: EventRunResumed}); err != nil {
		return RunResult{}, fmt.Errorf("recover agent: publish resumed event: %w", err)
	}
	if decision == agentsession.RecoveryAbandonRun {
		return r.abandonPendingRun(runCtx, request, dispatcher, snapshot)
	}
	if action.Kind == agentsession.RecoveryResolveInterrupt {
		return RunResult{}, errors.New("recover agent: pending external input must be resolved or the run must be abandoned")
	}
	if action.Kind == agentsession.RecoveryDecideRun {
		return r.markRecoveredRunFailed(runCtx, request, dispatcher, "recovery_missing_user_message")
	}
	if action.Tool != nil {
		interrupt, err := r.recoverPendingTool(runCtx, request, dispatcher, snapshot, *action.Tool, action.Kind, decision, recover.Automatic)
		if err != nil {
			return RunResult{}, err
		}
		if interrupt != nil {
			return RunResult{RunID: request.RunID, Status: RunInterrupted, Steps: completedStepCount(snapshot, request.RunID), Reason: "tool_interrupted", Interrupt: interrupt}, nil
		}
		if !recover.ContinueRun {
			return RunResult{RunID: request.RunID, Status: RunInterrupted, Steps: completedStepCount(snapshot, request.RunID), Reason: "recovery_checkpoint"}, nil
		}
	}
	return r.continueRecoveredRun(runCtx, request, dispatcher)
}

func (r *Runtime) normalizeRecoverRequest(recover RecoverRequest) (RunRequest, error) {
	if recover.SessionID == "" || recover.RunID == "" || strings.TrimSpace(recover.ActionID) == "" {
		return RunRequest{}, errors.New("recover agent: session, run, and action ids are required")
	}
	if err := recover.Model.Validate(); err != nil {
		return RunRequest{}, fmt.Errorf("recover agent: %w", err)
	}
	request := RunRequest{
		SessionID: recover.SessionID, Lane: recover.Lane, RunID: recover.RunID,
		SystemPrompt: recover.SystemPrompt, Model: recover.Model, UntrustedContext: cloneLLMMessages(recover.UntrustedContext), Tools: recover.Tools, Limits: recover.Limits,
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

func recoveryAction(snapshot agentsession.Snapshot, runID agentsession.RunID, actionID string) (agentsession.RecoveryAction, error) {
	for _, action := range agentsession.BuildRecoveryPlan(snapshot).Actions {
		if action.RunID == runID && action.ID == actionID {
			return action, nil
		}
	}
	return agentsession.RecoveryAction{}, fmt.Errorf("recover agent run %q: action %q is stale or no longer pending", runID, actionID)
}

func recoveryDecision(action agentsession.RecoveryAction, requested agentsession.RecoveryDecision, automatic bool) (agentsession.RecoveryDecision, error) {
	if automatic {
		if !action.Automatic {
			return "", fmt.Errorf("recover agent action %q is not eligible for automatic execution", action.ID)
		}
		switch action.Kind {
		case agentsession.RecoveryReconcileTool, agentsession.RecoveryRetryTool:
			return agentsession.RecoveryRetry, nil
		default:
			return "", fmt.Errorf("recover agent action %q has unsupported automatic kind %q", action.ID, action.Kind)
		}
	}
	if requested == "" && action.Kind == agentsession.RecoveryReconcileTool {
		return agentsession.RecoveryRetry, nil
	}
	for _, allowed := range action.Decisions {
		if requested == allowed {
			return requested, nil
		}
	}
	return "", fmt.Errorf("recover agent action %q does not allow decision %q", action.ID, requested)
}

func (r *Runtime) recoverPendingTool(ctx context.Context, request RunRequest, dispatcher *eventDispatcher, snapshot agentsession.Snapshot, pending agentsession.PendingTool, kind agentsession.RecoveryActionKind, decision agentsession.RecoveryDecision, automatic bool) (*tool.Interrupt, error) {
	durable := agentsession.ToolData{
		AssistantEntryID: pending.AssistantEntryID, ToolIndex: pending.ToolIndex,
		ToolCallID: pending.ToolCallID, ToolName: pending.ToolName,
		EffectiveArgs: append(json.RawMessage(nil), pending.EffectiveArgs...), IdempotencyKey: pending.IdempotencyKey,
		ResultEntryID: pending.ResultEntryID, ReplayPolicy: pending.ReplayPolicy,
	}
	if kind == agentsession.RecoveryReconcileTool {
		result, err := recoveredResultEntry(snapshot, pending)
		if err != nil {
			return nil, err
		}
		return nil, r.persistRecoveredToolFinish(ctx, request, dispatcher, &durable, result)
	}
	var result tool.Result
	switch decision {
	case agentsession.RecoveryConfirmExecuted:
		result = recoveryToolResult(tool.ResultCompleted, "The user confirmed that this Tool completed before CodePilot restarted.")
	case agentsession.RecoveryMarkFailed:
		result = recoveryToolResult(tool.ResultFailed, "The Tool outcome was marked failed during crash recovery.")
	case agentsession.RecoveryRetry:
		executable, found := request.Tools.Lookup(pending.ToolName)
		if !found {
			return nil, fmt.Errorf("recover agent Tool %q: Tool is no longer registered", pending.ToolName)
		}
		if string(executable.ReplayPolicy()) != pending.ReplayPolicy {
			return nil, fmt.Errorf("recover agent Tool %q: replay policy changed from %q to %q", pending.ToolName, pending.ReplayPolicy, executable.ReplayPolicy())
		}
		if automatic && pending.ReplayPolicy != string(tool.ReplaySafe) && pending.ReplayPolicy != string(tool.ReplayIdempotent) {
			return nil, fmt.Errorf("recover agent Tool %q: replay policy %q requires an explicit decision", pending.ToolName, pending.ReplayPolicy)
		}
		if pending.ReplayPolicy == string(tool.ReplayIdempotent) && pending.IdempotencyKey == "" {
			return nil, fmt.Errorf("recover agent Tool %q: original idempotency key is missing", pending.ToolName)
		}
		progress := &toolProgressSink{dispatcher: dispatcher, callID: pending.ToolCallID, name: pending.ToolName}
		var err error
		result, err = request.Tools.Execute(ctx, tool.Call{
			ID: pending.ToolCallID, Name: pending.ToolName, Arguments: pending.EffectiveArgs, IdempotencyKey: pending.IdempotencyKey,
		}, progress)
		if err != nil {
			return nil, fmt.Errorf("recover agent Tool %q: revalidation or execution failed: %w", pending.ToolName, err)
		}
		if result.Status == tool.ResultInterrupted {
			interrupt := result.Interrupt
			if err := r.appendRecord(ctx, request, agentsession.Record{
				Type: agentsession.RecordInterruptRequested, RunID: request.RunID,
				Interrupt: &agentsession.InterruptData{InterruptID: interrupt.ID, Kind: interrupt.Kind, ToolCallID: pending.ToolCallID, Payload: append(json.RawMessage(nil), interrupt.Payload...)},
			}); err != nil {
				return nil, err
			}
			if err := dispatcher.publish(ctx, Event{Kind: EventRunInterrupted, Interrupt: &InterruptEvent{ID: interrupt.ID, Kind: interrupt.Kind}}); err != nil {
				return nil, err
			}
			value := *interrupt
			value.Payload = append(json.RawMessage(nil), interrupt.Payload...)
			return &value, nil
		}
	default:
		return nil, fmt.Errorf("recover agent Tool %q: unsupported decision %q", pending.ToolName, decision)
	}
	return nil, r.persistToolResult(ctx, request, dispatcher, &durable, result)
}

func recoveredResultEntry(snapshot agentsession.Snapshot, pending agentsession.PendingTool) (tool.Result, error) {
	for _, entry := range snapshot.Entries {
		if entry.ID != pending.ResultEntryID || entry.Type != agentsession.EntryMessage || entry.Message == nil {
			continue
		}
		message := entry.Message
		if message.Role != llm.RoleTool || message.ToolCallID != pending.ToolCallID || message.ToolName != pending.ToolName {
			return tool.Result{}, fmt.Errorf("recover agent Tool %q: durable result entry does not match its Tool start", pending.ToolName)
		}
		status := tool.ResultCompleted
		if message.IsError {
			status = tool.ResultFailed
		}
		return tool.Result{Status: status, Content: cloneContent(message.Content), Details: append(json.RawMessage(nil), message.Details...)}, nil
	}
	return tool.Result{}, fmt.Errorf("recover agent Tool %q: durable result entry %q was not found", pending.ToolName, pending.ResultEntryID)
}

func (r *Runtime) persistRecoveredToolFinish(ctx context.Context, request RunRequest, dispatcher *eventDispatcher, pending *agentsession.ToolData, result tool.Result) error {
	summary := toolResultSummary(result)
	if err := r.appendRecord(ctx, request, agentsession.Record{
		Type: agentsession.RecordToolFinished, RunID: request.RunID,
		Tool: &agentsession.ToolData{
			AssistantEntryID: pending.AssistantEntryID, ToolIndex: pending.ToolIndex,
			ToolCallID: pending.ToolCallID, ToolName: pending.ToolName, IdempotencyKey: pending.IdempotencyKey,
			ResultEntryID: pending.ResultEntryID, ReplayPolicy: pending.ReplayPolicy,
			Status: string(result.Status), IsError: result.IsError(), Summary: summary,
		},
	}); err != nil {
		return err
	}
	return dispatcher.publish(ctx, Event{Kind: EventToolFinished, Tool: &ToolEvent{
		CallID: pending.ToolCallID, Name: pending.ToolName, Status: string(result.Status), Summary: summary, Details: append(json.RawMessage(nil), result.Details...),
	}})
}

func (r *Runtime) continueRecoveredRun(ctx context.Context, request RunRequest, dispatcher *eventDispatcher) (RunResult, error) {
	snapshot, err := r.sessions.Load(ctx, request.SessionID)
	if err != nil {
		return RunResult{}, fmt.Errorf("recover agent: reload session: %w", err)
	}
	latestStartedAttempt, latestStartedSequence := 0, uint64(0)
	latestFinishedAttempt := 0
	var latestFinishedEntry agentsession.EntryID
	for _, record := range snapshot.Records {
		if record.RunID != request.RunID || record.Step == nil {
			continue
		}
		switch record.Type {
		case agentsession.RecordStepStarted:
			if record.Sequence >= latestStartedSequence {
				latestStartedAttempt, latestStartedSequence = record.Step.Attempt, record.Sequence
			}
		case agentsession.RecordStepFinished:
			if record.Step.Attempt >= latestFinishedAttempt {
				latestFinishedAttempt, latestFinishedEntry = record.Step.Attempt, record.Step.AssistantEntryID
			}
		}
	}
	if latestStartedAttempt > latestFinishedAttempt {
		if assistantEntry, found := unfinishedAssistantEntry(snapshot, request.RunID, latestStartedSequence); found {
			if err := r.appendRecord(ctx, request, agentsession.Record{
				Type: agentsession.RecordStepFinished, RunID: request.RunID,
				Step: &agentsession.StepData{Attempt: latestStartedAttempt, AssistantEntryID: assistantEntry.ID, StopReason: string(assistantEntry.Message.StopReason)},
			}); err != nil {
				return RunResult{}, err
			}
			if err := dispatcher.publish(ctx, Event{Kind: EventStepFinished, Step: &StepEvent{Number: latestStartedAttempt, AssistantEntryID: string(assistantEntry.ID), StopReason: string(assistantEntry.Message.StopReason)}}); err != nil {
				return RunResult{}, err
			}
			return r.continueAfterAssistant(ctx, request, dispatcher, snapshot, assistantEntry.ID, latestStartedAttempt)
		}
		model, err := r.models.CreateModel(ctx, request.Model)
		if err != nil {
			return r.failRun(ctx, request, dispatcher, latestFinishedAttempt, "create_model_during_recovery", err)
		}
		return r.runSteps(ctx, request, dispatcher, model, latestStartedAttempt)
	}
	if latestFinishedAttempt > 0 {
		return r.continueAfterAssistant(ctx, request, dispatcher, snapshot, latestFinishedEntry, latestFinishedAttempt)
	}
	model, err := r.models.CreateModel(ctx, request.Model)
	if err != nil {
		return r.failRun(ctx, request, dispatcher, 0, "create_model_during_recovery", err)
	}
	return r.runSteps(ctx, request, dispatcher, model, 1)
}

func (r *Runtime) continueAfterAssistant(ctx context.Context, request RunRequest, dispatcher *eventDispatcher, snapshot agentsession.Snapshot, assistantEntryID agentsession.EntryID, step int) (RunResult, error) {
	assistant, err := assistantMessage(snapshot, assistantEntryID)
	if err != nil {
		return r.failRun(ctx, request, dispatcher, step, "load_recovered_assistant", err)
	}
	calls := assistant.ToolCalls()
	if err := validateExclusiveControlCalls(request.Tools, calls); err != nil {
		return r.failRun(ctx, request, dispatcher, step, "exclusive_control_conflict", err)
	}
	if len(calls) == 0 {
		if err := r.finishRun(ctx, request, dispatcher, step, RunCompleted, "recovered_model_completed"); err != nil {
			return RunResult{}, err
		}
		message := assistant.Clone()
		return RunResult{RunID: request.RunID, Status: RunCompleted, FinalMessage: &message, Steps: step, Reason: "recovered_model_completed"}, nil
	}
	started, finished := runToolFacts(snapshot, request.RunID)
	for index, call := range calls {
		if finished[call.ID] {
			continue
		}
		if started[call.ID] {
			return RunResult{}, fmt.Errorf("recover agent Tool %q: another recovery action is required", call.Name)
		}
		if reason, budgetErr := r.toolBudgetReason(ctx, request, call); budgetErr != nil {
			return r.failRun(ctx, request, dispatcher, step, "inspect_tool_budget_during_recovery", budgetErr)
		} else if reason != "" {
			for remaining := index; remaining < len(calls); remaining++ {
				candidate := calls[remaining]
				if finished[candidate.ID] {
					continue
				}
				if started[candidate.ID] {
					return RunResult{}, fmt.Errorf("recover agent Tool %q: another recovery action is required", candidate.Name)
				}
				if err := r.cancelToolCall(ctx, request, dispatcher, assistantEntryID, remaining, candidate, reason); err != nil {
					return r.failRun(ctx, request, dispatcher, step, "cancel_budgeted_tools_during_recovery", err)
				}
			}
			if err := r.finishRun(ctx, request, dispatcher, step, RunLimitReached, reason); err != nil {
				return RunResult{}, err
			}
			return RunResult{RunID: request.RunID, Status: RunLimitReached, Steps: step, Reason: reason}, nil
		}
		_, interrupted, err := r.executeTool(ctx, request, dispatcher, assistantEntryID, index, call)
		if err != nil {
			return r.failRun(ctx, request, dispatcher, step, "execute_tool_during_recovery", err)
		}
		if interrupted != nil {
			if err := dispatcher.publish(ctx, Event{Kind: EventRunInterrupted, Interrupt: &InterruptEvent{ID: interrupted.ID, Kind: interrupted.Kind}}); err != nil {
				return r.failRun(ctx, request, dispatcher, step, "publish_recovered_interrupt", err)
			}
			return RunResult{RunID: request.RunID, Status: RunInterrupted, Steps: step, Reason: "tool_interrupted", Interrupt: interrupted}, nil
		}
	}
	if step >= request.Limits.MaxSteps {
		if err := r.finishRun(ctx, request, dispatcher, step, RunLimitReached, "max_steps"); err != nil {
			return RunResult{}, err
		}
		return RunResult{RunID: request.RunID, Status: RunLimitReached, Steps: step, Reason: "max_steps"}, nil
	}
	model, err := r.models.CreateModel(ctx, request.Model)
	if err != nil {
		return r.failRun(ctx, request, dispatcher, step, "create_model_after_recovery", err)
	}
	return r.runSteps(ctx, request, dispatcher, model, step+1)
}

func unfinishedAssistantEntry(snapshot agentsession.Snapshot, runID agentsession.RunID, afterSequence uint64) (agentsession.Entry, bool) {
	var candidate agentsession.Entry
	for _, entry := range snapshot.Entries {
		if entry.RunID == runID && entry.Sequence > afterSequence && entry.Type == agentsession.EntryMessage && entry.Message != nil && entry.Message.Role == llm.RoleAssistant && entry.Sequence > candidate.Sequence {
			candidate = entry
		}
	}
	return candidate, candidate.ID != ""
}

func runToolFacts(snapshot agentsession.Snapshot, runID agentsession.RunID) (map[string]bool, map[string]bool) {
	started, finished := make(map[string]bool), make(map[string]bool)
	for _, record := range snapshot.Records {
		if record.RunID != runID || record.Tool == nil {
			continue
		}
		if record.Type == agentsession.RecordToolStarted {
			started[record.Tool.ToolCallID] = true
		} else if record.Type == agentsession.RecordToolFinished {
			finished[record.Tool.ToolCallID] = true
		}
	}
	return started, finished
}

func (r *Runtime) abandonPendingRun(ctx context.Context, request RunRequest, dispatcher *eventDispatcher, snapshot agentsession.Snapshot) (RunResult, error) {
	state := agentsession.AnalyzeRecovery(snapshot)
	for _, pending := range state.PendingTools {
		if pending.RunID != request.RunID {
			continue
		}
		durable := agentsession.ToolData{
			AssistantEntryID: pending.AssistantEntryID, ToolIndex: pending.ToolIndex,
			ToolCallID: pending.ToolCallID, ToolName: pending.ToolName, EffectiveArgs: append(json.RawMessage(nil), pending.EffectiveArgs...),
			IdempotencyKey: pending.IdempotencyKey, ResultEntryID: pending.ResultEntryID, ReplayPolicy: pending.ReplayPolicy,
		}
		if pending.ResultEntryPresent {
			result, err := recoveredResultEntry(snapshot, pending)
			if err != nil {
				return RunResult{}, err
			}
			if err := r.persistRecoveredToolFinish(ctx, request, dispatcher, &durable, result); err != nil {
				return RunResult{}, err
			}
		} else if err := r.persistToolResult(ctx, request, dispatcher, &durable, recoveryToolResult(tool.ResultCancelled, "The unfinished Tool was cancelled because the turn was abandoned during recovery.")); err != nil {
			return RunResult{}, err
		}
	}
	for _, pending := range state.PendingInterrupts {
		if pending.RunID == request.RunID {
			if err := r.appendRecord(ctx, request, agentsession.Record{
				Type: agentsession.RecordInterruptResolved, RunID: request.RunID,
				Interrupt: &agentsession.InterruptData{InterruptID: pending.InterruptID, Kind: pending.Kind, ToolCallID: pending.ToolCallID, Decision: string(tool.ResultCancelled)},
			}); err != nil {
				return RunResult{}, err
			}
		}
	}
	if err := r.finishRun(ctx, request, dispatcher, completedStepCount(snapshot, request.RunID), RunAborted, "abandoned_during_recovery"); err != nil {
		return RunResult{}, err
	}
	return RunResult{RunID: request.RunID, Status: RunAborted, Steps: completedStepCount(snapshot, request.RunID), Reason: "abandoned_during_recovery"}, nil
}

func (r *Runtime) markRecoveredRunFailed(ctx context.Context, request RunRequest, dispatcher *eventDispatcher, reason string) (RunResult, error) {
	steps := 0
	if snapshot, err := r.sessions.Load(ctx, request.SessionID); err == nil {
		steps = completedStepCount(snapshot, request.RunID)
	}
	if err := r.appendRecord(context.WithoutCancel(ctx), request, agentsession.Record{
		Type: agentsession.RecordOperationFinished, RunID: request.RunID,
		Operation: &agentsession.OperationData{Outcome: string(RunFailed), ErrorCode: reason, ErrorMessage: "The interrupted turn was marked failed during recovery."},
	}); err != nil {
		return RunResult{}, err
	}
	if err := dispatcher.publish(context.WithoutCancel(ctx), Event{Kind: EventRunFailed, Terminal: &TerminalEvent{Status: string(RunFailed), Reason: reason, Steps: steps}}); err != nil {
		return RunResult{}, err
	}
	return RunResult{RunID: request.RunID, Status: RunFailed, Steps: steps, Reason: reason}, nil
}

func recoveryToolResult(status tool.ResultStatus, message string) tool.Result {
	return tool.Result{Status: status, Content: []llm.Content{{Type: llm.ContentText, Text: message}}}
}
