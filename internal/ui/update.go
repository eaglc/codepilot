package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/eaglc/codepilot/internal/session"
)

const maxComposerRunes = 32 << 10

// Update applies terminal input, command results, and session events on the
// Bubble Tea loop. No background command mutates Model directly.
func (m *Model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	if m == nil {
		return m, nil
	}
	commands := make([]tea.Cmd, 0, 4)
	if m.providerPicker != nil {
		_, command := m.providerPicker.Update(message)
		commands = appendCommand(commands, command)
	}
	if m.sessionPicker != nil {
		commands = appendCommand(commands, m.sessionPicker.Update(message))
	}

	switch value := message.(type) {
	case tea.WindowSizeMsg:
		m.width = value.Width
		m.height = value.Height
	case tea.KeyPressMsg:
		commands = appendCommand(commands, m.handleKey(value))
	case tea.PasteMsg:
		commands = appendCommand(commands, m.handlePaste(value))
	case eventBridgeMsg:
		commands = appendCommand(commands, m.applyEvent(value.event))
		commands = appendCommand(commands, m.nextEventCmd())
	case eventBridgeClosedMsg:
		m.status = "Event stream closed."
	case sessionLoadedMsg:
		if value.err != nil {
			m.errorMessage = SafeErrorMessage(value.err, "The active session could not be refreshed.")
			break
		}
		previousRoot := m.snapshot.WorktreeState.Root
		m.snapshot = cloneSnapshot(value.snapshot)
		if previousRoot != m.snapshot.WorktreeState.Root {
			m.resetWorkspaceFileCache()
			m.closeCompletion()
		}
		if m.snapshot.RuntimeState == session.RuntimeIdle {
			m.assistant = ""
		}
		m.errorMessage = ""
		if (m.snapshot.Session.ProviderProfileID == "" || m.snapshot.Session.ModelID == "") && m.providerPicker != nil && m.providerPicker.Stage() == ProviderPickerClosed {
			commands = appendCommand(commands, m.openProviderPicker())
		}
	case workspaceFilesLoadedMsg:
		if value.root != m.snapshot.WorktreeState.Root {
			break
		}
		m.workspaceFilesRoot = value.root
		m.workspaceFilesLoading = false
		m.workspaceFilesLoaded = true
		m.workspaceFiles = append([]session.WorkspaceFile(nil), value.files.Files...)
		m.workspaceFilesTruncated = value.files.Truncated
		m.workspaceFilesError = SafeErrorMessage(value.err, "Workspace paths could not be loaded.")
		commands = appendCommand(commands, m.refreshCompletion())
	case diffLoadedMsg:
		if value.err != nil {
			m.errorMessage = SafeErrorMessage(value.err, "The diff could not be refreshed.")
			break
		}
		m.diff = value.diff
		m.errorMessage = ""
	case cancelTurnResultMsg:
		if value.err != nil {
			m.errorMessage = SafeErrorMessage(value.err, "The active turn could not be cancelled.")
			break
		}
		m.status = "Cancellation requested."
	case turnStartedMsg:
		m.inputBusy = false
		if value.err != nil {
			if len(m.composer) == 0 {
				m.composer = []rune(value.text)
			}
			m.errorMessage = SafeErrorMessage(value.err, "The turn could not be started.")
			if hasErrorCode(value.err, session.ErrProviderUnavailable) && m.providerPicker != nil {
				commands = appendCommand(commands, m.openProviderPicker())
			}
			break
		}
		m.activeTurn = value.turnID
		m.snapshot.RuntimeState = session.RuntimeRunning
		m.status = "Agent is working..."
		m.errorMessage = ""
	case operationResultMsg:
		if value.err != nil {
			m.errorMessage = SafeErrorMessage(value.err, value.failureMessage)
			break
		}
		m.errorMessage = ""
		m.status = value.successMessage
		if value.refreshSession {
			commands = appendCommand(commands, loadCurrentSessionCmd(m.client))
		}
		if value.refreshDiff {
			commands = appendCommand(commands, readDiffCmd(m.client, m.diffKind))
		}
	case workspacesLoadedMsg:
		if value.err != nil {
			m.errorMessage = SafeErrorMessage(value.err, "Registered worktrees could not be loaded.")
			break
		}
		m.showWorkspaceList(value.values)
	case approvalResolutionMsg:
		if value.err != nil {
			m.approval = value.request
			m.errorMessage = SafeErrorMessage(value.err, "The approval decision could not be submitted.")
			break
		}
		m.status = "Approval decision submitted."
	case modelSwitchedMsg:
		if value.message == "" {
			m.status = "Provider and model switched."
			commands = appendCommand(commands, loadCurrentSessionCmd(m.client))
		}
	case sessionSwitchedMsg:
		if value.message == "" {
			m.status = "Session switched."
			commands = appendCommand(commands, loadCurrentSessionCmd(m.client))
			commands = appendCommand(commands, readDiffCmd(m.client, m.diffKind))
		}
	}
	return m, batchCommands(commands...)
}

func (m *Model) handleKey(message tea.KeyPressMsg) tea.Cmd {
	if m.approval != nil {
		if isCancelKey(message) {
			m.snapshot.RuntimeState = session.RuntimeCancelling
			m.status = "Cancelling turn..."
			return cancelTurnCmd(m.client)
		}
		return m.handleApprovalKey(message)
	}
	if m.providerPicker != nil && m.providerPicker.Stage() != ProviderPickerClosed {
		return m.providerPicker.HandleKey(message)
	}
	if m.sessionPicker != nil && m.sessionPicker.Stage() != SessionPickerClosed {
		return m.sessionPicker.HandleKey(message)
	}
	key := message.Key()
	if m.overlayText != "" {
		if m.pendingWorkspace != "" {
			switch {
			case key.Code == tea.KeyEscape || key.Code == tea.KeyEsc || strings.EqualFold(key.Text, "n"):
				m.pendingWorkspace = ""
				m.closeTextOverlay()
			case strings.EqualFold(key.Text, "y"):
				path := m.pendingWorkspace
				m.pendingWorkspace = ""
				m.closeTextOverlay()
				m.status = "Opening worktree..."
				return openWorkspaceCmd(m.client, path)
			}
			return nil
		}
		if key.Code == tea.KeyEscape || key.Code == tea.KeyEsc || isEnterKey(key.Code) {
			m.closeTextOverlay()
		}
		return nil
	}
	if isCancelKey(message) {
		if m.turnIsActive() {
			m.snapshot.RuntimeState = session.RuntimeCancelling
			m.status = "Cancelling turn..."
			return cancelTurnCmd(m.client)
		}
		m.status = "No active turn. Use Ctrl+D or /exit to quit."
		return nil
	}
	if isControlKey(message, 'd') {
		if m.turnIsActive() {
			m.errorMessage = "Cancel the active turn before exiting."
			return nil
		}
		return tea.Quit
	}
	if isControlKey(message, 'n') {
		return m.createSession("")
	}
	if isControlKey(message, 'o') {
		return m.openSessionPicker(false)
	}
	if m.inputBusy {
		return nil
	}
	if handled, command := m.handleCompletionKey(message); handled {
		return command
	}
	if isPanelSwitchKey(message) {
		m.toggleFocus()
		return nil
	}
	if key.Code == tea.KeyBackspace {
		if len(m.composer) > 0 {
			m.composer[len(m.composer)-1] = 0
			m.composer = m.composer[:len(m.composer)-1]
		}
		return m.refreshCompletion()
	}
	if isEnterKey(key.Code) {
		if key.Mod&tea.ModAlt != 0 {
			if len(m.composer) < maxComposerRunes {
				m.composer = append(m.composer, '\n')
			}
			return m.refreshCompletion()
		}
		return m.submitComposer()
	}
	if key.Text != "" && key.Mod&tea.ModCtrl == 0 {
		input := []rune(key.Text)
		remaining := maxComposerRunes - len(m.composer)
		if remaining <= 0 {
			return nil
		}
		if len(input) > remaining {
			input = input[:remaining]
		}
		m.composer = append(m.composer, input...)
		return m.refreshCompletion()
	}
	return nil
}

func (m *Model) handlePaste(message tea.PasteMsg) tea.Cmd {
	if m.approval != nil {
		return nil
	}
	if m.providerPicker != nil && m.providerPicker.Stage() != ProviderPickerClosed {
		m.providerPicker.HandlePaste(message)
		return nil
	}
	if (m.sessionPicker != nil && m.sessionPicker.Stage() != SessionPickerClosed) || m.overlayText != "" || m.inputBusy {
		return nil
	}
	input := sanitizePasteRunes(message.Content, true)
	remaining := maxComposerRunes - len(m.composer)
	if remaining <= 0 {
		return nil
	}
	if len(input) > remaining {
		input = input[:remaining]
	}
	m.composer = append(m.composer, input...)
	return m.refreshCompletion()
}

func (m *Model) submitComposer() tea.Cmd {
	text := strings.TrimSpace(string(m.composer))
	if text == "" {
		return nil
	}
	if strings.HasPrefix(text, "/") {
		m.clearComposer()
		return m.executeCommand(text)
	}
	if m.turnIsActive() {
		m.errorMessage = "Wait for the active turn to finish or press Ctrl+C to cancel it."
		return nil
	}
	m.clearComposer()
	m.inputBusy = true
	m.status = "Starting turn..."
	return startTurnCmd(m.client, text)
}

func (m *Model) executeCommand(input string) tea.Cmd {
	command, err := parseSlashCommand(input)
	if err != nil {
		m.errorMessage = err.Error()
		return nil
	}
	switch command.name {
	case "exit":
		if m.turnIsActive() {
			m.errorMessage = "Cancel the active turn before exiting."
			return nil
		}
		return tea.Quit
	case "help":
		m.showTextOverlay("CodePilot help", helpText)
	case "model":
		if m.turnIsActive() {
			m.errorMessage = "Provider and model can only be changed between turns."
			return nil
		}
		if m.providerPicker != nil {
			return m.openProviderPicker()
		}
	case "session":
		return m.executeSessionCommand(command.arguments)
	case "permissions":
		return m.executePermissionCommand(command.arguments)
	case "workspace":
		return m.executeWorkspaceCommand(command.arguments)
	case "status":
		m.showStatusOverlay()
	case "diff":
		return m.executeDiffCommand(command.arguments)
	case "clear":
		// Creating a new persisted session preserves the previous conversation
		// while giving the agent a genuinely empty context and leaving files intact.
		return m.createSession("Cleared conversation")
	default:
		m.errorMessage = "Unknown command /" + command.name + ". Use /help to list commands."
	}
	return nil
}

func (m *Model) executeSessionCommand(arguments []string) tea.Cmd {
	if len(arguments) == 0 || arguments[0] == "list" {
		all := len(arguments) > 1 && arguments[1] == "--all"
		return m.openSessionPicker(all)
	}
	if m.turnIsActive() {
		m.errorMessage = "Sessions can only be changed between turns."
		return nil
	}
	switch arguments[0] {
	case "new", "create":
		return m.createSession(strings.TrimSpace(strings.Join(arguments[1:], " ")))
	case "switch":
		if len(arguments) != 2 {
			m.errorMessage = "Usage: /session switch ID"
			return nil
		}
		return switchSessionCmd(m.client, session.SessionID(arguments[1]))
	case "rename":
		title := strings.TrimSpace(strings.Join(arguments[1:], " "))
		if title == "" {
			m.errorMessage = "Usage: /session rename NAME"
			return nil
		}
		return renameSessionCmd(m.client, m.snapshot.Session.ID, title)
	case "archive":
		return archiveAndCreateSessionCmd(m.client, m.snapshot.Session.ID)
	default:
		m.errorMessage = "Usage: /session create|list [--all]|switch ID|rename NAME|archive"
	}
	return nil
}

func (m *Model) executePermissionCommand(arguments []string) tea.Cmd {
	if m.turnIsActive() {
		m.errorMessage = "Permissions can only be changed between turns."
		return nil
	}
	mode := nextPermissionMode(m.snapshot.Session.PermissionMode)
	if len(arguments) > 0 {
		mode = session.PermissionMode(arguments[0])
	}
	if mode != session.PermissionReadOnly && mode != session.PermissionAsk && mode != session.PermissionAutoEdit {
		m.errorMessage = "Usage: /permissions [read-only|ask|auto-edit]"
		return nil
	}
	return setPermissionModeCmd(m.client, mode)
}

func (m *Model) executeWorkspaceCommand(arguments []string) tea.Cmd {
	if len(arguments) == 0 || arguments[0] == "list" {
		return listWorkspacesCmd(m.client)
	}
	if arguments[0] != "open" || len(arguments) < 2 {
		m.errorMessage = "Usage: /workspace open PATH or /workspace list"
		return nil
	}
	if m.turnIsActive() {
		m.errorMessage = "Workspaces can only be changed between turns."
		return nil
	}
	path := trimCommandQuotes(strings.Join(arguments[1:], " "))
	if path == "" {
		m.errorMessage = "A worktree path is required."
		return nil
	}
	m.pendingWorkspace = path
	m.showTextOverlay("Confirm worktree switch", "CodePilot will activate and inspect:\n"+path+"\n\nY: trust and open | N/Esc: cancel")
	return nil
}

func (m *Model) executeDiffCommand(arguments []string) tea.Cmd {
	kind := nextDiffKind(m.diffKind)
	if len(arguments) > 0 {
		kind = session.DiffKind(arguments[0])
	}
	if kind != session.DiffProposed && kind != session.DiffSession && kind != session.DiffWorkspace {
		m.errorMessage = "Usage: /diff [proposed|session|workspace]"
		return nil
	}
	m.diffKind = kind
	m.focus = FocusDiff
	m.status = "Loading " + string(kind) + " diff..."
	return readDiffCmd(m.client, kind)
}

func (m *Model) createSession(title string) tea.Cmd {
	if m.turnIsActive() {
		m.errorMessage = "Sessions can only be created between turns."
		return nil
	}
	return createSessionCmd(m.client, title)
}

func (m *Model) openSessionPicker(all bool) tea.Cmd {
	if m.turnIsActive() {
		m.errorMessage = "Sessions can only be switched between turns."
		return nil
	}
	if m.sessionPicker == nil {
		m.errorMessage = "Session selection is unavailable."
		return nil
	}
	filter := session.SessionFilter{WorktreeID: m.snapshot.Session.WorktreeID}
	if all {
		filter.WorktreeID = ""
	}
	return m.sessionPicker.OpenForWorktree(context.Background(), filter, m.snapshot.Session.WorktreeID)
}

func (m *Model) handleApprovalKey(message tea.KeyPressMsg) tea.Cmd {
	if m.approval == nil {
		return nil
	}
	key := message.Key()
	decision := session.ApprovalDecisionKind("")
	switch {
	case key.Code == tea.KeyEscape || key.Code == tea.KeyEsc || strings.EqualFold(key.Text, "n"):
		decision = session.ApprovalDeny
	case strings.EqualFold(key.Text, "y"):
		decision = session.ApprovalAllowOnce
	case strings.EqualFold(key.Text, "s"):
		decision = session.ApprovalAllowSession
	default:
		return nil
	}
	request := m.approval
	m.approval = nil
	m.status = "Submitting approval decision..."
	return resolveApprovalCmd(m.client, request, decision)
}

func (m *Model) applyEvent(event session.Event) tea.Cmd {
	if m.eventIsStale(event) {
		m.staleEvents++
		return nil
	}
	m.errorMessage = ""
	switch event.Kind {
	case session.EventTurnStarted:
		m.activeTurn = event.TurnID
		m.assistant = ""
		m.snapshot.RuntimeState = session.RuntimeRunning
		m.status = "Agent is working..."
	case session.EventAssistantDelta:
		if event.Payload.Text != nil {
			m.assistant += event.Payload.Text.Text
		}
	case session.EventToolStarted:
		m.status = toolStatus("Running", event.Payload.Tool)
	case session.EventToolCompleted:
		m.status = toolStatus("Completed", event.Payload.Tool)
	case session.EventToolFailed:
		m.status = toolStatus("Tool failed", event.Payload.Tool)
	case session.EventApprovalRequested:
		m.snapshot.RuntimeState = session.RuntimeAwaitingApproval
		if event.Payload.Approval != nil && event.Payload.Approval.Request != nil {
			request := *event.Payload.Approval.Request
			request.Action = cloneAction(request.Action)
			m.approval = &request
		}
	case session.EventApprovalResolved:
		m.approval = nil
		m.snapshot.RuntimeState = session.RuntimeRunning
		m.status = "Approval resolved; resuming turn..."
	case session.EventPatchApplied:
		m.status = "Patch applied."
		return readDiffCmd(m.client, m.diffKind)
	case session.EventDiffChanged:
		if event.Payload.Diff != nil && event.Payload.Diff.Kind != "" {
			m.diffKind = event.Payload.Diff.Kind
			if event.Payload.Diff.Result != nil {
				m.diff = *event.Payload.Diff.Result
				m.diff.Files = append([]session.DiffFile(nil), event.Payload.Diff.Result.Files...)
				return nil
			}
		}
		return readDiffCmd(m.client, m.diffKind)
	case session.EventTurnCompleted:
		m.finishTurn("Turn completed.")
		return loadCurrentSessionCmd(m.client)
	case session.EventTurnCancelled:
		m.finishTurn("Turn cancelled.")
		return loadCurrentSessionCmd(m.client)
	case session.EventTurnFailed:
		m.finishTurn("Turn failed.")
		return loadCurrentSessionCmd(m.client)
	case session.EventSessionActivated, session.EventSessionSaved, session.EventWorkspaceChanged:
		return loadCurrentSessionCmd(m.client)
	case session.EventSessionSaveFailed:
		m.errorMessage = eventErrorMessage(event.Payload.Error)
	case session.EventProviderValidationStarted:
		m.status = "Validating provider..."
	}
	return nil
}

func (m *Model) eventIsStale(event session.Event) bool {
	if event.SessionID != "" && m.snapshot.Session.ID != "" && event.SessionID != m.snapshot.Session.ID {
		return true
	}
	if event.TurnID == "" {
		return false
	}
	if event.Kind == session.EventTurnStarted {
		return m.activeTurn != "" && event.TurnID != m.activeTurn
	}
	return m.activeTurn == "" || event.TurnID != m.activeTurn
}

func (m *Model) finishTurn(status string) {
	m.activeTurn = ""
	m.approval = nil
	m.snapshot.RuntimeState = session.RuntimeIdle
	m.status = status
}

func (m *Model) toggleFocus() {
	if m.focus == FocusDiff {
		m.focus = FocusConversation
		return
	}
	m.focus = FocusDiff
}

func (m *Model) turnIsActive() bool {
	return m.snapshot.RuntimeState == session.RuntimeRunning || m.snapshot.RuntimeState == session.RuntimeAwaitingApproval || m.snapshot.RuntimeState == session.RuntimeCancelling
}

func (m *Model) clearComposer() {
	for index := range m.composer {
		m.composer[index] = 0
	}
	m.composer = nil
	m.closeCompletion()
}

func (m *Model) openProviderPicker() tea.Cmd {
	if m.providerPicker == nil {
		return nil
	}
	return m.providerPicker.OpenForSelection(context.Background(), session.ModelSelection{
		ProviderProfileID: m.snapshot.Session.ProviderProfileID,
		ModelID:           m.snapshot.Session.ModelID,
	})
}

func (m *Model) resetWorkspaceFileCache() {
	m.workspaceFiles = nil
	m.workspaceFilesRoot = ""
	m.workspaceFilesLoaded = false
	m.workspaceFilesLoading = false
	m.workspaceFilesTruncated = false
	m.workspaceFilesError = ""
}

func (m *Model) showTextOverlay(title string, text string) {
	m.overlayTitle = title
	m.overlayText = text
}

func (m *Model) closeTextOverlay() {
	m.overlayTitle = ""
	m.overlayText = ""
}

func (m *Model) showStatusOverlay() {
	snapshot := m.snapshot
	provider := string(snapshot.Session.ProviderProfileID)
	if provider == "" {
		provider = "not configured"
	}
	model := snapshot.Session.ModelID
	if model == "" {
		model = "not configured"
	}
	m.showTextOverlay("Status", fmt.Sprintf(
		"Worktree: %s\nBranch: %s\nDirty: %t\nSession: %s\nProvider: %s\nModel: %s\nPermission: %s\nRuntime: %s",
		snapshot.WorktreeState.Root,
		snapshot.WorktreeState.Branch,
		snapshot.WorktreeState.Dirty,
		snapshot.Session.ID,
		provider,
		model,
		snapshot.Session.PermissionMode,
		snapshot.RuntimeState,
	))
}

func (m *Model) showWorkspaceList(values []session.WorktreeSummary) {
	lines := make([]string, 0, len(values)+1)
	for _, value := range values {
		state := "available"
		if !value.Available {
			state = "unavailable"
		}
		lines = append(lines, fmt.Sprintf("%s · %s · %s", value.Root, state, value.LastSessionID))
	}
	if len(lines) == 0 {
		lines = append(lines, "No registered worktrees.")
	}
	m.showTextOverlay("Registered worktrees", strings.Join(lines, "\n"))
}

func (m *Model) nextEventCmd() tea.Cmd {
	if m.bridge == nil {
		return nil
	}
	return m.bridge.WaitForEvent()
}

func toolStatus(prefix string, payload *session.ToolEventPayload) string {
	if payload == nil || strings.TrimSpace(payload.Name) == "" {
		return prefix + " tool."
	}
	return prefix + " " + payload.Name + "."
}

func eventErrorMessage(payload *session.ErrorEventPayload) string {
	if payload == nil {
		return "Session state could not be saved. Workspace changes were not rolled back."
	}
	if payload.Code == session.ErrPersistence && strings.TrimSpace(payload.UserMessage) == "" {
		return "Session state could not be saved. Workspace changes were not rolled back."
	}
	return SafeErrorMessage(&session.AppError{
		Code:        payload.Code,
		Operation:   payload.Operation,
		UserMessage: payload.UserMessage,
		Retryable:   payload.Retryable,
	}, "Session state could not be saved. Workspace changes were not rolled back.")
}

func batchCommands(commands ...tea.Cmd) tea.Cmd {
	values := make([]tea.Cmd, 0, len(commands))
	for _, command := range commands {
		if command != nil {
			values = append(values, command)
		}
	}
	if len(values) == 0 {
		return nil
	}
	if len(values) == 1 {
		return values[0]
	}
	return tea.Batch(values...)
}

func appendCommand(commands []tea.Cmd, command tea.Cmd) []tea.Cmd {
	if command != nil {
		return append(commands, command)
	}
	return commands
}

func cancelTurnCmd(client SessionClient) tea.Cmd {
	if client == nil {
		return func() tea.Msg {
			return cancelTurnResultMsg{err: &session.AppError{Code: session.ErrInvalidState}}
		}
	}
	return func() tea.Msg {
		return cancelTurnResultMsg{err: client.CancelTurn(context.Background())}
	}
}

func startTurnCmd(client SessionClient, text string) tea.Cmd {
	return func() tea.Msg {
		if client == nil {
			return turnStartedMsg{text: text, err: &session.AppError{Code: session.ErrInvalidState}}
		}
		turnID, err := client.StartTurn(context.Background(), text)
		return turnStartedMsg{text: text, turnID: turnID, err: err}
	}
}

func loadCurrentSessionCmd(client SessionClient) tea.Cmd {
	if client == nil {
		return nil
	}
	return func() tea.Msg {
		snapshot, err := client.CurrentSession(context.Background())
		return sessionLoadedMsg{snapshot: snapshot, err: err}
	}
}

func readDiffCmd(client SessionClient, kind session.DiffKind) tea.Cmd {
	if client == nil {
		return nil
	}
	return func() tea.Msg {
		diff, err := client.ReadDiff(context.Background(), kind)
		return diffLoadedMsg{diff: diff, err: err}
	}
}

func createSessionCmd(client SessionClient, title string) tea.Cmd {
	return func() tea.Msg {
		if client == nil {
			return operationResultMsg{failureMessage: "A new session could not be created.", err: &session.AppError{Code: session.ErrInvalidState}}
		}
		_, err := client.CreateSession(context.Background(), session.CreateSessionRequest{Title: title})
		return operationResultMsg{
			err: err, successMessage: "New session created.", failureMessage: "A new session could not be created.",
			refreshSession: true, refreshDiff: true,
		}
	}
}

func switchSessionCmd(client SessionClient, id session.SessionID) tea.Cmd {
	return func() tea.Msg {
		if client == nil {
			return operationResultMsg{failureMessage: "The session could not be switched.", err: &session.AppError{Code: session.ErrInvalidState}}
		}
		err := client.SwitchSession(context.Background(), id)
		return operationResultMsg{
			err: err, successMessage: "Session switched.", failureMessage: "The session could not be switched.",
			refreshSession: true, refreshDiff: true,
		}
	}
}

func renameSessionCmd(client SessionClient, id session.SessionID, title string) tea.Cmd {
	return func() tea.Msg {
		if client == nil {
			return operationResultMsg{failureMessage: "The session could not be renamed.", err: &session.AppError{Code: session.ErrInvalidState}}
		}
		err := client.RenameSession(context.Background(), id, title)
		return operationResultMsg{err: err, successMessage: "Session renamed.", failureMessage: "The session could not be renamed.", refreshSession: true}
	}
}

func archiveAndCreateSessionCmd(client SessionClient, id session.SessionID) tea.Cmd {
	return func() tea.Msg {
		if client == nil {
			return operationResultMsg{failureMessage: "The session could not be archived.", err: &session.AppError{Code: session.ErrInvalidState}}
		}
		ctx := context.Background()
		if err := client.ArchiveSession(ctx, id); err != nil {
			return operationResultMsg{failureMessage: "The session could not be archived.", err: err}
		}
		_, err := client.CreateSession(ctx, session.CreateSessionRequest{})
		return operationResultMsg{
			err: err, successMessage: "Session archived; a new session is active.", failureMessage: "The session was archived, but a replacement session could not be created.",
			refreshSession: true, refreshDiff: true,
		}
	}
}

func setPermissionModeCmd(client SessionClient, mode session.PermissionMode) tea.Cmd {
	return func() tea.Msg {
		if client == nil {
			return operationResultMsg{failureMessage: "The permission mode could not be changed.", err: &session.AppError{Code: session.ErrInvalidState}}
		}
		err := client.SetPermissionMode(context.Background(), mode)
		return operationResultMsg{
			err: err, successMessage: "Permission mode changed to " + string(mode) + ".", failureMessage: "The permission mode could not be changed.", refreshSession: true,
		}
	}
}

func openWorkspaceCmd(client SessionClient, path string) tea.Cmd {
	return func() tea.Msg {
		if client == nil {
			return operationResultMsg{failureMessage: "The worktree could not be opened.", err: &session.AppError{Code: session.ErrInvalidState}}
		}
		_, err := client.OpenWorkspace(context.Background(), path)
		return operationResultMsg{
			err: err, successMessage: "Worktree opened.", failureMessage: "The worktree could not be opened.", refreshSession: true, refreshDiff: true,
		}
	}
}

func listWorkspacesCmd(client SessionClient) tea.Cmd {
	return func() tea.Msg {
		if client == nil {
			return workspacesLoadedMsg{err: &session.AppError{Code: session.ErrInvalidState}}
		}
		values, err := client.ListWorkspaces(context.Background())
		return workspacesLoadedMsg{values: values, err: err}
	}
}

func resolveApprovalCmd(client SessionClient, request *session.ApprovalRequest, decision session.ApprovalDecisionKind) tea.Cmd {
	return func() tea.Msg {
		if client == nil || request == nil {
			return approvalResolutionMsg{request: request, err: &session.AppError{Code: session.ErrInvalidState}}
		}
		resolution := session.ApprovalResolution{
			RequestID: request.ID,
			SessionID: request.SessionID,
			TurnID:    request.TurnID,
			Decision:  session.ApprovalDecision{Kind: decision, DecidedAt: time.Now().UTC()},
		}
		return approvalResolutionMsg{request: request, err: client.ResolveApproval(context.Background(), resolution)}
	}
}

func nextPermissionMode(current session.PermissionMode) session.PermissionMode {
	switch current {
	case session.PermissionReadOnly:
		return session.PermissionAsk
	case session.PermissionAsk:
		return session.PermissionAutoEdit
	default:
		return session.PermissionReadOnly
	}
}

func nextDiffKind(current session.DiffKind) session.DiffKind {
	switch current {
	case session.DiffProposed:
		return session.DiffSession
	case session.DiffSession:
		return session.DiffWorkspace
	default:
		return session.DiffProposed
	}
}

func hasErrorCode(err error, code session.ErrorCode) bool {
	var appError *session.AppError
	return errors.As(err, &appError) && appError.Code == code
}

func trimCommandQuotes(value string) string {
	trimmed := strings.TrimSpace(value)
	if len(trimmed) >= 2 && ((trimmed[0] == '"' && trimmed[len(trimmed)-1] == '"') || (trimmed[0] == '\'' && trimmed[len(trimmed)-1] == '\'')) {
		return strings.TrimSpace(trimmed[1 : len(trimmed)-1])
	}
	return trimmed
}

type sessionLoadedMsg struct {
	snapshot session.SessionSnapshot
	err      error
}

type diffLoadedMsg struct {
	diff session.DiffResult
	err  error
}

type cancelTurnResultMsg struct {
	err error
}

type turnStartedMsg struct {
	text   string
	turnID session.TurnID
	err    error
}

type operationResultMsg struct {
	err            error
	successMessage string
	failureMessage string
	refreshSession bool
	refreshDiff    bool
}

type workspacesLoadedMsg struct {
	values []session.WorktreeSummary
	err    error
}

type approvalResolutionMsg struct {
	request *session.ApprovalRequest
	err     error
}
