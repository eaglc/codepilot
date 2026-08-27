package codingagent

import (
	"errors"
	"fmt"
	"strings"
	"time"

	agentsession "github.com/eaglc/codepilot/internal/agent/session"
)

// CapabilityProfile identifies a trusted prompt and tool capability set.
type CapabilityProfile string

const (
	// CapabilityDirect is the existing single-Agent implementation profile.
	CapabilityDirect CapabilityProfile = "direct"
)

// TurnPhase identifies the product-controlled phase of a user request.
type TurnPhase string

const (
	// TurnPhaseDirect executes a request with the existing direct capabilities.
	TurnPhaseDirect TurnPhase = "direct"
)

// TurnStatus identifies durable product-level progress independently of an Agent Run.
type TurnStatus string

const (
	TurnPending     TurnStatus = "pending"
	TurnRunning     TurnStatus = "running"
	TurnInterrupted TurnStatus = "interrupted"
	TurnCompleted   TurnStatus = "completed"
	TurnCancelled   TurnStatus = "cancelled"
	TurnFailed      TurnStatus = "failed"
)

// RunBindingStatus identifies the durable lifecycle of one Run belonging to a Turn.
type RunBindingStatus string

const (
	RunBindingPending     RunBindingStatus = "pending"
	RunBindingRunning     RunBindingStatus = "running"
	RunBindingInterrupted RunBindingStatus = "interrupted"
	RunBindingCompleted   RunBindingStatus = "completed"
	RunBindingCancelled   RunBindingStatus = "cancelled"
	RunBindingFailed      RunBindingStatus = "failed"
	RunBindingHandedOff   RunBindingStatus = "handed_off"
)

// ExecutionStrategy identifies how an approved request is executed.
type ExecutionStrategy string

const (
	// ExecutionSingle runs the request directly with one Agent.
	ExecutionSingle ExecutionStrategy = "single"
)

// RunBinding explicitly relates one generic Agent Run to its Product Turn.
type RunBinding struct {
	RunID       agentsession.RunID   `json:"run_id"`
	UserEntryID agentsession.EntryID `json:"user_entry_id,omitempty"`
	Phase       TurnPhase            `json:"phase"`
	Profile     CapabilityProfile    `json:"profile"`
	Status      RunBindingStatus     `json:"status"`
	StartedAt   time.Time            `json:"started_at,omitempty"`
	FinishedAt  time.Time            `json:"finished_at,omitempty"`
	Reason      string               `json:"reason,omitempty"`
}

// Turn is the durable product identity for one user request across Agent Runs.
// RequestText is retained until the first Run durably appends its user entry so
// recovery can close the create-Turn/start-Run crash gap without inventing input.
type Turn struct {
	ID          TurnID            `json:"id"`
	SessionID   SessionID         `json:"session_id"`
	RequestText string            `json:"request_text"`
	Phase       TurnPhase         `json:"phase"`
	Status      TurnStatus        `json:"status"`
	Strategy    ExecutionStrategy `json:"strategy"`
	Runs        []RunBinding      `json:"runs,omitempty"`
	PlanID      string            `json:"plan_id,omitempty"`
	WorkflowID  string            `json:"workflow_id,omitempty"`
	Revision    uint64            `json:"revision"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
	CompletedAt time.Time         `json:"completed_at,omitempty"`
}

// ActiveRun returns the last non-terminal Run binding, if any.
func (t Turn) ActiveRun() (RunBinding, bool) {
	for index := len(t.Runs) - 1; index >= 0; index-- {
		switch t.Runs[index].Status {
		case RunBindingPending, RunBindingRunning, RunBindingInterrupted:
			return t.Runs[index], true
		}
	}
	return RunBinding{}, false
}

// Run returns the binding for an exact generic Agent Run identity.
func (t Turn) Run(id agentsession.RunID) (RunBinding, bool) {
	for _, binding := range t.Runs {
		if binding.RunID == id {
			return binding, true
		}
	}
	return RunBinding{}, false
}

// ValidateTurn checks the shared Product Turn persistence contract.
func ValidateTurn(value Turn) error {
	if value.ID == "" || value.SessionID == "" || strings.TrimSpace(value.RequestText) == "" {
		return errors.New("Coding turn identity, session, and original request are required")
	}
	if len(value.RequestText) > 1<<20 {
		return errors.New("Coding turn original request exceeds its size limit")
	}
	if value.Phase != TurnPhaseDirect || value.Strategy != ExecutionSingle {
		return fmt.Errorf("Coding turn phase %q or strategy %q is unsupported", value.Phase, value.Strategy)
	}
	switch value.Status {
	case TurnPending, TurnRunning, TurnInterrupted, TurnCompleted, TurnCancelled, TurnFailed:
	default:
		return fmt.Errorf("Coding turn status %q is unsupported", value.Status)
	}
	if value.Revision == 0 || value.CreatedAt.IsZero() || value.UpdatedAt.IsZero() || value.UpdatedAt.Before(value.CreatedAt) {
		return errors.New("Coding turn revision and ordered timestamps are required")
	}
	terminal := value.Status == TurnCompleted || value.Status == TurnCancelled || value.Status == TurnFailed
	if terminal != !value.CompletedAt.IsZero() {
		return errors.New("Coding turn terminal status and completion timestamp must agree")
	}
	if !value.CompletedAt.IsZero() && value.CompletedAt.Before(value.CreatedAt) {
		return errors.New("Coding turn completion cannot precede creation")
	}
	if len(value.Runs) == 0 {
		return errors.New("Coding turn requires at least one Run binding")
	}
	seenRuns := make(map[agentsession.RunID]struct{}, len(value.Runs))
	seenEntries := make(map[agentsession.EntryID]struct{}, len(value.Runs))
	activeRuns := 0
	lastActiveIndex := -1
	for index, binding := range value.Runs {
		if binding.RunID == "" || binding.Phase != value.Phase || binding.Profile != CapabilityDirect {
			return fmt.Errorf("Coding turn run binding %d has invalid identity, phase, or profile", index)
		}
		if _, exists := seenRuns[binding.RunID]; exists {
			return fmt.Errorf("Coding turn run %q is duplicated", binding.RunID)
		}
		seenRuns[binding.RunID] = struct{}{}
		if binding.UserEntryID != "" {
			if _, exists := seenEntries[binding.UserEntryID]; exists {
				return fmt.Errorf("Coding turn user entry %q is duplicated", binding.UserEntryID)
			}
			seenEntries[binding.UserEntryID] = struct{}{}
		}
		if index == 0 && binding.UserEntryID == "" {
			return errors.New("Coding turn first Run requires the original user entry identity")
		}
		if index != 0 && binding.UserEntryID != "" {
			return fmt.Errorf("Coding turn continuation Run %q cannot append another user entry", binding.RunID)
		}
		switch binding.Status {
		case RunBindingPending, RunBindingRunning, RunBindingInterrupted, RunBindingCompleted, RunBindingCancelled, RunBindingFailed, RunBindingHandedOff:
		default:
			return fmt.Errorf("Coding turn run binding %d has unsupported status %q", index, binding.Status)
		}
		if binding.Status == RunBindingPending && (!binding.StartedAt.IsZero() || !binding.FinishedAt.IsZero()) {
			return fmt.Errorf("Coding turn pending run %q cannot have execution timestamps", binding.RunID)
		}
		if binding.Status != RunBindingPending && binding.StartedAt.IsZero() {
			return fmt.Errorf("Coding turn started run %q requires a start timestamp", binding.RunID)
		}
		if !binding.StartedAt.IsZero() && binding.StartedAt.Before(value.CreatedAt) {
			return fmt.Errorf("Coding turn Run %q cannot start before its Turn", binding.RunID)
		}
		finished := binding.Status == RunBindingCompleted || binding.Status == RunBindingCancelled || binding.Status == RunBindingFailed || binding.Status == RunBindingHandedOff
		if finished != !binding.FinishedAt.IsZero() {
			return fmt.Errorf("Coding turn run %q terminal status and finish timestamp must agree", binding.RunID)
		}
		if !binding.FinishedAt.IsZero() && binding.FinishedAt.Before(binding.StartedAt) {
			return fmt.Errorf("Coding turn Run %q cannot finish before it starts", binding.RunID)
		}
		if binding.Status == RunBindingPending || binding.Status == RunBindingRunning || binding.Status == RunBindingInterrupted {
			activeRuns++
			lastActiveIndex = index
		}
	}
	if activeRuns > 1 {
		return errors.New("Coding turn cannot contain multiple active Runs")
	}
	if lastActiveIndex >= 0 && lastActiveIndex != len(value.Runs)-1 {
		return fmt.Errorf("Coding turn Run %q is active before the latest binding", value.Runs[lastActiveIndex].RunID)
	}
	latest := value.Runs[len(value.Runs)-1]
	statusMatches := false
	switch latest.Status {
	case RunBindingPending:
		statusMatches = value.Status == TurnPending || value.Status == TurnRunning
	case RunBindingRunning, RunBindingHandedOff:
		statusMatches = value.Status == TurnRunning
	case RunBindingInterrupted:
		statusMatches = value.Status == TurnInterrupted
	case RunBindingCompleted:
		statusMatches = value.Status == TurnCompleted
	case RunBindingCancelled:
		statusMatches = value.Status == TurnCancelled
	case RunBindingFailed:
		statusMatches = value.Status == TurnFailed
	}
	if !statusMatches {
		return fmt.Errorf("Coding turn status %q does not match latest Run status %q", value.Status, latest.Status)
	}
	return nil
}

// ValidateTurnTransition verifies one compare-and-swap lifecycle update.
func ValidateTurnTransition(previous, next Turn) error {
	if err := ValidateTurn(previous); err != nil {
		return fmt.Errorf("previous Coding turn is invalid: %w", err)
	}
	if err := ValidateTurn(next); err != nil {
		return fmt.Errorf("next Coding turn is invalid: %w", err)
	}
	if previous.ID != next.ID || previous.SessionID != next.SessionID || previous.RequestText != next.RequestText || previous.Phase != next.Phase || previous.Strategy != next.Strategy || !previous.CreatedAt.Equal(next.CreatedAt) {
		return errors.New("Coding turn immutable identity changed")
	}
	if next.Revision != previous.Revision+1 {
		return errors.New("Coding turn revision must advance exactly once")
	}
	if len(next.Runs) < len(previous.Runs) || len(next.Runs) > len(previous.Runs)+1 {
		return errors.New("Coding turn Run bindings must be preserved and appended one at a time")
	}
	for index, before := range previous.Runs {
		after := next.Runs[index]
		if before.RunID != after.RunID || before.UserEntryID != after.UserEntryID || before.Phase != after.Phase || before.Profile != after.Profile {
			return fmt.Errorf("Coding turn Run binding %d identity changed", index)
		}
		if !before.StartedAt.IsZero() && !before.StartedAt.Equal(after.StartedAt) {
			return fmt.Errorf("Coding turn Run %q start timestamp changed", before.RunID)
		}
		if !before.FinishedAt.IsZero() && !before.FinishedAt.Equal(after.FinishedAt) {
			return fmt.Errorf("Coding turn Run %q finish timestamp changed", before.RunID)
		}
		if !validRunBindingTransition(before.Status, after.Status) {
			return fmt.Errorf("Coding turn Run %q cannot transition from %q to %q", before.RunID, before.Status, after.Status)
		}
	}
	if len(next.Runs) != len(previous.Runs) {
		latest := previous.Runs[len(previous.Runs)-1]
		appended := next.Runs[len(next.Runs)-1]
		if previous.Status != TurnRunning || latest.Status != RunBindingHandedOff || appended.Status != RunBindingPending || appended.UserEntryID != "" {
			return errors.New("Coding turn can append a continuation Run only after a control handoff")
		}
	}
	return nil
}

func validRunBindingTransition(previous, next RunBindingStatus) bool {
	if previous == next {
		return true
	}
	switch previous {
	case RunBindingPending:
		return next == RunBindingRunning || next == RunBindingCancelled || next == RunBindingFailed
	case RunBindingRunning:
		return next == RunBindingInterrupted || next == RunBindingCompleted || next == RunBindingCancelled || next == RunBindingFailed || next == RunBindingHandedOff
	case RunBindingInterrupted:
		return next == RunBindingRunning || next == RunBindingCompleted || next == RunBindingCancelled || next == RunBindingFailed || next == RunBindingHandedOff
	default:
		return false
	}
}
