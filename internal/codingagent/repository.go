package codingagent

import (
	"context"
	"errors"
	"time"
)

var (
	// ErrSessionNotFound identifies a missing Coding product session.
	ErrSessionNotFound = errors.New("Coding session not found")
	// ErrWorktreeNotFound identifies a missing Coding worktree binding.
	ErrWorktreeNotFound = errors.New("Coding worktree not found")
)

// SessionRepository persists Coding product session bindings and metadata.
type SessionRepository interface {
	CreateSession(ctx context.Context, session Session) error
	LoadSession(ctx context.Context, id SessionID) (Session, error)
	ListSessions(ctx context.Context) ([]Session, error)
	SaveSession(ctx context.Context, session Session) error
	BeginSessionCreation(ctx context.Context, intent SessionCreationIntent) error
	CompleteSessionCreation(ctx context.Context, id SessionCreationIntentID, completedAt time.Time) error
	ListSessionCreationIntents(ctx context.Context) ([]SessionCreationIntent, error)
}

// WorktreeReader resolves a Coding worktree by its stable product identity.
type WorktreeReader interface {
	LoadWorktree(ctx context.Context, id WorktreeID) (Worktree, error)
}

// WorkspaceRepository persists product workspaces and their concrete worktrees.
type WorkspaceRepository interface {
	WorktreeReader
	SaveWorkspace(ctx context.Context, workspace Workspace) error
	LoadWorkspace(ctx context.Context, id WorkspaceID) (Workspace, error)
	ListWorkspaces(ctx context.Context) ([]Workspace, error)
	SaveWorktree(ctx context.Context, worktree Worktree) error
	RelocateWorktree(ctx context.Context, id WorktreeID, expectedRoot, root, gitDir, gitCommonDir string, lastUsedAt time.Time) (Worktree, error)
	ListWorktrees(ctx context.Context, workspaceID WorkspaceID) ([]Worktree, error)
}
