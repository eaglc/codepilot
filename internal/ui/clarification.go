package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/eaglc/codepilot/internal/codingagent"
)

func clarificationChoices(request codingagent.ClarificationRequest) []codingagent.ClarificationOption {
	choices := append([]codingagent.ClarificationOption(nil), request.Options...)
	return append(choices, codingagent.ClarificationOption{
		ID: codingagent.ClarificationOtherOptionID, Label: "Other", Description: "Provide a different answer or additional context.",
	})
}

func (m *Model) syncClarificationState(pending codingagent.PendingInterrupt) {
	if m.clarificationID != pending.InterruptID {
		m.clarificationID = pending.InterruptID
		m.clarificationIndex = 0
		m.clarificationCursor = 0
		m.clarificationAnswers = nil
		m.clarificationSelected = make(map[string]bool)
	}
	if pending.Clarification == nil || len(pending.Clarification.Questions) == 0 {
		return
	}
	m.clarificationIndex = min(max(0, m.clarificationIndex), len(pending.Clarification.Questions)-1)
	count := len(clarificationChoices(pending.Clarification.Questions[m.clarificationIndex]))
	m.clarificationCursor = min(max(0, m.clarificationCursor), max(0, count-1))
}

func clarificationSelectionMode(request codingagent.ClarificationRequest) codingagent.ClarificationSelectionMode {
	if request.SelectionMode == codingagent.ClarificationSelectionMultiple {
		return codingagent.ClarificationSelectionMultiple
	}
	return codingagent.ClarificationSelectionSingle
}

func (m *Model) currentClarificationRequest(pending codingagent.PendingInterrupt) *codingagent.ClarificationRequest {
	if pending.Clarification == nil || len(pending.Clarification.Questions) == 0 {
		return nil
	}
	m.syncClarificationState(pending)
	return &pending.Clarification.Questions[m.clarificationIndex]
}

func (m *Model) handleClarificationKey(pending codingagent.PendingInterrupt, message tea.KeyPressMsg) tea.Cmd {
	if pending.Clarification == nil || m.busy {
		return nil
	}
	request := m.currentClarificationRequest(pending)
	if request == nil {
		return nil
	}
	choices := clarificationChoices(*request)
	mode := clarificationSelectionMode(*request)
	key := message.Key()
	switch {
	case key.Code == tea.KeyUp || strings.EqualFold(key.Text, "k"):
		m.clarificationCursor = max(0, m.clarificationCursor-1)
		m.followBottom = true
	case key.Code == tea.KeyDown || strings.EqualFold(key.Text, "j"):
		m.clarificationCursor = min(len(choices)-1, m.clarificationCursor+1)
		m.followBottom = true
	case key.Code == tea.KeySpace && mode == codingagent.ClarificationSelectionMultiple:
		return m.applyClarificationChoice(pending, choices[m.clarificationCursor])
	case key.Code == tea.KeyEnter && mode == codingagent.ClarificationSelectionMultiple:
		if choices[m.clarificationCursor].ID == codingagent.ClarificationOtherOptionID {
			return m.applyClarificationChoice(pending, choices[m.clarificationCursor])
		}
		return m.confirmMultipleClarification(pending, *request)
	case key.Code == tea.KeyEnter:
		return m.applyClarificationChoice(pending, choices[m.clarificationCursor])
	case len(key.Text) == 1 && key.Text[0] >= '1' && key.Text[0] <= '9':
		index := int(key.Text[0] - '1')
		if index < len(choices) {
			m.clarificationCursor = index
			return m.applyClarificationChoice(pending, choices[index])
		}
	}
	return nil
}

func (m *Model) applyClarificationChoice(pending codingagent.PendingInterrupt, choice codingagent.ClarificationOption) tea.Cmd {
	request := m.currentClarificationRequest(pending)
	if request == nil {
		return nil
	}
	if choice.ID == codingagent.ClarificationOtherOptionID {
		m.clarificationOther = true
		m.clearInput()
		m.errorMessage = ""
		m.status = "Enter your preferred answer."
		m.followBottom = true
		return nil
	}
	if clarificationSelectionMode(*request) == codingagent.ClarificationSelectionMultiple {
		if m.clarificationSelected == nil {
			m.clarificationSelected = make(map[string]bool)
		}
		m.clarificationSelected[choice.ID] = !m.clarificationSelected[choice.ID]
		m.errorMessage = ""
		m.followBottom = true
		return nil
	}
	return m.acceptClarificationAnswer(pending, codingagent.ClarificationAnswer{QuestionID: request.ID, OptionIDs: []string{choice.ID}})
}

func (m *Model) confirmMultipleClarification(pending codingagent.PendingInterrupt, request codingagent.ClarificationRequest) tea.Cmd {
	optionIDs := m.selectedClarificationOptionIDs(request)
	if len(optionIDs) == 0 {
		m.errorMessage = "Select one or more options, or choose Other."
		return nil
	}
	return m.acceptClarificationAnswer(pending, codingagent.ClarificationAnswer{QuestionID: request.ID, OptionIDs: optionIDs})
}

func (m *Model) selectedClarificationOptionIDs(request codingagent.ClarificationRequest) []string {
	optionIDs := make([]string, 0, len(request.Options))
	for _, option := range request.Options {
		if m.clarificationSelected[option.ID] {
			optionIDs = append(optionIDs, option.ID)
		}
	}
	return optionIDs
}

func (m *Model) handleClarificationOtherKey(message tea.KeyPressMsg) tea.Cmd {
	key := message.Key()
	if key.Code == tea.KeyEscape || key.Code == tea.KeyEsc {
		m.clarificationOther = false
		m.clearInput()
		m.status = "Waiting for your Plan choice."
		m.followBottom = true
		return nil
	}
	if key.Code == tea.KeyEnter && key.Mod&tea.ModAlt == 0 {
		other := strings.TrimSpace(string(m.input))
		if other == "" {
			m.errorMessage = "Describe the option you prefer, or press Esc."
			return nil
		}
		pending := m.pendingClarification()
		if pending == nil || pending.Clarification == nil {
			m.clarificationOther = false
			m.clearInput()
			return nil
		}
		request := m.currentClarificationRequest(*pending)
		if request == nil {
			m.clarificationOther = false
			m.clearInput()
			return nil
		}
		optionIDs := []string{codingagent.ClarificationOtherOptionID}
		if clarificationSelectionMode(*request) == codingagent.ClarificationSelectionMultiple {
			optionIDs = append(m.selectedClarificationOptionIDs(*request), codingagent.ClarificationOtherOptionID)
		}
		answer := codingagent.ClarificationAnswer{QuestionID: request.ID, OptionIDs: optionIDs, OtherText: other}
		m.clarificationOther = false
		m.clearInput()
		return m.acceptClarificationAnswer(*pending, answer)
	}
	switch key.Code {
	case tea.KeyEnter:
		m.insert([]rune{'\n'})
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
		}
	case tea.KeyDelete:
		if m.cursor < len(m.input) {
			m.input = append(m.input[:m.cursor], m.input[m.cursor+1:]...)
		}
	default:
		if key.Text != "" && key.Mod&tea.ModCtrl == 0 {
			m.insert([]rune(key.Text))
		}
	}
	m.followBottom = true
	return nil
}

func (m *Model) acceptClarificationAnswer(pending codingagent.PendingInterrupt, answer codingagent.ClarificationAnswer) tea.Cmd {
	if pending.Clarification == nil {
		return nil
	}
	answers := append(append([]codingagent.ClarificationAnswer(nil), m.clarificationAnswers...), answer)
	if len(answers) < len(pending.Clarification.Questions) {
		m.clarificationAnswers = answers
		m.clarificationIndex++
		m.clarificationCursor = 0
		m.clarificationSelected = make(map[string]bool)
		m.errorMessage = ""
		m.status = "Waiting for your next Plan choice."
		m.followBottom = true
		return nil
	}
	details, err := codingagent.EncodeClarificationAnswers(*pending.Clarification, answers)
	if err != nil {
		m.errorMessage = err.Error()
		return nil
	}
	m.busy = true
	m.errorMessage = ""
	m.status = "Continuing Plan with your answer..."
	return m.resumeWithDetails(pending, codingagent.ResolutionApproved, codingagent.PermissionGrantOnce, "", details)
}

func (m *Model) clarificationRows(pending codingagent.PendingInterrupt, width int) []renderRow {
	if pending.Clarification == nil {
		return nil
	}
	request := m.currentClarificationRequest(pending)
	if request == nil {
		return nil
	}
	choices := clarificationChoices(*request)
	progress := fmt.Sprintf("Question %d/%d", m.clarificationIndex+1, len(pending.Clarification.Questions))
	mode := clarificationSelectionMode(*request)
	modeLabel := "Single choice"
	if mode == codingagent.ClarificationSelectionMultiple {
		modeLabel = "Multiple choice"
	}
	rows := []renderRow{{text: theme.header.Render("Plan needs your input  •  " + progress + "  •  " + modeLabel + "  •  " + request.Header)}}
	rows = appendWrapped(rows, "", request.Question, width, theme.warning)
	if m.clarificationOther {
		value := string(m.input)
		if value == "" {
			value = "Describe the outcome you prefer…"
			rows = appendWrapped(rows, "Other ❯ ", value, width, theme.muted)
		} else {
			rows = appendWrapped(rows, "Other ❯ ", value, width, theme.selection)
		}
		rows = append(rows, renderRow{text: theme.muted.Render("Enter submit  •  Alt+Enter newline  •  Esc back")}, renderRow{})
		return rows
	}
	for index, choice := range choices {
		label := choice.Label
		if choice.Recommended {
			label += " (Recommended)"
		}
		prefix := fmt.Sprintf("  %d. ", index+1)
		if mode == codingagent.ClarificationSelectionMultiple {
			marker := "[ ]"
			if m.clarificationSelected[choice.ID] {
				marker = "[x]"
			}
			prefix = fmt.Sprintf("  %s %d. ", marker, index+1)
		}
		style := theme.muted
		if index == m.clarificationCursor {
			if mode == codingagent.ClarificationSelectionMultiple {
				marker := "[ ]"
				if m.clarificationSelected[choice.ID] {
					marker = "[x]"
				}
				prefix = fmt.Sprintf("❯ %s %d. ", marker, index+1)
			} else {
				prefix = fmt.Sprintf("❯ %d. ", index+1)
			}
			style = theme.selection
		}
		rows = appendWrapped(rows, prefix, label+" — "+choice.Description, width, style)
	}
	if m.busy {
		rows = append(rows, renderRow{text: theme.muted.Render("Applying answer...")})
	} else if mode == codingagent.ClarificationSelectionMultiple {
		rows = append(rows, renderRow{text: theme.muted.Render("↑/↓ move  •  Space/1-9 toggle  •  Enter confirm  •  Other opens text input")})
	} else {
		rows = append(rows, renderRow{text: theme.muted.Render("↑/↓ choose  •  Enter confirm  •  1-9 choose directly")})
	}
	return append(rows, renderRow{})
}
