package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/eaglc/codepilot/internal/codingagent"
)

func (m *Model) handlePlanFeedbackKey(message tea.KeyPressMsg) tea.Cmd {
	key := message.Key()
	if key.Code == tea.KeyEscape || key.Code == tea.KeyEsc {
		m.planFeedback = false
		m.clearInput()
		m.status = "Waiting for Plan approval."
		return nil
	}
	if key.Code == tea.KeyEnter && key.Mod&tea.ModAlt == 0 {
		feedback := strings.TrimSpace(string(m.input))
		if feedback == "" {
			m.errorMessage = "Describe the Plan changes you want, or press Esc."
			return nil
		}
		pending := m.pendingApproval()
		if pending == nil || pending.Kind != "plan_approval" {
			m.planFeedback = false
			m.clearInput()
			return nil
		}
		m.planFeedback = false
		m.clearInput()
		m.busy = true
		m.errorMessage = ""
		m.status = "Revising Plan (read-only)..."
		return m.resume(*pending, codingagent.ResolutionDenied, codingagent.PermissionGrantOnce, feedback)
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
	return nil
}

func (m *Model) planRows(plan codingagent.PlanSnapshot, width int) []renderRow {
	mode := "execute after approval"
	if plan.CompletionMode == codingagent.PlanCompletionDeliverable {
		mode = "deliverable only"
	}
	contextLabel := "workspace"
	if !plan.WorkspaceRelevant {
		contextLabel = "general"
	}
	rows := []renderRow{{text: theme.header.Render(fmt.Sprintf("Plan v%d  •  %s  •  %s", plan.Version, contextLabel, mode))}}
	strategy := "Direct single Agent"
	if plan.RecommendedStrategy == codingagent.ExecutionWorkflowSingle {
		strategy = "single-Agent Workflow"
	} else if plan.RecommendedStrategy == codingagent.ExecutionWorkflowMultiSerial {
		strategy = "serial multi-Agent Workflow"
	} else if plan.RecommendedStrategy == codingagent.ExecutionWorkflowMultiParallelReadOnly {
		strategy = "parallel read-only multi-Agent Workflow"
	} else if plan.RecommendedStrategy == codingagent.ExecutionWorkflowMultiParallelIsolatedWrite {
		strategy = "isolated parallel-write multi-Agent Workflow"
	}
	rows = appendWrapped(rows, "Recommended execution  ", strategy, width, theme.muted)
	if plan.ApprovedVersion == plan.Version && plan.ApprovedVersion != 0 {
		rows = append(rows, renderRow{text: theme.muted.Render(fmt.Sprintf("Approved exact version: v%d", plan.ApprovedVersion))})
	}
	if len(plan.Changes) != 0 {
		rows = appendPlanList(rows, "Changes in this version", plan.Changes, width)
	}
	if plan.RevisionReason != "" {
		rows = appendWrapped(rows, "Why reapproval is required  ", plan.RevisionReason, width, theme.warning)
	}
	if plan.WorkspaceDrift != nil {
		label := "Workspace drift"
		if plan.WorkspaceDrift.Severity == codingagent.WorkspaceDriftMaterial {
			label = "Material workspace drift"
		}
		rows = appendWrapped(rows, label+"  ", plan.WorkspaceDrift.Summary, width, theme.warning)
		if len(plan.WorkspaceDrift.Paths) != 0 {
			rows = appendWrapped(rows, "  Paths  ", strings.Join(plan.WorkspaceDrift.Paths, ", "), width, theme.muted)
		}
	}
	if plan.Replan != nil {
		decision := plan.Replan.Decision
		if decision == "" {
			decision = "awaiting user decision"
		}
		rows = appendWrapped(rows, "Replan  ", plan.Replan.Summary+" ("+decision+")", width, theme.warning)
	}
	rows = appendWrapped(rows, "Goal  ", plan.Goal, width, theme.assistant)
	rows = appendPlanList(rows, "In scope", plan.Scope.Included, width)
	if len(plan.Scope.Excluded) != 0 {
		rows = appendPlanList(rows, "Out of scope", plan.Scope.Excluded, width)
	}
	rows = appendPlanList(rows, "Findings", plan.Findings, width)
	if len(plan.Assumptions) != 0 {
		rows = appendPlanList(rows, "Assumptions", plan.Assumptions, width)
	}
	rows = appendPlanList(rows, "Risks", plan.Risks, width)
	rows = append(rows, renderRow{text: theme.tool.Render("Steps")})
	for index, step := range plan.Steps {
		label := fmt.Sprintf("  %d. ", index+1)
		rows = appendWrapped(rows, label, step.Goal, width, theme.assistant)
		if len(step.Files) != 0 {
			rows = appendWrapped(rows, "     Files  ", strings.Join(step.Files, ", "), width, theme.muted)
		}
		if len(step.DependsOn) != 0 {
			rows = appendWrapped(rows, "     Depends on  ", strings.Join(step.DependsOn, ", "), width, theme.muted)
		}
	}
	rows = appendPlanList(rows, "Acceptance", plan.AcceptanceCriteria, width)
	return append(rows, renderRow{})
}

func appendPlanList(rows []renderRow, title string, values []string, width int) []renderRow {
	rows = append(rows, renderRow{text: theme.tool.Render(title)})
	for _, value := range values {
		rows = appendWrapped(rows, "  • ", value, width, theme.assistant)
	}
	return rows
}
