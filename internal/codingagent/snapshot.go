package codingagent

import "time"

// TranscriptRole identifies a product-safe transcript item author.
type TranscriptRole string

const (
	TranscriptRoleUser      TranscriptRole = "user"
	TranscriptRoleAssistant TranscriptRole = "assistant"
	TranscriptRoleTool      TranscriptRole = "tool"
	TranscriptRoleSystem    TranscriptRole = "system"
)

// TranscriptKind identifies the safe presentation form of a transcript item.
type TranscriptKind string

const (
	TranscriptText       TranscriptKind = "text"
	TranscriptImage      TranscriptKind = "image"
	TranscriptThinking   TranscriptKind = "thinking_status"
	TranscriptToolCall   TranscriptKind = "tool_call"
	TranscriptToolResult TranscriptKind = "tool_result"
	TranscriptCompaction TranscriptKind = "compaction"
	TranscriptFailure    TranscriptKind = "failure"
)

// TranscriptTool contains display-safe tool identity without arguments or runtime handles.
type TranscriptTool struct {
	CallID string
	Name   string
	Status string
	// Summary is the compact, single-line text shown while the activity is collapsed.
	Summary string
	// Detail is the bounded tool output shown only after the user expands the activity.
	Detail  string
	IsError bool
	Diff    *InlineDiff
	// Resources lists bounded, display-safe files and line counts touched by the tool.
	Resources []ToolResource
}

// ToolResource describes one display-safe file range or change count.
type ToolResource struct {
	Path         string
	StartLine    int
	EndLine      int
	AddedLines   int
	DeletedLines int
}

// InlineDiff is a display-safe applied diff rendered in conversation order.
type InlineDiff struct {
	Text  string
	Files []string
}

// ProposedChange is the only whitelisted mutation preview exposed across the
// Coding product boundary. Integrity metadata and original tool arguments stay
// private in the durable interrupt payload.
type ProposedChange struct {
	Kind         string
	Summary      string
	Path         string
	PlanID       string
	Command      string
	Language     string
	Diff         InlineDiff
	AddedLines   int
	DeletedLines int
}

// TranscriptItem is a product-safe projection consumed by presentation layers.
type TranscriptItem struct {
	ID            string
	SourceEntryID string
	TurnID        TurnID
	Role          TranscriptRole
	Kind          TranscriptKind
	Text          string
	MIMEType      string
	Tool          *TranscriptTool
	Timestamp     time.Time
}

// PendingInterrupt is a product-safe resumable input request.
type PendingInterrupt struct {
	TurnID          TurnID
	RunID           RunID
	InterruptID     string
	Kind            string
	ToolCallID      string
	Summary         string
	Proposed        *ProposedChange
	CanGrantSession bool
}

// RecoveryDecision is a product-level operator choice for unfinished work.
type RecoveryDecision string

const (
	RecoveryRetry           RecoveryDecision = "retry"
	RecoveryConfirmExecuted RecoveryDecision = "confirm_executed"
	RecoveryMarkFailed      RecoveryDecision = "mark_failed"
	RecoveryAbandonTurn     RecoveryDecision = "abandon_turn"
)

// RecoveryAction is the secret-free projection of one Agent recovery boundary.
// Tool arguments, journal payloads and idempotency keys never cross this API.
type RecoveryAction struct {
	ID           string
	TurnID       TurnID
	RunID        RunID
	Kind         string
	ToolCallID   string
	ToolName     string
	ReplayPolicy string
	Automatic    bool
	Summary      string
	Decisions    []RecoveryDecision
}

// SessionMetrics is a presentation-safe projection derived from the durable
// active conversation branch. Token and cost totals include model and summary
// usage; step and timing fields describe the latest turn on that branch.
type SessionMetrics struct {
	LatestTurnID     TurnID
	LatestRunID      RunID
	LatestPhase      TurnPhase
	LatestProfile    CapabilityProfile
	LatestTurnStatus TurnStatus
	Steps            int
	InputTokens      int
	OutputTokens     int
	CacheReadTokens  int
	CacheWriteTokens int
	ReasoningTokens  int
	TotalTokens      int
	Cost             float64
	ContextTokens    int
	StartedAt        time.Time
	FinishedAt       time.Time
	Elapsed          time.Duration
}

// TurnSnapshot is the bounded Product Turn state shown by presentation layers.
type TurnSnapshot struct {
	ID       TurnID
	Phase    TurnPhase
	Status   TurnStatus
	Strategy ExecutionStrategy
	RunCount int
	Revision uint64
}

// Snapshot is the authoritative Coding Agent state exposed to UI and future clients.
type Snapshot struct {
	Revision          uint64
	Session           Session
	RuntimeState      RuntimeState
	Transcript        []TranscriptItem
	PendingInterrupts []PendingInterrupt
	RecoveryActions   []RecoveryAction
	RecoveryWarnings  []string
	Metrics           SessionMetrics
	ActiveTurn        *TurnSnapshot
}
