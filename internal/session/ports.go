package session

import (
	"context"
	"time"
)

// CodingAgent completes one coding turn using only neutral session data.
type CodingAgent interface {
	RunTurn(ctx context.Context, request TurnRequest, events EventSink) (TurnResult, error)
	Close() error
}

// CodingAgentFactory creates an agent bound to an immutable worktree and model.
type CodingAgentFactory interface {
	CreateCodingAgent(ctx context.Context, config CodingAgentConfig) (CodingAgent, error)
}

// SessionStore persists session metadata and append-only turn records.
type SessionStore interface {
	CreateSession(ctx context.Context, value Session) error
	LoadSession(ctx context.Context, id SessionID) (SessionSnapshot, error)
	ListSessions(ctx context.Context, filter SessionFilter) ([]SessionSummary, error)
	SaveSession(ctx context.Context, value Session) error
	AppendMessage(ctx context.Context, value Message) error
	AppendPatch(ctx context.Context, value PatchRecord) error
	CommitTurn(ctx context.Context, commit TurnCommit) error
	ArchiveSession(ctx context.Context, id SessionID, archivedAt time.Time) error
}

// WorkspaceRegistry persists CodePilot's workspace and worktree registrations.
type WorkspaceRegistry interface {
	SaveWorkspace(ctx context.Context, value WorkspaceRecord) error
	SaveWorktree(ctx context.Context, value WorktreeRecord) error
	LoadWorkspace(ctx context.Context, id WorkspaceID) (WorkspaceRecord, error)
	LoadWorktree(ctx context.Context, id WorktreeID) (WorktreeRecord, error)
	FindWorktreeByRoot(ctx context.Context, normalizedRoot string) (WorktreeRecord, bool, error)
	ListWorktrees(ctx context.Context) ([]WorktreeSummary, error)
	SaveLastActiveSession(ctx context.Context, id SessionID) error
}

// WorkspaceReader reads normalized Git and filesystem facts without mutation.
type WorkspaceReader interface {
	ResolveWorktree(ctx context.Context, path string) (ResolvedWorktree, error)
	ReadWorktreeState(ctx context.Context, root string) (WorktreeState, error)
	ReadDiff(ctx context.Context, request DiffRequest) (DiffResult, error)
}

// ModelCatalog exposes secret-free provider and model selection operations.
type ModelCatalog interface {
	ListProviderProfiles(ctx context.Context) ([]ProviderProfile, error)
	ConfigureProvider(ctx context.Context, request ConfigureProviderRequest) (ProviderProfile, error)
	ListModels(ctx context.Context, profileID ProviderProfileID) ([]ModelOption, error)
	ValidateSelection(ctx context.Context, selection ModelSelection) (ModelValidation, error)
}

// Authorizer evaluates validated actions and coordinates exact user decisions.
type Authorizer interface {
	Authorize(ctx context.Context, mode PermissionMode, action Action) (Authorization, error)
	WaitDecision(ctx context.Context, requestID ApprovalRequestID) (ApprovalDecision, error)
	Resolve(ctx context.Context, resolution ApprovalResolution) error
	ClearSession(ctx context.Context, sessionID SessionID) error
}

// EventSink receives ordered, stable product events for one session or turn.
type EventSink interface {
	Publish(ctx context.Context, event Event) error
}
