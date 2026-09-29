package codingagent

import "time"

// InstructionStatus describes why a project instruction source is or is not
// available to a Coding Agent run.
type InstructionStatus string

const (
	// InstructionLoaded means a canonical AGENTS.md file was validated and loaded.
	InstructionLoaded InstructionStatus = "loaded"
	// InstructionNotFound means the canonical instruction source does not exist.
	InstructionNotFound InstructionStatus = "not_found"
	// InstructionIgnored means a discovered source is intentionally not loaded.
	InstructionIgnored InstructionStatus = "ignored"
	// InstructionFailed means a canonical source could not be loaded safely.
	InstructionFailed InstructionStatus = "failed"
)

// InstructionSource is the bounded, content-free provenance of one project
// instruction discovery result.
type InstructionSource struct {
	Source     string            `json:"source"`
	Scope      string            `json:"scope"`
	SHA256     string            `json:"sha256,omitempty"`
	Status     InstructionStatus `json:"status"`
	Diagnostic string            `json:"diagnostic,omitempty"`
}

// InstructionReport is the stable product projection produced alongside the
// exact lower-trust instruction context sent to the model.
type InstructionReport struct {
	Sources []InstructionSource `json:"sources"`
}

// ContextCategory identifies one stable context ownership bucket.
type ContextCategory string

const (
	ContextSystem         ContextCategory = "system"
	ContextTask           ContextCategory = "task"
	ContextInstructions   ContextCategory = "instructions"
	ContextSkills         ContextCategory = "skills"
	ContextHistory        ContextCategory = "history"
	ContextToolResults    ContextCategory = "tool_results"
	ContextArtifacts      ContextCategory = "artifacts"
	ContextReservedOutput ContextCategory = "reserved_output"
)

// ContextCategoryStat is a content-free category estimate for one prepared request.
type ContextCategoryStat struct {
	Category ContextCategory `json:"category"`
	Tokens   int             `json:"tokens"`
	Items    int             `json:"items"`
	Source   string          `json:"source"`
}

// ContextDegradation is a bounded, product-safe context fallback explanation.
type ContextDegradation struct {
	Kind   string `json:"kind"`
	Reason string `json:"reason"`
}

// ContextReport describes the latest model request prepared for the active
// conversation lane. It never contains prompt, message, file, or Tool bodies.
type ContextReport struct {
	Available          bool                  `json:"available"`
	RunID              RunID                 `json:"run_id,omitempty"`
	Attempt            int                   `json:"attempt,omitempty"`
	CountSource        string                `json:"count_source,omitempty"`
	Estimated          bool                  `json:"estimated"`
	EstimatedInput     int                   `json:"estimated_input"`
	ProviderExact      bool                  `json:"provider_exact"`
	ProviderInput      int                   `json:"provider_input,omitempty"`
	ContextWindow      int                   `json:"context_window,omitempty"`
	InputBudget        int                   `json:"input_budget,omitempty"`
	ReservedOutput     int                   `json:"reserved_output,omitempty"`
	SafetyMargin       int                   `json:"safety_margin,omitempty"`
	SummarizeThreshold int                   `json:"summarize_threshold,omitempty"`
	HardLimit          int                   `json:"hard_limit,omitempty"`
	BudgetSource       string                `json:"budget_source,omitempty"`
	Compacted          bool                  `json:"compacted"`
	Categories         []ContextCategoryStat `json:"categories"`
	Degradations       []ContextDegradation  `json:"degradations,omitempty"`
}

// WorkspaceSnapshot is a bounded, content-free observation of the active
// worktree used by presentation layers. It never exposes absolute paths or
// changed file names.
type WorkspaceSnapshot struct {
	WorkspaceID  WorkspaceID `json:"workspace_id"`
	WorktreeID   WorktreeID  `json:"worktree_id"`
	DisplayName  string      `json:"display_name"`
	Branch       string      `json:"branch,omitempty"`
	Head         string      `json:"head,omitempty"`
	Available    bool        `json:"available"`
	Detached     bool        `json:"detached,omitempty"`
	Dirty        bool        `json:"dirty"`
	ChangedFiles int         `json:"changed_files,omitempty"`
	ObservedAt   time.Time   `json:"observed_at,omitempty"`
}

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
	TurnID             TurnID
	RunID              RunID
	ChildAgentID       ChildAgentID
	NodeID             NodeID
	Role               string
	InterruptID        string
	Kind               string
	ToolCallID         string
	Summary            string
	Proposed           *ProposedChange
	CanGrantSession    bool
	PlanID             PlanID
	PlanVersion        uint64
	PlanDigest         string
	PlanCompletion     PlanCompletionMode
	PlanStrategy       ExecutionStrategy
	PlanRecommendation StrategyRecommendation
	PlanEntryReason    PlanEntryReasonCode
	PlanReplanReason   PlanReplanReasonCode
	Clarification      *ClarificationPrompt
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
	ByPhase          []PhaseMetrics
	PlanTurns        int
	PlanApprovals    int
	PlanRevisions    int
	PlanApprovalRate float64
	PlanRevisionRate float64
	WorkspaceDrifts  int
	Replans          int
	Workflow         WorkflowMetrics
	Strategy         StrategyMetrics
}

// StrategyMetrics compares trusted recommendations with the user's approved
// execution choice without exposing Plan or model internals.
type StrategyMetrics struct {
	Recommendations           int
	MultiAgentRecommendations int
	AutoEligible              int
	ApprovedSelections        int
	SelectedMultiAgent        int
	OverridesToSingle         int
	ByRecommended             []StrategyCount
	BySelected                []StrategyCount
	Outcomes                  []StrategyOutcomeMetrics
}

type StrategyCount struct {
	Strategy ExecutionStrategy
	Count    int
}

// StrategyOutcomeMetrics supplies the durable comparison dimensions used by
// the P8 evaluation set: completion, rework, elapsed time, and total resources.
type StrategyOutcomeMetrics struct {
	Strategy        ExecutionStrategy
	Turns           int
	CompletedTurns  int
	FailedTurns     int
	CancelledTurns  int
	Runs            int
	FailedRuns      int
	Retries         int
	Replans         int
	WorkspaceDrifts int
	Steps           int
	TotalTokens     int
	Cost            float64
	Elapsed         time.Duration
}

// WorkflowMetrics separates serial Workflow cost and reliability from Direct
// execution while still counting one user request as one Product Turn.
type WorkflowMetrics struct {
	Turns          int
	CompletedTurns int
	FailedTurns    int
	CancelledTurns int
	NodeRuns       int
	FailedNodeRuns int
	Retries        int
	Steps          int
	TotalTokens    int
	Cost           float64
	Elapsed        time.Duration
}

// PhaseMetrics aggregates durable Run cost, elapsed time, and failures by the
// trusted Product Turn phase that produced them.
type PhaseMetrics struct {
	Phase       TurnPhase
	Runs        int
	FailedRuns  int
	TotalTokens int
	Cost        float64
	Elapsed     time.Duration
}

// TurnSnapshot is the bounded Product Turn state shown by presentation layers.
type TurnSnapshot struct {
	ID                  TurnID
	Phase               TurnPhase
	Status              TurnStatus
	Strategy            ExecutionStrategy
	RunCount            int
	Revision            uint64
	ApprovedPlanVersion uint64
}

// PlanVersionSummary identifies one immutable Plan revision in history.
type PlanVersionSummary struct {
	ID        PlanID
	Version   uint64
	Digest    string
	Goal      string
	Changes   []string
	CreatedAt time.Time
}

// PlanSnapshot is the bounded structured Plan shown by presentation layers.
type PlanSnapshot struct {
	ID                     PlanID
	TurnID                 TurnID
	Version                uint64
	Digest                 string
	Goal                   string
	Scope                  PlanScope
	Findings               []string
	Assumptions            []string
	Risks                  []string
	Steps                  []PlanStep
	AcceptanceCriteria     []string
	RecommendedStrategy    ExecutionStrategy
	StrategyRecommendation StrategyRecommendation
	WorkspaceRelevant      bool
	CompletionMode         PlanCompletionMode
	Changes                []string
	RevisionReason         string
	ApprovedVersion        uint64
	WorkspaceDrift         *WorkspaceDrift
	Replan                 *PlanReplanRequest
}

// WorkflowNodeSnapshot is the bounded, product-safe progress of one durable node.
type WorkflowNodeSnapshot struct {
	ID                 NodeID
	Goal               string
	DependsOn          []NodeID
	Role               string
	Capability         string
	Executor           string
	PolicyVersion      uint32
	Status             string
	Attempts           int
	MaxAttempts        int
	AcceptanceCriteria []string
	ReadPaths          []string
	WritePaths         []string
	ResultRef          string
	Failure            string
	StartedAt          time.Time
	FinishedAt         time.Time
}

// ChildAgentSnapshot is the bounded lifecycle and structured output shown to
// users. It intentionally excludes the child Agent transcript and raw prompts.
type ChildAgentSnapshot struct {
	ID            ChildAgentID
	Kind          ChildAgentKind
	TurnID        TurnID
	WorkflowID    string
	NodeID        NodeID
	Role          string
	Profile       CapabilityProfile
	PolicyVersion uint32
	Status        ChildAgentStatus
	Attempt       int
	Goal          string
	ReadPaths     []string
	WritePaths    []string
	Conclusion    string
	Evidence      []AgentTaskEvidence
	Validation    []string
	Artifacts     []string
	Unresolved    []string
	Failure       string
	ManagedStatus ManagedWorktreeStatus
	ChangeSetID   string
	ChangeFiles   []string
	PatchArtifact string
	IntegratedAt  time.Time
	StartedAt     time.Time
	CompletedAt   time.Time
}

// WorkflowSnapshot is sufficient to reconstruct progress without live Events.
type WorkflowSnapshot struct {
	ID             string
	TurnID         TurnID
	PlanID         PlanID
	PlanVersion    uint64
	PlanDigest     string
	Strategy       ExecutionStrategy
	Status         string
	Revision       uint64
	CurrentNode    NodeID
	CompletedNodes int
	BlockedNodes   int
	MaxRuns        int
	UsedRuns       int
	MaxAgentSteps  int
	UsedAgentSteps int
	WaitingReason  string
	Nodes          []WorkflowNodeSnapshot
}

// Snapshot is the authoritative Coding Agent state exposed to UI and future clients.
type Snapshot struct {
	Revision                 uint64
	Session                  Session
	Workspace                WorkspaceSnapshot
	RuntimeState             RuntimeState
	Transcript               []TranscriptItem
	PendingInterrupts        []PendingInterrupt
	RecoveryActions          []RecoveryAction
	RecoveryWarnings         []string
	Metrics                  SessionMetrics
	ActiveTurn               *TurnSnapshot
	ActivePlan               *PlanSnapshot
	ActiveWorkflow           *WorkflowSnapshot
	ChildAgents              []ChildAgentSnapshot
	PlanHistory              []PlanVersionSummary
	PendingPlanApproval      bool
	PendingPlanEntryApproval bool
	PendingPlanReplan        bool
}
