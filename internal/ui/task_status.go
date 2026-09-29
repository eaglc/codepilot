package ui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/eaglc/codepilot/internal/codingagent"
)

const minInlineTaskTreeWidth = 96

type taskState string

const (
	taskQueued    taskState = "queued"
	taskRunning   taskState = "running"
	taskWaiting   taskState = "waiting"
	taskBlocked   taskState = "blocked"
	taskFailed    taskState = "failed"
	taskCompleted taskState = "completed"
	taskCancelled taskState = "cancelled"
)

func (m *Model) taskHierarchyRows(width int, full bool) []renderRow {
	if m.snapshot.ActiveWorkflow == nil && len(m.snapshot.ChildAgents) == 0 {
		return nil
	}
	if !full {
		return m.compactTaskHierarchyRows(width)
	}
	var rows []renderRow
	usedChildren := make(map[codingagent.ChildAgentID]bool)
	if workflow := m.snapshot.ActiveWorkflow; workflow != nil {
		state := taskState(taskStateForWorkflow(workflow.Status))
		header := fmt.Sprintf("Task hierarchy  •  Workflow %s  •  %d/%d nodes complete  •  %d/%d steps", state, workflow.CompletedNodes, len(workflow.Nodes), workflow.UsedAgentSteps, workflow.MaxAgentSteps)
		if active := activeTaskCount(workflow.Nodes, m.snapshot.ChildAgents); active > 0 {
			header += fmt.Sprintf("  •  %d active", active)
		}
		rows = append(rows, renderRow{text: taskStyle(state).Render(header)})
		if workflow.WaitingReason != "" && (state == taskWaiting || state == taskBlocked || state == taskFailed) {
			rows = appendWrapped(rows, "  ! ", workflow.WaitingReason, width, theme.warning)
		}
		for _, node := range workflow.Nodes {
			child := latestNodeChild(workflow.ID, node.ID, m.snapshot.ChildAgents)
			if child != nil {
				usedChildren[child.ID] = true
			}
			rows = append(rows, m.workflowNodeRows(workflow.ID, node, child, width)...)
		}
	}
	standalone := make([]codingagent.ChildAgentSnapshot, 0)
	for _, child := range m.snapshot.ChildAgents {
		if !usedChildren[child.ID] {
			standalone = append(standalone, child)
		}
	}
	if len(standalone) != 0 {
		rows = append(rows, renderRow{text: theme.header.Render(fmt.Sprintf("Delegated Agents  •  %d", len(standalone)))})
		for _, child := range standalone {
			rows = append(rows, m.standaloneChildRows(child, width)...)
		}
	}
	return append(rows, renderRow{})
}

func (m *Model) compactTaskHierarchyRows(width int) []renderRow {
	state, summary := taskQueued, "Delegated work"
	if workflow := m.snapshot.ActiveWorkflow; workflow != nil {
		state = taskState(taskStateForWorkflow(workflow.Status))
		summary = fmt.Sprintf("Workflow %s  •  %d/%d nodes complete", state, workflow.CompletedNodes, len(workflow.Nodes))
	} else if len(m.snapshot.ChildAgents) != 0 {
		state = aggregateChildState(m.snapshot.ChildAgents)
		summary = fmt.Sprintf("Child Agents %s  •  %d delegated", state, len(m.snapshot.ChildAgents))
	}
	rows := []renderRow{{text: taskStyle(state).Render("Task status  •  " + summary)}}
	if attention := m.attentionBadgeLabel(); attention != "" {
		rows = append(rows, renderRow{text: theme.warning.Render("  " + attention)})
	}
	rows = append(rows, renderRow{text: theme.muted.Render(truncateANSI("  /status opens the full task tree", width))}, renderRow{})
	return rows
}

func (m *Model) workflowNodeRows(workflowID string, node codingagent.WorkflowNodeSnapshot, child *codingagent.ChildAgentSnapshot, width int) []renderRow {
	key := taskNodeKey(workflowID, node.ID)
	state := taskStateForNode(node, child)
	expanded := m.expanded[key] || m.taskStatusActive
	marker := "▶"
	if expanded {
		marker = "▼"
	}
	selector := "  "
	if m.selectedBlock == taskSelectionPrefix+key {
		selector = "❯ "
	}
	role := node.Role
	if node.Executor != "" {
		role += "/" + node.Executor
	}
	line := fmt.Sprintf("%s%s %s %s [%s] %s", selector, marker, taskGlyph(state), state, role, node.Goal)
	rows := appendTaskWrapped(nil, taskSelectionPrefix+key, "", line, width, taskStyle(state))
	meta := []string{taskDurationLabel(node.StartedAt, node.FinishedAt), fmt.Sprintf("attempt %d/%d", max(1, node.Attempts), max(1, node.MaxAttempts))}
	if scope := compactTaskScope(node.ReadPaths, node.WritePaths); scope != "" {
		meta = append(meta, scope)
	}
	if node.ResultRef != "" {
		meta = append(meta, "Evidence "+node.ResultRef)
	}
	if child != nil && child.ChangeSetID != "" {
		meta = append(meta, "ChangeSet")
	}
	if child != nil && len(child.Validation) != 0 {
		meta = append(meta, fmt.Sprintf("%d checks", len(child.Validation)))
	}
	rows = appendTaskWrapped(rows, taskSelectionPrefix+key, "     ", strings.Join(meta, "  •  "), width, theme.muted)
	if node.Failure != "" {
		label := "     Failure  "
		if state == taskBlocked || state == taskWaiting {
			label = "     Waiting  "
		}
		rows = appendTaskWrapped(rows, taskSelectionPrefix+key, label, node.Failure, width, theme.failure)
	}
	if !expanded {
		return rows
	}
	rows = appendTaskDetails(rows, taskSelectionPrefix+key, node.ReadPaths, node.WritePaths, node.ResultRef, child, width)
	return rows
}

func (m *Model) standaloneChildRows(child codingagent.ChildAgentSnapshot, width int) []renderRow {
	key := taskChildKey(child.ID)
	state := taskStateForChild(child.Status)
	expanded := m.expanded[key] || m.taskStatusActive
	marker := "▶"
	if expanded {
		marker = "▼"
	}
	selector := "  "
	if m.selectedBlock == taskSelectionPrefix+key {
		selector = "❯ "
	}
	line := fmt.Sprintf("%s%s %s %s [%s] %s", selector, marker, taskGlyph(state), state, child.Role, child.Goal)
	rows := appendTaskWrapped(nil, taskSelectionPrefix+key, "", line, width, taskStyle(state))
	meta := []string{taskDurationLabel(child.StartedAt, child.CompletedAt), fmt.Sprintf("attempt %d", max(1, child.Attempt))}
	if scope := compactTaskScope(child.ReadPaths, child.WritePaths); scope != "" {
		meta = append(meta, scope)
	}
	rows = appendTaskWrapped(rows, taskSelectionPrefix+key, "     ", strings.Join(meta, "  •  "), width, theme.muted)
	if child.Failure != "" {
		rows = appendTaskWrapped(rows, taskSelectionPrefix+key, "     Failure  ", child.Failure, width, theme.failure)
	}
	if expanded {
		rows = appendTaskDetails(rows, taskSelectionPrefix+key, child.ReadPaths, child.WritePaths, "", &child, width)
	}
	return rows
}

func appendTaskDetails(rows []renderRow, selectionID string, readPaths, writePaths []string, resultRef string, child *codingagent.ChildAgentSnapshot, width int) []renderRow {
	if len(readPaths) != 0 {
		rows = appendTaskWrapped(rows, selectionID, "     Read scope  ", strings.Join(readPaths, ", "), width, theme.muted)
	}
	if len(writePaths) != 0 {
		rows = appendTaskWrapped(rows, selectionID, "     Write scope  ", strings.Join(writePaths, ", "), width, theme.muted)
	}
	if resultRef != "" {
		rows = appendTaskWrapped(rows, selectionID, "     Evidence  ", resultRef, width, theme.muted)
	}
	if child == nil {
		return rows
	}
	if child.Conclusion != "" {
		rows = appendTaskWrapped(rows, selectionID, "     Conclusion  ", child.Conclusion, width, theme.muted)
	}
	if child.ChangeSetID != "" {
		value := child.ChangeSetID
		if len(child.ChangeFiles) != 0 {
			value += "  •  " + strings.Join(child.ChangeFiles, ", ")
		}
		rows = appendTaskWrapped(rows, selectionID, "     ChangeSet  ", value, width, theme.tool)
	}
	if len(child.Validation) != 0 {
		rows = appendTaskWrapped(rows, selectionID, "     Checks  ", strings.Join(child.Validation, "  •  "), width, theme.success)
	}
	if child.PatchArtifact != "" {
		rows = appendTaskWrapped(rows, selectionID, "     Artifact  ", child.PatchArtifact, width, theme.muted)
	}
	return rows
}

func appendTaskWrapped(rows []renderRow, selectionID, prefix, value string, width int, style lipgloss.Style) []renderRow {
	start := len(rows)
	rows = appendWrapped(rows, prefix, value, width, style)
	for index := start; index < len(rows); index++ {
		rows[index].selectionID = selectionID
	}
	return rows
}

func (m *Model) taskSelectableBlocks() []selectableBlock {
	if m.width < minInlineTaskTreeWidth || m.taskStatusActive {
		return nil
	}
	var blocks []selectableBlock
	usedChildren := make(map[codingagent.ChildAgentID]bool)
	if workflow := m.snapshot.ActiveWorkflow; workflow != nil {
		for _, node := range workflow.Nodes {
			child := latestNodeChild(workflow.ID, node.ID, m.snapshot.ChildAgents)
			if child != nil {
				usedChildren[child.ID] = true
			}
			key := taskNodeKey(workflow.ID, node.ID)
			blocks = append(blocks, selectableBlock{key: taskSelectionPrefix + key, kind: "task", expandID: key, text: taskNodeCopyText(node, child)})
		}
	}
	for _, child := range m.snapshot.ChildAgents {
		if usedChildren[child.ID] {
			continue
		}
		key := taskChildKey(child.ID)
		blocks = append(blocks, selectableBlock{key: taskSelectionPrefix + key, kind: "task", expandID: key, text: taskChildCopyText(child)})
	}
	return blocks
}

func taskNodeCopyText(node codingagent.WorkflowNodeSnapshot, child *codingagent.ChildAgentSnapshot) string {
	sections := []string{fmt.Sprintf("%s [%s] %s", taskStateForNode(node, child), node.Role, node.Goal)}
	if len(node.ReadPaths) != 0 {
		sections = append(sections, "Read scope: "+strings.Join(node.ReadPaths, ", "))
	}
	if len(node.WritePaths) != 0 {
		sections = append(sections, "Write scope: "+strings.Join(node.WritePaths, ", "))
	}
	if child != nil {
		sections = append(sections, taskChildCopyText(*child))
	}
	return strings.Join(sections, "\n")
}

func taskChildCopyText(child codingagent.ChildAgentSnapshot) string {
	sections := []string{fmt.Sprintf("%s [%s] %s", taskStateForChild(child.Status), child.Role, child.Goal)}
	if child.Conclusion != "" {
		sections = append(sections, "Conclusion: "+child.Conclusion)
	}
	if child.ChangeSetID != "" {
		sections = append(sections, "ChangeSet: "+child.ChangeSetID)
	}
	if len(child.Validation) != 0 {
		sections = append(sections, "Checks: "+strings.Join(child.Validation, " • "))
	}
	return strings.Join(sections, "\n")
}

func taskNodeKey(workflowID string, nodeID codingagent.NodeID) string {
	return "workflow:" + workflowID + ":" + string(nodeID)
}

func taskChildKey(id codingagent.ChildAgentID) string {
	return "agent:" + string(id)
}

func latestNodeChild(workflowID string, nodeID codingagent.NodeID, values []codingagent.ChildAgentSnapshot) *codingagent.ChildAgentSnapshot {
	var selected *codingagent.ChildAgentSnapshot
	for index := range values {
		child := &values[index]
		if child.WorkflowID != workflowID || child.NodeID != nodeID {
			continue
		}
		if selected == nil || child.Attempt > selected.Attempt {
			selected = child
		}
	}
	return selected
}

func activeTaskCount(nodes []codingagent.WorkflowNodeSnapshot, children []codingagent.ChildAgentSnapshot) int {
	active := 0
	for _, node := range nodes {
		if node.Status == "running" {
			active++
		}
	}
	for _, child := range children {
		if child.NodeID == "" && (child.Status == codingagent.ChildAgentRunning || child.Status == codingagent.ChildAgentAwaitingApproval) {
			active++
		}
	}
	return active
}

func taskStateForWorkflow(status string) string {
	switch status {
	case "pending":
		return string(taskQueued)
	case "running":
		return string(taskRunning)
	case "needs_replan":
		return string(taskWaiting)
	case "blocked":
		return string(taskBlocked)
	case "failed":
		return string(taskFailed)
	case "completed":
		return string(taskCompleted)
	case "cancelled":
		return string(taskCancelled)
	default:
		return string(taskQueued)
	}
}

func taskStateForNode(node codingagent.WorkflowNodeSnapshot, child *codingagent.ChildAgentSnapshot) taskState {
	if child != nil && child.Status == codingagent.ChildAgentAwaitingApproval {
		return taskWaiting
	}
	switch node.Status {
	case "pending":
		return taskQueued
	case "running":
		return taskRunning
	case "blocked":
		return taskBlocked
	case "failed":
		return taskFailed
	case "completed":
		return taskCompleted
	case "cancelled":
		return taskCancelled
	default:
		return taskQueued
	}
}

func taskStateForChild(status codingagent.ChildAgentStatus) taskState {
	switch status {
	case codingagent.ChildAgentCreating, codingagent.ChildAgentReady:
		return taskQueued
	case codingagent.ChildAgentRunning:
		return taskRunning
	case codingagent.ChildAgentAwaitingApproval:
		return taskWaiting
	case codingagent.ChildAgentCompleted:
		return taskCompleted
	case codingagent.ChildAgentFailed:
		return taskFailed
	case codingagent.ChildAgentCancelled:
		return taskCancelled
	default:
		return taskQueued
	}
}

func aggregateChildState(values []codingagent.ChildAgentSnapshot) taskState {
	state := taskCompleted
	for _, child := range values {
		next := taskStateForChild(child.Status)
		switch next {
		case taskFailed, taskBlocked:
			return next
		case taskWaiting:
			state = taskWaiting
		case taskRunning:
			if state != taskWaiting {
				state = taskRunning
			}
		case taskQueued:
			if state == taskCompleted {
				state = taskQueued
			}
		}
	}
	return state
}

func taskGlyph(state taskState) string {
	switch state {
	case taskRunning:
		return "▶"
	case taskWaiting:
		return "!"
	case taskBlocked, taskFailed:
		return "✗"
	case taskCompleted:
		return "✓"
	case taskCancelled:
		return "–"
	default:
		return "○"
	}
}

func taskStyle(state taskState) lipgloss.Style {
	switch state {
	case taskRunning, taskWaiting:
		return theme.warning
	case taskBlocked, taskFailed:
		return theme.failure
	case taskCompleted:
		return theme.success
	default:
		return theme.muted
	}
}

func compactTaskScope(readPaths, writePaths []string) string {
	paths := append(append([]string(nil), writePaths...), readPaths...)
	if len(paths) == 0 {
		return "scope unspecified"
	}
	sort.Strings(paths)
	paths = compactTaskStrings(paths)
	if len(paths) > 2 {
		return fmt.Sprintf("scope %s +%d", strings.Join(paths[:2], ","), len(paths)-2)
	}
	return "scope " + strings.Join(paths, ",")
}

func compactTaskStrings(values []string) []string {
	if len(values) < 2 {
		return values
	}
	result := values[:1]
	for _, value := range values[1:] {
		if value != result[len(result)-1] {
			result = append(result, value)
		}
	}
	return result
}

func taskDurationLabel(startedAt, finishedAt time.Time) string {
	if startedAt.IsZero() {
		return "elapsed —"
	}
	end := finishedAt
	if end.IsZero() {
		end = time.Now()
	}
	if end.Before(startedAt) {
		return "elapsed —"
	}
	return "elapsed " + formatMetricDuration(end.Sub(startedAt))
}

func (m *Model) handleTaskStatusKey(message tea.KeyPressMsg) tea.Cmd {
	key := message.Key()
	if key.Code == tea.KeyEscape || key.Code == tea.KeyEsc || strings.EqualFold(key.Text, "q") {
		m.taskStatusActive = false
		return nil
	}
	switch key.Code {
	case tea.KeyUp:
		m.taskStatusScroll = max(0, m.taskStatusScroll-1)
	case tea.KeyDown:
		m.taskStatusScroll++
	case tea.KeyPgUp:
		m.taskStatusScroll = max(0, m.taskStatusScroll-max(1, m.height-2))
	case tea.KeyPgDown:
		m.taskStatusScroll += max(1, m.height-2)
	case tea.KeyHome:
		m.taskStatusScroll = 0
	case tea.KeyEnd:
		m.taskStatusScroll = 1 << 20
	}
	return nil
}

func (m *Model) taskStatusView(width, height int) tea.View {
	lines := []string{
		theme.header.Render("Task status"),
		theme.muted.Render("↑/↓ scroll  •  q or Esc closes  •  durable Snapshot state only"),
		"",
	}
	if attention := m.attentionBadgeLabel(); attention != "" {
		lines = append(lines, theme.warning.Render(attention), "")
	}
	if plan := m.snapshot.ActivePlan; plan != nil {
		lines = append(lines, theme.header.Render(fmt.Sprintf("Plan v%d  •  %s", plan.Version, boundedHeaderLabel(plan.Goal, max(8, width-16)))), "")
	}
	rows := m.taskHierarchyRows(max(8, width-2), true)
	if len(rows) == 0 && m.snapshot.ActivePlan == nil && m.snapshot.ActiveTurn == nil {
		lines = append(lines, theme.muted.Render("No active Task, Plan, Workflow, or Child Agent."))
	}
	for _, row := range rows {
		lines = append(lines, row.text)
	}
	maxScroll := max(0, len(lines)-height)
	m.taskStatusScroll = min(max(0, m.taskStatusScroll), maxScroll)
	return pageView("CodePilot Task Status", lines[m.taskStatusScroll:], width, height)
}
