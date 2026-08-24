package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/eaglc/codepilot/internal/codingagent"
)

type approvalChoiceKind int

const (
	approvalAllowOnce approvalChoiceKind = iota
	approvalAllowSession
	approvalDeny
	approvalCancel
	approvalAbandonTurn
)

type approvalChoice struct {
	kind  approvalChoiceKind
	label string
}

func (m *Model) approvalChoices(pending codingagent.PendingInterrupt) []approvalChoice {
	choices := []approvalChoice{{kind: approvalAllowOnce, label: "Allow once"}}
	if pending.CanGrantSession {
		choices = append(choices, approvalChoice{kind: approvalAllowSession, label: "Allow for this session"})
	}
	choices = append(choices,
		approvalChoice{kind: approvalDeny, label: "Deny"},
		approvalChoice{kind: approvalCancel, label: "Cancel action"},
	)
	if recovery := m.recoveryForTurn(pending.TurnID); recovery != nil && recoveryAllows(*recovery, codingagent.RecoveryAbandonTurn) {
		choices = append(choices, approvalChoice{kind: approvalAbandonTurn, label: "Abandon turn"})
	}
	return choices
}

func (m *Model) syncApprovalCursor(pending codingagent.PendingInterrupt, count int) {
	if m.approvalInterruptID != pending.InterruptID {
		m.approvalInterruptID = pending.InterruptID
		m.approvalCursor = 0
	}
	m.approvalCursor = min(max(0, m.approvalCursor), max(0, count-1))
}

func (m *Model) handleApprovalKey(pending codingagent.PendingInterrupt, message tea.KeyPressMsg) tea.Cmd {
	choices := m.approvalChoices(pending)
	m.syncApprovalCursor(pending, len(choices))
	if m.busy || len(choices) == 0 {
		return nil
	}
	key := message.Key()
	switch {
	case key.Code == tea.KeyUp || strings.EqualFold(key.Text, "k"):
		m.approvalCursor = max(0, m.approvalCursor-1)
		m.followBottom = true
		return nil
	case key.Code == tea.KeyDown || strings.EqualFold(key.Text, "j"):
		m.approvalCursor = min(len(choices)-1, m.approvalCursor+1)
		m.followBottom = true
		return nil
	case key.Code == tea.KeyEscape || key.Code == tea.KeyEsc:
		for _, choice := range choices {
			if choice.kind == approvalCancel {
				return m.applyApprovalChoice(pending, choice)
			}
		}
		return nil
	case key.Code == tea.KeyEnter:
		return m.applyApprovalChoice(pending, choices[m.approvalCursor])
	}
	if len(key.Text) == 1 && key.Text[0] >= '1' && key.Text[0] <= '9' {
		index := int(key.Text[0] - '1')
		if index < len(choices) {
			m.approvalCursor = index
			m.followBottom = true
			return m.applyApprovalChoice(pending, choices[index])
		}
	}
	return nil
}

func (m *Model) applyApprovalChoice(pending codingagent.PendingInterrupt, choice approvalChoice) tea.Cmd {
	m.busy = true
	m.errorMessage = ""
	switch choice.kind {
	case approvalAllowOnce:
		m.status = "Applying approved action..."
		return m.resume(pending, codingagent.ResolutionApproved, codingagent.PermissionGrantOnce)
	case approvalAllowSession:
		m.status = "Allowing this scope for the session..."
		return m.resume(pending, codingagent.ResolutionApproved, codingagent.PermissionGrantSession)
	case approvalDeny:
		m.status = "Declining action..."
		return m.resume(pending, codingagent.ResolutionDenied, codingagent.PermissionGrantOnce)
	case approvalCancel:
		m.status = "Cancelling action..."
		return m.resume(pending, codingagent.ResolutionCancelled, codingagent.PermissionGrantOnce)
	case approvalAbandonTurn:
		if recovery := m.recoveryForTurn(pending.TurnID); recovery != nil && recoveryAllows(*recovery, codingagent.RecoveryAbandonTurn) {
			m.status = "Abandoning interrupted turn..."
			return m.recover(*recovery, codingagent.RecoveryAbandonTurn)
		}
	}
	m.busy = false
	return nil
}

func (m *Model) approvalRows(pending codingagent.PendingInterrupt, width int) []renderRow {
	choices := m.approvalChoices(pending)
	m.syncApprovalCursor(pending, len(choices))
	summary := strings.TrimSpace(pending.Summary)
	if summary == "" {
		summary = "The agent needs permission before continuing."
	}
	lines := []string{
		theme.header.Render("Permission required"),
		theme.warning.Render(summary),
	}
	details := approvalProposalLines(pending.Proposed, max(8, width-4))
	lines = append(lines, details...)
	for index, choice := range choices {
		label := fmt.Sprintf("  %d. %s", index+1, choice.label)
		if index == m.approvalCursor {
			label = fmt.Sprintf("❯ %d. %s", index+1, choice.label)
			lines = append(lines, theme.selection.Render(label))
		} else {
			lines = append(lines, theme.muted.Render(label))
		}
	}
	if m.busy {
		lines = append(lines, theme.muted.Render("Applying selection..."))
	}
	lines = append(lines, theme.muted.Render("↑/↓ choose  •  Enter confirm  •  1-9 choose directly  •  Esc cancel action"))
	rows := make([]renderRow, 0, len(lines)+1)
	for _, line := range lines {
		rows = append(rows, renderRow{text: truncateANSI(line, width)})
	}
	return append(rows, renderRow{})
}

func approvalProposalLines(proposed *codingagent.ProposedChange, width int) []string {
	if proposed == nil {
		return nil
	}
	var lines []string
	switch proposed.Kind {
	case "patch":
		lines = append(lines, theme.tool.Render(fmt.Sprintf("Proposed changes  %d file(s)  +%d -%d", len(proposed.Diff.Files), proposed.AddedLines, proposed.DeletedLines)))
		if len(proposed.Diff.Files) != 0 {
			lines = append(lines, theme.muted.Render("Files: "+strings.Join(proposed.Diff.Files, ", ")))
		}
	case "sensitive_read":
		lines = append(lines, theme.tool.Render("Sensitive read  "+proposed.Path))
		lines = append(lines, theme.muted.Render("Recognized secret values remain redacted after approval."))
	case "check":
		lines = append(lines, theme.tool.Render("Proposed check  "+proposed.PlanID))
		lines = append(lines, theme.muted.Render("Command: "+proposed.Command))
	case "lsp":
		lines = append(lines, theme.tool.Render("Language server  "+proposed.Language))
		lines = append(lines, theme.muted.Render("Command: "+proposed.Command))
	}
	return lines
}
