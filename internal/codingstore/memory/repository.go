// Package memory implements ephemeral Coding Agent product repositories.
package memory

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"sync"
	"time"

	"github.com/eaglc/codepilot/internal/codingagent"
	"github.com/eaglc/codepilot/internal/workflow"
)

// Repository stores Coding sessions and worktrees in memory.
type Repository struct {
	mu             sync.RWMutex
	sessions       map[codingagent.SessionID]codingagent.Session
	workspaces     map[codingagent.WorkspaceID]codingagent.Workspace
	worktrees      map[codingagent.WorktreeID]codingagent.Worktree
	intents        map[codingagent.SessionCreationIntentID]codingagent.SessionCreationIntent
	turns          map[codingagent.TurnID]codingagent.Turn
	plans          map[codingagent.PlanID][]codingagent.Plan
	workflows      map[workflow.ID]workflow.Workflow
	workflowEvents map[workflow.ID]map[string]workflow.Event
	children       map[codingagent.ChildAgentID]codingagent.ChildAgent
}

// NewRepository creates an empty product repository.
func NewRepository() *Repository {
	return &Repository{
		sessions: make(map[codingagent.SessionID]codingagent.Session), workspaces: make(map[codingagent.WorkspaceID]codingagent.Workspace),
		worktrees: make(map[codingagent.WorktreeID]codingagent.Worktree), intents: make(map[codingagent.SessionCreationIntentID]codingagent.SessionCreationIntent),
		turns:     make(map[codingagent.TurnID]codingagent.Turn),
		plans:     make(map[codingagent.PlanID][]codingagent.Plan),
		workflows: make(map[workflow.ID]workflow.Workflow), workflowEvents: make(map[workflow.ID]map[string]workflow.Event),
		children: make(map[codingagent.ChildAgentID]codingagent.ChildAgent),
	}
}

// CreateChildAgent persists a durable creation intent before its Agent session exists.
func (r *Repository) CreateChildAgent(ctx context.Context, value codingagent.ChildAgent) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := codingagent.ValidateChildAgent(value); err != nil {
		return fmt.Errorf("create Coding child Agent: %w", err)
	}
	if value.Status != codingagent.ChildAgentCreating || value.Revision != 1 {
		return errors.New("create Coding child Agent: initial state must be creating revision 1")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if current, exists := r.children[value.ID]; exists {
		if reflect.DeepEqual(current, value) {
			return nil
		}
		return fmt.Errorf("create Coding child Agent %q: identity already exists with different data", value.ID)
	}
	r.children[value.ID] = codingagent.CloneChildAgent(value)
	return nil
}

// LoadChildAgent returns an isolated durable child projection.
func (r *Repository) LoadChildAgent(ctx context.Context, id codingagent.ChildAgentID) (codingagent.ChildAgent, error) {
	if err := ctx.Err(); err != nil {
		return codingagent.ChildAgent{}, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	value, exists := r.children[id]
	if !exists {
		return codingagent.ChildAgent{}, fmt.Errorf("load Coding child Agent %q: %w", id, codingagent.ErrChildAgentNotFound)
	}
	return codingagent.CloneChildAgent(value), nil
}

// ListChildAgents returns stable creation ordering for one parent Turn.
func (r *Repository) ListChildAgents(ctx context.Context, turnID codingagent.TurnID) ([]codingagent.ChildAgent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	values := make([]codingagent.ChildAgent, 0)
	for _, value := range r.children {
		if turnID == "" || value.ParentTurnID == turnID {
			values = append(values, codingagent.CloneChildAgent(value))
		}
	}
	sort.Slice(values, func(left, right int) bool {
		if values[left].CreatedAt.Equal(values[right].CreatedAt) {
			return values[left].ID < values[right].ID
		}
		return values[left].CreatedAt.Before(values[right].CreatedAt)
	})
	return values, nil
}

// SaveChildAgent applies one lifecycle transition with revision CAS.
func (r *Repository) SaveChildAgent(ctx context.Context, value codingagent.ChildAgent, expectedRevision uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	current, exists := r.children[value.ID]
	if !exists {
		return fmt.Errorf("save Coding child Agent %q: %w", value.ID, codingagent.ErrChildAgentNotFound)
	}
	if current.Revision != expectedRevision {
		return fmt.Errorf("save Coding child Agent %q: expected revision %d, found %d: %w", value.ID, expectedRevision, current.Revision, codingagent.ErrChildAgentConflict)
	}
	if err := codingagent.ValidateChildAgentTransition(current, value); err != nil {
		return fmt.Errorf("save Coding child Agent %q: %w", value.ID, err)
	}
	r.children[value.ID] = codingagent.CloneChildAgent(value)
	return nil
}

// CreateWorkflow stores immutable Workflow metadata before any state event.
func (r *Repository) CreateWorkflow(ctx context.Context, value workflow.Workflow) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := workflow.Validate(value); err != nil {
		return fmt.Errorf("create Coding workflow: %w", err)
	}
	if value.Status != workflow.StatusPending || value.Revision != 1 {
		return errors.New("create Coding workflow: initial state must be pending revision 1")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.workflows[value.ID]; exists {
		return fmt.Errorf("create Coding workflow %q: already exists", value.ID)
	}
	r.workflows[value.ID] = workflow.Clone(value)
	r.workflowEvents[value.ID] = make(map[string]workflow.Event)
	return nil
}

// LoadWorkflow rebuilds one Workflow from its in-memory event projection.
func (r *Repository) LoadWorkflow(ctx context.Context, id workflow.ID) (workflow.Workflow, error) {
	if err := ctx.Err(); err != nil {
		return workflow.Workflow{}, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	value, exists := r.workflows[id]
	if !exists {
		return workflow.Workflow{}, fmt.Errorf("load Coding workflow %q: %w", id, workflow.ErrNotFound)
	}
	return workflow.Clone(value), nil
}

// ListWorkflows returns stable creation ordering for one Product Turn owner.
func (r *Repository) ListWorkflows(ctx context.Context, ownerID string) ([]workflow.Workflow, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	values := make([]workflow.Workflow, 0)
	for _, value := range r.workflows {
		if ownerID == "" || value.OwnerID == ownerID {
			values = append(values, workflow.Clone(value))
		}
	}
	sort.Slice(values, func(left, right int) bool {
		if values[left].CreatedAt.Equal(values[right].CreatedAt) {
			return values[left].ID < values[right].ID
		}
		return values[left].CreatedAt.Before(values[right].CreatedAt)
	})
	return values, nil
}

// AppendWorkflowEvent applies one idempotent event using revision CAS.
func (r *Repository) AppendWorkflowEvent(ctx context.Context, id workflow.ID, expectedRevision uint64, event workflow.Event) (workflow.Workflow, error) {
	if err := ctx.Err(); err != nil {
		return workflow.Workflow{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	current, exists := r.workflows[id]
	if !exists {
		return workflow.Workflow{}, fmt.Errorf("append Coding workflow event %q: %w", id, workflow.ErrNotFound)
	}
	if previous, duplicate := r.workflowEvents[id][event.ID]; duplicate {
		if !reflect.DeepEqual(previous, event) {
			return workflow.Workflow{}, fmt.Errorf("append Coding workflow event %q: event id %q changed", id, event.ID)
		}
		return workflow.Clone(current), nil
	}
	if current.Revision != expectedRevision {
		return workflow.Workflow{}, fmt.Errorf("append Coding workflow event %q: expected revision %d, found %d: %w", id, expectedRevision, current.Revision, workflow.ErrConflict)
	}
	next, err := workflow.ApplyEvent(current, event)
	if err != nil {
		return workflow.Workflow{}, fmt.Errorf("append Coding workflow event %q: %w", id, err)
	}
	r.workflows[id] = workflow.Clone(next)
	if r.workflowEvents[id] == nil {
		r.workflowEvents[id] = make(map[string]workflow.Event)
	}
	r.workflowEvents[id][event.ID] = event
	return workflow.Clone(next), nil
}

// CreatePlanVersion appends one immutable, sequential Plan revision.
func (r *Repository) CreatePlanVersion(ctx context.Context, value codingagent.Plan) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := codingagent.ValidatePlan(value); err != nil {
		return fmt.Errorf("create Coding plan version: %w", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	turn, exists := r.turns[value.TurnID]
	if !exists {
		return fmt.Errorf("create Coding plan %q: turn %q not found", value.ID, value.TurnID)
	}
	versions := r.plans[value.ID]
	if len(versions) == 0 {
		if value.Version != 1 {
			return fmt.Errorf("create Coding plan %q: initial version must be 1", value.ID)
		}
	} else {
		latest := versions[len(versions)-1]
		if latest.TurnID != value.TurnID || value.Version != latest.Version+1 {
			return fmt.Errorf("create Coding plan %q: version or immutable Turn binding is invalid", value.ID)
		}
	}
	if turn.PlanID != "" && turn.PlanID != value.ID {
		return fmt.Errorf("create Coding plan %q: Product Turn references another Plan", value.ID)
	}
	r.plans[value.ID] = append(versions, clonePlan(value))
	return nil
}

// LoadPlan returns one exact immutable Plan revision.
func (r *Repository) LoadPlan(ctx context.Context, id codingagent.PlanID, version uint64) (codingagent.Plan, error) {
	if err := ctx.Err(); err != nil {
		return codingagent.Plan{}, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, value := range r.plans[id] {
		if value.Version == version {
			return clonePlan(value), nil
		}
	}
	return codingagent.Plan{}, fmt.Errorf("load Coding plan %q version %d: %w", id, version, codingagent.ErrPlanNotFound)
}

// ListPlanVersions returns immutable Plan revisions in ascending version order.
func (r *Repository) ListPlanVersions(ctx context.Context, id codingagent.PlanID) ([]codingagent.Plan, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	versions := r.plans[id]
	values := make([]codingagent.Plan, len(versions))
	for index := range versions {
		values[index] = clonePlan(versions[index])
	}
	return values, nil
}

// CreateTurn stores a new Product Turn with its initial revision.
func (r *Repository) CreateTurn(ctx context.Context, value codingagent.Turn) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := codingagent.ValidateTurn(value); err != nil {
		return fmt.Errorf("create Coding turn: %w", err)
	}
	if value.Revision != 1 {
		return fmt.Errorf("create Coding turn %q: initial revision must be 1", value.ID)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.turns[value.ID]; exists {
		return fmt.Errorf("create Coding turn %q: already exists", value.ID)
	}
	if _, exists := r.sessions[value.SessionID]; !exists {
		return fmt.Errorf("create Coding turn %q: session %q not found", value.ID, value.SessionID)
	}
	r.turns[value.ID] = cloneTurn(value)
	return nil
}

// LoadTurn returns one Product Turn by stable identity.
func (r *Repository) LoadTurn(ctx context.Context, id codingagent.TurnID) (codingagent.Turn, error) {
	if err := ctx.Err(); err != nil {
		return codingagent.Turn{}, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	value, exists := r.turns[id]
	if !exists {
		return codingagent.Turn{}, fmt.Errorf("load Coding turn %q: %w", id, codingagent.ErrTurnNotFound)
	}
	return cloneTurn(value), nil
}

// ListTurns returns Product Turns for one session in creation order.
func (r *Repository) ListTurns(ctx context.Context, sessionID codingagent.SessionID) ([]codingagent.Turn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	values := make([]codingagent.Turn, 0)
	for _, value := range r.turns {
		if value.SessionID == sessionID {
			values = append(values, cloneTurn(value))
		}
	}
	sort.Slice(values, func(left, right int) bool {
		if values[left].CreatedAt.Equal(values[right].CreatedAt) {
			return values[left].ID < values[right].ID
		}
		return values[left].CreatedAt.Before(values[right].CreatedAt)
	})
	return values, nil
}

// SaveTurn atomically replaces a Product Turn at the expected revision.
func (r *Repository) SaveTurn(ctx context.Context, value codingagent.Turn, expectedRevision uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := codingagent.ValidateTurn(value); err != nil {
		return fmt.Errorf("save Coding turn: %w", err)
	}
	if value.Revision != expectedRevision+1 {
		return fmt.Errorf("save Coding turn %q: next revision is invalid", value.ID)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	previous, exists := r.turns[value.ID]
	if !exists {
		return fmt.Errorf("save Coding turn %q: %w", value.ID, codingagent.ErrTurnNotFound)
	}
	if previous.Revision != expectedRevision {
		return fmt.Errorf("save Coding turn %q: expected revision %d, found %d: %w", value.ID, expectedRevision, previous.Revision, codingagent.ErrTurnConflict)
	}
	if err := codingagent.ValidateTurnTransition(previous, value); err != nil {
		return fmt.Errorf("save Coding turn %q: %w", value.ID, err)
	}
	r.turns[value.ID] = cloneTurn(value)
	return nil
}

// SaveWorkspace stores a logical Coding workspace.
func (r *Repository) SaveWorkspace(ctx context.Context, value codingagent.Workspace) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := codingagent.ValidateWorkspace(value); err != nil {
		return fmt.Errorf("save Coding workspace: %w", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if previous, found := r.workspaces[value.ID]; found && (!previous.CreatedAt.Equal(value.CreatedAt) || (previous.RepositoryFingerprint != "" && value.RepositoryFingerprint != previous.RepositoryFingerprint)) {
		return fmt.Errorf("save Coding workspace %q: immutable identity changed", value.ID)
	}
	r.workspaces[value.ID] = value
	return nil
}

// LoadWorkspace returns one logical Coding workspace.
func (r *Repository) LoadWorkspace(ctx context.Context, id codingagent.WorkspaceID) (codingagent.Workspace, error) {
	if err := ctx.Err(); err != nil {
		return codingagent.Workspace{}, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	value, exists := r.workspaces[id]
	if !exists {
		return codingagent.Workspace{}, fmt.Errorf("Coding workspace %q not found", id)
	}
	return value, nil
}

// ListWorkspaces returns stable workspace ordering by ID.
func (r *Repository) ListWorkspaces(ctx context.Context) ([]codingagent.Workspace, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	values := make([]codingagent.Workspace, 0, len(r.workspaces))
	for _, value := range r.workspaces {
		values = append(values, value)
	}
	sort.Slice(values, func(left, right int) bool { return values[left].ID < values[right].ID })
	return values, nil
}

// SaveWorktree stores a Coding worktree for subsequent session validation.
func (r *Repository) SaveWorktree(ctx context.Context, value codingagent.Worktree) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := codingagent.ValidateWorktree(value); err != nil {
		return fmt.Errorf("save Coding worktree: %w", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, found := r.workspaces[value.WorkspaceID]; !found {
		return fmt.Errorf("save Coding worktree %q: workspace %q not found", value.ID, value.WorkspaceID)
	}
	if previous, found := r.worktrees[value.ID]; found && (previous.WorkspaceID != value.WorkspaceID || previous.Root != value.Root || previous.GitDir != value.GitDir || !previous.CreatedAt.Equal(value.CreatedAt)) {
		return fmt.Errorf("save Coding worktree %q: immutable identity changed", value.ID)
	}
	r.worktrees[value.ID] = value
	return nil
}

// RelocateWorktree explicitly changes one persisted checkout location using an
// expected-root compare-and-swap. Ordinary SaveWorktree remains path agnostic in
// this test backend, while production callers use this method for re-binding.
func (r *Repository) RelocateWorktree(ctx context.Context, id codingagent.WorktreeID, expectedRoot, root, gitDir, gitCommonDir string, lastUsedAt time.Time) (codingagent.Worktree, error) {
	if err := ctx.Err(); err != nil {
		return codingagent.Worktree{}, err
	}
	if id == "" || expectedRoot == "" || root == "" || gitDir == "" || gitCommonDir == "" || lastUsedAt.IsZero() || !filepath.IsAbs(root) || !filepath.IsAbs(gitDir) || !filepath.IsAbs(gitCommonDir) {
		return codingagent.Worktree{}, fmt.Errorf("relocate Coding worktree: complete normalized locations and timestamp are required")
	}
	root, gitDir, gitCommonDir = filepath.Clean(root), filepath.Clean(gitDir), filepath.Clean(gitCommonDir)
	r.mu.Lock()
	defer r.mu.Unlock()
	value, found := r.worktrees[id]
	if !found {
		return codingagent.Worktree{}, fmt.Errorf("relocate Coding worktree %q: %w", id, codingagent.ErrWorktreeNotFound)
	}
	workspace, found := r.workspaces[value.WorkspaceID]
	if !found {
		return codingagent.Worktree{}, fmt.Errorf("relocate Coding worktree %q: workspace %q not found", id, value.WorkspaceID)
	}
	if value.Root != expectedRoot && !codingagent.SameLocation(value.Root, root) {
		return codingagent.Worktree{}, fmt.Errorf("relocate Coding worktree %q: stored location changed", id)
	}
	for otherID, other := range r.worktrees {
		if otherID != id && codingagent.SameLocation(other.Root, root) {
			return codingagent.Worktree{}, fmt.Errorf("relocate Coding worktree %q: target location is already registered", id)
		}
	}
	value.Root, value.GitDir, value.LastUsedAt = root, gitDir, lastUsedAt
	r.worktrees[id] = value
	workspace.GitCommonDir, workspace.UpdatedAt = gitCommonDir, lastUsedAt
	r.workspaces[value.WorkspaceID] = workspace
	return value, nil
}

// LoadWorktree returns one Coding worktree.
func (r *Repository) LoadWorktree(ctx context.Context, id codingagent.WorktreeID) (codingagent.Worktree, error) {
	if err := ctx.Err(); err != nil {
		return codingagent.Worktree{}, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	value, exists := r.worktrees[id]
	if !exists {
		return codingagent.Worktree{}, fmt.Errorf("load Coding worktree %q: %w", id, codingagent.ErrWorktreeNotFound)
	}
	return value, nil
}

// ListWorktrees returns stable worktree ordering for one workspace.
func (r *Repository) ListWorktrees(ctx context.Context, workspaceID codingagent.WorkspaceID) ([]codingagent.Worktree, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	var values []codingagent.Worktree
	for _, value := range r.worktrees {
		if value.WorkspaceID == workspaceID {
			values = append(values, value)
		}
	}
	sort.Slice(values, func(left, right int) bool { return values[left].ID < values[right].ID })
	return values, nil
}

// CreateSession stores a new Coding product session.
func (r *Repository) CreateSession(ctx context.Context, value codingagent.Session) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := codingagent.ValidateSession(value); err != nil {
		return fmt.Errorf("create Coding session: %w", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.sessions[value.ID]; exists {
		return fmt.Errorf("create Coding session %q: already exists", value.ID)
	}
	worktree, found := r.worktrees[value.WorktreeID]
	if !found {
		return fmt.Errorf("create Coding session %q: worktree %q not found", value.ID, value.WorktreeID)
	}
	if worktree.WorkspaceID != value.WorkspaceID {
		return fmt.Errorf("create Coding session %q: worktree belongs to another workspace", value.ID)
	}
	r.sessions[value.ID] = cloneSession(value)
	return nil
}

// LoadSession returns one Coding product session.
func (r *Repository) LoadSession(ctx context.Context, id codingagent.SessionID) (codingagent.Session, error) {
	if err := ctx.Err(); err != nil {
		return codingagent.Session{}, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	value, exists := r.sessions[id]
	if !exists {
		return codingagent.Session{}, fmt.Errorf("load Coding session %q: %w", id, codingagent.ErrSessionNotFound)
	}
	return cloneSession(value), nil
}

// ListSessions returns stable session ordering by most recently updated first.
func (r *Repository) ListSessions(ctx context.Context) ([]codingagent.Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	values := make([]codingagent.Session, 0, len(r.sessions))
	for _, value := range r.sessions {
		values = append(values, cloneSession(value))
	}
	sort.Slice(values, func(left, right int) bool {
		if values[left].UpdatedAt.Equal(values[right].UpdatedAt) {
			return values[left].ID < values[right].ID
		}
		return values[left].UpdatedAt.After(values[right].UpdatedAt)
	})
	return values, nil
}

// SaveSession updates mutable Coding product metadata while preserving identity bindings.
func (r *Repository) SaveSession(ctx context.Context, value codingagent.Session) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := codingagent.ValidateSession(value); err != nil {
		return fmt.Errorf("save Coding session: %w", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	previous, exists := r.sessions[value.ID]
	if !exists {
		return fmt.Errorf("save Coding session %q: %w", value.ID, codingagent.ErrSessionNotFound)
	}
	if previous.AgentSessionID != value.AgentSessionID || previous.WorkspaceID != value.WorkspaceID || previous.WorktreeID != value.WorktreeID || !previous.CreatedAt.Equal(value.CreatedAt) {
		return fmt.Errorf("save Coding session %q: immutable binding changed", value.ID)
	}
	r.sessions[value.ID] = cloneSession(value)
	return nil
}

// BeginSessionCreation persists an idempotent recoverable transaction intent.
func (r *Repository) BeginSessionCreation(ctx context.Context, intent codingagent.SessionCreationIntent) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := codingagent.ValidateSessionCreationIntent(intent); err != nil {
		return fmt.Errorf("begin Coding session creation: %w", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if previous, found := r.intents[intent.ID]; found {
		if !reflect.DeepEqual(previous.Session, intent.Session) || !previous.CreatedAt.Equal(intent.CreatedAt) {
			return fmt.Errorf("begin Coding session creation %q: immutable intent changed", intent.ID)
		}
		return nil
	}
	intent.Session = cloneSession(intent.Session)
	r.intents[intent.ID] = intent
	return nil
}

// CompleteSessionCreation marks a durable transaction intent reconciled.
func (r *Repository) CompleteSessionCreation(ctx context.Context, id codingagent.SessionCreationIntentID, completedAt time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if id == "" || completedAt.IsZero() {
		return fmt.Errorf("complete Coding session creation: id and timestamp are required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	intent, found := r.intents[id]
	if !found {
		return fmt.Errorf("complete Coding session creation %q: not found", id)
	}
	if intent.Status == codingagent.SessionCreationCompleted {
		return nil
	}
	intent.Status = codingagent.SessionCreationCompleted
	intent.UpdatedAt = completedAt
	intent.CompletedAt = completedAt
	r.intents[id] = intent
	return nil
}

// ListSessionCreationIntents returns stable transaction intent ordering.
func (r *Repository) ListSessionCreationIntents(ctx context.Context) ([]codingagent.SessionCreationIntent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	values := make([]codingagent.SessionCreationIntent, 0, len(r.intents))
	for _, intent := range r.intents {
		intent.Session = cloneSession(intent.Session)
		values = append(values, intent)
	}
	sort.Slice(values, func(left, right int) bool { return values[left].ID < values[right].ID })
	return values, nil
}

func cloneSession(value codingagent.Session) codingagent.Session {
	value.PermissionGrants = append([]codingagent.PermissionGrant(nil), value.PermissionGrants...)
	for index := range value.PermissionGrants {
		value.PermissionGrants[index].Paths = append([]string(nil), value.PermissionGrants[index].Paths...)
	}
	value.SensitivePaths = append([]string(nil), value.SensitivePaths...)
	return value
}

func cloneTurn(value codingagent.Turn) codingagent.Turn {
	value.Runs = append([]codingagent.RunBinding(nil), value.Runs...)
	value.PendingPlanExploreIDs = append([]codingagent.ChildAgentID(nil), value.PendingPlanExploreIDs...)
	for index := range value.Runs {
		value.Runs[index].TerminalOutput = append([]byte(nil), value.Runs[index].TerminalOutput...)
	}
	return value
}

func clonePlan(value codingagent.Plan) codingagent.Plan {
	value.Scope.Included = append([]string(nil), value.Scope.Included...)
	value.Scope.Excluded = append([]string(nil), value.Scope.Excluded...)
	value.Findings = append([]string(nil), value.Findings...)
	value.Assumptions = append([]string(nil), value.Assumptions...)
	value.Risks = append([]string(nil), value.Risks...)
	value.AcceptanceCriteria = append([]string(nil), value.AcceptanceCriteria...)
	value.Steps = append([]codingagent.PlanStep(nil), value.Steps...)
	for index := range value.Steps {
		value.Steps[index].DependsOn = append([]string(nil), value.Steps[index].DependsOn...)
		value.Steps[index].Files = append([]string(nil), value.Steps[index].Files...)
		value.Steps[index].Validation = append([]string(nil), value.Steps[index].Validation...)
	}
	return value
}
