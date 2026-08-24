package session

import (
	"encoding/json"
	"sort"

	"github.com/eaglc/codepilot/internal/llm"
)

// RecoveryActionKind identifies the next durable boundary that can be handled.
type RecoveryActionKind string

const (
	RecoveryResolveInterrupt RecoveryActionKind = "resolve_interrupt"
	RecoveryReconcileTool    RecoveryActionKind = "reconcile_tool"
	RecoveryRetryTool        RecoveryActionKind = "retry_tool"
	RecoveryDecideTool       RecoveryActionKind = "decide_tool"
	RecoveryContinueRun      RecoveryActionKind = "continue_run"
	RecoveryDecideRun        RecoveryActionKind = "decide_run"
)

// RecoveryDecision is a generic operator decision. Product layers may expose
// a safe subset without forwarding Tool arguments or journal records.
type RecoveryDecision string

const (
	RecoveryRetry           RecoveryDecision = "retry"
	RecoveryConfirmExecuted RecoveryDecision = "confirm_executed"
	RecoveryMarkFailed      RecoveryDecision = "mark_failed"
	RecoveryAbandonRun      RecoveryDecision = "abandon_run"
)

// PendingTool describes a ToolStarted record with no matching durable finish.
// EffectiveArgs and IdempotencyKey are private Agent recovery material.
type PendingTool struct {
	RunID              RunID
	Lane               Lane
	StartedSequence    uint64
	AssistantEntryID   EntryID
	ToolIndex          int
	ToolCallID         string
	ToolName           string
	EffectiveArgs      json.RawMessage
	IdempotencyKey     string
	ResultEntryID      EntryID
	ReplayPolicy       string
	ResultEntryPresent bool
}

// PendingInterrupt describes durable external input that has not been resolved.
type PendingInterrupt struct {
	RunID       RunID
	Lane        Lane
	InterruptID string
	Kind        string
	ToolCallID  string
	Payload     []byte
}

// RecoveryState summarizes incomplete durable work without deciding policy.
type RecoveryState struct {
	PendingRuns       []RunID
	PendingTools      []PendingTool
	PendingInterrupts []PendingInterrupt
}

// RecoveryAction is one typed, stable decision boundary derived from durable
// state. Only the Agent layer receives Tool, which contains private arguments.
type RecoveryAction struct {
	ID        string
	RunID     RunID
	Lane      Lane
	Kind      RecoveryActionKind
	Automatic bool
	Reason    string
	Tool      *PendingTool
	Interrupt *PendingInterrupt
	Decisions []RecoveryDecision
}

// RecoveryPlan contains at most one next action per unfinished run. Rebuilding
// the plan after every persisted action makes recovery restart-safe.
type RecoveryPlan struct {
	Actions []RecoveryAction
}

// AnalyzeRecovery finds unfinished operations, Tools and interrupts.
func AnalyzeRecovery(snapshot Snapshot) RecoveryState {
	startedRuns := make(map[RunID]struct{})
	finishedRuns := make(map[RunID]struct{})
	startedTools := make(map[string]PendingTool)
	finishedTools := make(map[string]struct{})
	requestedInterrupts := make(map[string]PendingInterrupt)
	resolvedInterrupts := make(map[string]struct{})
	entryIDs := make(map[EntryID]struct{}, len(snapshot.Entries))
	for _, entry := range snapshot.Entries {
		entryIDs[entry.ID] = struct{}{}
	}
	for _, record := range snapshot.Records {
		switch record.Type {
		case RecordOperationStarted:
			startedRuns[record.RunID] = struct{}{}
		case RecordOperationFinished:
			finishedRuns[record.RunID] = struct{}{}
		case RecordToolStarted:
			if record.Tool != nil {
				key := recoveryToolKey(record.RunID, record.Tool.ToolCallID)
				_, resultPresent := entryIDs[record.Tool.ResultEntryID]
				startedTools[key] = PendingTool{
					RunID: record.RunID, Lane: record.Lane, StartedSequence: record.Sequence,
					AssistantEntryID: record.Tool.AssistantEntryID, ToolIndex: record.Tool.ToolIndex,
					ToolCallID: record.Tool.ToolCallID, ToolName: record.Tool.ToolName,
					EffectiveArgs:  append(json.RawMessage(nil), record.Tool.EffectiveArgs...),
					IdempotencyKey: record.Tool.IdempotencyKey, ResultEntryID: record.Tool.ResultEntryID,
					ReplayPolicy: record.Tool.ReplayPolicy, ResultEntryPresent: resultPresent,
				}
			}
		case RecordToolFinished:
			if record.Tool != nil {
				finishedTools[recoveryToolKey(record.RunID, record.Tool.ToolCallID)] = struct{}{}
			}
		case RecordInterruptRequested:
			if record.Interrupt != nil {
				key := recoveryInterruptKey(record.RunID, record.Interrupt.InterruptID)
				requestedInterrupts[key] = PendingInterrupt{
					RunID: record.RunID, Lane: record.Lane, InterruptID: record.Interrupt.InterruptID,
					Kind: record.Interrupt.Kind, ToolCallID: record.Interrupt.ToolCallID,
					Payload: append([]byte(nil), record.Interrupt.Payload...),
				}
			}
		case RecordInterruptResolved:
			if record.Interrupt != nil {
				resolvedInterrupts[recoveryInterruptKey(record.RunID, record.Interrupt.InterruptID)] = struct{}{}
			}
		}
	}
	var state RecoveryState
	for runID := range startedRuns {
		if _, finished := finishedRuns[runID]; !finished {
			state.PendingRuns = append(state.PendingRuns, runID)
		}
	}
	pendingRun := make(map[RunID]struct{}, len(state.PendingRuns))
	for _, runID := range state.PendingRuns {
		pendingRun[runID] = struct{}{}
	}
	for key, pending := range startedTools {
		_, runPending := pendingRun[pending.RunID]
		if _, finished := finishedTools[key]; runPending && !finished {
			state.PendingTools = append(state.PendingTools, pending)
		}
	}
	for key, pending := range requestedInterrupts {
		_, runPending := pendingRun[pending.RunID]
		_, runStarted := startedRuns[pending.RunID]
		if _, resolved := resolvedInterrupts[key]; !resolved && (runPending || !runStarted) {
			state.PendingInterrupts = append(state.PendingInterrupts, pending)
		}
	}
	sortRecoveryState(&state)
	return state
}

// BuildRecoveryPlan applies generic replay policy to durable unfinished state.
// It never executes a Tool and never treats an unknown policy as replayable.
func BuildRecoveryPlan(snapshot Snapshot) RecoveryPlan {
	state := AnalyzeRecovery(snapshot)
	toolsByRun := make(map[RunID][]PendingTool)
	interruptsByRun := make(map[RunID][]PendingInterrupt)
	for _, pending := range state.PendingTools {
		toolsByRun[pending.RunID] = append(toolsByRun[pending.RunID], pending)
	}
	for _, pending := range state.PendingInterrupts {
		interruptsByRun[pending.RunID] = append(interruptsByRun[pending.RunID], pending)
	}
	var plan RecoveryPlan
	for _, runID := range state.PendingRuns {
		pendingTools := toolsByRun[runID]
		pendingInterrupts := interruptsByRun[runID]
		if len(pendingInterrupts) != 0 {
			pending := clonePendingInterrupt(pendingInterrupts[0])
			plan.Actions = append(plan.Actions, RecoveryAction{
				ID: recoveryActionID(runID, pending.ToolCallID), RunID: runID, Lane: pending.Lane,
				Kind: RecoveryResolveInterrupt, Reason: "external input is still pending", Interrupt: &pending,
			})
			continue
		}
		if len(pendingTools) != 0 {
			pending := clonePendingTool(pendingTools[0])
			action := RecoveryAction{ID: recoveryActionID(runID, pending.ToolCallID), RunID: runID, Lane: pending.Lane, Tool: &pending}
			switch {
			case pending.ResultEntryPresent:
				action.Kind, action.Automatic, action.Reason = RecoveryReconcileTool, true, "the Tool result is durable but its finish record is missing"
			case pending.ReplayPolicy == string(llm.ReplaySafe):
				action.Kind, action.Automatic, action.Reason = RecoveryRetryTool, true, "the Tool declares safe replay after validation"
				action.Decisions = []RecoveryDecision{RecoveryRetry, RecoveryMarkFailed, RecoveryAbandonRun}
			case pending.ReplayPolicy == string(llm.ReplayIdempotent) && pending.IdempotencyKey != "":
				action.Kind, action.Automatic, action.Reason = RecoveryRetryTool, true, "the original idempotency key is available"
				action.Decisions = []RecoveryDecision{RecoveryRetry, RecoveryMarkFailed, RecoveryAbandonRun}
			default:
				action.Kind, action.Reason = RecoveryDecideTool, "Tool execution may have produced an external side effect"
				action.Decisions = []RecoveryDecision{RecoveryConfirmExecuted, RecoveryMarkFailed, RecoveryRetry, RecoveryAbandonRun}
			}
			plan.Actions = append(plan.Actions, action)
			continue
		}
		if runHasContext(snapshot, runID) {
			plan.Actions = append(plan.Actions, RecoveryAction{
				ID: recoveryActionID(runID, "run"), RunID: runID, Lane: runLane(snapshot, runID),
				Kind: RecoveryContinueRun, Reason: "the previous durable boundary is complete",
				Decisions: []RecoveryDecision{RecoveryRetry, RecoveryAbandonRun},
			})
		} else {
			plan.Actions = append(plan.Actions, RecoveryAction{
				ID: recoveryActionID(runID, "run"), RunID: runID, Lane: runLane(snapshot, runID),
				Kind: RecoveryDecideRun, Reason: "the run started before its user message became durable",
				Decisions: []RecoveryDecision{RecoveryMarkFailed, RecoveryAbandonRun},
			})
		}
	}
	return plan
}

func sortRecoveryState(state *RecoveryState) {
	sort.Slice(state.PendingRuns, func(left, right int) bool { return state.PendingRuns[left] < state.PendingRuns[right] })
	sort.Slice(state.PendingTools, func(left, right int) bool {
		if state.PendingTools[left].RunID != state.PendingTools[right].RunID {
			return state.PendingTools[left].RunID < state.PendingTools[right].RunID
		}
		if state.PendingTools[left].StartedSequence != state.PendingTools[right].StartedSequence {
			return state.PendingTools[left].StartedSequence < state.PendingTools[right].StartedSequence
		}
		return state.PendingTools[left].ToolCallID < state.PendingTools[right].ToolCallID
	})
	sort.Slice(state.PendingInterrupts, func(left, right int) bool {
		if state.PendingInterrupts[left].RunID != state.PendingInterrupts[right].RunID {
			return state.PendingInterrupts[left].RunID < state.PendingInterrupts[right].RunID
		}
		return state.PendingInterrupts[left].InterruptID < state.PendingInterrupts[right].InterruptID
	})
}

func runHasContext(snapshot Snapshot, runID RunID) bool {
	for _, entry := range snapshot.Entries {
		if entry.RunID == runID && entry.Type == EntryMessage && entry.Message != nil {
			return true
		}
	}
	return false
}

func runLane(snapshot Snapshot, runID RunID) Lane {
	for _, record := range snapshot.Records {
		if record.RunID == runID && record.Type == RecordOperationStarted {
			if record.Lane != "" {
				return record.Lane
			}
			return MainLane
		}
	}
	return MainLane
}

func recoveryToolKey(runID RunID, callID string) string { return string(runID) + "\x00" + callID }
func recoveryInterruptKey(runID RunID, interruptID string) string {
	return string(runID) + "\x00" + interruptID
}
func recoveryActionID(runID RunID, boundary string) string { return string(runID) + ":" + boundary }

func clonePendingTool(value PendingTool) PendingTool {
	value.EffectiveArgs = append(json.RawMessage(nil), value.EffectiveArgs...)
	return value
}

func clonePendingInterrupt(value PendingInterrupt) PendingInterrupt {
	value.Payload = append([]byte(nil), value.Payload...)
	return value
}
