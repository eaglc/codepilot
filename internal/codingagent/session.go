package codingagent

import (
	"time"

	agentsession "github.com/eaglc/codepilot/internal/agent/session"
)

// WorkspaceID identifies one logical source repository.
type WorkspaceID string

// WorktreeID identifies one concrete checkout of a workspace.
type WorktreeID string

// SessionCreationIntentID identifies one recoverable cross-repository creation transaction.
type SessionCreationIntentID string

// SessionCreationStatus describes whether both durable session bindings were committed.
type SessionCreationStatus string

const (
	// SessionCreationPending means reconciliation must inspect both repositories.
	SessionCreationPending SessionCreationStatus = "pending"
	// SessionCreationCompleted means both repositories contain the intended binding.
	SessionCreationCompleted SessionCreationStatus = "completed"
)

// PermissionMode controls Coding Agent side effects.
type PermissionMode string

const (
	// PermissionReadOnly forbids workspace mutations.
	PermissionReadOnly PermissionMode = "read_only"
	// PermissionAsk requires product approval before mutations.
	PermissionAsk PermissionMode = "ask"
	// PermissionAutoEdit allows policy-approved edits without another prompt.
	PermissionAutoEdit PermissionMode = "auto_edit"
)

// RuntimeState describes transient Coding Agent execution state.
type RuntimeState string

const (
	// RuntimeIdle means no turn is currently executing.
	RuntimeIdle RuntimeState = "idle"
	// RuntimeRunning means the Agent loop is executing.
	RuntimeRunning RuntimeState = "running"
	// RuntimeAwaitingApproval means product approval is required.
	RuntimeAwaitingApproval RuntimeState = "awaiting_approval"
	// RuntimeInterrupted means a durable interrupt can be resumed.
	RuntimeInterrupted RuntimeState = "interrupted"
	// RuntimeCancelling means cancellation has been requested but not finalized.
	RuntimeCancelling RuntimeState = "cancelling"
)

// Session binds a Coding product session to one generic Agent session and worktree.
type Session struct {
	ID                SessionID         `json:"id"`
	AgentSessionID    agentsession.ID   `json:"agent_session_id"`
	WorkspaceID       WorkspaceID       `json:"workspace_id"`
	WorktreeID        WorktreeID        `json:"worktree_id"`
	Title             string            `json:"title,omitempty"`
	ProviderProfileID string            `json:"provider_profile_id"`
	ModelID           string            `json:"model_id"`
	PermissionMode    PermissionMode    `json:"permission_mode"`
	PermissionGrants  []PermissionGrant `json:"permission_grants,omitempty"`
	SensitivePaths    []string          `json:"sensitive_paths,omitempty"`
	ActiveLane        agentsession.Lane `json:"active_lane,omitempty"`
	BaseCommit        string            `json:"base_commit,omitempty"`
	Archived          bool              `json:"archived,omitempty"`
	CreatedAt         time.Time         `json:"created_at"`
	UpdatedAt         time.Time         `json:"updated_at"`
}

// SessionCreationIntent stores the exact immutable input required to finish a
// Coding/Agent session creation after a crash between repository writes.
type SessionCreationIntent struct {
	ID          SessionCreationIntentID `json:"id"`
	Session     Session                 `json:"session"`
	Status      SessionCreationStatus   `json:"status"`
	CreatedAt   time.Time               `json:"created_at"`
	UpdatedAt   time.Time               `json:"updated_at"`
	CompletedAt time.Time               `json:"completed_at,omitempty"`
}

// CreationIntentID returns the deterministic transaction identity for a product session.
func CreationIntentID(id SessionID) SessionCreationIntentID {
	return SessionCreationIntentID("create_" + string(id))
}

// Workspace identifies a logical repository without exposing it to generic Agent packages.
type Workspace struct {
	ID                    WorkspaceID `json:"id"`
	DisplayName           string      `json:"display_name"`
	GitCommonDir          string      `json:"git_common_dir"`
	RepositoryFingerprint string      `json:"repository_fingerprint,omitempty"`
	Trusted               bool        `json:"trusted"`
	CreatedAt             time.Time   `json:"created_at"`
	UpdatedAt             time.Time   `json:"updated_at"`
}

// Worktree identifies a concrete trusted checkout.
type Worktree struct {
	ID          WorktreeID  `json:"id"`
	WorkspaceID WorkspaceID `json:"workspace_id"`
	Root        string      `json:"root"`
	GitDir      string      `json:"git_dir"`
	CreatedAt   time.Time   `json:"created_at"`
	LastUsedAt  time.Time   `json:"last_used_at"`
}
