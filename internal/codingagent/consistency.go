package codingagent

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	agentsession "github.com/eaglc/codepilot/internal/agent/session"
)

// ConsistencyIssueKind classifies durable relationships that cannot currently be followed.
type ConsistencyIssueKind string

const (
	IssueCreationPending       ConsistencyIssueKind = "creation_pending"
	IssueOrphanAgentSession    ConsistencyIssueKind = "orphan_agent_session"
	IssueDanglingCodingSession ConsistencyIssueKind = "dangling_coding_session"
	IssueMissingWorktree       ConsistencyIssueKind = "missing_worktree"
)

// ConsistencyRepairAction describes the explicit non-destructive repair for an issue.
type ConsistencyRepairAction string

const (
	RepairCompleteCreation     ConsistencyRepairAction = "complete_creation"
	RepairArchiveAgentSession  ConsistencyRepairAction = "archive_agent_session"
	RepairArchiveCodingSession ConsistencyRepairAction = "archive_coding_session"
)

// ConsistencyIssue is a product-safe diagnostic. It intentionally contains no
// conversation content, tool arguments, credentials, or absolute worktree paths.
type ConsistencyIssue struct {
	ID                string                  `json:"id"`
	Kind              ConsistencyIssueKind    `json:"kind"`
	Message           string                  `json:"message"`
	RepairAction      ConsistencyRepairAction `json:"repair_action"`
	IntentID          SessionCreationIntentID `json:"intent_id,omitempty"`
	CodingSessionID   SessionID               `json:"coding_session_id,omitempty"`
	AgentSessionID    agentsession.ID         `json:"agent_session_id,omitempty"`
	WorktreeID        WorktreeID              `json:"worktree_id,omitempty"`
	BlockedByIntentID SessionCreationIntentID `json:"blocked_by_intent_id,omitempty"`
}

// ConsistencyReport is a read-only point-in-time cross-repository diagnosis.
type ConsistencyReport struct {
	GeneratedAt time.Time          `json:"generated_at"`
	Issues      []ConsistencyIssue `json:"issues"`
}

// RepairActionResult records one explicit reconciliation or reversible archive operation.
type RepairActionResult struct {
	Action          ConsistencyRepairAction `json:"action"`
	CodingSessionID SessionID               `json:"coding_session_id,omitempty"`
	AgentSessionID  agentsession.ID         `json:"agent_session_id,omitempty"`
	IntentID        SessionCreationIntentID `json:"intent_id,omitempty"`
	Completed       bool                    `json:"completed"`
	Message         string                  `json:"message"`
}

// ConsistencyRepairReport contains both the original and remaining diagnosis.
type ConsistencyRepairReport struct {
	Before  ConsistencyReport    `json:"before"`
	Actions []RepairActionResult `json:"actions"`
	After   ConsistencyReport    `json:"after"`
}

// ConsistencyDependencies are the minimal durable capabilities needed for diagnosis and repair.
type ConsistencyDependencies struct {
	Sessions      SessionRepository
	AgentSessions agentsession.Repository
	Worktrees     WorktreeReader
}

// ConsistencyManager reconciles product bindings without depending on Agent runtime or UI modules.
type ConsistencyManager struct{ deps ConsistencyDependencies }

// NewConsistencyManager validates the cross-repository maintenance boundary.
func NewConsistencyManager(deps ConsistencyDependencies) (*ConsistencyManager, error) {
	if deps.Sessions == nil || deps.AgentSessions == nil || deps.Worktrees == nil {
		return nil, errors.New("create Coding consistency manager: dependencies are incomplete")
	}
	return &ConsistencyManager{deps: deps}, nil
}

// Diagnose compares transaction intents, Coding sessions, Agent sessions, and worktrees without mutation.
func (m *ConsistencyManager) Diagnose(ctx context.Context) (ConsistencyReport, error) {
	if err := ctx.Err(); err != nil {
		return ConsistencyReport{}, err
	}
	products, err := m.deps.Sessions.ListSessions(ctx)
	if err != nil {
		return ConsistencyReport{}, fmt.Errorf("diagnose Coding consistency: list product sessions: %w", err)
	}
	agents, err := m.deps.AgentSessions.List(ctx)
	if err != nil {
		return ConsistencyReport{}, fmt.Errorf("diagnose Coding consistency: list Agent sessions: %w", err)
	}
	intents, err := m.deps.Sessions.ListSessionCreationIntents(ctx)
	if err != nil {
		return ConsistencyReport{}, fmt.Errorf("diagnose Coding consistency: list creation intents: %w", err)
	}

	productByAgent := make(map[agentsession.ID]Session, len(products))
	agentByID := make(map[agentsession.ID]agentsession.Metadata, len(agents))
	pendingByAgent := make(map[agentsession.ID]SessionCreationIntentID)
	issues := make([]ConsistencyIssue, 0)
	for _, metadata := range agents {
		agentByID[metadata.ID] = metadata
	}
	for _, product := range products {
		productByAgent[product.AgentSessionID] = product
	}
	for _, intent := range intents {
		if intent.Status != SessionCreationPending {
			continue
		}
		pendingByAgent[intent.Session.AgentSessionID] = intent.ID
		issues = append(issues, ConsistencyIssue{
			ID: "creation_pending:" + string(intent.ID), Kind: IssueCreationPending,
			Message: "A session creation transaction did not record completion.", RepairAction: RepairCompleteCreation,
			IntentID: intent.ID, CodingSessionID: intent.Session.ID, AgentSessionID: intent.Session.AgentSessionID, WorktreeID: intent.Session.WorktreeID,
		})
	}
	for _, product := range products {
		if product.Archived {
			continue
		}
		if _, found := agentByID[product.AgentSessionID]; !found {
			issues = append(issues, ConsistencyIssue{
				ID: "dangling_coding_session:" + string(product.ID), Kind: IssueDanglingCodingSession,
				Message: "The Coding session references a missing Agent session.", RepairAction: RepairArchiveCodingSession,
				CodingSessionID: product.ID, AgentSessionID: product.AgentSessionID, WorktreeID: product.WorktreeID,
			})
		}
		if _, loadErr := m.deps.Worktrees.LoadWorktree(ctx, product.WorktreeID); loadErr != nil {
			if !errors.Is(loadErr, ErrWorktreeNotFound) {
				return ConsistencyReport{}, fmt.Errorf("diagnose Coding consistency: load worktree %q: %w", product.WorktreeID, loadErr)
			}
			issues = append(issues, ConsistencyIssue{
				ID: "missing_worktree:" + string(product.ID), Kind: IssueMissingWorktree,
				Message: "The Coding session references a missing worktree record.", RepairAction: RepairArchiveCodingSession,
				CodingSessionID: product.ID, AgentSessionID: product.AgentSessionID, WorktreeID: product.WorktreeID,
			})
		}
	}
	for _, metadata := range agents {
		if metadata.Archived {
			continue
		}
		if _, found := productByAgent[metadata.ID]; found {
			continue
		}
		intentID := pendingByAgent[metadata.ID]
		issue := ConsistencyIssue{
			ID: "orphan_agent_session:" + string(metadata.ID), Kind: IssueOrphanAgentSession,
			Message: "The Agent session has no Coding session binding.", RepairAction: RepairArchiveAgentSession,
			AgentSessionID: metadata.ID, BlockedByIntentID: intentID,
		}
		if intentID != "" {
			issue.RepairAction = RepairCompleteCreation
			issue.Message = "The Agent session has no Coding binding because its creation transaction is pending."
		}
		issues = append(issues, issue)
	}
	sort.Slice(issues, func(left, right int) bool { return issues[left].ID < issues[right].ID })
	return ConsistencyReport{GeneratedAt: time.Now().UTC(), Issues: issues}, nil
}

// Repair explicitly completes pending creation transactions and archives broken
// unreferenced bindings. Archive operations preserve all journal and metadata files.
func (m *ConsistencyManager) Repair(ctx context.Context) (ConsistencyRepairReport, error) {
	before, err := m.Diagnose(ctx)
	if err != nil {
		return ConsistencyRepairReport{}, err
	}
	result := ConsistencyRepairReport{Before: before}
	intents, err := m.deps.Sessions.ListSessionCreationIntents(ctx)
	if err != nil {
		return ConsistencyRepairReport{}, fmt.Errorf("repair Coding consistency: list creation intents: %w", err)
	}
	for _, intent := range intents {
		if intent.Status != SessionCreationPending {
			continue
		}
		action := RepairActionResult{Action: RepairCompleteCreation, IntentID: intent.ID, CodingSessionID: intent.Session.ID, AgentSessionID: intent.Session.AgentSessionID}
		if _, loadErr := m.deps.Worktrees.LoadWorktree(ctx, intent.Session.WorktreeID); loadErr != nil {
			action.Message = fmt.Sprintf("creation remains pending: worktree %q is unavailable", intent.Session.WorktreeID)
			result.Actions = append(result.Actions, action)
			continue
		}
		if repairErr := reconcileSessionCreation(ctx, m.deps.Sessions, m.deps.AgentSessions, intent); repairErr != nil {
			action.Message = "creation remains pending: " + repairErr.Error()
		} else {
			action.Completed = true
			action.Message = "completed the durable session creation transaction"
		}
		result.Actions = append(result.Actions, action)
	}

	intermediate, err := m.Diagnose(ctx)
	if err != nil {
		return ConsistencyRepairReport{}, err
	}
	archivedCoding := make(map[SessionID]struct{})
	for _, issue := range intermediate.Issues {
		switch issue.RepairAction {
		case RepairArchiveAgentSession:
			if issue.BlockedByIntentID != "" {
				continue
			}
			action := RepairActionResult{Action: RepairArchiveAgentSession, AgentSessionID: issue.AgentSessionID}
			if archiveErr := m.deps.AgentSessions.SetArchived(ctx, issue.AgentSessionID, true); archiveErr != nil {
				action.Message = "could not archive orphan Agent session: " + archiveErr.Error()
			} else {
				action.Completed = true
				action.Message = "archived the orphan Agent session without deleting its journal"
			}
			result.Actions = append(result.Actions, action)
		case RepairArchiveCodingSession:
			if _, done := archivedCoding[issue.CodingSessionID]; done {
				continue
			}
			archivedCoding[issue.CodingSessionID] = struct{}{}
			action := RepairActionResult{Action: RepairArchiveCodingSession, CodingSessionID: issue.CodingSessionID, AgentSessionID: issue.AgentSessionID}
			product, loadErr := m.deps.Sessions.LoadSession(ctx, issue.CodingSessionID)
			if loadErr == nil {
				product.Archived = true
				product.UpdatedAt = time.Now().UTC()
				loadErr = m.deps.Sessions.SaveSession(ctx, product)
			}
			if loadErr != nil {
				action.Message = "could not archive broken Coding session: " + loadErr.Error()
			} else {
				action.Completed = true
				action.Message = "archived the broken Coding session without deleting durable state"
			}
			result.Actions = append(result.Actions, action)
		}
	}
	result.After, err = m.Diagnose(ctx)
	if err != nil {
		return ConsistencyRepairReport{}, err
	}
	return result, nil
}

func reconcileSessionCreation(ctx context.Context, products SessionRepository, agents agentsession.Repository, intent SessionCreationIntent) error {
	desired := intent.Session
	durable, err := agents.Load(ctx, desired.AgentSessionID)
	if errors.Is(err, agentsession.ErrNotFound) {
		err = agents.Create(ctx, agentsession.Metadata{
			ID: desired.AgentSessionID, Name: desired.Title, CreatedAt: desired.CreatedAt, UpdatedAt: desired.UpdatedAt,
		})
	} else if err == nil && !sameAgentSessionIdentity(durable.Metadata, desired) {
		err = errors.New("an existing Agent session has a different identity")
	}
	if err != nil {
		return fmt.Errorf("ensure Agent session %q: %w", desired.AgentSessionID, err)
	}
	product, err := products.LoadSession(ctx, desired.ID)
	if errors.Is(err, ErrSessionNotFound) {
		err = products.CreateSession(ctx, desired)
	} else if err == nil && !sameProductSessionBinding(product, desired) {
		err = errors.New("an existing Coding session has a different immutable binding")
	}
	if err != nil {
		return fmt.Errorf("ensure Coding session %q: %w", desired.ID, err)
	}
	if err := products.CompleteSessionCreation(ctx, intent.ID, time.Now().UTC()); err != nil {
		return fmt.Errorf("complete creation intent: %w", err)
	}
	return nil
}

func sameAgentSessionIdentity(metadata agentsession.Metadata, desired Session) bool {
	return metadata.ID == desired.AgentSessionID && metadata.ParentSessionID == "" && metadata.Name == desired.Title &&
		metadata.CreatedAt.Equal(desired.CreatedAt) && !metadata.Archived
}

func sameProductSessionBinding(existing Session, desired Session) bool {
	return existing.ID == desired.ID && existing.AgentSessionID == desired.AgentSessionID && existing.WorkspaceID == desired.WorkspaceID &&
		existing.WorktreeID == desired.WorktreeID && existing.CreatedAt.Equal(desired.CreatedAt)
}
