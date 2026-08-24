package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/eaglc/codepilot/internal/codingagent"
)

type sessionPicker struct {
	active   bool
	loading  bool
	sessions []codingagent.Session
	cursor   int
	error    string
}

type sessionsMsg struct {
	sessions   []codingagent.Session
	err        error
	generation uint64
}

type sessionSwitchedMsg struct {
	snapshot   codingagent.Snapshot
	err        error
	generation uint64
}

type sessionCreatedMsg struct {
	snapshot   codingagent.Snapshot
	err        error
	generation uint64
}

type sessionRenamedMsg struct {
	session    codingagent.Session
	err        error
	generation uint64
}

type sessionArchivedMsg struct {
	session    codingagent.Session
	err        error
	generation uint64
}

type laneForkedMsg struct {
	snapshot   codingagent.Snapshot
	err        error
	generation uint64
}

func newSessionPicker() sessionPicker {
	return sessionPicker{active: true, loading: true}
}

func (p *sessionPicker) selected() *codingagent.Session {
	if p == nil || len(p.sessions) == 0 {
		return nil
	}
	p.cursor = min(max(0, p.cursor), len(p.sessions)-1)
	return &p.sessions[p.cursor]
}

func (m *Model) loadSessions() tea.Cmd {
	client, ctx, generation := m.client, m.ctx, m.generation
	options := codingagent.SessionListOptions{WorktreeID: m.snapshot.Session.WorktreeID}
	return func() tea.Msg {
		values, err := client.ListSessions(ctx, options)
		return sessionsMsg{sessions: values, err: err, generation: generation}
	}
}

func (m *Model) handleSessionKey(message tea.KeyPressMsg) tea.Cmd {
	key := message.Key()
	if key.Code == tea.KeyEscape || key.Code == tea.KeyEsc {
		m.sessionPicker = sessionPicker{}
		return nil
	}
	if m.sessionPicker.loading {
		return nil
	}
	switch {
	case key.Code == tea.KeyUp || strings.EqualFold(key.Text, "k"):
		m.sessionPicker.cursor = max(0, m.sessionPicker.cursor-1)
	case key.Code == tea.KeyDown || strings.EqualFold(key.Text, "j"):
		m.sessionPicker.cursor = min(max(0, len(m.sessionPicker.sessions)-1), m.sessionPicker.cursor+1)
	case key.Code == tea.KeyEnter:
		selected := m.sessionPicker.selected()
		if selected == nil {
			return nil
		}
		if selected.ID == m.sessionID {
			m.sessionPicker = sessionPicker{}
			return nil
		}
		m.sessionPicker.loading = true
		m.sessionPicker.error = ""
		return m.switchSession(selected.ID)
	case strings.EqualFold(key.Text, "n"):
		m.sessionPicker = sessionPicker{}
		return m.createSession("")
	case strings.EqualFold(key.Text, "a"):
		selected := m.sessionPicker.selected()
		if selected == nil {
			return nil
		}
		if selected.ID == m.sessionID {
			m.sessionPicker.error = "Switch to another session before archiving this one."
			return nil
		}
		m.sessionPicker.loading = true
		client, ctx, generation, id := m.client, m.ctx, m.generation, selected.ID
		return func() tea.Msg {
			session, err := client.ArchiveSession(ctx, id)
			return sessionArchivedMsg{session: session, err: err, generation: generation}
		}
	}
	return nil
}

func (m *Model) switchSession(id codingagent.SessionID) tea.Cmd {
	client, ctx, generation := m.client, m.ctx, m.generation
	return func() tea.Msg {
		snapshot, err := client.SwitchSession(ctx, id)
		return sessionSwitchedMsg{snapshot: snapshot, err: err, generation: generation}
	}
}

func (m *Model) createSession(title string) tea.Cmd {
	title = strings.TrimSpace(title)
	template := m.snapshot.Session
	request := codingagent.Session{
		WorkspaceID: template.WorkspaceID, WorktreeID: template.WorktreeID, Title: title,
		ProviderProfileID: template.ProviderProfileID, ModelID: template.ModelID,
		PermissionMode: template.PermissionMode, BaseCommit: template.BaseCommit,
	}
	m.busy = true
	m.errorMessage = ""
	m.status = "Creating session..."
	client, ctx, generation := m.client, m.ctx, m.generation
	return func() tea.Msg {
		created, err := client.CreateSession(ctx, request)
		if err != nil {
			return sessionCreatedMsg{err: err, generation: generation}
		}
		snapshot, err := client.SwitchSession(ctx, created.ID)
		return sessionCreatedMsg{snapshot: snapshot, err: err, generation: generation}
	}
}

func (m *Model) renameSession(title string) tea.Cmd {
	m.busy = true
	m.errorMessage = ""
	m.status = "Renaming session..."
	client, ctx, generation, id := m.client, m.ctx, m.generation, m.sessionID
	return func() tea.Msg {
		session, err := client.RenameSession(ctx, id, title)
		return sessionRenamedMsg{session: session, err: err, generation: generation}
	}
}

func (m *Model) forkLane(entryID string) tea.Cmd {
	m.busy = true
	m.errorMessage = ""
	m.status = "Forking conversation branch..."
	client, ctx, generation, id := m.client, m.ctx, m.generation, m.sessionID
	return func() tea.Msg {
		snapshot, err := client.ForkLane(ctx, codingagent.ForkLaneRequest{SessionID: id, FromEntryID: entryID})
		return laneForkedMsg{snapshot: snapshot, err: err, generation: generation}
	}
}

func (m *Model) sessionView(width, height int) tea.View {
	lines := []string{theme.header.Render("Sessions"), theme.muted.Render("Enter switch  •  n new  •  a archive  •  Esc close"), ""}
	if m.sessionPicker.loading {
		lines = append(lines, theme.muted.Render("Loading sessions..."))
	} else if m.sessionPicker.error != "" {
		lines = append(lines, theme.failure.Render(m.sessionPicker.error), "")
	}
	if !m.sessionPicker.loading && len(m.sessionPicker.sessions) == 0 {
		lines = append(lines, theme.muted.Render("No sessions are available for this worktree."))
	}
	visible := max(1, (height-7)/2)
	start, end := pickerWindow(m.sessionPicker.cursor, len(m.sessionPicker.sessions), visible)
	if start > 0 {
		lines = append(lines, theme.muted.Render("  … earlier sessions"))
	}
	for index := start; index < end; index++ {
		session := m.sessionPicker.sessions[index]
		marker := "  "
		if index == m.sessionPicker.cursor {
			marker = "❯ "
		}
		active := ""
		if session.ID == m.sessionID {
			active = "  • active"
		}
		title := strings.TrimSpace(session.Title)
		if title == "" {
			title = string(session.ID)
		}
		row := fmt.Sprintf("%s%s%s", marker, title, active)
		lines = append(lines, theme.user.Render(row))
		lines = append(lines, theme.muted.Render(fmt.Sprintf("    %s/%s  •  %s", session.ProviderProfileID, session.ModelID, session.UpdatedAt.Local().Format("2006-01-02 15:04"))))
	}
	if end < len(m.sessionPicker.sessions) {
		lines = append(lines, theme.muted.Render("  … more sessions"))
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	view := tea.NewView(strings.Join(lines[:min(len(lines), height)], "\n"))
	view.AltScreen = true
	view.WindowTitle = "CodePilot Sessions"
	return view
}
