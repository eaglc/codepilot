package session

import "time"

// SessionID uniquely identifies a persisted coding session.
type SessionID string

// TurnID uniquely identifies one user-driven agent turn.
type TurnID string

// MessageID uniquely identifies a persisted neutral message.
type MessageID string

// PatchID uniquely identifies one successfully applied patch.
type PatchID string

// WorkspaceID uniquely identifies a logical Git repository.
type WorkspaceID string

// WorktreeID uniquely identifies a concrete Git checkout.
type WorktreeID string

// ProviderProfileID uniquely identifies a configured provider profile.
type ProviderProfileID string

// ApprovalRequestID uniquely identifies a pending approval request.
type ApprovalRequestID string

// PermissionMode controls which side effects require approval.
type PermissionMode string

const (
	// PermissionReadOnly permits reads and rejects all side effects.
	PermissionReadOnly PermissionMode = "read-only"
	// PermissionAsk prompts before applying patches or running checks.
	PermissionAsk PermissionMode = "ask"
	// PermissionAutoEdit permits validated patches but still prompts for checks.
	PermissionAutoEdit PermissionMode = "auto-edit"
)

// Session stores the durable metadata for a coding conversation.
type Session struct {
	ID                SessionID
	WorkspaceID       WorkspaceID
	WorktreeID        WorktreeID
	Title             string
	ProviderProfileID ProviderProfileID
	ModelID           string
	PermissionMode    PermissionMode
	BaseCommit        string
	LastTurnStatus    TurnStatus
	Archived          bool
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// RuntimeState describes the transient execution state of the active session.
type RuntimeState string

const (
	// RuntimeIdle indicates that the session can accept a new command.
	RuntimeIdle RuntimeState = "idle"
	// RuntimeRunning indicates that an agent turn is executing.
	RuntimeRunning RuntimeState = "running"
	// RuntimeAwaitingApproval indicates that a turn is paused for user approval.
	RuntimeAwaitingApproval RuntimeState = "awaiting-approval"
	// RuntimeCancelling indicates that cancellation is in progress.
	RuntimeCancelling RuntimeState = "cancelling"
)

// CreateSessionRequest contains user-controlled values for a new session.
type CreateSessionRequest struct {
	Title string
}

// SessionFilter limits session listings without loading full session history.
type SessionFilter struct {
	WorkspaceID     WorkspaceID
	WorktreeID      WorktreeID
	IncludeArchived bool
}

// SessionSummary is the lightweight representation used by session pickers.
type SessionSummary struct {
	ID                SessionID
	WorkspaceID       WorkspaceID
	WorktreeID        WorktreeID
	Title             string
	ProviderProfileID ProviderProfileID
	ModelID           string
	PermissionMode    PermissionMode
	LastTurnStatus    TurnStatus
	Archived          bool
	UpdatedAt         time.Time
}

// ProviderProfile is the secret-free provider view exposed to the application.
type ProviderProfile struct {
	ID                 ProviderProfileID
	Kind               string
	DisplayName        string
	BaseURL            string
	ModelID            string
	CredentialRef      string
	CredentialLocation string
	ValidatedAt        time.Time
}

// ModelOption describes a model that can be selected for a provider profile.
type ModelOption struct {
	ID          string
	DisplayName string
	Recommended bool
	Source      string
}

// WorkspaceFile is a safe worktree-relative file or inferred directory exposed
// for UI mentions. Directory paths end with a slash.
type WorkspaceFile struct {
	Path      string
	Size      int64
	Directory bool
}

// WorkspaceFileList contains bounded paths available to the active session.
type WorkspaceFileList struct {
	Files     []WorkspaceFile
	Truncated bool
}

// ModelValidation reports whether a provider and model selection is usable.
type ModelValidation struct {
	Valid       bool
	UserMessage string
	Retryable   bool
}
