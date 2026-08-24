// Package memory implements ephemeral Coding Agent product repositories.
package memory

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"sync"
	"time"

	"github.com/eaglc/codepilot/internal/codingagent"
)

// Repository stores Coding sessions and worktrees in memory.
type Repository struct {
	mu         sync.RWMutex
	sessions   map[codingagent.SessionID]codingagent.Session
	workspaces map[codingagent.WorkspaceID]codingagent.Workspace
	worktrees  map[codingagent.WorktreeID]codingagent.Worktree
	intents    map[codingagent.SessionCreationIntentID]codingagent.SessionCreationIntent
}

// NewRepository creates an empty product repository.
func NewRepository() *Repository {
	return &Repository{
		sessions: make(map[codingagent.SessionID]codingagent.Session), workspaces: make(map[codingagent.WorkspaceID]codingagent.Workspace),
		worktrees: make(map[codingagent.WorktreeID]codingagent.Worktree), intents: make(map[codingagent.SessionCreationIntentID]codingagent.SessionCreationIntent),
	}
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
