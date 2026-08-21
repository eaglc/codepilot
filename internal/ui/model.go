package ui

import (
	"context"

	tea "charm.land/bubbletea/v2"
	"github.com/eaglc/codepilot/internal/session"
)

var _ tea.Model = (*Model)(nil)

// TurnController exposes turn commands used by the root UI.
type TurnController interface {
	StartTurn(ctx context.Context, text string) (session.TurnID, error)
	CancelTurn(ctx context.Context) error
	ResolveApproval(ctx context.Context, resolution session.ApprovalResolution) error
}

// SessionController exposes persisted session commands and queries used by the UI.
type SessionController interface {
	CurrentSession(ctx context.Context) (session.SessionSnapshot, error)
	CreateSession(ctx context.Context, request session.CreateSessionRequest) (session.SessionSummary, error)
	ListSessions(ctx context.Context, filter session.SessionFilter) ([]session.SessionSummary, error)
	SwitchSession(ctx context.Context, id session.SessionID) error
	RenameSession(ctx context.Context, id session.SessionID, title string) error
	ArchiveSession(ctx context.Context, id session.SessionID) error
}

// ModelController is the application boundary used by the provider picker.
// It deliberately exposes no provider adapter or credential store details.
type ModelController interface {
	ListProviderProfiles(ctx context.Context) ([]session.ProviderProfile, error)
	ConfigureProvider(ctx context.Context, request session.ConfigureProviderRequest) (session.ProviderProfile, error)
	ListModels(ctx context.Context, profileID session.ProviderProfileID) ([]session.ModelOption, error)
	SwitchModel(ctx context.Context, selection session.ModelSelection) error
}

// WorkspaceController exposes worktree, diff, and permission operations used by the UI.
type WorkspaceController interface {
	OpenWorkspace(ctx context.Context, path string) (session.WorktreeSummary, error)
	ListWorkspaces(ctx context.Context) ([]session.WorktreeSummary, error)
	ListWorkspaceFiles(ctx context.Context, limit int) (session.WorkspaceFileList, error)
	ReadDiff(ctx context.Context, kind session.DiffKind) (session.DiffResult, error)
	SetPermissionMode(ctx context.Context, mode session.PermissionMode) error
}

// SessionClient groups the small, typed UI-facing application boundaries.
type SessionClient interface {
	TurnController
	SessionController
	ModelController
	WorkspaceController
}

// Model is the root Bubble Tea state for one active CodePilot session.
// Background workers communicate with it exclusively through EventBridge messages.
type Model struct {
	client SessionClient
	bridge *EventBridge

	snapshot         session.SessionSnapshot
	diff             session.DiffResult
	diffKind         session.DiffKind
	width            int
	height           int
	focus            PanelFocus
	activeTurn       session.TurnID
	assistant        string
	status           string
	errorMessage     string
	approval         *session.ApprovalRequest
	staleEvents      uint64
	composer         []rune
	inputBusy        bool
	overlayTitle     string
	overlayText      string
	pendingWorkspace string
	completion       completionState

	workspaceFiles          []session.WorkspaceFile
	workspaceFilesRoot      string
	workspaceFilesLoaded    bool
	workspaceFilesLoading   bool
	workspaceFilesTruncated bool
	workspaceFilesError     string

	providerPicker *ProviderPicker
	sessionPicker  *SessionPicker
}

// NewModel creates the root TUI model with a restored or newly activated snapshot.
func NewModel(client SessionClient, bridge *EventBridge, snapshot session.SessionSnapshot) *Model {
	model := &Model{
		client:   client,
		bridge:   bridge,
		snapshot: cloneSnapshot(snapshot),
		diffKind: session.DiffSession,
		width:    80,
		height:   24,
		focus:    FocusConversation,
	}
	if client != nil {
		model.providerPicker = NewProviderPicker(client)
		model.sessionPicker = NewSessionPicker(client)
	}
	return model
}

// Init starts the single event wait owned by the Bubble Tea update loop.
func (m *Model) Init() tea.Cmd {
	if m == nil {
		return nil
	}
	var commands []tea.Cmd
	if m.bridge != nil {
		commands = append(commands, m.bridge.WaitForEvent())
	}
	if m.client != nil {
		commands = append(commands, readDiffCmd(m.client, m.diffKind))
	}
	if m.providerPicker != nil && (m.snapshot.Session.ProviderProfileID == "" || m.snapshot.Session.ModelID == "") {
		commands = append(commands, m.openProviderPicker())
	}
	return batchCommands(commands...)
}

func cloneSnapshot(value session.SessionSnapshot) session.SessionSnapshot {
	value.Messages = append([]session.Message(nil), value.Messages...)
	value.Turns = append([]session.TurnRecord(nil), value.Turns...)
	value.Patches = append([]session.PatchRecord(nil), value.Patches...)
	for index := range value.Patches {
		value.Patches[index].Files = append([]session.PatchedFile(nil), value.Patches[index].Files...)
	}
	value.RecoveryWarnings = append([]session.RecoveryWarning(nil), value.RecoveryWarnings...)
	return value
}
