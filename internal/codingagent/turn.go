package codingagent

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	agentsession "github.com/eaglc/codepilot/internal/agent/session"
	"github.com/eaglc/codepilot/internal/codingagent/roleprofile"
)

// CapabilityProfile identifies a trusted prompt and tool capability set.
type CapabilityProfile string

const (
	// CapabilityDirect is the existing single-Agent implementation profile.
	CapabilityDirect CapabilityProfile = "direct"
	// CapabilityPlan exposes only Plan controls until workspace relevance is declared.
	CapabilityPlan CapabilityProfile = "plan"
	// CapabilityPlanWorkspace adds trusted workspace reads after an explicit
	// relevance handoff from the initial Plan profile.
	CapabilityPlanWorkspace CapabilityProfile = "plan_workspace"
	// CapabilityExplore is a strictly read-only child Agent exploration profile.
	CapabilityExplore CapabilityProfile = CapabilityProfile(roleprofile.ProfileExplore)
	// CapabilityImplement runs one bounded single-Agent Workflow write node.
	CapabilityImplement CapabilityProfile = CapabilityProfile(roleprofile.ProfileImplement)
	// CapabilityValidate runs checks and reads without product-code writes.
	CapabilityValidate CapabilityProfile = CapabilityProfile(roleprofile.ProfileValidate)
	// CapabilityReview inspects source, diffs, and evidence without writes.
	CapabilityReview CapabilityProfile = CapabilityProfile(roleprofile.ProfileReview)
	// CapabilityIntegrate is reserved for trusted serial integration nodes.
	CapabilityIntegrate CapabilityProfile = CapabilityProfile(roleprofile.ProfileIntegrate)
)

// TurnPhase identifies the product-controlled phase of a user request.
type TurnPhase string

const (
	// TurnPhaseDirect executes a request with the existing direct capabilities.
	TurnPhaseDirect TurnPhase = "direct"
	// TurnPhaseAwaitingPlanEntryApproval waits for the user to accept or reject
	// an Agent suggestion to switch the current Direct task into Plan mode.
	TurnPhaseAwaitingPlanEntryApproval TurnPhase = "awaiting_plan_entry_approval"
	// TurnPhasePlanning performs read-only exploration and Plan authoring.
	TurnPhasePlanning TurnPhase = "planning"
	// TurnPhaseAwaitingPlanApproval waits for a decision on one exact Plan revision.
	TurnPhaseAwaitingPlanApproval TurnPhase = "awaiting_plan_approval"
	// TurnPhaseExecuting implements an approved Plan with normal permission checks.
	TurnPhaseExecuting TurnPhase = "executing"
	// TurnPhaseNeedsReplan pauses an execution Run at a product decision boundary
	// after material workspace drift or an Agent-reported Plan deviation.
	TurnPhaseNeedsReplan TurnPhase = "needs_replan"
)

// TurnEntrySource records how the product-level request entered its initial phase.
type TurnEntrySource string

const (
	TurnEntryDirect   TurnEntrySource = "direct"
	TurnEntryUserPlan TurnEntrySource = "user_plan"
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
	// ExecutionWorkflowSingle runs a durable dependency graph serially with one Agent.
	ExecutionWorkflowSingle ExecutionStrategy = "workflow_single"
	// ExecutionWorkflowMultiSerial delegates bounded nodes to independent child
	// Agent sessions while keeping one active Agent at a time.
	ExecutionWorkflowMultiSerial ExecutionStrategy = "workflow_multi_serial"
)

// RunBinding explicitly relates one generic Agent Run to its Product Turn.
type RunBinding struct {
	RunID          agentsession.RunID   `json:"run_id"`
	UserEntryID    agentsession.EntryID `json:"user_entry_id,omitempty"`
	NodeID         NodeID               `json:"node_id,omitempty"`
	ChildAgentID   ChildAgentID         `json:"child_agent_id,omitempty"`
	Phase          TurnPhase            `json:"phase"`
	Profile        CapabilityProfile    `json:"profile"`
	Status         RunBindingStatus     `json:"status"`
	Steps          int                  `json:"steps,omitempty"`
	TerminalOutput json.RawMessage      `json:"terminal_output,omitempty"`
	StartedAt      time.Time            `json:"started_at,omitempty"`
	FinishedAt     time.Time            `json:"finished_at,omitempty"`
	Reason         string               `json:"reason,omitempty"`
}

// Turn is the durable product identity for one user request across Agent Runs.
// RequestText is retained until the first Run durably appends its user entry so
// recovery can close the create-Turn/start-Run crash gap without inventing input.
type Turn struct {
	ID          TurnID            `json:"id"`
	SessionID   SessionID         `json:"session_id"`
	RequestText string            `json:"request_text"`
	EntrySource TurnEntrySource   `json:"entry_source,omitempty"`
	Phase       TurnPhase         `json:"phase"`
	Status      TurnStatus        `json:"status"`
	Strategy    ExecutionStrategy `json:"strategy"`
	Runs        []RunBinding      `json:"runs,omitempty"`
	// PlanEntrySuggestion records the latest bounded Agent proposal to enter
	// Plan mode. The durable Agent interrupt remains the authority for a pending
	// user decision; this copy supports validation, recovery, and audit.
	PlanEntrySuggestion *PlanEntrySuggestion  `json:"plan_entry_suggestion,omitempty"`
	DeclinedPlanReasons []PlanEntryReasonCode `json:"declined_plan_reasons,omitempty"`
	PlanID              PlanID                `json:"plan_id,omitempty"`
	PlanVersion         uint64                `json:"plan_version,omitempty"`
	PlanDigest          string                `json:"plan_digest,omitempty"`
	ApprovedPlanVersion uint64                `json:"approved_plan_version,omitempty"`
	ApprovedPlanDigest  string                `json:"approved_plan_digest,omitempty"`
	ApprovedAt          time.Time             `json:"approved_at,omitempty"`
	WorkspaceDrift      *WorkspaceDrift       `json:"workspace_drift,omitempty"`
	WorkspaceDriftCount uint64                `json:"workspace_drift_count,omitempty"`
	PlanReplan          *PlanReplanRequest    `json:"plan_replan,omitempty"`
	PlanReplanCount     uint64                `json:"plan_replan_count,omitempty"`
	// PendingPlanExploreID is the one serial read-only child delegation requested
	// by the parent Planning Agent. It is cleared only after the child is terminal.
	PendingPlanExploreID ChildAgentID `json:"pending_plan_explore_id,omitempty"`
	WorkflowID           string       `json:"workflow_id,omitempty"`
	Revision             uint64       `json:"revision"`
	CreatedAt            time.Time    `json:"created_at"`
	UpdatedAt            time.Time    `json:"updated_at"`
	CompletedAt          time.Time    `json:"completed_at,omitempty"`
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
	if !validTurnPhase(value.Phase) || !validExecutionStrategy(value.Strategy) {
		return fmt.Errorf("Coding turn phase %q or strategy %q is unsupported", value.Phase, value.Strategy)
	}
	if value.EntrySource != "" && value.EntrySource != TurnEntryDirect && value.EntrySource != TurnEntryUserPlan {
		return fmt.Errorf("Coding turn entry source %q is unsupported", value.EntrySource)
	}
	if value.EntrySource == TurnEntryUserPlan && value.Phase == TurnPhaseDirect {
		return errors.New("User Plan entry cannot use the Direct phase")
	}
	if err := validateTurnPlanEntry(value); err != nil {
		return err
	}
	if err := validateTurnPlanReference(value); err != nil {
		return err
	}
	if err := validateTurnPlanLifecycle(value); err != nil {
		return err
	}
	if value.Strategy == ExecutionSingle && value.WorkflowID != "" {
		return errors.New("Direct single-Agent turn cannot reference a Workflow")
	}
	if value.PendingPlanExploreID != "" && (value.Phase != TurnPhasePlanning || value.Status != TurnRunning || value.Strategy != ExecutionSingle) {
		return errors.New("pending Plan exploration requires one running read-only Planning turn")
	}
	if isWorkflowStrategy(value.Strategy) && (value.Phase == TurnPhaseExecuting || value.Phase == TurnPhaseNeedsReplan) && value.WorkflowID == "" {
		return errors.New("executing Workflow turn requires a durable Workflow identity")
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
		if binding.RunID == "" || !validRunPhaseProfile(binding.Phase, binding.Profile) {
			return fmt.Errorf("Coding turn run binding %d has invalid identity, phase, or profile", index)
		}
		if binding.NodeID != "" && (!isWorkflowStrategy(value.Strategy) || binding.Phase != TurnPhaseExecuting || binding.Profile == CapabilityDirect || binding.Profile == CapabilityPlan || binding.Profile == CapabilityPlanWorkspace || binding.Profile == CapabilityExplore) {
			return fmt.Errorf("Coding turn run %q has an invalid Workflow node binding", binding.RunID)
		}
		if binding.NodeID == "" && (binding.Profile == CapabilityImplement || binding.Profile == CapabilityValidate || binding.Profile == CapabilityReview || binding.Profile == CapabilityIntegrate) {
			return fmt.Errorf("Coding turn run %q requires a Workflow node binding", binding.RunID)
		}
		planExploreChild := binding.ChildAgentID != "" && binding.NodeID == "" && binding.Phase == TurnPhasePlanning && binding.Profile == CapabilityExplore
		workflowChild := binding.ChildAgentID != "" && binding.NodeID != "" && value.Strategy == ExecutionWorkflowMultiSerial && binding.Phase == TurnPhaseExecuting
		if binding.ChildAgentID != "" && !planExploreChild && !workflowChild || binding.ChildAgentID == "" && binding.Profile == CapabilityExplore {
			return fmt.Errorf("Coding turn run %q has an invalid child Agent binding", binding.RunID)
		}
		if len(binding.TerminalOutput) != 0 && (binding.ChildAgentID == "" || len(binding.TerminalOutput) > 64<<10 || !json.Valid(binding.TerminalOutput)) {
			return fmt.Errorf("Coding turn run %q has invalid child terminal output", binding.RunID)
		}
		if binding.ChildAgentID != "" && binding.Status == RunBindingCompleted && len(binding.TerminalOutput) == 0 {
			return fmt.Errorf("Coding turn child run %q completed without structured terminal output", binding.RunID)
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
		if binding.Steps < 0 || binding.Status == RunBindingPending && binding.Steps != 0 {
			return fmt.Errorf("Coding turn run %q has invalid durable step usage", binding.RunID)
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
	if !latestPhaseMatchesTurn(value.Phase, latest.Phase) {
		return fmt.Errorf("Coding turn phase %q does not match latest Run phase %q", value.Phase, latest.Phase)
	}
	statusMatches := false
	switch latest.Status {
	case RunBindingPending:
		statusMatches = value.Status == TurnPending || value.Status == TurnRunning
	case RunBindingRunning, RunBindingHandedOff:
		statusMatches = value.Status == TurnRunning || latest.Status == RunBindingHandedOff && isWorkflowStrategy(value.Strategy) && value.Status == TurnCancelled
	case RunBindingInterrupted:
		statusMatches = value.Status == TurnInterrupted
	case RunBindingCompleted:
		statusMatches = value.Status == TurnCompleted || isWorkflowStrategy(value.Strategy) && (value.Status == TurnRunning || value.Status == TurnCancelled) || latest.Profile == CapabilityExplore && value.Status == TurnRunning
	case RunBindingCancelled:
		statusMatches = value.Status == TurnCancelled
	case RunBindingFailed:
		statusMatches = value.Status == TurnFailed || isWorkflowStrategy(value.Strategy) && (value.Status == TurnRunning || value.Status == TurnCancelled) || latest.Profile == CapabilityExplore && value.Status == TurnRunning
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
	if previous.ID != next.ID || previous.SessionID != next.SessionID || previous.RequestText != next.RequestText || previous.EntrySource != next.EntrySource || !previous.CreatedAt.Equal(next.CreatedAt) {
		return errors.New("Coding turn immutable identity changed")
	}
	strategyDecision := previous.Phase == TurnPhaseAwaitingPlanApproval && next.Phase == TurnPhaseAwaitingPlanApproval && previous.Status == TurnInterrupted
	if previous.Strategy != next.Strategy && !strategyDecision {
		return errors.New("Coding turn execution strategy can change only at exact Plan approval")
	}
	if previous.WorkflowID != next.WorkflowID && !(strategyDecision || previous.Phase == TurnPhaseAwaitingPlanApproval && next.Phase == TurnPhaseExecuting && previous.Status == TurnRunning) {
		return errors.New("Coding turn Workflow identity can change only when an exact Plan begins execution")
	}
	if previous.PendingPlanExploreID != "" && next.PendingPlanExploreID != "" && previous.PendingPlanExploreID != next.PendingPlanExploreID {
		return errors.New("pending Plan exploration identity cannot be replaced")
	}
	if !validTurnPhaseTransition(previous.Phase, next.Phase) {
		return fmt.Errorf("Coding turn phase cannot transition from %q to %q", previous.Phase, next.Phase)
	}
	if err := validateTurnPlanTransition(previous, next); err != nil {
		return err
	}
	if err := validateTurnPlanEntryTransition(previous, next); err != nil {
		return err
	}
	if err := validateTurnPlanLifecycleTransition(previous, next); err != nil {
		return err
	}
	if next.Revision != previous.Revision+1 {
		return errors.New("Coding turn revision must advance exactly once")
	}
	if len(next.Runs) < len(previous.Runs) || len(next.Runs) > len(previous.Runs)+1 {
		return errors.New("Coding turn Run bindings must be preserved and appended one at a time")
	}
	for index, before := range previous.Runs {
		after := next.Runs[index]
		if before.RunID != after.RunID || before.UserEntryID != after.UserEntryID || before.NodeID != after.NodeID || before.ChildAgentID != after.ChildAgentID || before.Phase != after.Phase || before.Profile != after.Profile {
			return fmt.Errorf("Coding turn Run binding %d identity changed", index)
		}
		if len(before.TerminalOutput) != 0 && string(before.TerminalOutput) != string(after.TerminalOutput) {
			return fmt.Errorf("Coding turn Run %q terminal output changed", before.RunID)
		}
		if !before.StartedAt.IsZero() && !before.StartedAt.Equal(after.StartedAt) {
			return fmt.Errorf("Coding turn Run %q start timestamp changed", before.RunID)
		}
		if !before.FinishedAt.IsZero() && !before.FinishedAt.Equal(after.FinishedAt) {
			return fmt.Errorf("Coding turn Run %q finish timestamp changed", before.RunID)
		}
		if after.Steps < before.Steps || before.FinishedAt.IsZero() == false && after.Steps != before.Steps {
			return fmt.Errorf("Coding turn Run %q durable step usage changed invalidly", before.RunID)
		}
		if !validRunBindingTransition(before.Status, after.Status) {
			return fmt.Errorf("Coding turn Run %q cannot transition from %q to %q", before.RunID, before.Status, after.Status)
		}
	}
	if len(next.Runs) != len(previous.Runs) {
		latest := previous.Runs[len(previous.Runs)-1]
		appended := next.Runs[len(next.Runs)-1]
		workflowContinuation := isWorkflowStrategy(previous.Strategy) && previous.Phase == TurnPhaseExecuting && previous.Status == TurnRunning && (latest.Status == RunBindingCompleted || latest.Status == RunBindingFailed)
		planExploreContinuation := previous.Phase == TurnPhasePlanning && latest.Profile == CapabilityExplore && (latest.Status == RunBindingCompleted || latest.Status == RunBindingFailed)
		if previous.Status != TurnRunning || (latest.Status != RunBindingHandedOff && !workflowContinuation && !planExploreContinuation) || appended.Status != RunBindingPending || appended.UserEntryID != "" || appended.Phase != next.Phase {
			return errors.New("Coding turn can append a continuation Run only after a control handoff")
		}
	}
	return nil
}

func validTurnPhase(value TurnPhase) bool {
	switch value {
	case TurnPhaseDirect, TurnPhaseAwaitingPlanEntryApproval, TurnPhasePlanning, TurnPhaseAwaitingPlanApproval, TurnPhaseExecuting, TurnPhaseNeedsReplan:
		return true
	default:
		return false
	}
}

func validRunPhaseProfile(phase TurnPhase, profile CapabilityProfile) bool {
	switch phase {
	case TurnPhasePlanning:
		return profile == CapabilityPlan || profile == CapabilityPlanWorkspace || profile == CapabilityExplore
	case TurnPhaseDirect, TurnPhaseAwaitingPlanEntryApproval, TurnPhaseExecuting:
		return profile == CapabilityDirect || phase == TurnPhaseExecuting && (profile == CapabilityImplement || profile == CapabilityValidate || profile == CapabilityReview || profile == CapabilityIntegrate)
	default:
		return false
	}
}

func validExecutionStrategy(value ExecutionStrategy) bool {
	return value == ExecutionSingle || value == ExecutionWorkflowSingle || value == ExecutionWorkflowMultiSerial
}

func isWorkflowStrategy(value ExecutionStrategy) bool {
	return value == ExecutionWorkflowSingle || value == ExecutionWorkflowMultiSerial
}

func latestPhaseMatchesTurn(turnPhase, runPhase TurnPhase) bool {
	if turnPhase == TurnPhaseAwaitingPlanEntryApproval {
		return runPhase == TurnPhaseDirect
	}
	if turnPhase == TurnPhaseAwaitingPlanApproval {
		return runPhase == TurnPhasePlanning
	}
	if turnPhase == TurnPhaseNeedsReplan {
		return runPhase == TurnPhaseExecuting
	}
	return turnPhase == runPhase
}

func validTurnPhaseTransition(previous, next TurnPhase) bool {
	if previous == next {
		return true
	}
	switch previous {
	case TurnPhaseDirect:
		return next == TurnPhaseAwaitingPlanEntryApproval
	case TurnPhaseAwaitingPlanEntryApproval:
		return next == TurnPhaseDirect || next == TurnPhasePlanning
	case TurnPhasePlanning:
		return next == TurnPhaseAwaitingPlanApproval
	case TurnPhaseAwaitingPlanApproval:
		return next == TurnPhasePlanning || next == TurnPhaseExecuting
	case TurnPhaseExecuting:
		return next == TurnPhaseNeedsReplan
	case TurnPhaseNeedsReplan:
		return next == TurnPhaseExecuting || next == TurnPhasePlanning
	default:
		return false
	}
}

func validateTurnPlanReference(value Turn) error {
	hasIdentity := value.PlanID != "" || value.PlanVersion != 0 || value.PlanDigest != ""
	complete := value.PlanID != "" && value.PlanVersion != 0 && isHexDigest(value.PlanDigest, 64, 64)
	if hasIdentity && !complete {
		return errors.New("Coding turn Plan reference must contain id, version, and digest")
	}
	if (value.Phase == TurnPhaseAwaitingPlanApproval || value.Phase == TurnPhaseExecuting || value.Phase == TurnPhaseNeedsReplan) && !complete {
		return errors.New("Coding turn phase requires an exact Plan reference")
	}
	if value.Phase == TurnPhaseDirect && hasIdentity {
		return errors.New("Direct Coding turn cannot reference a Plan")
	}
	return nil
}

func validateTurnPlanTransition(previous, next Turn) error {
	if previous.PlanID != "" && previous.PlanID != next.PlanID {
		return errors.New("Coding turn Plan identity cannot change")
	}
	if previous.PlanVersion > next.PlanVersion || next.PlanVersion > previous.PlanVersion+1 {
		return errors.New("Coding turn Plan version must be preserved or advance exactly once")
	}
	if previous.PlanVersion == next.PlanVersion && previous.PlanDigest != next.PlanDigest {
		return errors.New("Coding turn Plan digest changed without a new version")
	}
	if previous.PlanVersion != 0 && next.PlanVersion == previous.PlanVersion+1 && previous.PlanDigest == next.PlanDigest {
		return errors.New("Coding turn new Plan version must have new canonical content")
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
	case RunBindingHandedOff:
		return next == RunBindingCancelled
	default:
		return false
	}
}
