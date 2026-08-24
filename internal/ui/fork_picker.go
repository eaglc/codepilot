package ui

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/eaglc/codepilot/internal/codingagent"
)

type forkPoint struct {
	entryID   string
	role      codingagent.TranscriptRole
	text      string
	timestamp time.Time
}

type forkPicker struct {
	active  bool
	loading bool
	items   []forkPoint
	cursor  int
	error   string
}

func newForkPicker(transcript []codingagent.TranscriptItem) forkPicker {
	picker := forkPicker{active: true}
	positions := make(map[string]int)
	for _, item := range transcript {
		entryID := strings.TrimSpace(item.SourceEntryID)
		text := strings.TrimSpace(item.Text)
		if item.Kind != codingagent.TranscriptText || entryID == "" || text == "" ||
			(item.Role != codingagent.TranscriptRoleUser && item.Role != codingagent.TranscriptRoleAssistant) {
			continue
		}
		if index, found := positions[entryID]; found {
			picker.items[index].text += "\n" + text
			continue
		}
		positions[entryID] = len(picker.items)
		picker.items = append(picker.items, forkPoint{entryID: entryID, role: item.Role, text: text, timestamp: item.Timestamp})
	}
	if len(picker.items) == 0 {
		picker.error = "No historical user or assistant messages are available to fork."
	} else {
		picker.cursor = len(picker.items) - 1
	}
	return picker
}

func (p *forkPicker) selected() *forkPoint {
	if p == nil || len(p.items) == 0 {
		return nil
	}
	p.cursor = min(max(0, p.cursor), len(p.items)-1)
	return &p.items[p.cursor]
}

func (m *Model) handleForkKey(message tea.KeyPressMsg) tea.Cmd {
	key := message.Key()
	if key.Code == tea.KeyEscape || key.Code == tea.KeyEsc {
		if !m.forkPicker.loading {
			m.forkPicker = forkPicker{}
		}
		return nil
	}
	if m.forkPicker.loading {
		return nil
	}
	switch {
	case key.Code == tea.KeyUp || strings.EqualFold(key.Text, "k"):
		m.forkPicker.cursor = max(0, m.forkPicker.cursor-1)
	case key.Code == tea.KeyDown || strings.EqualFold(key.Text, "j"):
		m.forkPicker.cursor = min(max(0, len(m.forkPicker.items)-1), m.forkPicker.cursor+1)
	case key.Code == tea.KeyEnter:
		selected := m.forkPicker.selected()
		if selected == nil {
			return nil
		}
		m.forkPicker.loading = true
		m.forkPicker.error = ""
		return m.forkLane(selected.entryID)
	}
	return nil
}

func (m *Model) forkView(width, height int) tea.View {
	lines := []string{
		theme.header.Render("Fork conversation"),
		theme.muted.Render("Choose the last message to include  •  Enter fork  •  Esc close"),
		"",
	}
	if m.forkPicker.loading {
		lines = append(lines, theme.muted.Render("Creating conversation branch..."), "")
	} else if m.forkPicker.error != "" {
		lines = append(lines, theme.failure.Render(m.forkPicker.error), "")
	}
	visible := max(1, (height-8)/2)
	start, end := pickerWindow(m.forkPicker.cursor, len(m.forkPicker.items), visible)
	if start > 0 {
		lines = append(lines, theme.muted.Render("  … earlier messages"))
	}
	for index := start; index < end; index++ {
		item := m.forkPicker.items[index]
		marker := "  "
		style := theme.muted
		if index == m.forkPicker.cursor {
			marker = "❯ "
			style = theme.user
		}
		label := forkRoleLabel(item.role)
		if !item.timestamp.IsZero() {
			label += "  •  " + item.timestamp.Local().Format("2006-01-02 15:04")
		}
		lines = append(lines, style.Render(marker+label))
		lines = append(lines, theme.muted.Render("    "+forkPreview(item.text, max(8, width-4))))
	}
	if end < len(m.forkPicker.items) {
		lines = append(lines, theme.muted.Render("  … newer messages"))
	}
	lines = append(lines, "", theme.muted.Render("The new branch includes the selected message and everything before it."))
	return pageView("CodePilot Fork Conversation", lines, width, height)
}

func forkRoleLabel(role codingagent.TranscriptRole) string {
	if role == codingagent.TranscriptRoleUser {
		return "You"
	}
	return "Assistant"
}

func forkPreview(value string, width int) string {
	value = strings.Join(strings.Fields(value), " ")
	if value == "" {
		return "(empty message)"
	}
	return truncateANSI(value, width)
}
