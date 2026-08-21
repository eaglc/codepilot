package session

import "time"

// WorkspaceRecord stores CodePilot metadata for a logical Git repository.
type WorkspaceRecord struct {
	ID           WorkspaceID
	DisplayName  string
	GitCommonDir string
	Trusted      bool
	CreatedAt    time.Time
	LastUsedAt   time.Time
}

// WorktreeRecord stores CodePilot metadata for a concrete Git checkout.
type WorktreeRecord struct {
	ID            WorktreeID
	WorkspaceID   WorkspaceID
	Root          string
	GitDir        string
	LastSessionID SessionID
	CreatedAt     time.Time
	LastUsedAt    time.Time
}

// ResolvedWorktree contains normalized Git paths discovered from a local path.
type ResolvedWorktree struct {
	DisplayName  string
	Root         string
	GitDir       string
	GitCommonDir string
}

// WorktreeState captures the current Git facts shown by the application.
type WorktreeState struct {
	Root       string
	Branch     string
	HeadCommit string
	Dirty      bool
	Available  bool
}

// WorktreeSummary is the lightweight registered-worktree representation.
type WorktreeSummary struct {
	ID            WorktreeID
	WorkspaceID   WorkspaceID
	DisplayName   string
	Root          string
	LastSessionID SessionID
	Available     bool
	LastUsedAt    time.Time
}

// DiffKind selects the source of diff truth shown to the user.
type DiffKind string

const (
	// DiffProposed is a patch proposal that has not been applied.
	DiffProposed DiffKind = "proposed"
	// DiffSession contains changes recorded for the active session.
	DiffSession DiffKind = "session"
	// DiffWorkspace contains all worktree changes relative to Git HEAD.
	DiffWorkspace DiffKind = "workspace"
)

// DiffRequest binds a diff query to a trusted worktree and optional session.
type DiffRequest struct {
	WorktreeRoot string
	SessionID    SessionID
	Kind         DiffKind
	Files        []string
	// ExpectedHashes contains the latest session-owned post-patch hash by path.
	// It is empty for workspace and proposed diffs.
	ExpectedHashes map[string]string
}

// DiffFile summarizes one file represented in a diff.
type DiffFile struct {
	Path      string
	Status    string
	Additions int
	Deletions int
}

// DiffResult is a bounded diff response from the current worktree state.
type DiffResult struct {
	Kind      DiffKind
	Text      string
	Files     []DiffFile
	Truncated bool
	Drifted   bool
}

// RecoveryWarningCode identifies a non-fatal persisted-state repair.
type RecoveryWarningCode string

const (
	// RecoveryTruncatedLog indicates that an incomplete final JSONL record was
	// ignored while restoring an otherwise valid session.
	RecoveryTruncatedLog RecoveryWarningCode = "truncated-log"
)

// RecoveryWarning is a safe user-facing notice attached to a restored session.
type RecoveryWarning struct {
	Code        RecoveryWarningCode
	Stream      string
	UserMessage string
}

// SessionSnapshot is the complete application view of the active session.
type SessionSnapshot struct {
	Session       Session
	RuntimeState  RuntimeState
	Messages      []Message
	Turns         []TurnRecord
	Patches       []PatchRecord
	WorktreeState WorktreeState
	// RecoveryWarnings do not block activation but should remain visible until
	// the user has seen that incomplete trailing records were ignored.
	RecoveryWarnings []RecoveryWarning
}
