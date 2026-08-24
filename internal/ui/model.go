package ui

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/x/ansi"

	"github.com/eaglc/codepilot/internal/codingagent"
)

const (
	maxInputRunes   = 32 << 10
	maxInputHistory = 100
)

// Client is the complete product boundary used by the terminal UI.
type Client interface {
	Snapshot(ctx context.Context, id codingagent.SessionID) (codingagent.Snapshot, error)
	ListSessions(ctx context.Context, options codingagent.SessionListOptions) ([]codingagent.Session, error)
	CreateSession(ctx context.Context, session codingagent.Session) (codingagent.Session, error)
	SwitchSession(ctx context.Context, id codingagent.SessionID) (codingagent.Snapshot, error)
	RenameSession(ctx context.Context, id codingagent.SessionID, title string) (codingagent.Session, error)
	SetPermissionMode(ctx context.Context, id codingagent.SessionID, mode codingagent.PermissionMode) (codingagent.Session, error)
	ArchiveSession(ctx context.Context, id codingagent.SessionID) (codingagent.Session, error)
	ForkLane(ctx context.Context, request codingagent.ForkLaneRequest) (codingagent.Snapshot, error)
	StartTurn(ctx context.Context, request codingagent.TurnRequest) (codingagent.TurnResult, error)
	ResumeTurn(ctx context.Context, request codingagent.ResumeTurnRequest) (codingagent.TurnResult, error)
	RecoverTurn(ctx context.Context, request codingagent.RecoverTurnRequest) (codingagent.TurnResult, error)
	CancelTurn(ctx context.Context, id codingagent.SessionID) error
	ListProviderProfiles(ctx context.Context) ([]codingagent.ProviderProfile, error)
	ConfigureProvider(ctx context.Context, request codingagent.ConfigureProviderRequest) (codingagent.ProviderProfile, error)
	ListProviderModels(ctx context.Context, profileID string) ([]codingagent.ProviderModel, error)
	SelectProviderModel(ctx context.Context, sessionID codingagent.SessionID, profileID, modelID string) (codingagent.Session, error)
	ListWorkspaces(ctx context.Context) ([]codingagent.WorkspaceSummary, error)
	RelocateWorktree(ctx context.Context, request codingagent.RelocateWorktreeRequest) (codingagent.Worktree, error)
}

// Option configures initial product presentation state.
type Option func(*Model)

// WithProviderIssue opens the Provider picker with a safe preflight failure.
func WithProviderIssue(issue *codingagent.ProviderIssue) Option {
	return func(model *Model) {
		if issue == nil {
			return
		}
		model.picker = newProviderPicker(issue.Message, true)
	}
}

type styles struct {
	header, muted, user, userInput, assistant, tool, success, warning, failure lipgloss.Style
	added, removed, hunk, selection, textSelected                              lipgloss.Style
}

var theme = styles{
	header:       lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#A78BFA")),
	muted:        lipgloss.NewStyle().Foreground(lipgloss.Color("#7C8496")),
	user:         lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#67E8F9")),
	userInput:    lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#ECFEFF")).Background(lipgloss.Color("#164E63")),
	assistant:    lipgloss.NewStyle().Foreground(lipgloss.Color("#E5E7EB")),
	tool:         lipgloss.NewStyle().Foreground(lipgloss.Color("#C4B5FD")),
	success:      lipgloss.NewStyle().Foreground(lipgloss.Color("#6EE7B7")),
	warning:      lipgloss.NewStyle().Foreground(lipgloss.Color("#FCD34D")),
	failure:      lipgloss.NewStyle().Foreground(lipgloss.Color("#FCA5A5")),
	added:        lipgloss.NewStyle().Foreground(lipgloss.Color("#86EFAC")),
	removed:      lipgloss.NewStyle().Foreground(lipgloss.Color("#FDA4AF")),
	hunk:         lipgloss.NewStyle().Foreground(lipgloss.Color("#93C5FD")),
	selection:    lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#F8FAFC")).Background(lipgloss.Color("#334155")),
	textSelected: lipgloss.NewStyle().Foreground(lipgloss.Color("#FFFFFF")).Background(lipgloss.Color("#2563EB")),
}

type renderRow struct {
	text        string
	toolID      string
	selectionID string
}

type scrollbarState struct {
	active      bool
	dragging    bool
	trackTop    int
	trackHeight int
	thumbTop    int
	thumbHeight int
	maxScroll   int
	dragOffset  int
}

type Model struct {
	ctx       context.Context
	client    Client
	bridge    *EventBridge
	sessionID codingagent.SessionID
	snapshot  codingagent.Snapshot

	width, height       int
	input               []rune
	cursor              int
	history             []string
	historyIndex        int
	busy                bool
	thinking            bool
	status              string
	errorMessage        string
	liveAssistant       string
	activities          map[string]codingagent.ToolActivityEvent
	expanded            map[string]bool
	selectedTool        string
	selectedBlock       string
	textSelection       textSelection
	mouseDownBlock      string
	scroll              int
	followBottom        bool
	scrollbar           scrollbarState
	hitRows             map[int]string
	hitBlocks           map[int]string
	hitTextRows         map[int]textHit
	markdownCache       map[string][]string
	markdownEnabled     bool
	completionCursor    int
	completionDismissed bool
	quitting            bool
	turnCancel          context.CancelFunc
	picker              providerPicker
	sessionPicker       sessionPicker
	workspacePicker     workspacePicker
	permissionPicker    permissionPicker
	approvalCursor      int
	approvalInterruptID string
	forkPicker          forkPicker
	helpActive          bool
	generation          uint64
}

type eventMsg struct{ event codingagent.Event }
type eventClosedMsg struct{}
type snapshotMsg struct {
	snapshot   codingagent.Snapshot
	err        error
	sessionID  codingagent.SessionID
	generation uint64
}
type turnResultMsg struct {
	result     codingagent.TurnResult
	err        error
	sessionID  codingagent.SessionID
	generation uint64
}
type resumeResultMsg struct {
	result     codingagent.TurnResult
	err        error
	sessionID  codingagent.SessionID
	generation uint64
}
type recoveryResultMsg struct {
	result     codingagent.TurnResult
	err        error
	sessionID  codingagent.SessionID
	generation uint64
}
type cancelResultMsg struct {
	err        error
	sessionID  codingagent.SessionID
	generation uint64
}
type autoTitleMsg struct {
	session    codingagent.Session
	err        error
	sessionID  codingagent.SessionID
	generation uint64
}
type blinkMsg struct{}

// NewModel creates a single-column, command-line style conversation UI.
func NewModel(ctx context.Context, client Client, bridge *EventBridge, initial codingagent.Snapshot, options ...Option) (*Model, error) {
	if ctx == nil || client == nil || bridge == nil || initial.Session.ID == "" {
		return nil, fmt.Errorf("create terminal UI: context, client, event bridge, and active session are required")
	}
	model := &Model{
		ctx: ctx, client: client, bridge: bridge, sessionID: initial.Session.ID, snapshot: initial,
		width: 80, height: 24, activities: make(map[string]codingagent.ToolActivityEvent),
		expanded: make(map[string]bool), markdownCache: make(map[string][]string), markdownEnabled: true, followBottom: true, history: historyFromSnapshot(initial), historyIndex: -1, generation: 1,
		hitTextRows: make(map[int]textHit),
	}
	for _, option := range options {
		if option != nil {
			option(model)
		}
	}
	return model, nil
}

func (m *Model) Init() tea.Cmd {
	commands := []tea.Cmd{m.waitEvent(), blinkCmd()}
	if m.picker.active {
		commands = append(commands, m.loadProviderProfiles())
	}
	return tea.Batch(commands...)
}

func (m *Model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch value := message.(type) {
	case tea.WindowSizeMsg:
		if m.width != value.Width || m.height != value.Height {
			m.clearTextSelection()
			m.scrollbar.dragging = false
		}
		m.width, m.height = max(20, value.Width), max(6, value.Height)
	case tea.KeyPressMsg:
		if m.helpActive {
			return m, m.handleHelpKey(value)
		}
		if m.forkPicker.active {
			return m, m.handleForkKey(value)
		}
		if m.permissionPicker.active {
			return m, m.handlePermissionKey(value)
		}
		if m.workspacePicker.active {
			return m, m.handleWorkspaceKey(value)
		}
		if m.sessionPicker.active {
			return m, m.handleSessionKey(value)
		}
		if m.picker.active {
			return m, m.handleProviderKey(value)
		}
		return m, m.handleKey(value)
	case tea.PasteMsg:
		if m.helpActive || m.permissionPicker.active || m.forkPicker.active {
			break
		} else if m.workspacePicker.active {
			m.pasteWorkspaceInput(value.Content)
		} else if m.sessionPicker.active {
			break
		} else if m.picker.active {
			m.pasteProviderInput(value.Content)
		} else if !m.busy && m.pendingApproval() == nil && m.pendingRecovery() == nil {
			m.clearTextSelection()
			m.insert([]rune(value.Content))
		}
	case tea.MouseWheelMsg:
		m.scrollbar.dragging = false
		if value.Mouse().Button == tea.MouseWheelUp {
			m.followBottom = false
			m.scroll = max(0, m.scroll-3)
		} else if value.Mouse().Button == tea.MouseWheelDown {
			m.scroll += 3
		}
	case tea.MouseClickMsg:
		if value.Mouse().Button == tea.MouseLeft && !m.overlayActive() {
			if !m.beginScrollbarDrag(value.Mouse()) {
				m.beginMouseTextSelection(value.Mouse())
			}
		}
	case tea.MouseMotionMsg:
		if !m.overlayActive() {
			if m.scrollbar.dragging {
				m.updateScrollbarDrag(value.Mouse())
			} else {
				m.updateMouseTextSelection(value.Mouse())
			}
		}
	case tea.MouseReleaseMsg:
		if value.Mouse().Button == tea.MouseLeft && !m.overlayActive() {
			if m.scrollbar.dragging {
				m.updateScrollbarDrag(value.Mouse())
				m.scrollbar.dragging = false
			} else {
				m.finishMouseTextSelection(value.Mouse())
			}
		}
	case eventMsg:
		commands := []tea.Cmd{m.waitEvent()}
		if value.event.SessionID != m.sessionID {
			return m, tea.Batch(commands...)
		}
		m.applyEvent(value.event)
		if eventNeedsSnapshot(value.event.Kind) {
			commands = append(commands, m.loadSnapshot())
		}
		return m, tea.Batch(commands...)
	case eventClosedMsg:
		m.status = "Event stream closed."
	case snapshotMsg:
		if value.generation != m.generation || value.sessionID != m.sessionID {
			break
		}
		if value.err != nil {
			m.errorMessage = safeError(value.err)
		} else if value.snapshot.Session.ID == m.sessionID && value.snapshot.Revision >= m.snapshot.Revision {
			m.snapshot = value.snapshot
		}
	case turnResultMsg:
		if value.generation != m.generation || value.sessionID != m.sessionID {
			break
		}
		m.clearTurnCancel()
		m.busy = false
		if value.err != nil {
			m.errorMessage = safeError(value.err)
			m.status = "Turn failed."
		} else if value.result.Status == "interrupted" {
			m.status = "Waiting for approval."
		} else {
			m.status = "Ready"
			m.liveAssistant = ""
		}
		return m, m.loadSnapshot()
	case resumeResultMsg:
		if value.generation != m.generation || value.sessionID != m.sessionID {
			break
		}
		m.clearTurnCancel()
		m.busy = false
		if value.err != nil {
			m.errorMessage = safeError(value.err)
			m.status = "The approval decision could not be applied."
		} else if value.result.Status == "interrupted" {
			m.status = "Waiting for approval."
		} else {
			m.status = "Ready"
			m.liveAssistant = ""
		}
		return m, m.loadSnapshot()
	case recoveryResultMsg:
		if value.generation != m.generation || value.sessionID != m.sessionID {
			break
		}
		m.clearTurnCancel()
		m.busy = false
		if value.err != nil {
			m.errorMessage = safeError(value.err)
			m.status = "Recovery action failed safely."
		} else if value.result.Status == "interrupted" {
			m.status = "Recovery needs another decision."
		} else {
			m.status = "Ready"
			m.liveAssistant = ""
		}
		return m, m.loadSnapshot()
	case cancelResultMsg:
		if value.generation != m.generation || value.sessionID != m.sessionID {
			break
		}
		if value.err != nil {
			m.errorMessage = safeError(value.err)
			m.status = "Cancellation request failed."
		} else {
			m.status = "Cancellation requested..."
		}
	case autoTitleMsg:
		if value.generation == m.generation && value.sessionID == m.sessionID && value.err == nil && value.session.ID == m.sessionID {
			m.snapshot.Session = value.session
		}
	case blinkMsg:
		return m, blinkCmd()
	case providerProfilesMsg:
		m.applyProviderProfiles(value)
	case providerModelsMsg:
		m.applyProviderModels(value)
	case providerSavedMsg:
		return m, m.applyProviderSaved(value)
	case providerSelectedMsg:
		return m, m.applyProviderSelected(value)
	case sessionsMsg:
		if value.generation != m.generation || !m.sessionPicker.active {
			break
		}
		m.sessionPicker.loading = false
		if value.err != nil {
			m.sessionPicker.error = safeError(value.err)
		} else {
			m.sessionPicker.sessions = value.sessions
			m.sessionPicker.cursor = 0
			for index := range value.sessions {
				if value.sessions[index].ID == m.sessionID {
					m.sessionPicker.cursor = index
					break
				}
			}
		}
	case sessionSwitchedMsg:
		if value.generation != m.generation {
			break
		}
		if value.err != nil {
			m.sessionPicker.loading = false
			m.sessionPicker.error = safeError(value.err)
		} else {
			m.activateSnapshot(value.snapshot)
		}
	case sessionCreatedMsg:
		if value.generation != m.generation {
			break
		}
		m.busy = false
		if value.err != nil {
			m.errorMessage = safeError(value.err)
			m.status = "Session creation failed."
		} else {
			m.activateSnapshot(value.snapshot)
		}
	case sessionRenamedMsg:
		if value.generation != m.generation {
			break
		}
		m.busy = false
		if value.err != nil {
			m.errorMessage = safeError(value.err)
			m.status = "Session rename failed."
		} else {
			m.snapshot.Session = value.session
			m.status = "Ready"
		}
	case sessionArchivedMsg:
		if value.generation != m.generation || !m.sessionPicker.active {
			break
		}
		m.sessionPicker.loading = false
		if value.err != nil {
			m.sessionPicker.error = safeError(value.err)
		} else {
			m.sessionPicker.loading = true
			return m, m.loadSessions()
		}
	case laneForkedMsg:
		if value.generation != m.generation {
			break
		}
		m.busy = false
		if value.err != nil {
			if m.forkPicker.active {
				m.forkPicker.loading = false
				m.forkPicker.error = safeError(value.err)
			} else {
				m.errorMessage = safeError(value.err)
			}
			m.status = "Conversation fork failed."
		} else {
			m.activateSnapshot(value.snapshot)
			m.status = "Conversation branch activated."
		}
	case permissionModeChangedMsg:
		m.applyPermissionModeChanged(value)
	case workspacesMsg:
		m.applyWorkspaces(value)
	case workspaceActivatedMsg:
		m.applyWorkspaceActivated(value)
	}
	return m, nil
}

func (m *Model) handleKey(message tea.KeyPressMsg) tea.Cmd {
	key := message.Key()
	if key.Mod&tea.ModAlt != 0 && key.Code == 'm' && !m.busy {
		m.setMarkdownEnabled(!m.markdownEnabled)
		return nil
	}
	if key.Mod&tea.ModCtrl != 0 && key.Code == 's' && !m.busy {
		m.sessionPicker = newSessionPicker()
		return m.loadSessions()
	}
	if key.Code == tea.KeyPgUp {
		m.followBottom = false
		m.scroll = max(0, m.scroll-max(3, m.bodyHeight()-2))
		return nil
	}
	if key.Code == tea.KeyPgDown {
		m.scroll += max(3, m.bodyHeight()-2)
		return nil
	}
	if pending := m.pendingApproval(); pending != nil {
		return m.handleApprovalKey(*pending, message)
	}
	if pending := m.pendingRecovery(); pending != nil {
		decision := codingagent.RecoveryDecision("")
		switch strings.ToLower(key.Text) {
		case "r":
			decision = codingagent.RecoveryRetry
		case "x":
			decision = codingagent.RecoveryConfirmExecuted
		case "f":
			decision = codingagent.RecoveryMarkFailed
		case "a":
			decision = codingagent.RecoveryAbandonTurn
		}
		if decision != "" && recoveryAllows(*pending, decision) {
			m.busy = true
			m.errorMessage = ""
			m.status = "Applying recovery decision..."
			return m.recover(*pending, decision)
		}
		return nil
	}
	if m.textSelection.hasRange() && !m.busy {
		switch {
		case strings.EqualFold(key.Text, "y"), key.Mod&tea.ModCtrl != 0 && key.Code == 'c':
			return m.copyTextSelection()
		case key.Code == tea.KeyEscape || key.Code == tea.KeyEsc:
			m.clearTextSelection()
			return nil
		}
	}
	if key.Mod&tea.ModCtrl != 0 && key.Code == 'c' {
		if m.busy {
			if m.turnCancel != nil {
				m.turnCancel()
			}
			m.status = "Cancelling active operation..."
			return m.cancelTurn()
		}
		return tea.Quit
	}
	if key.Mod&tea.ModCtrl != 0 && key.Code == 'd' && len(m.input) == 0 && !m.busy {
		return tea.Quit
	}
	if m.busy {
		return nil
	}
	if m.completionActive() {
		switch key.Code {
		case tea.KeyEscape:
			m.completionDismissed = true
			return nil
		case tea.KeyUp:
			m.moveCompletionSelection(-1)
			return nil
		case tea.KeyDown:
			m.moveCompletionSelection(1)
			return nil
		case tea.KeyTab:
			if key.Mod&tea.ModAlt == 0 {
				m.completeCommand()
				return nil
			}
		case tea.KeyEnter:
			if key.Mod&tea.ModAlt == 0 {
				return m.submitSelectedCommand()
			}
		}
	}
	if key.Code == tea.KeyEscape || key.Code == tea.KeyEsc {
		m.clearAllSelections()
		return nil
	}
	if key.Code == tea.KeyTab && len(m.input) == 0 {
		m.clearTextSelection()
		m.cycleSelection()
		return nil
	}
	if m.selectedBlock != "" && len(m.input) == 0 {
		switch {
		case strings.EqualFold(key.Text, "y"):
			return m.copySelection()
		case key.Code == tea.KeyEnter || key.Text == " ":
			if m.selectedTool != "" {
				m.expanded[m.selectedTool] = !m.expanded[m.selectedTool]
			}
			return nil
		case key.Code == tea.KeyLeft:
			m.expanded[m.selectedTool] = false
			return nil
		case key.Code == tea.KeyRight:
			m.expanded[m.selectedTool] = true
			return nil
		}
	}
	switch key.Code {
	case tea.KeyEnter:
		if key.Mod&tea.ModAlt != 0 {
			m.insert([]rune{'\n'})
			return nil
		}
		return m.submit()
	case tea.KeyLeft:
		m.cursor = max(0, m.cursor-1)
	case tea.KeyRight:
		m.cursor = min(len(m.input), m.cursor+1)
	case tea.KeyHome:
		m.cursor = 0
	case tea.KeyEnd:
		m.cursor = len(m.input)
	case tea.KeyBackspace:
		if m.cursor > 0 {
			m.input = append(m.input[:m.cursor-1], m.input[m.cursor:]...)
			m.cursor--
			m.resetCompletion()
		}
	case tea.KeyDelete:
		if m.cursor < len(m.input) {
			m.input = append(m.input[:m.cursor], m.input[m.cursor+1:]...)
			m.resetCompletion()
		}
	case tea.KeyUp:
		m.historyBack()
	case tea.KeyDown:
		m.historyForward()
	default:
		if key.Text != "" && key.Mod&tea.ModCtrl == 0 {
			m.resetCompletion()
			m.insert([]rune(key.Text))
		}
	}
	return nil
}

func (m *Model) cancelTurn() tea.Cmd {
	client := m.client
	ctx := m.ctx
	sessionID := m.sessionID
	generation := m.generation
	return func() tea.Msg {
		return cancelResultMsg{err: client.CancelTurn(ctx, sessionID), sessionID: sessionID, generation: generation}
	}
}

func (m *Model) submit() tea.Cmd {
	text := strings.TrimSpace(string(m.input))
	if text == "" {
		return nil
	}
	if strings.HasPrefix(text, "/") {
		return m.submitCommand(text)
	}
	m.clearTextSelection()
	m.history = append(m.history, text)
	if len(m.history) > maxInputHistory {
		m.history = append([]string(nil), m.history[len(m.history)-maxInputHistory:]...)
	}
	m.historyIndex = -1
	m.clearInput()
	m.busy = true
	m.errorMessage = ""
	m.liveAssistant = ""
	m.activities = make(map[string]codingagent.ToolActivityEvent)
	m.followBottom = true
	m.status = "Agent is working..."
	turnCtx, cancel := context.WithCancel(m.ctx)
	m.turnCancel = cancel
	client, ctx, sessionID, generation := m.client, m.ctx, m.sessionID, m.generation
	turnCommand := func() tea.Msg {
		result, err := client.StartTurn(turnCtx, codingagent.TurnRequest{SessionID: sessionID, Text: text})
		return turnResultMsg{result: result, err: err, sessionID: sessionID, generation: generation}
	}
	if strings.TrimSpace(m.snapshot.Session.Title) == "" {
		title := titleFromPrompt(text)
		return tea.Batch(turnCommand, func() tea.Msg {
			session, err := client.RenameSession(ctx, sessionID, title)
			return autoTitleMsg{session: session, err: err, sessionID: sessionID, generation: generation}
		})
	}
	return turnCommand
}

func (m *Model) resume(pending codingagent.PendingInterrupt, decision codingagent.ResolutionDecision, scope codingagent.PermissionGrantScope) tea.Cmd {
	turnCtx, cancel := context.WithCancel(m.ctx)
	m.turnCancel = cancel
	client, sessionID, generation := m.client, m.sessionID, m.generation
	return func() tea.Msg {
		result, err := client.ResumeTurn(turnCtx, codingagent.ResumeTurnRequest{
			SessionID: sessionID, TurnID: pending.TurnID, InterruptID: pending.InterruptID, Decision: decision, GrantScope: scope,
		})
		return resumeResultMsg{result: result, err: err, sessionID: sessionID, generation: generation}
	}
}

func (m *Model) recover(pending codingagent.RecoveryAction, decision codingagent.RecoveryDecision) tea.Cmd {
	turnCtx, cancel := context.WithCancel(m.ctx)
	m.turnCancel = cancel
	client, sessionID, generation := m.client, m.sessionID, m.generation
	return func() tea.Msg {
		result, err := client.RecoverTurn(turnCtx, codingagent.RecoverTurnRequest{
			SessionID: sessionID, TurnID: pending.TurnID, ActionID: pending.ID, Decision: decision,
		})
		return recoveryResultMsg{result: result, err: err, sessionID: sessionID, generation: generation}
	}
}

func (m *Model) clearTurnCancel() {
	if m.turnCancel != nil {
		m.turnCancel()
		m.turnCancel = nil
	}
}

func (m *Model) insert(value []rune) {
	remaining := maxInputRunes - len(m.input)
	if remaining <= 0 {
		return
	}
	if len(value) > remaining {
		value = value[:remaining]
	}
	m.input = append(m.input, make([]rune, len(value))...)
	copy(m.input[m.cursor+len(value):], m.input[m.cursor:len(m.input)-len(value)])
	copy(m.input[m.cursor:], value)
	m.cursor += len(value)
	m.completionCursor = 0
}

func (m *Model) replaceInput(value string) {
	clear(m.input)
	m.input = []rune(value)
	m.cursor = len(m.input)
	m.completionCursor = 0
	m.completionDismissed = false
}

func (m *Model) clearInput() {
	clear(m.input)
	m.input = nil
	m.cursor = 0
	m.resetCompletion()
}

func (m *Model) resetCompletion() {
	m.completionCursor = 0
	m.completionDismissed = false
}

func (m *Model) historyBack() {
	if len(m.history) == 0 {
		return
	}
	if m.historyIndex < 0 {
		m.historyIndex = len(m.history) - 1
	} else if m.historyIndex > 0 {
		m.historyIndex--
	}
	m.input = []rune(m.history[m.historyIndex])
	m.cursor = len(m.input)
}

func (m *Model) historyForward() {
	if m.historyIndex < 0 {
		return
	}
	if m.historyIndex < len(m.history)-1 {
		m.historyIndex++
		m.input = []rune(m.history[m.historyIndex])
	} else {
		m.historyIndex = -1
		m.clearInput()
	}
	m.cursor = len(m.input)
}

func (m *Model) waitEvent() tea.Cmd {
	events := m.bridge.Events()
	return func() tea.Msg {
		event, ok := <-events
		if !ok {
			return eventClosedMsg{}
		}
		return eventMsg{event: event}
	}
}

func (m *Model) loadSnapshot() tea.Cmd {
	client, ctx, sessionID, generation := m.client, m.ctx, m.sessionID, m.generation
	return func() tea.Msg {
		snapshot, err := client.Snapshot(ctx, sessionID)
		return snapshotMsg{snapshot: snapshot, err: err, sessionID: sessionID, generation: generation}
	}
}

func (m *Model) activateSnapshot(snapshot codingagent.Snapshot) {
	if snapshot.Session.ID == "" {
		return
	}
	m.clearTurnCancel()
	m.generation++
	m.sessionID = snapshot.Session.ID
	m.snapshot = snapshot
	m.clearInput()
	m.history = historyFromSnapshot(snapshot)
	m.historyIndex = -1
	m.busy = false
	m.thinking = false
	m.status = "Ready"
	m.errorMessage = ""
	m.liveAssistant = ""
	m.activities = make(map[string]codingagent.ToolActivityEvent)
	m.expanded = make(map[string]bool)
	m.clearAllSelections()
	m.scroll = 0
	m.followBottom = true
	m.hitRows = make(map[int]string)
	m.hitBlocks = make(map[int]string)
	m.hitTextRows = make(map[int]textHit)
	m.markdownCache = make(map[string][]string)
	m.sessionPicker = sessionPicker{}
	m.picker = providerPicker{}
	m.workspacePicker = workspacePicker{}
	m.permissionPicker = permissionPicker{}
	m.approvalCursor = 0
	m.approvalInterruptID = ""
	m.forkPicker = forkPicker{}
	m.helpActive = false
}

func historyFromSnapshot(snapshot codingagent.Snapshot) []string {
	values := make([]string, 0, min(len(snapshot.Transcript), maxInputHistory))
	for _, item := range snapshot.Transcript {
		if item.Kind != codingagent.TranscriptText || item.Role != codingagent.TranscriptRoleUser {
			continue
		}
		value := strings.TrimSpace(item.Text)
		if value == "" || utf8.RuneCountInString(value) > maxInputRunes {
			continue
		}
		values = append(values, value)
		if len(values) > maxInputHistory {
			values = append([]string(nil), values[len(values)-maxInputHistory:]...)
		}
	}
	return values
}

func (m *Model) applyEvent(event codingagent.Event) {
	if event.SessionID != m.sessionID {
		return
	}
	if event.Payload.Turn != nil && event.Payload.Turn.Steps > m.snapshot.Metrics.Steps {
		m.snapshot.Metrics.Steps = event.Payload.Turn.Steps
	}
	switch event.Kind {
	case codingagent.EventTurnStarted:
		m.snapshot.Metrics.LatestTurnID = event.TurnID
		m.snapshot.Metrics.Steps = 0
		m.snapshot.Metrics.StartedAt = event.Timestamp
		m.snapshot.Metrics.FinishedAt = time.Time{}
		m.snapshot.Metrics.Elapsed = 0
	case codingagent.EventAssistantOutputDelta:
		if event.Payload.AssistantOutput != nil {
			m.liveAssistant += event.Payload.AssistantOutput.Delta
			m.followBottom = true
		}
	case codingagent.EventAssistantStatusChanged:
		if event.Payload.AssistantStatus != nil {
			m.thinking = event.Payload.AssistantStatus.Thinking
		}
	case codingagent.EventAssistantOutputFinished:
		m.liveAssistant = ""
	case codingagent.EventToolActivityStarted, codingagent.EventToolActivityUpdated, codingagent.EventToolActivityFinished:
		if event.Payload.Tool != nil {
			m.activities[event.Payload.Tool.CallID] = *event.Payload.Tool
			m.followBottom = true
		}
	case codingagent.EventApprovalRequested:
		m.busy = false
		m.status = "Waiting for approval."
		m.followBottom = true
	case codingagent.EventTurnCompleted, codingagent.EventTurnFailed, codingagent.EventTurnCancelled:
		m.busy = false
		m.thinking = false
		m.snapshot.Metrics.FinishedAt = event.Timestamp
		if !m.snapshot.Metrics.StartedAt.IsZero() && !event.Timestamp.Before(m.snapshot.Metrics.StartedAt) {
			m.snapshot.Metrics.Elapsed = event.Timestamp.Sub(m.snapshot.Metrics.StartedAt)
		}
	}
}

func eventNeedsSnapshot(kind codingagent.EventKind) bool {
	switch kind {
	case codingagent.EventAssistantOutputDelta,
		codingagent.EventAssistantStatusChanged,
		codingagent.EventToolActivityUpdated,
		codingagent.EventTurnProgressChanged:
		return false
	default:
		return true
	}
}

func (m *Model) pendingApproval() *codingagent.PendingInterrupt {
	for index := range m.snapshot.PendingInterrupts {
		if m.snapshot.PendingInterrupts[index].Kind == "approval" {
			return &m.snapshot.PendingInterrupts[index]
		}
	}
	return nil
}

func (m *Model) pendingRecovery() *codingagent.RecoveryAction {
	if m.busy || m.snapshot.RuntimeState == codingagent.RuntimeRunning || m.snapshot.RuntimeState == codingagent.RuntimeCancelling {
		return nil
	}
	for index := range m.snapshot.RecoveryActions {
		if m.snapshot.RecoveryActions[index].Kind != "resolve_interrupt" {
			return &m.snapshot.RecoveryActions[index]
		}
	}
	if m.pendingApproval() == nil && len(m.snapshot.RecoveryActions) != 0 {
		return &m.snapshot.RecoveryActions[0]
	}
	return nil
}

func (m *Model) recoveryForTurn(turnID codingagent.TurnID) *codingagent.RecoveryAction {
	for index := range m.snapshot.RecoveryActions {
		if m.snapshot.RecoveryActions[index].TurnID == turnID {
			return &m.snapshot.RecoveryActions[index]
		}
	}
	return nil
}

func recoveryAllows(action codingagent.RecoveryAction, decision codingagent.RecoveryDecision) bool {
	for _, allowed := range action.Decisions {
		if allowed == decision {
			return true
		}
	}
	return false
}

func (m *Model) View() tea.View {
	width, height := max(20, m.width), max(6, m.height)
	if m.workspacePicker.active {
		return m.workspaceView(width, height)
	}
	if m.helpActive {
		return m.helpView(width, height)
	}
	if m.forkPicker.active {
		return m.forkView(width, height)
	}
	if m.permissionPicker.active {
		return m.permissionView(width, height)
	}
	if m.sessionPicker.active {
		return m.sessionView(width, height)
	}
	if m.picker.active {
		return m.providerView(width, height)
	}
	completionLines := m.commandCompletionLines(width, max(0, height-6))
	rows := m.conversationRows(width)
	bodyHeight := m.bodyHeight()
	maxScroll := max(0, len(rows)-bodyHeight)
	if m.followBottom || m.scroll >= maxScroll {
		m.scroll = maxScroll
		m.followBottom = true
	} else {
		m.scroll = min(m.scroll, maxScroll)
	}
	m.configureScrollbar(bodyHeight, maxScroll)
	visible := rows[m.scroll:min(len(rows), m.scroll+bodyHeight)]
	m.hitRows = make(map[int]string)
	m.hitBlocks = make(map[int]string)
	m.hitTextRows = make(map[int]textHit)
	rootName := filepath.Base(m.snapshot.Session.Title)
	if strings.TrimSpace(rootName) == "" || rootName == "." {
		rootName = "CodePilot"
	}
	header := theme.header.Render("CodePilot") + theme.muted.Render("  "+rootName+"  •  "+m.snapshot.Session.ProviderProfileID+"/"+m.snapshot.Session.ModelID)
	if distanceFromBottom := maxScroll - m.scroll; distanceFromBottom > 0 {
		header += theme.muted.Render(fmt.Sprintf("  •  %d lines below", distanceFromBottom))
	}
	lines := []string{truncateANSI(header, width)}
	for index := 0; index < bodyHeight; index++ {
		screenY := index + 1
		if index < len(visible) {
			lines = append(lines, m.renderScrollbarColumn(truncateANSI(visible[index].text, width), screenY, width))
			m.hitTextRows[screenY] = textHit{row: m.scroll + index, text: ansi.Strip(visible[index].text)}
			if visible[index].selectionID != "" {
				m.hitBlocks[screenY] = visible[index].selectionID
			}
			if visible[index].toolID != "" {
				m.hitRows[screenY] = visible[index].toolID
			}
		} else {
			lines = append(lines, m.renderScrollbarColumn("", screenY, width))
		}
	}
	lines = append(lines, completionLines...)
	promptY := len(lines)
	prompt, promptX, promptCursor := m.renderPrompt(width)
	lines = append(lines, prompt)
	lines = append(lines, truncateANSI(m.footerDividerLine(width), width))
	lines = append(lines, truncateANSI(m.sessionMetricsLine(), width))
	lines = append(lines, truncateANSI(m.statusLine(), width))
	view := tea.NewView(strings.Join(lines, "\n"))
	view.AltScreen = true
	view.MouseMode = tea.MouseModeCellMotion
	view.WindowTitle = "CodePilot"
	view.BackgroundColor = lipgloss.Color("#111318")
	view.ForegroundColor = lipgloss.Color("#E5E7EB")
	if promptCursor {
		view.Cursor = nativeTextCursor(promptX, promptY)
	}
	return view
}

func (m *Model) overlayActive() bool {
	return m.workspacePicker.active || m.helpActive || m.forkPicker.active || m.permissionPicker.active || m.sessionPicker.active || m.picker.active
}

func (m *Model) conversationRows(width int) []renderRow {
	contentWidth := max(8, width-2)
	results := make(map[string]codingagent.TranscriptTool)
	for _, item := range m.snapshot.Transcript {
		if item.Kind == codingagent.TranscriptToolResult && item.Tool != nil {
			results[item.Tool.CallID] = *item.Tool
		}
	}
	var rows []renderRow
	for _, warning := range m.snapshot.RecoveryWarnings {
		if strings.TrimSpace(warning) != "" {
			rows = appendWrapped(rows, "⚠ ", warning, contentWidth, theme.warning)
			rows = append(rows, renderRow{})
		}
	}
	seenResults := make(map[string]bool)
	for itemIndex, item := range m.snapshot.Transcript {
		switch item.Kind {
		case codingagent.TranscriptText:
			selectionID := messageSelectionKey(item, itemIndex)
			start := len(rows)
			selected := selectionID == m.selectedBlock
			if item.Role == codingagent.TranscriptRoleUser {
				rows = appendUserMessage(rows, item.Text, contentWidth)
				if elapsed, finished, ok := m.turnElapsed(item); ok {
					label := "Working for"
					if finished {
						label = "Worked for"
					}
					progress := fmt.Sprintf("  ✻ %s %s", label, formatMetricDuration(elapsed))
					if item.TurnID == m.snapshot.Metrics.LatestTurnID && m.snapshot.Metrics.Steps > 0 {
						progress += fmt.Sprintf("  •  %d %s", m.snapshot.Metrics.Steps, pluralize(m.snapshot.Metrics.Steps, "step", "steps"))
					}
					rows = append(rows, renderRow{text: theme.muted.Render(progress)})
				}
			} else if item.Role == codingagent.TranscriptRoleAssistant {
				rows = m.appendAssistant(rows, item.ID, item.Text, contentWidth)
			} else {
				rows = appendWrapped(rows, "", item.Text, contentWidth, theme.assistant)
			}
			if selected {
				highlightSelectedMessage(rows, start, len(rows), contentWidth)
			}
			for index := start; index < len(rows); index++ {
				rows[index].selectionID = selectionID
			}
			rows = append(rows, renderRow{})
		case codingagent.TranscriptToolCall:
			if item.Tool == nil {
				continue
			}
			activity := *item.Tool
			if result, found := results[activity.CallID]; found {
				activity = result
				seenResults[activity.CallID] = true
			} else if live, found := m.activities[activity.CallID]; found {
				activity.Status, activity.Summary, activity.Detail, activity.Diff, activity.Resources = live.Status, live.Summary, live.Detail, live.Diff, live.Resources
			}
			rows = append(rows, m.toolRows(activity, contentWidth)...)
		case codingagent.TranscriptToolResult:
			if item.Tool != nil && !seenResults[item.Tool.CallID] {
				rows = append(rows, m.toolRows(*item.Tool, contentWidth)...)
			}
		case codingagent.TranscriptCompaction:
			rows = append(rows, renderRow{text: theme.muted.Render("  • Earlier context was compacted")})
		case codingagent.TranscriptFailure:
			rows = appendWrapped(rows, "✗ ", friendlyFailure(item.Text), contentWidth, theme.failure)
			rows = append(rows, renderRow{})
		}
	}
	if m.liveAssistant != "" {
		rows = m.appendAssistant(rows, "live-assistant", m.liveAssistant, contentWidth)
	}
	for callID, live := range m.activities {
		if _, durable := results[callID]; durable || transcriptHasCall(m.snapshot.Transcript, callID) {
			continue
		}
		rows = append(rows, m.toolRows(codingagent.TranscriptTool{CallID: callID, Name: live.Name, Status: live.Status, Summary: live.Summary, Detail: live.Detail, Diff: live.Diff, Resources: live.Resources}, contentWidth)...)
	}
	if pending := m.pendingApproval(); pending != nil {
		rows = append(rows, m.approvalRows(*pending, contentWidth)...)
	}
	if recovery := m.pendingRecovery(); recovery != nil {
		title := "  Crash recovery required"
		if recovery.ToolName != "" {
			title += "  •  " + recovery.ToolName
		}
		rows = append(rows, renderRow{text: theme.warning.Render(title)})
		rows = appendWrapped(rows, "  ", recovery.Summary, contentWidth, theme.muted)
		rows = append(rows, renderRow{text: theme.muted.Render("  " + recoveryDecisionHelp(*recovery))}, renderRow{})
	}
	return m.applyTextSelection(rows)
}

func (m *Model) appendMarkdown(rows []renderRow, id, value string, width int) []renderRow {
	cacheKey := fmt.Sprintf("%d:%s:%s", width, id, value)
	cacheable := id != "live-assistant"
	lines, found := m.markdownCache[cacheKey]
	if !found {
		renderer, err := glamour.NewTermRenderer(glamour.WithStandardStyle("dark"), glamour.WithWordWrap(width))
		if err != nil {
			return appendWrapped(rows, "", value, width, theme.assistant)
		}
		rendered, err := renderer.Render(value)
		if err != nil {
			return appendWrapped(rows, "", value, width, theme.assistant)
		}
		lines = strings.Split(strings.Trim(rendered, "\n"), "\n")
		if cacheable {
			if len(m.markdownCache) >= 256 {
				m.markdownCache = make(map[string][]string)
			}
			m.markdownCache[cacheKey] = append([]string(nil), lines...)
		}
	}
	for _, line := range lines {
		rows = append(rows, renderRow{text: line})
	}
	return rows
}

func (m *Model) appendAssistant(rows []renderRow, id, value string, width int) []renderRow {
	if m.markdownEnabled {
		return m.appendMarkdown(rows, id, value, width)
	}
	return appendWrapped(rows, "", value, width, theme.assistant)
}

func appendUserMessage(rows []renderRow, value string, width int) []renderRow {
	start := len(rows)
	rows = appendWrapped(rows, "❯ ", value, width, theme.userInput)
	for index := start; index < len(rows); index++ {
		plain := truncateANSI(ansi.Strip(rows[index].text), width)
		if padding := width - ansi.StringWidth(plain); padding > 0 {
			plain += strings.Repeat(" ", padding)
		}
		rows[index].text = theme.userInput.Render(plain)
	}
	return rows
}

func (m *Model) turnElapsed(user codingagent.TranscriptItem) (time.Duration, bool, bool) {
	if user.TurnID == "" {
		return 0, false, false
	}
	metrics := m.snapshot.Metrics
	if user.TurnID == metrics.LatestTurnID && !metrics.StartedAt.IsZero() {
		if metrics.Elapsed > 0 {
			return metrics.Elapsed, true, true
		}
		if metrics.FinishedAt.IsZero() && (m.busy || m.snapshot.RuntimeState == codingagent.RuntimeRunning) {
			return max(time.Duration(0), time.Since(metrics.StartedAt)), false, true
		}
	}
	if user.Timestamp.IsZero() {
		return 0, false, false
	}
	finishedAt := user.Timestamp
	for _, item := range m.snapshot.Transcript {
		if item.TurnID == user.TurnID && item.Timestamp.After(finishedAt) {
			finishedAt = item.Timestamp
		}
	}
	if !finishedAt.After(user.Timestamp) {
		return 0, false, false
	}
	return finishedAt.Sub(user.Timestamp), true, true
}

func highlightSelectedMessage(rows []renderRow, start, end, width int) {
	start, end = max(0, start), min(len(rows), end)
	for index := start; index < end; index++ {
		plain := ansi.Strip(rows[index].text)
		plain = truncateANSI(plain, width)
		if padding := width - ansi.StringWidth(plain); padding > 0 {
			plain += strings.Repeat(" ", padding)
		}
		rows[index].text = theme.selection.Render(plain)
	}
}

func (m *Model) setMarkdownEnabled(enabled bool) {
	m.markdownEnabled = enabled
	m.markdownCache = make(map[string][]string)
	if enabled {
		m.status = "Markdown rendering enabled."
	} else {
		m.status = "Markdown rendering disabled; assistant messages use plain text."
	}
}

func (m *Model) toolRows(activity codingagent.TranscriptTool, width int) []renderRow {
	id := activity.CallID
	expanded := m.expanded[id]
	marker := "▶"
	if expanded {
		marker = "▼"
	}
	status := toolStatusGlyph(activity.Status, activity.IsError)
	selector := "  "
	if id == m.selectedTool {
		selector = "❯ "
	}
	line := fmt.Sprintf("%s%s %s %s", selector, marker, status, activity.Name)
	selectionID := toolSelectionPrefix + id
	rows := []renderRow{{text: theme.tool.Render(line), toolID: id, selectionID: selectionID}}
	for _, resource := range activity.Resources {
		for _, detail := range wrapLines("↳ "+formatToolResource(resource), max(8, width-6)) {
			rows = append(rows, renderRow{text: theme.muted.Render("      " + detail), toolID: id, selectionID: selectionID})
		}
	}
	if expanded && strings.TrimSpace(activity.Detail) != "" {
		for _, detail := range wrapLines(activity.Detail, max(8, width-6)) {
			rows = append(rows, renderRow{text: theme.muted.Render("      " + detail), toolID: id, selectionID: selectionID})
		}
	}
	if expanded && activity.Diff != nil && activity.Diff.Text != "" {
		rows = append(rows, renderRow{text: theme.muted.Render("      Applied changes"), toolID: id, selectionID: selectionID})
		diffText := strings.ReplaceAll(activity.Diff.Text, "\r\n", "\n")
		for _, line := range strings.Split(strings.TrimSuffix(diffText, "\n"), "\n") {
			rows = append(rows, renderRow{text: "      " + styleDiffLine(escapeTerminalControls(line)), toolID: id, selectionID: selectionID})
		}
	}
	rows = append(rows, renderRow{})
	return rows
}

func (m *Model) statusLine() string {
	if m.errorMessage != "" {
		return theme.failure.Render(m.errorMessage)
	}
	if recovery := m.pendingRecovery(); recovery != nil {
		return theme.warning.Render("Recovery: " + recoveryDecisionHelp(*recovery))
	}
	if m.textSelection.hasRange() {
		return theme.muted.Render("Text selected")
	}
	if m.selectedBlock != "" {
		if m.selectedTool != "" {
			return theme.muted.Render("Tool selected")
		}
		return theme.muted.Render("Message selected")
	}
	if m.thinking {
		return theme.muted.Render("Thinking…")
	}
	if m.status != "" {
		return theme.muted.Render(m.status)
	}
	return theme.muted.Render("Ready")
}

func (m *Model) footerDividerLine(width int) string {
	return theme.muted.Render(strings.Repeat("─", max(1, width)))
}

func (m *Model) sessionMetricsLine() string {
	metrics := m.snapshot.Metrics
	var contextValues []string
	if metrics.ContextTokens > 0 {
		contextValues = append(contextValues, "Current context "+formatMetricCount(metrics.ContextTokens))
	}
	var sessionValues []string
	if metrics.TotalTokens > 0 {
		sessionValues = append(sessionValues, "Tokens "+formatMetricCount(metrics.TotalTokens))
		if metrics.Cost > 0 {
			sessionValues = append(sessionValues, fmt.Sprintf("Cost $%.4f", metrics.Cost))
		} else {
			sessionValues = append(sessionValues, "Cost n/a")
		}
	}
	var sections []string
	if len(contextValues) != 0 {
		sections = append(sections, strings.Join(contextValues, "  •  "))
	}
	if len(sessionValues) != 0 {
		sections = append(sections, "Session total  "+strings.Join(sessionValues, "  •  "))
	}
	if len(sections) == 0 {
		return ""
	}
	return theme.muted.Render(strings.Join(sections, "  │  "))
}

func formatMetricCount(value int) string {
	switch {
	case value >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(value)/1_000_000)
	case value >= 10_000:
		return fmt.Sprintf("%.1fk", float64(value)/1_000)
	default:
		return fmt.Sprintf("%d", value)
	}
}

func formatMetricDuration(value time.Duration) string {
	if value < time.Second {
		return value.Round(10 * time.Millisecond).String()
	}
	if value < time.Minute {
		return value.Round(100 * time.Millisecond).String()
	}
	return value.Round(time.Second).String()
}

func (m *Model) promptLine() string {
	line, _, _ := m.renderPrompt(max(20, m.width))
	return line
}

func (m *Model) renderPrompt(width int) (string, int, bool) {
	prefix := theme.user.Render("❯ ")
	if m.busy || m.pendingApproval() != nil || m.pendingRecovery() != nil {
		return truncateANSI(theme.muted.Render("❯ "), width), 0, false
	}
	prefixWidth := ansi.StringWidth(prefix)
	viewport := renderInputViewport(m.input, m.cursor, max(1, width-prefixWidth))
	line := prefix + theme.assistant.Render(viewport.text)
	if len(m.input) == 0 {
		line = prefix + " " + theme.muted.Render("Ask CodePilot anything…")
	}
	return truncateANSI(line, width), min(width-1, prefixWidth+viewport.cursorOffset), true
}

func recoveryDecisionHelp(action codingagent.RecoveryAction) string {
	var values []string
	for _, decision := range action.Decisions {
		switch decision {
		case codingagent.RecoveryRetry:
			values = append(values, "[r] retry/continue")
		case codingagent.RecoveryConfirmExecuted:
			values = append(values, "[x] confirm executed")
		case codingagent.RecoveryMarkFailed:
			values = append(values, "[f] mark failed")
		case codingagent.RecoveryAbandonTurn:
			values = append(values, "[a] abandon turn")
		}
	}
	return strings.Join(values, "  ")
}

func (m *Model) bodyHeight() int {
	completionLines := m.commandCompletionLines(max(20, m.width), max(0, m.height-6))
	return max(1, m.height-5-len(completionLines))
}

func pluralize(value int, singular, plural string) string {
	if value == 1 {
		return singular
	}
	return plural
}

func (m *Model) configureScrollbar(trackHeight, maxScroll int) {
	dragging, dragOffset := m.scrollbar.dragging, m.scrollbar.dragOffset
	if maxScroll <= 0 || trackHeight <= 0 {
		m.scrollbar = scrollbarState{}
		return
	}
	contentHeight := trackHeight + maxScroll
	thumbHeight := max(1, trackHeight*trackHeight/contentHeight)
	thumbHeight = min(trackHeight, thumbHeight)
	travel := trackHeight - thumbHeight
	thumbOffset := 0
	if travel > 0 {
		thumbOffset = (m.scroll*travel + maxScroll/2) / maxScroll
	}
	m.scrollbar = scrollbarState{
		active: true, dragging: dragging, trackTop: 1, trackHeight: trackHeight,
		thumbTop: 1 + thumbOffset, thumbHeight: thumbHeight, maxScroll: maxScroll, dragOffset: dragOffset,
	}
}

func (m *Model) renderScrollbarColumn(line string, screenY, width int) string {
	if !m.scrollbar.active || width <= 1 {
		return line
	}
	line = truncateANSI(line, width-1)
	if padding := width - 1 - ansi.StringWidth(line); padding > 0 {
		line += strings.Repeat(" ", padding)
	}
	glyph := theme.muted.Render("│")
	if screenY >= m.scrollbar.thumbTop && screenY < m.scrollbar.thumbTop+m.scrollbar.thumbHeight {
		glyph = theme.tool.Render("█")
	}
	return line + glyph
}

func (m *Model) beginScrollbarDrag(mouse tea.Mouse) bool {
	if !m.scrollbar.active || mouse.X != max(20, m.width)-1 || mouse.Y < m.scrollbar.trackTop || mouse.Y >= m.scrollbar.trackTop+m.scrollbar.trackHeight {
		return false
	}
	m.clearAllSelections()
	m.scrollbar.dragging = true
	if mouse.Y >= m.scrollbar.thumbTop && mouse.Y < m.scrollbar.thumbTop+m.scrollbar.thumbHeight {
		m.scrollbar.dragOffset = mouse.Y - m.scrollbar.thumbTop
	} else {
		m.scrollbar.dragOffset = m.scrollbar.thumbHeight / 2
		m.updateScrollbarDrag(mouse)
	}
	return true
}

func (m *Model) updateScrollbarDrag(mouse tea.Mouse) {
	if !m.scrollbar.active || !m.scrollbar.dragging {
		return
	}
	travel := m.scrollbar.trackHeight - m.scrollbar.thumbHeight
	if travel <= 0 {
		m.scroll = 0
		m.followBottom = true
		return
	}
	offset := min(max(0, mouse.Y-m.scrollbar.trackTop-m.scrollbar.dragOffset), travel)
	m.scroll = (offset*m.scrollbar.maxScroll + travel/2) / travel
	m.followBottom = m.scroll >= m.scrollbar.maxScroll
	thumbTop := m.scrollbar.trackTop + offset
	m.scrollbar.thumbTop = thumbTop
}

func formatToolResource(resource codingagent.ToolResource) string {
	if resource.StartLine > 0 && resource.EndLine >= resource.StartLine {
		if resource.StartLine == resource.EndLine {
			return fmt.Sprintf("%s  line %d", resource.Path, resource.StartLine)
		}
		return fmt.Sprintf("%s  lines %d–%d (%d lines)", resource.Path, resource.StartLine, resource.EndLine, resource.EndLine-resource.StartLine+1)
	}
	if resource.AddedLines > 0 || resource.DeletedLines > 0 {
		return fmt.Sprintf("%s  +%d -%d", resource.Path, resource.AddedLines, resource.DeletedLines)
	}
	return resource.Path
}

func titleFromPrompt(text string) string {
	runes := []rune(strings.TrimSpace(text))
	if len(runes) <= 60 {
		return string(runes)
	}
	return string(runes[:60]) + "…"
}

func appendWrapped(rows []renderRow, prefix, text string, width int, style lipgloss.Style) []renderRow {
	paragraphs := strings.Split(text, "\n")
	for paragraphIndex, paragraph := range paragraphs {
		available := width
		currentPrefix := prefix
		if currentPrefix != "" {
			available = max(4, width-utf8.RuneCountInString(currentPrefix))
		}
		wrapped := ansi.Hardwrap(paragraph, available, true)
		lines := strings.Split(wrapped, "\n")
		for index, line := range lines {
			linePrefix := ""
			if index == 0 {
				linePrefix = currentPrefix
			} else if currentPrefix != "" {
				linePrefix = strings.Repeat(" ", utf8.RuneCountInString(currentPrefix))
			}
			rows = append(rows, renderRow{text: style.Render(linePrefix + line)})
		}
		if paragraphIndex < len(paragraphs)-1 && paragraph == "" {
			rows = append(rows, renderRow{})
		}
	}
	return rows
}

func wrapLines(text string, width int) []string {
	var output []string
	for _, line := range strings.Split(text, "\n") {
		output = append(output, strings.Split(ansi.Hardwrap(line, width, true), "\n")...)
	}
	return output
}

func transcriptHasCall(items []codingagent.TranscriptItem, callID string) bool {
	for _, item := range items {
		if item.Kind == codingagent.TranscriptToolCall && item.Tool != nil && item.Tool.CallID == callID {
			return true
		}
	}
	return false
}

func toolStatusGlyph(status string, isError bool) string {
	if isError || status == "failed" || status == "denied" || status == "cancelled" || status == "error" {
		return theme.failure.Render("✗")
	}
	if status == "completed" {
		return theme.success.Render("✓")
	}
	if status == "interrupted" || status == "requested" {
		return theme.warning.Render("◌")
	}
	return theme.muted.Render("●")
}

func styleDiffLine(line string) string {
	switch {
	case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
		return theme.added.Render(line)
	case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
		return theme.removed.Render(line)
	case strings.HasPrefix(line, "@@"):
		return theme.hunk.Render(line)
	default:
		return theme.muted.Render(line)
	}
}

func escapeTerminalControls(value string) string {
	var result strings.Builder
	for _, char := range value {
		switch {
		case char == '\t':
			result.WriteString("    ")
		case char == '\r':
			result.WriteString(`\r`)
		case char < ' ' || char == '\x7f':
			fmt.Fprintf(&result, `\x%02x`, char)
		default:
			result.WriteRune(char)
		}
	}
	return result.String()
}

func truncateANSI(value string, width int) string { return ansi.Truncate(value, width, "") }

func safeError(err error) string {
	if err == nil {
		return ""
	}
	value := friendlyFailure(strings.TrimSpace(codingagent.RedactSensitiveText(err.Error())))
	if len(value) > 512 {
		value = value[:512] + "…"
	}
	return value
}

func friendlyFailure(value string) string {
	lower := strings.ToLower(value)
	if strings.Contains(lower, "127.0.0.1:11434") && (strings.Contains(lower, "connection refused") || strings.Contains(lower, "actively refused")) {
		return "Cannot connect to Ollama at 127.0.0.1:11434. Start Ollama, or restart CodePilot with --provider openai/deepseek and the corresponding API key."
	}
	return value
}

func blinkCmd() tea.Cmd {
	return tea.Tick(530*time.Millisecond, func(time.Time) tea.Msg { return blinkMsg{} })
}

// Run starts Bubble Tea using the supplied process-owned streams.
func Run(ctx context.Context, model *Model, input io.Reader, output io.Writer) error {
	options := []tea.ProgramOption{tea.WithContext(ctx), tea.WithoutSignalHandler()}
	if input != nil {
		options = append(options, tea.WithInput(input))
	} else {
		options = append(options, tea.WithInput(nil))
	}
	if output != nil {
		options = append(options, tea.WithOutput(output))
	}
	_, err := tea.NewProgram(model, options...).Run()
	return err
}

var _ tea.Model = (*Model)(nil)
