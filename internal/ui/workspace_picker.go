package ui

import (
	"fmt"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/eaglc/codepilot/internal/codingagent"
)

const maxWorkspacePathRunes = 4096

type workspacePicker struct {
	active     bool
	loading    bool
	items      []workspacePickerItem
	cursor     int
	error      string
	relocating bool
	pathInput  []rune
}

type workspacePickerItem struct {
	workspace codingagent.WorkspaceSummary
	worktree  codingagent.WorktreeSummary
}

type workspacesMsg struct {
	values     []codingagent.WorkspaceSummary
	err        error
	generation uint64
}

type workspaceActivatedMsg struct {
	snapshot   codingagent.Snapshot
	err        error
	generation uint64
}

func newWorkspacePicker() workspacePicker { return workspacePicker{active: true, loading: true} }

func (p *workspacePicker) selected() *workspacePickerItem {
	if p == nil || len(p.items) == 0 {
		return nil
	}
	p.cursor = min(max(0, p.cursor), len(p.items)-1)
	return &p.items[p.cursor]
}

func (m *Model) loadWorkspaces() tea.Cmd {
	client, ctx, generation := m.client, m.ctx, m.generation
	return func() tea.Msg {
		values, err := client.ListWorkspaces(ctx)
		return workspacesMsg{values: values, err: err, generation: generation}
	}
}

func (m *Model) applyWorkspaces(message workspacesMsg) {
	if message.generation != m.generation || !m.workspacePicker.active {
		return
	}
	m.workspacePicker.loading = false
	if message.err != nil {
		m.workspacePicker.error = safeError(message.err)
		return
	}
	m.workspacePicker.error = ""
	m.workspacePicker.items = nil
	for _, workspace := range message.values {
		for _, worktree := range workspace.Worktrees {
			m.workspacePicker.items = append(m.workspacePicker.items, workspacePickerItem{workspace: workspace, worktree: worktree})
		}
	}
	m.workspacePicker.cursor = 0
	for index := range m.workspacePicker.items {
		if m.workspacePicker.items[index].worktree.ID == m.snapshot.Session.WorktreeID {
			m.workspacePicker.cursor = index
			break
		}
	}
}

func (m *Model) handleWorkspaceKey(message tea.KeyPressMsg) tea.Cmd {
	key := message.Key()
	if m.workspacePicker.relocating {
		switch {
		case key.Code == tea.KeyEscape || key.Code == tea.KeyEsc:
			m.workspacePicker.relocating = false
			m.workspacePicker.pathInput = nil
		case key.Code == tea.KeyBackspace:
			if len(m.workspacePicker.pathInput) != 0 {
				m.workspacePicker.pathInput = m.workspacePicker.pathInput[:len(m.workspacePicker.pathInput)-1]
			}
		case key.Code == tea.KeyEnter:
			selected := m.workspacePicker.selected()
			path := strings.TrimSpace(string(m.workspacePicker.pathInput))
			if selected == nil || path == "" {
				m.workspacePicker.error = "Enter the new Git worktree path."
				return nil
			}
			m.workspacePicker.loading = true
			m.workspacePicker.error = ""
			return m.activateWorkspace(*selected, path)
		default:
			if key.Text != "" && key.Mod&tea.ModCtrl == 0 {
				m.appendWorkspacePath(key.Text)
			}
		}
		return nil
	}
	if key.Code == tea.KeyEscape || key.Code == tea.KeyEsc {
		m.workspacePicker = workspacePicker{}
		return nil
	}
	if m.workspacePicker.loading {
		return nil
	}
	switch {
	case key.Code == tea.KeyUp || strings.EqualFold(key.Text, "k"):
		m.workspacePicker.cursor = max(0, m.workspacePicker.cursor-1)
	case key.Code == tea.KeyDown || strings.EqualFold(key.Text, "j"):
		m.workspacePicker.cursor = min(max(0, len(m.workspacePicker.items)-1), m.workspacePicker.cursor+1)
	case key.Code == tea.KeyEnter:
		selected := m.workspacePicker.selected()
		if selected == nil {
			return nil
		}
		if selected.worktree.Availability != codingagent.WorktreeAvailable {
			m.workspacePicker.relocating = true
			m.workspacePicker.pathInput = nil
			m.workspacePicker.error = ""
			return nil
		}
		if selected.worktree.ID == m.snapshot.Session.WorktreeID {
			m.workspacePicker = workspacePicker{}
			return nil
		}
		m.workspacePicker.loading = true
		return m.activateWorkspace(*selected, "")
	case strings.EqualFold(key.Text, "r"):
		selected := m.workspacePicker.selected()
		if selected != nil && selected.worktree.Availability != codingagent.WorktreeAvailable {
			m.workspacePicker.relocating = true
			m.workspacePicker.pathInput = nil
			m.workspacePicker.error = ""
		}
	}
	return nil
}

func (m *Model) pasteWorkspaceInput(value string) {
	if m.workspacePicker.active && m.workspacePicker.relocating {
		m.appendWorkspacePath(value)
	}
}

func (m *Model) appendWorkspacePath(value string) {
	remaining := maxWorkspacePathRunes - len(m.workspacePicker.pathInput)
	if remaining <= 0 {
		return
	}
	input := []rune(value)
	if len(input) > remaining {
		input = input[:remaining]
	}
	m.workspacePicker.pathInput = append(m.workspacePicker.pathInput, input...)
}

func (m *Model) activateWorkspace(selected workspacePickerItem, relocationPath string) tea.Cmd {
	client, ctx, generation := m.client, m.ctx, m.generation
	template := m.snapshot.Session
	return func() tea.Msg {
		if relocationPath != "" {
			if _, err := client.RelocateWorktree(ctx, codingagent.RelocateWorktreeRequest{WorktreeID: selected.worktree.ID, NewPath: relocationPath}); err != nil {
				return workspaceActivatedMsg{err: err, generation: generation}
			}
		}
		sessions, err := client.ListSessions(ctx, codingagent.SessionListOptions{WorktreeID: selected.worktree.ID})
		if err != nil {
			return workspaceActivatedMsg{err: err, generation: generation}
		}
		var target codingagent.Session
		if len(sessions) != 0 {
			target = sessions[0]
		} else {
			title := filepath.Base(selected.worktree.Root)
			if relocationPath != "" {
				title = filepath.Base(filepath.Clean(relocationPath))
			}
			target, err = client.CreateSession(ctx, codingagent.Session{
				WorkspaceID: selected.workspace.ID, WorktreeID: selected.worktree.ID, Title: title,
				ProviderProfileID: template.ProviderProfileID, ModelID: template.ModelID, PermissionMode: template.PermissionMode,
				SensitivePaths: append([]string(nil), template.SensitivePaths...),
			})
			if err != nil {
				return workspaceActivatedMsg{err: err, generation: generation}
			}
		}
		snapshot, err := client.SwitchSession(ctx, target.ID)
		return workspaceActivatedMsg{snapshot: snapshot, err: err, generation: generation}
	}
}

func (m *Model) applyWorkspaceActivated(message workspaceActivatedMsg) {
	if message.generation != m.generation || !m.workspacePicker.active {
		return
	}
	if message.err != nil {
		m.workspacePicker.loading = false
		m.workspacePicker.error = safeError(message.err)
		return
	}
	m.activateSnapshot(message.snapshot)
}

func (m *Model) workspaceView(width, height int) tea.View {
	lines := []string{theme.header.Render("Workspaces"), theme.muted.Render("Enter open  •  r repair unavailable  •  Esc close"), ""}
	var viewCursor *tea.Cursor
	if m.workspacePicker.loading {
		lines = append(lines, theme.muted.Render("Checking saved worktrees..."))
	} else if m.workspacePicker.error != "" {
		lines = append(lines, theme.failure.Render(m.workspacePicker.error), "")
	}
	if m.workspacePicker.relocating {
		selected := m.workspacePicker.selected()
		if selected != nil {
			lines = append(lines, theme.warning.Render("Repair "+selected.workspace.DisplayName), theme.muted.Render("Stored: "+selected.worktree.Root), "", "New Git worktree path:")
			prefix := theme.user.Render("❯ ")
			viewport := renderInputViewport(m.workspacePicker.pathInput, len(m.workspacePicker.pathInput), max(1, width-ansi.StringWidth(prefix)))
			cursorY := len(lines)
			lines = append(lines, prefix+viewport.text, "", theme.muted.Render("Enter verify and relocate  •  Esc cancel"))
			if !m.workspacePicker.loading {
				viewCursor = nativeTextCursor(ansi.StringWidth(prefix)+viewport.cursorOffset, cursorY)
			}
		}
	} else {
		if !m.workspacePicker.loading && len(m.workspacePicker.items) == 0 {
			lines = append(lines, theme.muted.Render("No saved worktrees are available."))
		}
		visible := max(1, (height-7)/2)
		start, end := pickerWindow(m.workspacePicker.cursor, len(m.workspacePicker.items), visible)
		if start > 0 {
			lines = append(lines, theme.muted.Render("  … earlier worktrees"))
		}
		for index := start; index < end; index++ {
			item := m.workspacePicker.items[index]
			marker := "  "
			if index == m.workspacePicker.cursor {
				marker = "❯ "
			}
			active := ""
			if item.worktree.ID == m.snapshot.Session.WorktreeID {
				active = "  • active"
			}
			status := theme.success.Render("available")
			if item.worktree.Availability == codingagent.WorktreeUnavailable {
				status = theme.warning.Render("unavailable")
			} else if item.worktree.Availability == codingagent.WorktreeIdentityChanged {
				status = theme.failure.Render("identity changed")
			}
			lines = append(lines, theme.user.Render(fmt.Sprintf("%s%s%s", marker, item.workspace.DisplayName, active)))
			lines = append(lines, fmt.Sprintf("    %s  •  %s", item.worktree.Root, status))
		}
		if end < len(m.workspacePicker.items) {
			lines = append(lines, theme.muted.Render("  … more worktrees"))
		}
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	for index := range lines {
		lines[index] = truncateANSI(lines[index], width)
	}
	view := tea.NewView(strings.Join(lines[:min(len(lines), height)], "\n"))
	view.AltScreen = true
	view.WindowTitle = "CodePilot Workspaces"
	if viewCursor != nil && viewCursor.Y < height {
		view.Cursor = viewCursor
	}
	return view
}
