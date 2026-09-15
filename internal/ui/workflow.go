package ui

import (
	"fmt"
	"strings"

	"github.com/eaglc/codepilot/internal/codingagent"
)

func (m *Model) workflowRows(value codingagent.WorkflowSnapshot, width int) []renderRow {
	header := fmt.Sprintf("Workflow  •  %s  •  %d/%d nodes complete", value.Status, value.CompletedNodes, len(value.Nodes))
	if value.MaxAgentSteps > 0 {
		header += fmt.Sprintf("  •  %d/%d steps", value.UsedAgentSteps, value.MaxAgentSteps)
	}
	rows := []renderRow{{text: theme.header.Render(header)}}
	for index, node := range value.Nodes {
		marker := "○"
		style := theme.muted
		switch node.Status {
		case "running":
			marker, style = "▶", theme.warning
		case "completed":
			marker, style = "✓", theme.tool
		case "failed", "blocked":
			marker, style = "✗", theme.failure
		case "cancelled":
			marker = "–"
		}
		identity := node.Role
		if node.Executor != "" {
			identity += " · " + node.Executor
		}
		label := fmt.Sprintf("  %s %d. [%s] %s", marker, index+1, identity, node.Goal)
		rows = appendWrapped(rows, "", label, width, style)
		if len(node.DependsOn) != 0 {
			dependencies := make([]string, len(node.DependsOn))
			for index := range node.DependsOn {
				dependencies[index] = string(node.DependsOn[index])
			}
			rows = appendWrapped(rows, "     Depends on  ", strings.Join(dependencies, ", "), width, theme.muted)
		}
		if node.Attempts != 0 || node.MaxAttempts > 1 {
			rows = appendWrapped(rows, "     Attempts  ", fmt.Sprintf("%d/%d", node.Attempts, node.MaxAttempts), width, theme.muted)
		}
		if node.Failure != "" {
			label := "     Failure  "
			if node.Status == "blocked" {
				label = "     Blocked  "
			}
			rows = appendWrapped(rows, label, node.Failure, width, theme.failure)
		}
		if node.ResultRef != "" {
			rows = appendWrapped(rows, "     Evidence  ", node.ResultRef, width, theme.muted)
		}
	}
	if value.WaitingReason != "" && (value.Status == "blocked" || value.Status == "needs_replan") {
		rows = appendWrapped(rows, "  Waiting  ", value.WaitingReason, width, theme.warning)
	}
	return append(rows, renderRow{})
}

func (m *Model) childAgentRows(values []codingagent.ChildAgentSnapshot, width int) []renderRow {
	if len(values) == 0 {
		return nil
	}
	rows := []renderRow{{text: theme.header.Render(fmt.Sprintf("Child Agents  •  %d delegated", len(values)))}}
	for _, child := range values {
		marker, style := "○", theme.muted
		switch child.Status {
		case codingagent.ChildAgentRunning:
			marker, style = "▶", theme.warning
		case codingagent.ChildAgentAwaitingApproval:
			marker, style = "!", theme.warning
		case codingagent.ChildAgentCompleted:
			marker, style = "✓", theme.tool
		case codingagent.ChildAgentFailed:
			marker, style = "✗", theme.failure
		case codingagent.ChildAgentCancelled:
			marker = "–"
		}
		identity := fmt.Sprintf("  %s [%s · %s] %s", marker, child.Role, child.Status, child.Goal)
		if child.NodeID != "" {
			identity = fmt.Sprintf("  %s [%s · %s · %s] %s", marker, child.Role, child.Status, child.NodeID, child.Goal)
		}
		rows = appendWrapped(rows, "", identity, width, style)
		if child.Conclusion != "" {
			rows = appendWrapped(rows, "     Result  ", child.Conclusion, width, theme.muted)
		}
		if child.Failure != "" {
			rows = appendWrapped(rows, "     Failure  ", child.Failure, width, theme.failure)
		}
		if child.ManagedStatus != "" {
			rows = appendWrapped(rows, "     Isolated change  ", string(child.ManagedStatus), width, theme.muted)
		}
		if child.ChangeSetID != "" {
			change := child.ChangeSetID
			if len(child.ChangeFiles) != 0 {
				change += " · " + strings.Join(child.ChangeFiles, ", ")
			}
			rows = appendWrapped(rows, "     Change set  ", change, width, theme.muted)
		}
		if child.PatchArtifact != "" {
			rows = appendWrapped(rows, "     Patch artifact  ", child.PatchArtifact, width, theme.muted)
		}
	}
	return append(rows, renderRow{})
}
