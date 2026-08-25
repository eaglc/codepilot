// Package session defines durable, provider-neutral Agent session contracts.
package session

import (
	"encoding/json"
	"time"

	"github.com/eaglc/codepilot/internal/llm"
)

// ID uniquely identifies one durable Agent session.
type ID string

// EntryID uniquely identifies one context-tree entry.
type EntryID string

// RecordID uniquely identifies one execution-journal record.
type RecordID string

// RunID uniquely identifies one user-triggered Agent run.
type RunID string

// Lane identifies a mutable leaf pointer in one session tree.
type Lane string

// MainLane is the default conversation lane.
const MainLane Lane = "main"

// Metadata contains only generic Agent session identity and lifecycle fields.
type Metadata struct {
	ID              ID        `json:"id"`
	ParentSessionID ID        `json:"parent_session_id,omitempty"`
	Name            string    `json:"name,omitempty"`
	Archived        bool      `json:"archived,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// EntryType identifies data in the durable conversation tree.
type EntryType string

const (
	EntryMessage             EntryType = "message"
	EntryModelChange         EntryType = "model_change"
	EntryThinkingLevelChange EntryType = "thinking_level_change"
	EntryActiveToolsChange   EntryType = "active_tools_change"
	EntryCompaction          EntryType = "compaction"
	EntryBranchSummary       EntryType = "branch_summary"
	EntryCustomMessage       EntryType = "custom_message"
)

// Compaction stores a durable, model-neutral summary and its exact source boundary.
type Compaction struct {
	Summary             string           `json:"summary"`
	CoversFromEntryID   EntryID          `json:"covers_from_entry_id"`
	CoversToEntryID     EntryID          `json:"covers_to_entry_id"`
	RetainedFromEntryID EntryID          `json:"retained_from_entry_id,omitempty"`
	SourceDigest        string           `json:"source_digest"`
	Strategy            string           `json:"strategy"`
	StrategyVersion     string           `json:"strategy_version"`
	SummaryModel        llm.ModelRef     `json:"summary_model"`
	Usage               *llm.Usage       `json:"usage,omitempty"`
	Facts               []CompactionFact `json:"facts,omitempty"`
	Details             json.RawMessage  `json:"details,omitempty"`
}

// CompactionFact is a stable source-derived value retained across rolling
// summary generations.
type CompactionFact struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

// BranchSummary stores a summary of history left behind during navigation.
type BranchSummary struct {
	FromEntryID EntryID         `json:"from_entry_id"`
	Summary     string          `json:"summary"`
	Usage       *llm.Usage      `json:"usage,omitempty"`
	Details     json.RawMessage `json:"details,omitempty"`
}

// CustomMessage contains extension-owned content that intentionally enters model context.
type CustomMessage struct {
	Type    string          `json:"type"`
	Content []llm.Content   `json:"content"`
	Details json.RawMessage `json:"details,omitempty"`
}

// Entry is a tagged union persisted in the context tree.
type Entry struct {
	ID        EntryID   `json:"id"`
	Sequence  uint64    `json:"sequence"`
	Lane      Lane      `json:"lane"`
	RunID     RunID     `json:"run_id,omitempty"`
	ParentID  EntryID   `json:"parent_id,omitempty"`
	Timestamp time.Time `json:"timestamp"`
	Type      EntryType `json:"type"`

	Message       *llm.Message   `json:"message,omitempty"`
	Model         *llm.ModelRef  `json:"model,omitempty"`
	ThinkingLevel string         `json:"thinking_level,omitempty"`
	ActiveTools   []string       `json:"active_tools,omitempty"`
	Compaction    *Compaction    `json:"compaction,omitempty"`
	BranchSummary *BranchSummary `json:"branch_summary,omitempty"`
	CustomMessage *CustomMessage `json:"custom_message,omitempty"`
}

// RecordType identifies one durable execution fact.
type RecordType string

const (
	RecordOperationStarted   RecordType = "operation_started"
	RecordAbortRequested     RecordType = "abort_requested"
	RecordOperationFinished  RecordType = "operation_finished"
	RecordStepStarted        RecordType = "step_started"
	RecordStepFinished       RecordType = "step_finished"
	RecordToolStarted        RecordType = "tool_started"
	RecordToolFinished       RecordType = "tool_finished"
	RecordInterruptRequested RecordType = "interrupt_requested"
	RecordInterruptResolved  RecordType = "interrupt_resolved"
	RecordApprovalRequested  RecordType = "approval_requested"
	RecordApprovalResolved   RecordType = "approval_resolved"
	RecordCheckpointSaved    RecordType = "checkpoint_saved"
	RecordUsage              RecordType = "usage"
	RecordLaneForked         RecordType = "lane_forked"
)

// OperationIntent identifies the requested durable operation.
type OperationIntent string

const (
	OperationRun        OperationIntent = "run"
	OperationCompaction OperationIntent = "compaction"
	OperationNavigation OperationIntent = "navigation"
)

// OperationData stores operation start or terminal facts.
type OperationData struct {
	Intent       OperationIntent `json:"intent,omitempty"`
	SourceLeafID EntryID         `json:"source_leaf_id,omitempty"`
	Outcome      string          `json:"outcome,omitempty"`
	ErrorCode    string          `json:"error_code,omitempty"`
	ErrorMessage string          `json:"error_message,omitempty"`
}

// StepData stores one model-step attempt and its assistant entry.
type StepData struct {
	Attempt          int     `json:"attempt"`
	AssistantEntryID EntryID `json:"assistant_entry_id,omitempty"`
	StopReason       string  `json:"stop_reason,omitempty"`
}

// ToolData stores the before/after facts required to audit and recover a tool execution.
type ToolData struct {
	AssistantEntryID EntryID         `json:"assistant_entry_id"`
	ToolIndex        int             `json:"tool_index"`
	ToolCallID       string          `json:"tool_call_id"`
	ToolName         string          `json:"tool_name"`
	EffectiveArgs    json.RawMessage `json:"effective_args,omitempty"`
	IdempotencyKey   string          `json:"idempotency_key,omitempty"`
	ResultEntryID    EntryID         `json:"result_entry_id"`
	ReplayPolicy     string          `json:"replay_policy,omitempty"`
	Status           string          `json:"status,omitempty"`
	IsError          bool            `json:"is_error,omitempty"`
	Summary          string          `json:"summary,omitempty"`
}

// ApprovalData stores a stable external-decision identity without product-specific payloads.
type ApprovalData struct {
	RequestID string          `json:"request_id"`
	Kind      string          `json:"kind"`
	Decision  string          `json:"decision,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

// InterruptData stores the durable external-input boundary for a pending tool.
type InterruptData struct {
	InterruptID string          `json:"interrupt_id"`
	Kind        string          `json:"kind"`
	ToolCallID  string          `json:"tool_call_id"`
	Decision    string          `json:"decision,omitempty"`
	Payload     json.RawMessage `json:"payload,omitempty"`
}

// CheckpointData points at durable resumable runtime state.
type CheckpointData struct {
	CheckpointID string `json:"checkpoint_id"`
	Digest       string `json:"digest"`
}

// LaneForkData records the creation point of a durable branch lane.
type LaneForkData struct {
	Lane        Lane    `json:"lane"`
	FromEntryID EntryID `json:"from_entry_id,omitempty"`
}

// Record is a tagged union in the append-only execution journal.
type Record struct {
	ID        RecordID   `json:"id"`
	Sequence  uint64     `json:"sequence"`
	Lane      Lane       `json:"lane"`
	Timestamp time.Time  `json:"timestamp"`
	Type      RecordType `json:"type"`
	RunID     RunID      `json:"run_id,omitempty"`

	Operation  *OperationData  `json:"operation,omitempty"`
	Step       *StepData       `json:"step,omitempty"`
	Tool       *ToolData       `json:"tool,omitempty"`
	Interrupt  *InterruptData  `json:"interrupt,omitempty"`
	Approval   *ApprovalData   `json:"approval,omitempty"`
	Checkpoint *CheckpointData `json:"checkpoint,omitempty"`
	Usage      *llm.Usage      `json:"usage,omitempty"`
	LaneFork   *LaneForkData   `json:"lane_fork,omitempty"`
}

// LanePointer stores the current leaf of one named lane.
type LanePointer struct {
	Lane   Lane    `json:"lane"`
	LeafID EntryID `json:"leaf_id,omitempty"`
}

// LogItem preserves the shared sequence across entries and records.
type LogItem struct {
	Sequence uint64  `json:"sequence"`
	Entry    *Entry  `json:"entry,omitempty"`
	Record   *Record `json:"record,omitempty"`
}

// Snapshot is an isolated durable view of a session.
type Snapshot struct {
	Metadata Metadata          `json:"metadata"`
	Entries  []Entry           `json:"entries"`
	Records  []Record          `json:"records"`
	Lanes    []LanePointer     `json:"lanes"`
	Log      []LogItem         `json:"log"`
	Warnings []RecoveryWarning `json:"warnings,omitempty"`
}

// RecoveryWarning reports ignored recoverable storage damage.
type RecoveryWarning struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
}
