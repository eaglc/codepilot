package ui

import (
	"fmt"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/eaglc/codepilot/internal/codingagent"
)

const (
	minComposerHeight = 2
	maxComposerHeight = 8
)

// ComposerKeyBinding identifies one keyboard shortcut for a Composer action.
type ComposerKeyBinding struct {
	// Code is the Bubble Tea key code that triggers the action.
	Code rune
	// Mod is the exact modifier set required for the binding.
	Mod tea.KeyMod
}

// ComposerKeymap configures submission and newline shortcuts.
type ComposerKeymap struct {
	// Submit contains shortcuts that send the current input.
	Submit []ComposerKeyBinding
	// Newline contains shortcuts that insert a line break.
	Newline []ComposerKeyBinding
}

// DefaultComposerKeymap submits with Enter and inserts a newline with Alt+Enter.
func DefaultComposerKeymap() ComposerKeymap {
	return ComposerKeymap{
		Submit:  []ComposerKeyBinding{{Code: tea.KeyEnter}},
		Newline: []ComposerKeyBinding{{Code: tea.KeyEnter, Mod: tea.ModAlt}},
	}
}

// WithComposerKeymap overrides the Composer submission and newline shortcuts.
func WithComposerKeymap(keymap ComposerKeymap) Option {
	return func(model *Model) {
		model.composerKeymap = cloneComposerKeymap(keymap)
	}
}

type composerDraft struct {
	input     []rune
	cursor    int
	planInput bool
}

type composerView struct {
	lines            []string
	cursorX, cursorY int
	cursorActive     bool
}

func cloneComposerKeymap(keymap ComposerKeymap) ComposerKeymap {
	return ComposerKeymap{
		Submit:  append([]ComposerKeyBinding(nil), keymap.Submit...),
		Newline: append([]ComposerKeyBinding(nil), keymap.Newline...),
	}
}

func validateComposerKeymap(keymap ComposerKeymap) error {
	if len(keymap.Submit) == 0 || len(keymap.Newline) == 0 {
		return fmt.Errorf("configure terminal UI Composer: submit and newline bindings are required")
	}
	for _, submit := range keymap.Submit {
		for _, newline := range keymap.Newline {
			if submit == newline {
				return fmt.Errorf("configure terminal UI Composer: submit and newline bindings overlap")
			}
		}
	}
	return nil
}

func matchesComposerBinding(key tea.Key, bindings []ComposerKeyBinding) bool {
	for _, binding := range bindings {
		if key.Code == binding.Code && key.Mod == binding.Mod {
			return true
		}
	}
	return false
}

func (draft composerDraft) clone() composerDraft {
	draft.input = append([]rune(nil), draft.input...)
	return draft
}

func (m *Model) renderComposer(width, maximumHeight int) composerView {
	width = max(1, width)
	maximumHeight = min(maxComposerHeight, max(minComposerHeight, maximumHeight))
	disabled := m.busy || (m.pendingApproval() != nil && !m.planFeedback) || m.pendingClarification() != nil || m.pendingRecovery() != nil
	if disabled {
		return composerView{lines: []string{truncateANSI(theme.muted.Render("❯ "), width), ""}}
	}
	prefixText := "❯ "
	if m.planInput {
		prefixText = "Plan ❯ "
	} else if m.planFeedback {
		prefixText = "Revise ❯ "
	}
	prefix := theme.user.Render(prefixText)
	prefixWidth := ansi.StringWidth(prefix)
	contentWidth := max(1, width-prefixWidth)
	document := layoutComposer(m.input, m.cursor, contentWidth)
	height := min(maximumHeight, max(minComposerHeight, len(document.lines)))
	start := 0
	if document.cursor.line >= height {
		start = document.cursor.line - height + 1
	}
	if start+height > len(document.lines) {
		start = max(0, len(document.lines)-height)
	}
	view := composerView{lines: make([]string, height), cursorActive: true}
	for row := 0; row < height; row++ {
		visualLine := start + row
		linePrefix := strings.Repeat(" ", prefixWidth)
		if visualLine == 0 {
			linePrefix = prefix
		} else if row == 0 && start > 0 {
			linePrefix = theme.muted.Render("↑" + strings.Repeat(" ", max(0, prefixWidth-1)))
		}
		content := ""
		if visualLine < len(document.lines) {
			content = document.lines[visualLine]
		}
		if len(m.input) == 0 && visualLine == 0 {
			placeholder := "Ask CodePilot anything…"
			if m.planInput {
				placeholder = "Describe what should be planned…"
			} else if m.planFeedback {
				placeholder = "Describe the Plan changes you want…"
			}
			content = " " + theme.muted.Render(placeholder)
		} else {
			content = theme.assistant.Render(content)
		}
		view.lines[row] = truncateANSI(linePrefix+content, width)
	}
	view.cursorX = min(width-1, prefixWidth+document.cursor.column)
	view.cursorY = document.cursor.line - start
	return view
}

func (m *Model) currentDraft() composerDraft {
	return composerDraft{input: append([]rune(nil), m.input...), cursor: m.cursor, planInput: m.planInput}
}

func (m *Model) composerOwnsInput() bool {
	return !m.planFeedback && !m.clarificationOther
}

func (m *Model) saveCurrentDraft() {
	if !m.composerOwnsInput() || m.sessionID == "" || m.historyIndex >= 0 {
		return
	}
	draft := m.currentDraft()
	if len(draft.input) == 0 && !draft.planInput {
		delete(m.drafts, m.sessionID)
		return
	}
	m.drafts[m.sessionID] = draft
}

func (m *Model) restoreDraft(sessionID codingagent.SessionID) {
	draft, ok := m.drafts[sessionID]
	if !ok {
		m.input = nil
		m.cursor = 0
		m.planInput = false
		return
	}
	draft = draft.clone()
	m.input = draft.input
	m.cursor = nearestGraphemeBoundary(m.input, draft.cursor)
	m.planInput = draft.planInput
}

func (m *Model) syncDraft() {
	if m.historyIndex < 0 {
		m.saveCurrentDraft()
	}
}

func (m *Model) beginComposerEdit() {
	if m.historyIndex >= 0 {
		m.historyIndex = -1
		m.historyDraft = nil
	}
}

func (m *Model) resetComposerGoalColumn() {
	m.composerGoalColumn = -1
}

func (m *Model) moveComposerVertical(direction int) bool {
	prefixWidth := ansi.StringWidth(theme.user.Render(m.composerPrefixText()))
	document := layoutComposer(m.input, m.cursor, max(1, max(20, m.width)-prefixWidth))
	targetLine := document.cursor.line + direction
	if targetLine < 0 || targetLine >= len(document.lines) {
		return false
	}
	goalColumn := m.composerGoalColumn
	if goalColumn < 0 {
		goalColumn = document.cursor.column
		m.composerGoalColumn = goalColumn
	}
	best := -1
	bestDistance := int(^uint(0) >> 1)
	for _, point := range document.points {
		if point.line != targetLine {
			continue
		}
		distance := point.column - goalColumn
		if distance < 0 {
			distance = -distance
		}
		if distance < bestDistance || distance == bestDistance && point.column <= goalColumn {
			best, bestDistance = point.index, distance
		}
	}
	if best < 0 {
		return false
	}
	m.cursor = best
	return true
}

func (m *Model) moveComposerHome(documentStart bool) {
	if documentStart {
		m.cursor = 0
		m.resetComposerGoalColumn()
		return
	}
	document := layoutComposer(m.input, m.cursor, m.composerContentWidth())
	for _, point := range document.points {
		if point.line == document.cursor.line {
			m.cursor = point.index
			break
		}
	}
	m.resetComposerGoalColumn()
}

func (m *Model) moveComposerEnd(documentEnd bool) {
	if documentEnd {
		m.cursor = len(m.input)
		m.resetComposerGoalColumn()
		return
	}
	document := layoutComposer(m.input, m.cursor, m.composerContentWidth())
	for _, point := range document.points {
		if point.line == document.cursor.line {
			m.cursor = point.index
		}
	}
	m.resetComposerGoalColumn()
}

func (m *Model) composerPrefixText() string {
	if m.planInput {
		return "Plan ❯ "
	}
	if m.planFeedback {
		return "Revise ❯ "
	}
	return "❯ "
}

func (m *Model) composerContentWidth() int {
	return max(1, max(20, m.width)-lipgloss.Width(theme.user.Render(m.composerPrefixText())))
}

func (m *Model) deleteComposerBackward() {
	if m.cursor <= 0 {
		return
	}
	m.beginComposerEdit()
	previous := previousGraphemeBoundary(m.input, m.cursor)
	m.input = append(m.input[:previous], m.input[m.cursor:]...)
	m.cursor = previous
	m.resetCompletion()
	m.resetComposerGoalColumn()
	m.syncDraft()
}

func (m *Model) deleteComposerForward() {
	if m.cursor >= len(m.input) {
		return
	}
	m.beginComposerEdit()
	next := nextGraphemeBoundary(m.input, m.cursor)
	m.input = append(m.input[:m.cursor], m.input[next:]...)
	m.resetCompletion()
	m.resetComposerGoalColumn()
	m.syncDraft()
}

func normalizeComposerText(value string) []rune {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	result := make([]rune, 0, len(value))
	for _, character := range value {
		if character == '\n' || character == '\t' || !unicode.IsControl(character) {
			result = append(result, character)
		}
	}
	return result
}
