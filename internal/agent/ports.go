package agent

import (
	"context"
	"errors"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/eaglc/codepilot/internal/language"
	"github.com/eaglc/codepilot/internal/provider"
	"github.com/eaglc/codepilot/internal/session"
)

// ListFilesRequest asks for bounded, worktree-relative file names.
type ListFilesRequest struct {
	WorktreeRoot string
	Pattern      string
	Limit        int
}

// FileInfo is a secret-free file summary returned to the coding agent.
type FileInfo struct {
	Path string
	Size int64
}

// ListFilesResult contains safe worktree-relative files.
type ListFilesResult struct {
	Files     []FileInfo
	Truncated bool
}

// SearchCodeRequest describes a bounded literal or regular-expression search.
type SearchCodeRequest struct {
	WorktreeRoot string
	Query        string
	Regex        bool
	Glob         string
	Limit        int
}

// SearchMatch identifies one matching source line.
type SearchMatch struct {
	Path   string
	Line   int
	Column int
	Text   string
}

// SearchCodeResult contains bounded, ordered search matches.
type SearchCodeResult struct {
	Matches   []SearchMatch
	Truncated bool
}

// ReadFileRequest selects a safe file range using one-based line numbers.
type ReadFileRequest struct {
	WorktreeRoot string
	Path         string
	StartLine    int
	LineCount    int
}

// ReadFileResult contains a normalized, bounded text range.
type ReadFileResult struct {
	Path            string
	Content         string
	StartLine       int
	EndLine         int
	TotalLines      int
	TotalLinesKnown bool
	Truncated       bool
}

// GitStatusRequest asks for safe, structured status for one trusted worktree.
type GitStatusRequest struct {
	WorktreeRoot string
}

// GitStatusEntry describes one non-sensitive changed path.
type GitStatusEntry struct {
	Path   string
	Status string
}

// GitStatusResult contains bounded Git state exposed to the coding agent.
type GitStatusResult struct {
	Branch        string
	HeadCommit    string
	Entries       []GitStatusEntry
	Dirty         bool
	HiddenEntries int
	Truncated     bool
}

// ReadDiffRequest uses the same neutral request as the Session application API.
type ReadDiffRequest = session.DiffRequest

// ApplyPatchRequest binds a unified diff to one immutable coding turn.
type ApplyPatchRequest struct {
	WorktreeRoot   string
	SessionID      session.SessionID
	TurnID         session.TurnID
	PermissionMode session.PermissionMode
	Patch          string
	Intent         string
}

// ApplyPatchResult reports either a safe proposal, a policy denial, or an
// applied patch record. Approval prompts are returned with ProposedDiff and an
// ApprovalRequiredError.
type ApplyPatchResult struct {
	Applied      bool
	Denied       bool
	Reason       string
	ProposedDiff session.DiffResult
	PatchRecord  session.PatchRecord
}

// LanguageResolver detects a worktree language and returns only trusted,
// pre-defined guidance and check plans.
type LanguageResolver interface {
	ResolveLanguage(ctx context.Context, root string) (language.LanguageProfile, error)
}

// ErrCodeNavigationUnavailable indicates that code intelligence can safely
// fall back to text search and bounded file reads.
var ErrCodeNavigationUnavailable = errors.New("code navigation is unavailable")

// NavigationScope contains trusted turn facts captured outside model input.
type NavigationScope struct {
	SessionID      session.SessionID
	TurnID         session.TurnID
	WorktreeID     session.WorktreeID
	WorktreeRoot   string
	PermissionMode session.PermissionMode
	Language       language.LanguageID
}

// CodePosition is a one-based source position. Column counts UTF-16 code units
// to match the Language Server Protocol while keeping zero reserved as invalid.
type CodePosition struct {
	Line   int
	Column int
}

// CodeRange is a one-based, end-exclusive source range.
type CodeRange struct {
	Start CodePosition
	End   CodePosition
}

// Location identifies a worktree-relative source range.
type Location struct {
	Path  string
	Range CodeRange
}

// DefinitionRequest asks for definitions at one source position.
type DefinitionRequest struct {
	Scope    NavigationScope
	Path     string
	Position CodePosition
}

// ReferencesRequest asks for bounded references at one source position.
type ReferencesRequest struct {
	Scope              NavigationScope
	Path               string
	Position           CodePosition
	IncludeDeclaration bool
	Limit              int
}

// SymbolsRequest asks for bounded workspace symbols matching a query.
type SymbolsRequest struct {
	Scope NavigationScope
	Query string
	Limit int
}

// DiagnosticsRequest asks for bounded diagnostics for one source document.
type DiagnosticsRequest struct {
	Scope NavigationScope
	Path  string
	Limit int
}

// Symbol is a named workspace symbol at a safe worktree location.
type Symbol struct {
	Name     string
	Kind     string
	Location Location
}

// DiagnosticSeverity is a provider-neutral diagnostic level.
type DiagnosticSeverity string

const (
	// DiagnosticError represents an error reported by the language server.
	DiagnosticError DiagnosticSeverity = "error"
	// DiagnosticWarning represents a warning reported by the language server.
	DiagnosticWarning DiagnosticSeverity = "warning"
	// DiagnosticInformation represents informational feedback.
	DiagnosticInformation DiagnosticSeverity = "information"
	// DiagnosticHint represents a low-priority hint.
	DiagnosticHint DiagnosticSeverity = "hint"
)

// Diagnostic is a bounded, worktree-relative language-server finding.
type Diagnostic struct {
	Path     string
	Range    CodeRange
	Severity DiagnosticSeverity
	Message  string
	Source   string
	Code     string
}

// CodeNavigator is the optional code-intelligence boundary consumed by the
// coding agent. Implementations own all protocol and process state.
type CodeNavigator interface {
	Definition(ctx context.Context, request DefinitionRequest) ([]Location, error)
	References(ctx context.Context, request ReferencesRequest) ([]Location, error)
	Symbols(ctx context.Context, request SymbolsRequest) ([]Symbol, error)
	Diagnostics(ctx context.Context, request DiagnosticsRequest) ([]Diagnostic, error)
	CloseWorktree(ctx context.Context, worktreeID session.WorktreeID) error
}

// RunChecksRequest binds one pre-defined check plan to a coding turn.
type RunChecksRequest struct {
	WorktreeRoot   string
	SessionID      session.SessionID
	TurnID         session.TurnID
	PermissionMode session.PermissionMode
	Command        language.CheckCommand
}

// RunChecksResult contains bounded evidence and a programmatic check outcome.
type RunChecksResult struct {
	PlanID    string
	Outcome   session.CheckOutcome
	Summary   string
	ExitCode  int
	Stdout    string
	Stderr    string
	Duration  time.Duration
	TimedOut  bool
	Truncated bool
	Denied    bool
	Reason    string
}

// WorkspaceTools is the strongly typed boundary captured by one coding turn.
type WorkspaceTools interface {
	ListFiles(ctx context.Context, request ListFilesRequest) (ListFilesResult, error)
	SearchCode(ctx context.Context, request SearchCodeRequest) (SearchCodeResult, error)
	ReadFile(ctx context.Context, request ReadFileRequest) (ReadFileResult, error)
	GitStatus(ctx context.Context, request GitStatusRequest) (GitStatusResult, error)
	ReadDiff(ctx context.Context, request ReadDiffRequest) (session.DiffResult, error)
	ApplyPatch(ctx context.Context, request ApplyPatchRequest) (ApplyPatchResult, error)
	RunChecks(ctx context.Context, request RunChecksRequest) (RunChecksResult, error)
}

// ModelFactory creates the Eino model consumed only by EinoInvoker.
type ModelFactory interface {
	NewChatModel(ctx context.Context, modelRef provider.ModelRef) (model.ToolCallingChatModel, error)
}
