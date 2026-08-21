package session

import "time"

// EventKind identifies a stable product event emitted by the session runtime.
type EventKind string

const (
	// EventTurnStarted indicates that a new turn began.
	EventTurnStarted EventKind = "turn-started"
	// EventAssistantDelta carries streamed assistant text.
	EventAssistantDelta EventKind = "assistant-delta"
	// EventToolStarted indicates that a tool invocation began.
	EventToolStarted EventKind = "tool-started"
	// EventToolCompleted indicates that a tool invocation completed.
	EventToolCompleted EventKind = "tool-completed"
	// EventToolFailed indicates that a tool invocation failed.
	EventToolFailed EventKind = "tool-failed"
	// EventApprovalRequested carries a pending approval request.
	EventApprovalRequested EventKind = "approval-requested"
	// EventApprovalResolved indicates that a pending approval was resolved.
	EventApprovalResolved EventKind = "approval-resolved"
	// EventPatchApplied carries a successfully applied patch record.
	EventPatchApplied EventKind = "patch-applied"
	// EventDiffChanged indicates that a visible diff should be refreshed.
	EventDiffChanged EventKind = "diff-changed"
	// EventTurnCompleted indicates that a turn reached a final result.
	EventTurnCompleted EventKind = "turn-completed"
	// EventTurnCancelled indicates that a turn was cancelled.
	EventTurnCancelled EventKind = "turn-cancelled"
	// EventTurnFailed indicates that a turn ended with an application error.
	EventTurnFailed EventKind = "turn-failed"
	// EventSessionActivated indicates that the active session changed.
	EventSessionActivated EventKind = "session-activated"
	// EventSessionSaved indicates that durable session state was saved.
	EventSessionSaved EventKind = "session-saved"
	// EventSessionSaveFailed indicates that durable session state needs retry.
	EventSessionSaveFailed EventKind = "session-save-failed"
	// EventProviderValidationStarted indicates that provider validation began.
	EventProviderValidationStarted EventKind = "provider-validation-started"
	// EventWorkspaceChanged indicates that the active worktree changed.
	EventWorkspaceChanged EventKind = "workspace-changed"
)

// Event is the stable, secret-free message published to the UI.
type Event struct {
	ID        string
	Sequence  uint64
	SessionID SessionID
	TurnID    TurnID
	Kind      EventKind
	Time      time.Time
	Payload   EventPayload
}

// EventPayload is a typed union of the payloads used by product events.
type EventPayload struct {
	Text     *TextEventPayload
	Tool     *ToolEventPayload
	Approval *ApprovalEventPayload
	Patch    *PatchEventPayload
	Diff     *DiffEventPayload
	Turn     *TurnEventPayload
	Error    *ErrorEventPayload
}

// TextEventPayload contains bounded display text such as an assistant delta.
type TextEventPayload struct {
	Text string
}

// ToolEventPayload contains a secret-free summary of a tool invocation.
type ToolEventPayload struct {
	Name    string
	Status  string
	Summary string
}

// ApprovalEventPayload contains either a request or its resulting decision.
type ApprovalEventPayload struct {
	Request  *ApprovalRequest
	Decision *ApprovalDecision
}

// PatchEventPayload contains the durable record for an applied patch.
type PatchEventPayload struct {
	Record PatchRecord
}

// DiffEventPayload identifies the diff source that changed. Result is carried
// only for transient proposed diffs because they do not exist in the workspace
// reader after the tool call returns.
type DiffEventPayload struct {
	Kind   DiffKind
	Result *DiffResult
}

// TurnEventPayload contains the evidence-based outcome of a completed turn.
type TurnEventPayload struct {
	Status            TurnStatus
	TerminationReason string
	CheckSummary      CheckSummary
}

// ErrorEventPayload contains safe application error details for the UI.
type ErrorEventPayload struct {
	Code        ErrorCode
	Operation   string
	UserMessage string
	Retryable   bool
}
