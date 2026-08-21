package session

import "time"

// ActionKind identifies an operation considered by the authorization policy.
type ActionKind string

const (
	// ActionRead represents a worktree-scoped read.
	ActionRead ActionKind = "read"
	// ActionApplyPatch represents a validated worktree patch.
	ActionApplyPatch ActionKind = "apply-patch"
	// ActionRunCheck represents execution of an approved project check.
	ActionRunCheck ActionKind = "run-check"
	// ActionStartLanguageServer represents starting one trusted code-intelligence process.
	ActionStartLanguageServer ActionKind = "start-language-server"
)

// PatchAction contains the bounded patch details displayed for approval.
type PatchAction struct {
	Patch string
	Files []string
}

// CommandAction contains the structured command details displayed for approval.
type CommandAction struct {
	Program string
	Args    []string
	Timeout time.Duration
}

// Action is a normalized and hard-validated operation awaiting authorization.
type Action struct {
	ID           string
	SessionID    SessionID
	TurnID       TurnID
	Kind         ActionKind
	WorktreeRoot string
	Summary      string
	Fingerprint  string
	Patch        *PatchAction
	Command      *CommandAction
}

// AuthorizationOutcome is the policy result for a validated action.
type AuthorizationOutcome string

const (
	// AuthorizationAllow permits the action immediately.
	AuthorizationAllow AuthorizationOutcome = "allow"
	// AuthorizationPrompt pauses the action for user approval.
	AuthorizationPrompt AuthorizationOutcome = "prompt"
	// AuthorizationDeny rejects the action.
	AuthorizationDeny AuthorizationOutcome = "deny"
)

// Authorization contains the policy result and optional approval request.
type Authorization struct {
	Outcome AuthorizationOutcome
	Request *ApprovalRequest
	Reason  string
}

// ApprovalRequest identifies one exact action waiting for a user decision.
type ApprovalRequest struct {
	ID        ApprovalRequestID
	SessionID SessionID
	TurnID    TurnID
	Action    Action
	CreatedAt time.Time
}

// ApprovalDecisionKind identifies the scope of a user's decision.
type ApprovalDecisionKind string

const (
	// ApprovalAllowOnce permits one exact action execution.
	ApprovalAllowOnce ApprovalDecisionKind = "allow-once"
	// ApprovalAllowSession permits the exact fingerprint for the active session.
	ApprovalAllowSession ApprovalDecisionKind = "allow-session"
	// ApprovalDeny rejects the pending action.
	ApprovalDeny ApprovalDecisionKind = "deny"
	// ApprovalCancelled indicates that the pending request was cleared or closed.
	ApprovalCancelled ApprovalDecisionKind = "cancelled"
)

// ApprovalDecision records a user's decision and its timestamp.
type ApprovalDecision struct {
	Kind      ApprovalDecisionKind
	DecidedAt time.Time
}

// ApprovalResolution binds a decision to the exact pending request and turn.
type ApprovalResolution struct {
	RequestID ApprovalRequestID
	SessionID SessionID
	TurnID    TurnID
	Decision  ApprovalDecision
}

// ModelSelection identifies the provider profile and model used by a session.
type ModelSelection struct {
	ProviderProfileID ProviderProfileID
	ModelID           string
}

// ConfigureProviderRequest contains provider settings and short-lived credentials.
type ConfigureProviderRequest struct {
	Kind            string
	DisplayName     string
	BaseURL         string
	ModelID         string
	CredentialInput []byte
}
