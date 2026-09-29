package ui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/eaglc/codepilot/internal/codingagent"
)

type headerBadge struct {
	text     string
	style    lipgloss.Style
	priority int
}

func (m *Model) headerLine(fallbackWorkspace string, width, linesBelow int) string {
	workspace := strings.TrimSpace(m.snapshot.Workspace.DisplayName)
	if workspace == "" {
		workspace = fallbackWorkspace
	}
	badges := []headerBadge{
		{text: boundedHeaderLabel(workspace, 18), style: theme.user, priority: 3},
		{text: m.gitBadgeLabel(), style: m.gitBadgeStyle(), priority: 4},
		{text: m.modeBadgeLabel(), style: theme.tool, priority: 2},
		{text: permissionBadgeLabel(m.snapshot.Session.PermissionMode), style: theme.muted, priority: 5},
		{text: m.contextBadgeLabel(), style: theme.muted, priority: 6},
		{text: providerBadgeLabel(m.snapshot.Session.ProviderProfileID, m.snapshot.Session.ModelID), style: theme.muted, priority: 7},
	}
	if linesBelow > 0 {
		badges = append([]headerBadge{{text: fmt.Sprintf("%d lines below", linesBelow), style: theme.muted, priority: 1}}, badges...)
	}
	if attention := m.attentionBadgeLabel(); attention != "" {
		badges = append([]headerBadge{{text: attention, style: theme.warning, priority: 0}}, badges...)
	}
	for {
		line := renderHeaderBadges(badges)
		if ansi.StringWidth(line) <= width || len(badges) <= 2 {
			return truncateANSI(line, width)
		}
		remove := -1
		highest := -1
		for index, badge := range badges {
			if badge.priority > highest {
				remove, highest = index, badge.priority
			}
		}
		badges = append(badges[:remove], badges[remove+1:]...)
	}
}

func renderHeaderBadges(badges []headerBadge) string {
	parts := []string{theme.header.Render("CodePilot")}
	for _, badge := range badges {
		if strings.TrimSpace(badge.text) == "" {
			continue
		}
		parts = append(parts, badge.style.Render("["+badge.text+"]"))
	}
	return strings.Join(parts, " ")
}

func (m *Model) gitBadgeLabel() string {
	workspace := m.snapshot.Workspace
	if !workspace.Available {
		return "git unavailable"
	}
	branch := workspace.Branch
	if workspace.Detached {
		branch = "detached@" + workspace.Head
	}
	branch = boundedHeaderLabel(branch, 20)
	if workspace.Dirty {
		return fmt.Sprintf("%s dirty:%d", branch, workspace.ChangedFiles)
	}
	return branch + " clean"
}

func (m *Model) gitBadgeStyle() lipgloss.Style {
	if !m.snapshot.Workspace.Available || m.snapshot.Workspace.Dirty {
		return theme.warning
	}
	return theme.success
}

func (m *Model) modeBadgeLabel() string {
	if workflow := m.snapshot.ActiveWorkflow; workflow != nil {
		return "Workflow " + taskStateForWorkflow(workflow.Status)
	}
	if turn := m.snapshot.ActiveTurn; turn != nil {
		switch turn.Phase {
		case codingagent.TurnPhasePlanning, codingagent.TurnPhaseAwaitingPlanApproval, codingagent.TurnPhaseNeedsReplan:
			return "Plan"
		case codingagent.TurnPhaseExecuting:
			return "Plan execute"
		}
	}
	if m.planInput {
		return "Plan draft"
	}
	return "Direct"
}

func permissionBadgeLabel(mode codingagent.PermissionMode) string {
	switch mode {
	case codingagent.PermissionReadOnly:
		return "read-only"
	case codingagent.PermissionAutoEdit:
		return "auto-edit"
	case codingagent.PermissionAsk:
		return "ask"
	default:
		return "permission ?"
	}
}

func (m *Model) contextBadgeLabel() string {
	used := max(0, m.snapshot.Metrics.ContextTokens)
	if m.contextReport.Available && m.contextReport.InputBudget > 0 {
		return fmt.Sprintf("ctx %d/%d", used, m.contextReport.InputBudget)
	}
	return fmt.Sprintf("ctx %d", used)
}

func providerBadgeLabel(profileID, modelID string) string {
	profileID = boundedHeaderLabel(profileID, 12)
	modelID = boundedHeaderLabel(modelID, 18)
	if profileID == "" && modelID == "" {
		return "provider ?"
	}
	if profileID == "" {
		return modelID
	}
	if modelID == "" {
		return profileID
	}
	return profileID + "/" + modelID
}

func (m *Model) attentionBadgeLabel() string {
	if m.pendingRecovery() != nil {
		return "ACTION recovery"
	}
	if m.pendingClarification() != nil {
		return "ACTION question"
	}
	if pending := m.pendingApproval(); pending != nil {
		switch pending.Kind {
		case "plan_replan_approval":
			return "ACTION replan"
		case "plan_approval":
			return "ACTION plan"
		default:
			return "ACTION approval"
		}
	}
	return ""
}

func boundedHeaderLabel(value string, limit int) string {
	value = strings.Join(strings.Fields(escapeTerminalControls(value)), " ")
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	if limit <= 1 {
		return string(runes[:limit])
	}
	return string(runes[:limit-1]) + "…"
}
