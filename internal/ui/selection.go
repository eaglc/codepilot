package ui

import (
	"fmt"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/eaglc/codepilot/internal/codingagent"
)

const (
	messageSelectionPrefix = "message:"
	toolSelectionPrefix    = "tool:"
)

type selectableBlock struct {
	key    string
	kind   string
	toolID string
	text   string
}

type textPosition struct {
	row    int
	column int
}

type textSelection struct {
	tracking bool
	dragged  bool
	anchor   textPosition
	focus    textPosition
}

type textHit struct {
	row  int
	text string
}

func (s textSelection) hasRange() bool {
	return s.dragged && (s.anchor.row != s.focus.row || s.anchor.column != s.focus.column)
}

func (s textSelection) bounds() (textPosition, textPosition, bool) {
	if !s.hasRange() {
		return textPosition{}, textPosition{}, false
	}
	start, end := s.anchor, s.focus
	if end.row < start.row || end.row == start.row && end.column < start.column {
		start, end = end, start
	}
	end.column++
	return start, end, true
}

func (m *Model) selectableBlocks() []selectableBlock {
	results := make(map[string]codingagent.TranscriptTool)
	for _, item := range m.snapshot.Transcript {
		if item.Kind == codingagent.TranscriptToolResult && item.Tool != nil {
			results[item.Tool.CallID] = *item.Tool
		}
	}
	seenTools := make(map[string]struct{})
	var blocks []selectableBlock
	for index, item := range m.snapshot.Transcript {
		switch item.Kind {
		case codingagent.TranscriptText:
			if item.Role != codingagent.TranscriptRoleUser && item.Role != codingagent.TranscriptRoleAssistant {
				continue
			}
			blocks = append(blocks, selectableBlock{key: messageSelectionKey(item, index), kind: "message", text: item.Text})
		case codingagent.TranscriptToolCall, codingagent.TranscriptToolResult:
			if item.Tool == nil || item.Tool.CallID == "" {
				continue
			}
			if _, exists := seenTools[item.Tool.CallID]; exists {
				continue
			}
			seenTools[item.Tool.CallID] = struct{}{}
			activity := *item.Tool
			if result, found := results[item.Tool.CallID]; found {
				activity = result
			}
			blocks = append(blocks, selectableBlock{key: toolSelectionPrefix + item.Tool.CallID, kind: "tool", toolID: item.Tool.CallID, text: toolCopyText(activity)})
		}
	}
	var liveIDs []string
	for id := range m.activities {
		if _, exists := seenTools[id]; id != "" && !exists {
			liveIDs = append(liveIDs, id)
		}
	}
	sort.Strings(liveIDs)
	for _, id := range liveIDs {
		activity := m.activities[id]
		blocks = append(blocks, selectableBlock{key: toolSelectionPrefix + id, kind: "tool", toolID: id, text: toolCopyText(codingagent.TranscriptTool{
			CallID: id, Name: activity.Name, Status: activity.Status, Summary: activity.Summary, Detail: activity.Detail, Diff: activity.Diff,
		})})
	}
	return blocks
}

func messageSelectionKey(item codingagent.TranscriptItem, index int) string {
	id := strings.TrimSpace(item.ID)
	if id == "" {
		id = fmt.Sprintf("index-%d", index)
	}
	return messageSelectionPrefix + id
}

func toolCopyText(activity codingagent.TranscriptTool) string {
	var sections []string
	if strings.TrimSpace(activity.Detail) != "" {
		sections = append(sections, activity.Detail)
	}
	if activity.Diff != nil {
		if strings.TrimSpace(activity.Diff.Text) != "" {
			sections = append(sections, activity.Diff.Text)
		}
	}
	if len(sections) == 0 {
		if strings.TrimSpace(activity.Summary) != "" {
			sections = append(sections, activity.Summary)
		} else if strings.TrimSpace(activity.Name) != "" {
			sections = append(sections, activity.Name)
		}
	}
	return strings.Join(sections, "\n\n")
}

func (m *Model) cycleSelection() {
	blocks := m.selectableBlocks()
	if len(blocks) == 0 {
		m.clearSelection()
		return
	}
	for index, block := range blocks {
		if block.key == m.selectedBlock {
			m.selectBlock(blocks[(index+1)%len(blocks)])
			return
		}
	}
	m.selectBlock(blocks[0])
}

func (m *Model) selectBlock(block selectableBlock) {
	m.selectedBlock = block.key
	m.selectedTool = block.toolID
}

func (m *Model) selectBlockByKey(key string) (selectableBlock, bool) {
	for _, block := range m.selectableBlocks() {
		if block.key == key {
			m.selectBlock(block)
			return block, true
		}
	}
	return selectableBlock{}, false
}

func (m *Model) clearSelection() {
	m.selectedBlock = ""
	m.selectedTool = ""
}

func (m *Model) clearTextSelection() {
	m.textSelection = textSelection{}
	m.mouseDownBlock = ""
}

func (m *Model) clearAllSelections() {
	m.clearSelection()
	m.clearTextSelection()
}

func (m *Model) copySelection() tea.Cmd {
	block, found := m.selectBlockByKey(m.selectedBlock)
	if !found || strings.TrimSpace(block.text) == "" {
		m.errorMessage = "The selected block has no copyable text."
		return nil
	}
	m.clearSelection()
	m.errorMessage = ""
	m.status = "Copied selected " + block.kind + " to the system clipboard."
	return tea.SetClipboard(block.text)
}

func (m *Model) copyTextSelection() tea.Cmd {
	value := m.selectedText(m.conversationRows(max(20, m.width)))
	if strings.TrimSpace(value) == "" {
		m.errorMessage = "The text selection has no copyable content."
		return nil
	}
	m.clearTextSelection()
	m.errorMessage = ""
	m.status = "Copied selected text to the system clipboard."
	return tea.SetClipboard(value)
}

func (m *Model) selectedText(rows []renderRow) string {
	start, end, ok := m.textSelection.bounds()
	if !ok || len(rows) == 0 {
		return ""
	}
	start.row = min(max(0, start.row), len(rows)-1)
	end.row = min(max(0, end.row), len(rows)-1)
	var selected []string
	for row := start.row; row <= end.row; row++ {
		plain := ansi.Strip(rows[row].text)
		from, to := 0, ansi.StringWidth(plain)
		if row == start.row {
			from = start.column
		}
		if row == end.row {
			to = end.column
		}
		_, middle, _ := splitDisplayRange(plain, from, to)
		selected = append(selected, middle)
	}
	return strings.Join(selected, "\n")
}

func splitDisplayRange(value string, start, end int) (string, string, string) {
	start, end = max(0, start), max(start, end)
	var before, middle, after strings.Builder
	column := 0
	for _, value := range value {
		width := max(0, ansi.StringWidth(string(value)))
		target := &middle
		if column+width <= start {
			target = &before
		} else if column >= end {
			target = &after
		}
		target.WriteRune(value)
		column += width
	}
	return before.String(), middle.String(), after.String()
}

func (m *Model) beginMouseTextSelection(mouse tea.Mouse) {
	position, ok := m.mouseTextPosition(mouse.X, mouse.Y, true)
	if !ok {
		m.clearAllSelections()
		return
	}
	m.clearTextSelection()
	m.textSelection = textSelection{tracking: true, anchor: position, focus: position}
	m.mouseDownBlock = m.hitBlocks[mouse.Y]
}

func (m *Model) updateMouseTextSelection(mouse tea.Mouse) {
	if !m.textSelection.tracking {
		return
	}
	position, ok := m.mouseTextPosition(mouse.X, mouse.Y, false)
	if !ok {
		return
	}
	if position != m.textSelection.anchor {
		m.textSelection.dragged = true
		m.clearSelection()
	}
	m.textSelection.focus = position
}

func (m *Model) finishMouseTextSelection(mouse tea.Mouse) {
	if !m.textSelection.tracking {
		return
	}
	m.updateMouseTextSelection(mouse)
	m.textSelection.tracking = false
	blockKey := m.mouseDownBlock
	m.mouseDownBlock = ""
	if m.textSelection.hasRange() {
		m.clearSelection()
		return
	}
	m.clearTextSelection()
	if blockKey == "" {
		m.clearSelection()
		return
	}
	block, found := m.selectBlockByKey(blockKey)
	if found && block.toolID != "" {
		m.expanded[block.toolID] = !m.expanded[block.toolID]
	}
}

func (m *Model) mouseTextPosition(x, y int, requireContent bool) (textPosition, bool) {
	hit, found := m.hitTextRows[y]
	if !found && !requireContent && len(m.hitTextRows) != 0 {
		nearestY := y
		if y < 1 {
			nearestY = 1
		} else {
			nearestY = 0
			for candidate := range m.hitTextRows {
				nearestY = max(nearestY, candidate)
			}
		}
		hit, found = m.hitTextRows[nearestY]
	}
	if !found || strings.TrimSpace(hit.text) == "" {
		return textPosition{}, false
	}
	contentWidth := ansi.StringWidth(strings.TrimRight(hit.text, " "))
	if contentWidth == 0 || requireContent && (x < 0 || x >= contentWidth) {
		return textPosition{}, false
	}
	return textPosition{row: hit.row, column: min(max(0, x), contentWidth-1)}, true
}

func (m *Model) applyTextSelection(rows []renderRow) []renderRow {
	start, end, ok := m.textSelection.bounds()
	if !ok {
		return rows
	}
	for row := max(0, start.row); row <= min(len(rows)-1, end.row); row++ {
		plain := ansi.Strip(rows[row].text)
		from, to := 0, ansi.StringWidth(plain)
		if row == start.row {
			from = start.column
		}
		if row == end.row {
			to = end.column
		}
		before, middle, after := splitDisplayRange(plain, from, to)
		if middle == "" {
			continue
		}
		rows[row].text = theme.assistant.Render(before) + theme.textSelected.Render(middle) + theme.assistant.Render(after)
	}
	return rows
}
