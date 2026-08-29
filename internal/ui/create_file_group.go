package ui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/eaglc/codepilot/internal/codingagent"
)

const createFileToolName = "create_file"

type createFileGroup struct {
	activities []codingagent.TranscriptTool
}

func (g createFileGroup) primaryID() string {
	if len(g.activities) == 0 {
		return ""
	}
	return g.activities[0].CallID
}

func (m *Model) resolvedTranscriptTool(item codingagent.TranscriptItem, results map[string]codingagent.TranscriptTool) (codingagent.TranscriptTool, bool) {
	if item.Tool == nil || item.Tool.CallID == "" {
		return codingagent.TranscriptTool{}, false
	}
	activity := *item.Tool
	if result, found := results[activity.CallID]; found {
		activity = result
	} else if live, found := m.activities[activity.CallID]; found {
		activity.Status, activity.Summary, activity.Detail, activity.Diff, activity.Resources = live.Status, live.Summary, live.Detail, live.Diff, live.Resources
		if strings.TrimSpace(live.Name) != "" {
			activity.Name = live.Name
		}
	}
	return activity, true
}

// collectConsecutiveCreateFiles folds adjacent create_file call/result records
// into one presentation group. Durable transcript records remain untouched.
func (m *Model) collectConsecutiveCreateFiles(start int, results map[string]codingagent.TranscriptTool) (createFileGroup, int) {
	group := createFileGroup{}
	seen := make(map[string]struct{})
	index := start
	for index < len(m.snapshot.Transcript) {
		item := m.snapshot.Transcript[index]
		if item.Kind != codingagent.TranscriptToolCall && item.Kind != codingagent.TranscriptToolResult {
			break
		}
		activity, ok := m.resolvedTranscriptTool(item, results)
		if !ok || activity.Name != createFileToolName {
			break
		}
		if _, exists := seen[activity.CallID]; !exists {
			seen[activity.CallID] = struct{}{}
			group.activities = append(group.activities, activity)
		}
		index++
	}
	return group, index
}

func (m *Model) createFileGroupRows(group createFileGroup, width int) []renderRow {
	if len(group.activities) == 0 {
		return nil
	}
	id := group.primaryID()
	expanded := m.expanded[id]
	marker := "▶"
	if expanded {
		marker = "▼"
	}
	status := group.activities[len(group.activities)-1].Status
	isError := false
	for _, activity := range group.activities {
		isError = isError || activity.IsError
		if activity.Status == "failed" || activity.Status == "denied" || activity.Status == "cancelled" || activity.Status == "error" {
			isError = true
		}
	}
	paths := createFileGroupPaths(group)
	count := len(paths)
	unit := "file"
	if count != 1 {
		unit = "files"
	}
	if count == 0 {
		count = len(group.activities)
		unit = "pending"
	}
	selector := "  "
	if id == m.selectedTool {
		selector = "❯ "
	}
	line := fmt.Sprintf("%s%s %s %s  %d %s", selector, marker, toolStatusGlyph(status, isError), createFileToolName, count, unit)
	selectionID := toolSelectionPrefix + id
	rows := []renderRow{{text: theme.tool.Render(line), toolID: id, selectionID: selectionID}}
	for _, treeLine := range fileTreeLines(paths) {
		text := "      " + escapeTerminalControls(treeLine)
		rows = append(rows, renderRow{text: theme.muted.Render(truncateANSI(text, max(8, width))), toolID: id, selectionID: selectionID})
	}
	if expanded {
		for _, activity := range group.activities {
			if strings.TrimSpace(activity.Detail) != "" {
				for _, detail := range wrapLines(activity.Detail, max(8, width-6)) {
					rows = append(rows, renderRow{text: theme.muted.Render("      " + detail), toolID: id, selectionID: selectionID})
				}
			}
			if !m.diffPaneActive && activity.Diff != nil && activity.Diff.Text != "" {
				rows = append(rows, renderRow{text: theme.muted.Render("      Applied changes"), toolID: id, selectionID: selectionID})
				diffText := strings.ReplaceAll(activity.Diff.Text, "\r\n", "\n")
				for _, line := range strings.Split(strings.TrimSuffix(diffText, "\n"), "\n") {
					rows = append(rows, renderRow{text: "      " + styleDiffLine(escapeTerminalControls(line)), toolID: id, selectionID: selectionID})
				}
			}
		}
	}
	return append(rows, renderRow{})
}

func createFileGroupPaths(group createFileGroup) []string {
	seen := make(map[string]struct{})
	var paths []string
	for _, activity := range group.activities {
		for _, resource := range activity.Resources {
			value := strings.Trim(strings.ReplaceAll(strings.TrimSpace(resource.Path), "\\", "/"), "/")
			if value == "" {
				continue
			}
			if _, exists := seen[value]; exists {
				continue
			}
			seen[value] = struct{}{}
			paths = append(paths, value)
		}
	}
	sort.Strings(paths)
	return paths
}

type fileTreeNode struct {
	children map[string]*fileTreeNode
}

func fileTreeLines(paths []string) []string {
	root := &fileTreeNode{children: make(map[string]*fileTreeNode)}
	for _, value := range paths {
		parts := strings.Split(strings.Trim(strings.ReplaceAll(value, "\\", "/"), "/"), "/")
		node := root
		for _, part := range parts {
			if part == "" {
				continue
			}
			if node.children[part] == nil {
				node.children[part] = &fileTreeNode{children: make(map[string]*fileTreeNode)}
			}
			node = node.children[part]
		}
	}
	var lines []string
	appendFileTreeLines(root, "", &lines)
	return lines
}

func appendFileTreeLines(node *fileTreeNode, prefix string, lines *[]string) {
	names := make([]string, 0, len(node.children))
	for name := range node.children {
		names = append(names, name)
	}
	sort.Strings(names)
	for index, name := range names {
		child := node.children[name]
		last := index == len(names)-1
		connector := "├── "
		nextPrefix := prefix + "│   "
		if last {
			connector = "└── "
			nextPrefix = prefix + "    "
		}
		label := name
		if len(child.children) != 0 {
			label += "/"
		}
		*lines = append(*lines, prefix+connector+label)
		appendFileTreeLines(child, nextPrefix, lines)
	}
}

func createFileGroupCopyText(group createFileGroup) string {
	sections := []string{createFileToolName}
	if tree := fileTreeLines(createFileGroupPaths(group)); len(tree) != 0 {
		sections = append(sections, strings.Join(tree, "\n"))
	}
	for _, activity := range group.activities {
		value := toolCopyText(activity)
		if strings.TrimSpace(value) != "" && strings.TrimSpace(value) != createFileToolName {
			sections = append(sections, value)
		}
	}
	return strings.Join(sections, "\n\n")
}

func (m *Model) unanchoredLiveTools(results map[string]codingagent.TranscriptTool) []codingagent.TranscriptTool {
	var ids []string
	for id := range m.activities {
		if _, durable := results[id]; durable || transcriptHasCall(m.snapshot.Transcript, id) {
			continue
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	activities := make([]codingagent.TranscriptTool, 0, len(ids))
	for _, id := range ids {
		live := m.activities[id]
		activities = append(activities, codingagent.TranscriptTool{CallID: id, Name: live.Name, Status: live.Status, Summary: live.Summary, Detail: live.Detail, Diff: live.Diff, Resources: live.Resources})
	}
	return activities
}
