package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/eaglc/codepilot/internal/codingagent"
)

type permissionOption struct {
	mode        codingagent.PermissionMode
	label       string
	description string
}

var permissionOptions = []permissionOption{
	{mode: codingagent.PermissionReadOnly, label: "Read only", description: "Allow inspection only; block edits, checks, and language-server startup."},
	{mode: codingagent.PermissionAsk, label: "Ask before acting", description: "Ask before edits, checks, and language-server startup."},
	{mode: codingagent.PermissionAutoEdit, label: "Auto edit", description: "Apply policy-safe edits automatically; checks and process startup still ask."},
}

type permissionPicker struct {
	active  bool
	loading bool
	cursor  int
	error   string
}

type permissionModeChangedMsg struct {
	session    codingagent.Session
	err        error
	generation uint64
}

func newPermissionPicker(current codingagent.PermissionMode) permissionPicker {
	picker := permissionPicker{active: true}
	for index := range permissionOptions {
		if permissionOptions[index].mode == current {
			picker.cursor = index
			break
		}
	}
	return picker
}

func (m *Model) handleHelpKey(message tea.KeyPressMsg) tea.Cmd {
	key := message.Key()
	if key.Code == tea.KeyEscape || key.Code == tea.KeyEsc || key.Code == tea.KeyEnter || strings.EqualFold(key.Text, "q") {
		m.helpActive = false
	}
	return nil
}

func (m *Model) handlePermissionKey(message tea.KeyPressMsg) tea.Cmd {
	key := message.Key()
	if key.Code == tea.KeyEscape || key.Code == tea.KeyEsc {
		m.permissionPicker = permissionPicker{}
		return nil
	}
	if m.permissionPicker.loading {
		return nil
	}
	switch {
	case key.Code == tea.KeyUp || strings.EqualFold(key.Text, "k"):
		m.permissionPicker.cursor = max(0, m.permissionPicker.cursor-1)
	case key.Code == tea.KeyDown || strings.EqualFold(key.Text, "j"):
		m.permissionPicker.cursor = min(len(permissionOptions)-1, m.permissionPicker.cursor+1)
	case key.Code == tea.KeyEnter:
		selected := permissionOptions[m.permissionPicker.cursor]
		if selected.mode == m.snapshot.Session.PermissionMode {
			m.permissionPicker = permissionPicker{}
			return nil
		}
		m.permissionPicker.loading = true
		m.permissionPicker.error = ""
		client, ctx, sessionID, generation := m.client, m.ctx, m.sessionID, m.generation
		return func() tea.Msg {
			session, err := client.SetPermissionMode(ctx, sessionID, selected.mode)
			return permissionModeChangedMsg{session: session, err: err, generation: generation}
		}
	}
	return nil
}

func (m *Model) applyPermissionModeChanged(message permissionModeChangedMsg) {
	if message.generation != m.generation || !m.permissionPicker.active {
		return
	}
	if message.err != nil {
		m.permissionPicker.loading = false
		m.permissionPicker.error = safeError(message.err)
		return
	}
	m.snapshot.Session = message.session
	m.permissionPicker = permissionPicker{}
	m.status = "Permissions changed to " + permissionModeLabel(message.session.PermissionMode) + "."
}

func permissionModeLabel(mode codingagent.PermissionMode) string {
	for _, option := range permissionOptions {
		if option.mode == mode {
			return option.label
		}
	}
	return string(mode)
}

func (m *Model) helpView(width, height int) tea.View {
	lines := []string{theme.header.Render("Commands"), theme.muted.Render("Enter, q, or Esc closes this page"), ""}
	for _, command := range registeredCommands() {
		lines = append(lines, theme.user.Render(fmt.Sprintf("  %-20s", command.usage))+theme.muted.Render(command.description))
	}
	lines = append(lines, "", theme.muted.Render("Conversation: drag selects text  •  y/Ctrl+C copies  •  Tab selects a message/tool  •  Alt+M toggles Markdown"))
	return pageView("CodePilot Help", lines, width, height)
}

func (m *Model) permissionView(width, height int) tea.View {
	lines := []string{
		theme.header.Render("Session permissions"),
		theme.muted.Render("↑/↓ or j/k choose  •  Enter apply  •  Esc close"),
		"",
	}
	if m.permissionPicker.loading {
		lines = append(lines, theme.muted.Render("Updating permission mode..."), "")
	}
	if m.permissionPicker.error != "" {
		lines = append(lines, theme.failure.Render(m.permissionPicker.error), "")
	}
	for index, option := range permissionOptions {
		marker := "  "
		if index == m.permissionPicker.cursor {
			marker = "❯ "
		}
		current := ""
		if option.mode == m.snapshot.Session.PermissionMode {
			current = "  • current"
		}
		lines = append(lines, theme.user.Render(marker+option.label+current))
		lines = append(lines, theme.muted.Render("    "+option.description), "")
	}
	lines = append(lines, theme.muted.Render("Changing modes revokes reusable approvals already granted by this session."))
	return pageView("CodePilot Permissions", lines, width, height)
}

func pageView(title string, lines []string, width, height int) tea.View {
	for index := range lines {
		lines[index] = truncateANSI(lines[index], width)
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	view := tea.NewView(strings.Join(lines[:min(len(lines), height)], "\n"))
	view.AltScreen = true
	view.WindowTitle = title
	return view
}
