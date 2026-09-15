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
	approvalAllowWorkflow
	approvalAllowMultiWorkflow
	approvalAllowParallelWorkflow
	approvalAllowParallelWriteWorkflow
	approvalAllowDirect
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
	if pending.Kind == "plan_entry_approval" {
		return []approvalChoice{
			{kind: approvalAllowOnce, label: "Enter Plan mode"},
			{kind: approvalDeny, label: "Continue Direct"},
			{kind: approvalCancel, label: "Cancel task"},
		}
	}
	if pending.Kind == "plan_approval" {
		approveLabel := "Approve Plan and execute"
		if pending.PlanCompletion == codingagent.PlanCompletionDeliverable {
			approveLabel = "Accept Plan and finish"
		}
		approve := approvalChoice{kind: approvalAllowOnce, label: approveLabel}
		choices := []approvalChoice{approve}
		if pending.PlanCompletion == codingagent.PlanCompletionExecute && pending.PlanStrategy == codingagent.ExecutionWorkflowSingle {
			choices = []approvalChoice{
				{kind: approvalAllowWorkflow, label: "Approve with single-Agent Workflow"},
				{kind: approvalAllowDirect, label: "Approve and execute Direct with one Agent"},
			}
		}
		if pending.PlanCompletion == codingagent.PlanCompletionExecute && pending.PlanStrategy == codingagent.ExecutionWorkflowMultiSerial {
			choices = []approvalChoice{
				{kind: approvalAllowMultiWorkflow, label: "Approve with serial multi-Agent Workflow"},
				{kind: approvalAllowDirect, label: "Approve and execute Direct with one Agent"},
			}
		}
		if pending.PlanCompletion == codingagent.PlanCompletionExecute && pending.PlanStrategy == codingagent.ExecutionWorkflowMultiParallelReadOnly {
			choices = []approvalChoice{
				{kind: approvalAllowParallelWorkflow, label: "Approve with parallel read-only multi-Agent Workflow"},
				{kind: approvalAllowDirect, label: "Approve and execute Direct with one Agent"},
			}
		}
		if pending.PlanCompletion == codingagent.PlanCompletionExecute && pending.PlanStrategy == codingagent.ExecutionWorkflowMultiParallelIsolatedWrite {
			choices = []approvalChoice{
				{kind: approvalAllowParallelWriteWorkflow, label: "Approve with isolated parallel-write multi-Agent Workflow"},
				{kind: approvalAllowDirect, label: "Approve and execute Direct with one Agent"},
			}
		}
		return append(choices,
			approvalChoice{kind: approvalDeny, label: "Request Plan revision"},
			approvalChoice{kind: approvalCancel, label: "Cancel task"},
		)
	}
	if pending.Kind == "plan_replan_approval" {
		return []approvalChoice{
			{kind: approvalAllowOnce, label: "Return to Plan mode"},
			{kind: approvalDeny, label: "Continue approved Plan"},
			{kind: approvalCancel, label: "Cancel task"},
		}
	}
	choices := []approvalChoice{{kind: approvalAllowOnce, label: "Allow once"}}
	if pending.CanGrantSession {
		label := "Allow for this session"
		if m.pendingToolName(pending) == createFileToolName {
			label += " (new files in this worktree)"
		}
		choices = append(choices, approvalChoice{kind: approvalAllowSession, label: label})
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
		if pending.Kind == "plan_entry_approval" {
			m.status = "Entering Plan mode..."
		}
		if pending.Kind == "plan_approval" && pending.PlanCompletion == codingagent.PlanCompletionDeliverable {
			m.status = "Accepting Plan..."
		}
		if pending.Kind == "plan_replan_approval" {
			m.status = "Returning to read-only planning..."
		}
		return m.resume(pending, codingagent.ResolutionApproved, codingagent.PermissionGrantOnce)
	case approvalAllowWorkflow:
		m.status = "Starting single-Agent Workflow..."
		return m.resumeWithStrategy(pending, codingagent.ResolutionApproved, codingagent.PermissionGrantOnce, codingagent.ExecutionWorkflowSingle)
	case approvalAllowMultiWorkflow:
		m.status = "Starting serial multi-Agent Workflow..."
		return m.resumeWithStrategy(pending, codingagent.ResolutionApproved, codingagent.PermissionGrantOnce, codingagent.ExecutionWorkflowMultiSerial)
	case approvalAllowParallelWorkflow:
		m.status = "Starting parallel read-only multi-Agent Workflow..."
		return m.resumeWithStrategy(pending, codingagent.ResolutionApproved, codingagent.PermissionGrantOnce, codingagent.ExecutionWorkflowMultiParallelReadOnly)
	case approvalAllowParallelWriteWorkflow:
		m.status = "Starting isolated parallel-write multi-Agent Workflow..."
		return m.resumeWithStrategy(pending, codingagent.ResolutionApproved, codingagent.PermissionGrantOnce, codingagent.ExecutionWorkflowMultiParallelIsolatedWrite)
	case approvalAllowDirect:
		m.status = "Executing approved Plan directly..."
		return m.resumeWithStrategy(pending, codingagent.ResolutionApproved, codingagent.PermissionGrantOnce, codingagent.ExecutionSingle)
	case approvalAllowSession:
		m.status = "Allowing this scope for the session..."
		return m.resume(pending, codingagent.ResolutionApproved, codingagent.PermissionGrantSession)
	case approvalDeny:
		if pending.Kind == "plan_approval" {
			m.busy = false
			m.planFeedback = true
			m.clearInput()
			m.status = "Enter Plan revision feedback."
			return nil
		}
		if pending.Kind == "plan_entry_approval" {
			m.status = "Continuing Direct task..."
			return m.resume(pending, codingagent.ResolutionDenied, codingagent.PermissionGrantOnce)
		}
		if pending.Kind == "plan_replan_approval" {
			m.status = "Continuing the approved Plan..."
			return m.resume(pending, codingagent.ResolutionDenied, codingagent.PermissionGrantOnce)
		}
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
	title := "Permission required"
	if pending.Kind == "plan_entry_approval" {
		title = "Plan mode suggested"
	}
	if pending.Kind == "plan_approval" {
		title = "Plan approval required"
	}
	if pending.Kind == "plan_replan_approval" {
		title = "Plan deviation detected"
	}
	if pending.ChildAgentID != "" {
		title = "Child Agent permission required"
		identity := fmt.Sprintf("Child %s", pending.ChildAgentID)
		if pending.NodeID != "" {
			identity += fmt.Sprintf("  •  node %s", pending.NodeID)
		}
		if pending.Role != "" {
			identity += fmt.Sprintf("  •  %s", pending.Role)
		}
		summary = identity + "\n" + summary
	}
	lines := []string{
		theme.header.Render(title),
		theme.warning.Render(summary),
	}
	details := approvalProposalLines(pending.Proposed, max(8, width-4))
	lines = append(lines, details...)
	if pending.CanGrantSession && m.pendingToolName(pending) == createFileToolName {
		lines = append(lines, theme.muted.Render("Session scope only skips repeated create_file prompts; path and content safety checks still apply."))
	}
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
	help := "↑/↓ choose  •  Enter confirm  •  1-9 choose directly  •  Esc cancel action"
	if pending.Kind == "plan_entry_approval" || pending.Kind == "plan_approval" || pending.Kind == "plan_replan_approval" {
		help = "↑/↓ choose  •  Enter confirm  •  1-9 choose directly  •  Esc cancel task"
	}
	lines = append(lines, theme.muted.Render(help))
	rows := make([]renderRow, 0, len(lines)+1)
	for _, line := range lines {
		rows = append(rows, renderRow{text: truncateANSI(line, width)})
	}
	return append(rows, renderRow{})
}

func (m *Model) pendingToolName(pending codingagent.PendingInterrupt) string {
	for _, item := range m.snapshot.Transcript {
		if item.Tool != nil && item.Tool.CallID == pending.ToolCallID && strings.TrimSpace(item.Tool.Name) != "" {
			return item.Tool.Name
		}
	}
	if activity, found := m.activities[pending.ToolCallID]; found {
		return activity.Name
	}
	return ""
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
