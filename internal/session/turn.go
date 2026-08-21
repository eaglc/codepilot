package session

import "time"

// TurnStatus is the evidence-based outcome recorded for a completed turn.
type TurnStatus string

const (
	// TurnCompleted indicates that the agent returned a final response without
	// applying a patch. The final text remains the source of the actual outcome.
	TurnCompleted TurnStatus = "completed"
	// TurnVerified indicates that all required checks passed.
	TurnVerified TurnStatus = "verified"
	// TurnUnverified indicates that changes exist without sufficient check evidence.
	TurnUnverified TurnStatus = "unverified"
	// TurnFailed indicates that the turn could not complete successfully.
	TurnFailed TurnStatus = "failed"
	// TurnCancelled indicates that the user cancelled the turn.
	TurnCancelled TurnStatus = "cancelled"
)

// RunLimits bounds agent iteration, duration, and output sizes.
type RunLimits struct {
	MaxSteps              int
	MaxTurnDuration       time.Duration
	CommandTimeout        time.Duration
	ToolResultMaxBytes    int
	CommandOutputMaxBytes int
}

// TurnScope is the immutable trusted context captured when a turn starts.
type TurnScope struct {
	TurnID            TurnID
	SessionID         SessionID
	WorkspaceID       WorkspaceID
	WorktreeID        WorktreeID
	WorktreeRoot      string
	ProviderProfileID ProviderProfileID
	ModelID           string
	PermissionMode    PermissionMode
	Limits            RunLimits
}

// CheckOutcome describes the structured outcome of project verification.
type CheckOutcome string

const (
	// CheckNotRun indicates that no verification command was attempted.
	CheckNotRun CheckOutcome = "not-run"
	// CheckPassed indicates that all required verification commands passed.
	CheckPassed CheckOutcome = "passed"
	// CheckFailed indicates that verification ran and found a relevant failure.
	CheckFailed CheckOutcome = "failed"
	// CheckInconclusive indicates that unrelated or pre-existing failures blocked verification.
	CheckInconclusive CheckOutcome = "inconclusive"
	// CheckDenied indicates that the user denied verification execution.
	CheckDenied CheckOutcome = "denied"
	// CheckTimedOut indicates that verification exceeded its command deadline.
	CheckTimedOut CheckOutcome = "timed-out"
	// CheckUnavailable indicates that the required runtime or environment was unavailable.
	CheckUnavailable CheckOutcome = "unavailable"
	// CheckCancelled indicates that verification stopped because the turn was cancelled.
	CheckCancelled CheckOutcome = "cancelled"
)

// CheckSummary stores bounded, structured evidence used to classify a turn.
type CheckSummary struct {
	Outcome   CheckOutcome
	Summary   string
	Truncated bool
}

// TurnRequest contains the immutable scope and neutral conversation input.
type TurnRequest struct {
	Scope       TurnScope
	History     []Message
	UserMessage Message
}

// TurnResult contains facts returned by a coding agent without deciding status.
type TurnResult struct {
	FinalText         string
	Steps             int
	TerminationReason string
	CheckSummary      CheckSummary
	AppliedPatches    []PatchRecord
}

// CodingAgentConfig contains the trusted immutable dependencies of one agent.
type CodingAgentConfig struct {
	SessionID         SessionID
	WorkspaceID       WorkspaceID
	WorktreeID        WorktreeID
	WorktreeRoot      string
	ProviderProfileID ProviderProfileID
	ModelID           string
	Limits            RunLimits
}

// TurnRecord is the durable provider-neutral summary of a completed turn.
type TurnRecord struct {
	ID                TurnID
	SessionID         SessionID
	UserMessageID     MessageID
	Status            TurnStatus
	TerminationReason string
	ProviderProfileID ProviderProfileID
	ModelID           string
	Steps             int
	CheckSummary      CheckSummary
	StartedAt         time.Time
	CompletedAt       time.Time
}

// PatchRecord describes one patch that was successfully applied to a worktree.
type PatchRecord struct {
	ID        PatchID
	SessionID SessionID
	TurnID    TurnID
	Patch     string
	Files     []PatchedFile
	AppliedAt time.Time
}

// PatchedFile records the content hashes around an applied file change.
type PatchedFile struct {
	Path       string
	BeforeHash string
	AfterHash  string
}

// TurnCommit groups the durable records written when a turn finishes.
type TurnCommit struct {
	Session          Session
	AssistantMessage Message
	Turn             TurnRecord
}
