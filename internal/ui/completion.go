package ui

import (
	"context"
	"sort"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/eaglc/codepilot/internal/session"
)

const (
	maxVisibleCompletions = 6
	maxCompletionFiles    = 500
)

type completionKind uint8

const (
	completionNone completionKind = iota
	completionCommand
	completionFile
)

type completionItem struct {
	value          string
	insert         string
	description    string
	keepOpen       bool
	executeOnEnter bool
}

type completionState struct {
	kind       completionKind
	items      []completionItem
	cursor     int
	tokenStart int
	loading    bool
	truncated  bool
	message    string
}

func (c completionState) active() bool {
	return c.kind != completionNone
}

func (m *Model) refreshCompletion() tea.Cmd {
	if prefix, ok := commandCompletionPrefix(m.composer); ok {
		if commandArgumentStarted(prefix) {
			m.closeCompletion()
			return nil
		}
		m.setCompletion(completionState{kind: completionCommand, items: commandCompletionItems(prefix)})
		return nil
	}
	start, query, ok := fileCompletionToken(m.composer)
	if !ok {
		m.closeCompletion()
		return nil
	}
	root := m.snapshot.WorktreeState.Root
	if root == "" {
		m.setCompletion(completionState{kind: completionFile, tokenStart: start, message: "No active worktree."})
		return nil
	}
	if m.workspaceFilesLoaded && m.workspaceFilesRoot == root {
		m.setCompletion(completionState{
			kind: completionFile, items: fileCompletionItems(m.workspaceFiles, query), tokenStart: start,
			truncated: m.workspaceFilesTruncated, message: m.workspaceFilesError,
		})
		return nil
	}
	m.setCompletion(completionState{kind: completionFile, tokenStart: start, loading: true})
	if m.workspaceFilesLoading && m.workspaceFilesRoot == root {
		return nil
	}
	m.workspaceFilesRoot = root
	m.workspaceFilesLoading = true
	return loadWorkspaceFilesCmd(m.client, root)
}

func (m *Model) setCompletion(next completionState) {
	selected := ""
	if m.completion.cursor >= 0 && m.completion.cursor < len(m.completion.items) {
		selected = m.completion.items[m.completion.cursor].value
	}
	if selected != "" {
		for index, item := range next.items {
			if item.value == selected {
				next.cursor = index
				break
			}
		}
	}
	if len(next.items) == 0 {
		next.cursor = 0
	} else if next.cursor >= len(next.items) {
		next.cursor = len(next.items) - 1
	}
	m.completion = next
}

func (m *Model) closeCompletion() {
	m.completion = completionState{}
}

func (m *Model) handleCompletionKey(message tea.KeyPressMsg) (bool, tea.Cmd) {
	if !m.completion.active() {
		return false, nil
	}
	key := message.Key()
	if key.Code == tea.KeyEscape || key.Code == tea.KeyEsc {
		m.closeCompletion()
		return true, nil
	}
	if movePickerCursor(&m.completion.cursor, message, len(m.completion.items)) {
		return true, nil
	}
	if key.Code == tea.KeyTab {
		return true, m.acceptCompletion()
	}
	if isEnterKey(key.Code) {
		if key.Mod&tea.ModAlt != 0 {
			return false, nil
		}
		if m.completion.loading {
			return true, nil
		}
		if len(m.completion.items) == 0 {
			m.closeCompletion()
			return false, nil
		}
		item := m.completion.items[m.completion.cursor]
		if m.completion.kind == completionCommand && item.executeOnEnter && string(m.composer) == item.insert {
			m.closeCompletion()
			return false, nil
		}
		return true, m.acceptCompletion()
	}
	return false, nil
}

func (m *Model) completionView(width int) []string {
	if !m.completion.active() || width <= 0 {
		return nil
	}
	title := "Commands"
	if m.completion.kind == completionFile {
		title = "Workspace paths"
	}
	content := make([]string, 0, maxVisibleCompletions+2)
	switch {
	case m.completion.loading:
		content = append(content, "Loading workspace files...")
	case m.completion.message != "":
		content = append(content, "Error: "+m.completion.message)
	case len(m.completion.items) == 0:
		content = append(content, "No matching items.")
	default:
		start, end := completionWindow(m.completion.cursor, len(m.completion.items))
		for index := start; index < end; index++ {
			item := m.completion.items[index]
			label := item.value
			if item.description != "" {
				label += "  ·  " + item.description
			}
			content = append(content, pickerLine(index == m.completion.cursor, label))
		}
		if m.completion.truncated {
			content = append(content, "Path list truncated; type more characters to narrow it.")
		}
	}
	return renderPanel(title, content, width, len(content)+2, true, styleDialogLine)
}

func completionWindow(cursor int, count int) (int, int) {
	if count <= maxVisibleCompletions {
		return 0, count
	}
	start := cursor - maxVisibleCompletions/2
	if start < 0 {
		start = 0
	}
	if start+maxVisibleCompletions > count {
		start = count - maxVisibleCompletions
	}
	return start, start + maxVisibleCompletions
}

func (m *Model) acceptCompletion() tea.Cmd {
	if m.completion.cursor < 0 || m.completion.cursor >= len(m.completion.items) {
		return nil
	}
	item := m.completion.items[m.completion.cursor]
	switch m.completion.kind {
	case completionCommand:
		m.composer = []rune(item.insert)
	case completionFile:
		start := m.completion.tokenStart
		if start < 0 || start > len(m.composer) {
			return nil
		}
		prefix := append([]rune(nil), m.composer[:start]...)
		m.composer = append(prefix, []rune(item.insert)...)
	}
	m.closeCompletion()
	if item.keepOpen {
		return m.refreshCompletion()
	}
	return nil
}

func commandCompletionPrefix(composer []rune) (string, bool) {
	if len(composer) == 0 || composer[0] != '/' {
		return "", false
	}
	if strings.ContainsAny(string(composer), "\r\n") {
		return "", false
	}
	prefix := strings.ToLower(string(composer[1:]))
	prefixRunes := []rune(prefix)
	trailingSpace := len(prefixRunes) > 0 && unicode.IsSpace(prefixRunes[len(prefixRunes)-1])
	prefix = strings.Join(strings.Fields(prefix), " ")
	if trailingSpace && prefix != "" {
		prefix += " "
	}
	return prefix, true
}

func commandCompletionItems(prefix string) []completionItem {
	items := make([]completionItem, 0, len(slashCommandDefinitions))
	for _, definition := range slashCommandDefinitions {
		if !strings.HasPrefix(definition.command, prefix) {
			continue
		}
		items = append(items, completionItem{
			value: definition.usage, insert: definition.insert, description: definition.description,
			executeOnEnter: definition.executeOnEnter,
		})
	}
	return items
}

func commandArgumentStarted(prefix string) bool {
	for _, definition := range slashCommandDefinitions {
		if definition.executeOnEnter || !strings.HasSuffix(definition.insert, " ") {
			continue
		}
		argumentPrefix := strings.TrimPrefix(definition.insert, "/")
		if strings.HasPrefix(prefix, argumentPrefix) {
			return true
		}
	}
	return false
}

func fileCompletionToken(composer []rune) (int, string, bool) {
	if len(composer) == 0 {
		return 0, "", false
	}
	start := len(composer)
	for start > 0 && !unicode.IsSpace(composer[start-1]) {
		start--
	}
	if start >= len(composer) || composer[start] != '@' {
		return 0, "", false
	}
	return start, strings.ToLower(string(composer[start+1:])), true
}

func fileCompletionItems(files []session.WorkspaceFile, query string) []completionItem {
	type rankedFile struct {
		path string
		rank int
	}
	values := make([]rankedFile, 0, len(files))
	for _, file := range files {
		path := strings.TrimSpace(file.Path)
		lower := strings.ToLower(path)
		if path == "" || (query != "" && !strings.Contains(lower, query)) {
			continue
		}
		if file.Directory && lower == query {
			continue
		}
		rank := 2
		if strings.HasPrefix(lower, query) {
			rank = 0
		} else if slash := strings.LastIndexByte(lower, '/'); strings.HasPrefix(lower[slash+1:], query) {
			rank = 1
		}
		if file.Directory {
			rank--
		}
		values = append(values, rankedFile{path: path, rank: rank})
	}
	sort.SliceStable(values, func(left int, right int) bool {
		if values[left].rank != values[right].rank {
			return values[left].rank < values[right].rank
		}
		return values[left].path < values[right].path
	})
	items := make([]completionItem, 0, len(values))
	for _, value := range values {
		directory := strings.HasSuffix(value.path, "/")
		insert := "@" + value.path
		description := "directory"
		if !directory {
			insert += " "
			description = "file"
		}
		items = append(items, completionItem{value: value.path, insert: insert, description: description, keepOpen: directory})
	}
	return items
}

func loadWorkspaceFilesCmd(client SessionClient, root string) tea.Cmd {
	return func() tea.Msg {
		if client == nil {
			return workspaceFilesLoadedMsg{root: root, err: &session.AppError{Code: session.ErrInvalidState}}
		}
		files, err := client.ListWorkspaceFiles(context.Background(), maxCompletionFiles)
		return workspaceFilesLoadedMsg{root: root, files: files, err: err}
	}
}

type workspaceFilesLoadedMsg struct {
	root  string
	files session.WorkspaceFileList
	err   error
}
