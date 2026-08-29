package ui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/eaglc/codepilot/internal/codingagent"
)

const (
	minDiffLayoutWidth   = 96
	minDiffPaneWidth     = 42
	minConversationWidth = 48
)

type diffPane struct {
	toolName string
	files    []string
	text     string
}

func diffPaneLayout(width int, available bool) (int, int) {
	if !available || width < minDiffLayoutWidth {
		return width, 0
	}
	diffWidth := min(72, max(minDiffPaneWidth, width*45/100))
	conversationWidth := width - diffWidth - 1
	if conversationWidth < minConversationWidth {
		return width, 0
	}
	return conversationWidth, diffWidth
}

func (m *Model) selectedDiffPane() (diffPane, bool) {
	if m.selectedTool == "" || !m.expanded[m.selectedTool] {
		return diffPane{}, false
	}
	results := make(map[string]codingagent.TranscriptTool)
	for _, item := range m.snapshot.Transcript {
		if item.Kind == codingagent.TranscriptToolResult && item.Tool != nil {
			results[item.Tool.CallID] = *item.Tool
		}
	}
	for index := 0; index < len(m.snapshot.Transcript); index++ {
		item := m.snapshot.Transcript[index]
		if item.Tool == nil {
			continue
		}
		activity, ok := m.resolvedTranscriptTool(item, results)
		if !ok {
			continue
		}
		if activity.Name == createFileToolName {
			group, next := m.collectConsecutiveCreateFiles(index, results)
			if group.primaryID() == m.selectedTool {
				return diffPaneForActivities(group.activities)
			}
			index = next - 1
			continue
		}
		if activity.CallID == m.selectedTool {
			return diffPaneForActivities([]codingagent.TranscriptTool{activity})
		}
	}
	if live, found := m.activities[m.selectedTool]; found {
		return diffPaneForActivities([]codingagent.TranscriptTool{{
			CallID: live.CallID, Name: live.Name, Status: live.Status, Summary: live.Summary,
			Detail: live.Detail, Diff: live.Diff, Resources: live.Resources,
		}})
	}
	return diffPane{}, false
}

func diffPaneForActivities(activities []codingagent.TranscriptTool) (diffPane, bool) {
	if len(activities) == 0 {
		return diffPane{}, false
	}
	pane := diffPane{toolName: activities[0].Name}
	seenFiles := make(map[string]struct{})
	var diffs []string
	for _, activity := range activities {
		if activity.Diff == nil || strings.TrimSpace(activity.Diff.Text) == "" {
			continue
		}
		diffs = append(diffs, strings.TrimSpace(strings.ReplaceAll(activity.Diff.Text, "\r\n", "\n")))
		for _, file := range activity.Diff.Files {
			file = strings.TrimSpace(strings.ReplaceAll(file, "\\", "/"))
			if file != "" {
				seenFiles[file] = struct{}{}
			}
		}
	}
	if len(diffs) == 0 {
		return diffPane{}, false
	}
	for file := range seenFiles {
		pane.files = append(pane.files, file)
	}
	sort.Strings(pane.files)
	pane.text = strings.Join(diffs, "\n")
	return pane, true
}

func renderDiffPane(pane diffPane, width, height, scroll int) ([]string, int) {
	if width <= 0 || height <= 0 {
		return nil, 0
	}
	title := " Changes"
	if pane.toolName != "" {
		title += "  •  " + pane.toolName
	}
	if len(pane.files) != 0 {
		title += fmt.Sprintf("  •  %d %s", len(pane.files), pluralize(len(pane.files), "file", "files"))
	}
	header := []string{theme.header.Render(truncateANSI(title, width))}
	if len(pane.files) != 0 {
		header = append(header, theme.muted.Render(truncateANSI(" "+strings.Join(pane.files, ", "), width)))
	}
	header = append(header, theme.muted.Render(strings.Repeat("─", max(1, width))))
	content := diffPaneContentLines(pane.text, width)
	available := max(0, height-len(header))
	maxScroll := max(0, len(content)-available)
	scroll = min(max(0, scroll), maxScroll)
	visible := content[scroll:min(len(content), scroll+available)]
	rows := append(header, visible...)
	if maxScroll > 0 && len(rows) != 0 {
		position := fmt.Sprintf(" %d/%d", scroll+1, maxScroll+1)
		rows[len(header)-1] = truncateANSI(rows[len(header)-1], max(1, width-ansi.StringWidth(position))) + theme.muted.Render(position)
	}
	return rows, maxScroll
}

func diffPaneContentLines(value string, width int) []string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	oldLine, newLine := 0, 0
	var rows []string
	for _, raw := range strings.Split(strings.TrimSuffix(value, "\n"), "\n") {
		line := escapeTerminalControls(raw)
		switch {
		case strings.HasPrefix(line, "@@"):
			if oldStart, newStart, ok := diffHunkStarts(line); ok {
				oldLine, newLine = oldStart, newStart
			}
			rows = append(rows, theme.hunk.Render(truncateANSI(fmt.Sprintf(" %4s %4s │ %s", "", "", line), width)))
		case strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++ "):
			rows = append(rows, theme.tool.Render(truncateANSI(fmt.Sprintf(" %4s %4s │ %s", "", "", line), width)))
		case strings.HasPrefix(line, "-"):
			rows = append(rows, theme.removed.Render(truncateANSI(fmt.Sprintf(" %4d %4s │ %s", oldLine, "", line), width)))
			oldLine++
		case strings.HasPrefix(line, "+"):
			rows = append(rows, theme.added.Render(truncateANSI(fmt.Sprintf(" %4s %4d │ %s", "", newLine, line), width)))
			newLine++
		case strings.HasPrefix(line, " "):
			rows = append(rows, theme.muted.Render(truncateANSI(fmt.Sprintf(" %4d %4d │ %s", oldLine, newLine, line), width)))
			oldLine++
			newLine++
		default:
			rows = append(rows, theme.muted.Render(truncateANSI(fmt.Sprintf(" %4s %4s │ %s", "", "", line), width)))
		}
	}
	return rows
}

func diffHunkStarts(line string) (int, int, bool) {
	fields := strings.Fields(line)
	if len(fields) < 3 || fields[0] != "@@" {
		return 0, 0, false
	}
	oldStart, oldOK := diffRangeStart(fields[1], '-')
	newStart, newOK := diffRangeStart(fields[2], '+')
	return oldStart, newStart, oldOK && newOK
}

func diffRangeStart(value string, marker byte) (int, bool) {
	if len(value) < 2 || value[0] != marker {
		return 0, false
	}
	value = strings.SplitN(value[1:], ",", 2)[0]
	line, err := strconv.Atoi(value)
	return line, err == nil
}

func padANSI(value string, width int) string {
	value = truncateANSI(value, width)
	if padding := width - ansi.StringWidth(value); padding > 0 {
		value += strings.Repeat(" ", padding)
	}
	return value
}
