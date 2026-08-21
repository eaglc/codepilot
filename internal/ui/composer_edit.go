package ui

import (
	"unicode"

	tea "charm.land/bubbletea/v2"
)

// handleHistoryKey implements ↑/↓ input-history recall. It runs only while the
// composer owns the keyboard (not inputBusy) and no completion menu is open, so
// ↑/↓ never collide with panel scrolling or completion navigation.
func (m *Model) handleHistoryKey(key tea.Key) (bool, tea.Cmd) {
	switch key.Code {
	case tea.KeyUp:
		return m.historyBack()
	case tea.KeyDown:
		return m.historyForward()
	default:
		return false, nil
	}
}

func (m *Model) historyBack() (bool, tea.Cmd) {
	if len(m.history) == 0 {
		return true, nil
	}
	if m.historyIndex < 0 {
		// First ↑ stashes the in-progress edit so ↓ can restore it afterward.
		m.historyStash = append(m.historyStash[:0], m.composer...)
		m.historyIndex = len(m.history) - 1
	} else if m.historyIndex > 0 {
		m.historyIndex--
	}
	m.restoreHistoryEntry()
	return true, m.refreshCompletion()
}

func (m *Model) historyForward() (bool, tea.Cmd) {
	if m.historyIndex < 0 {
		return true, nil
	}
	if m.historyIndex < len(m.history)-1 {
		m.historyIndex++
		m.restoreHistoryEntry()
		return true, m.refreshCompletion()
	}
	// Past the newest entry, restore the stashed in-progress edit.
	m.historyIndex = -1
	m.composer = append(m.composer[:0], m.historyStash...)
	m.composerCursor = len(m.composer)
	m.historyStash = nil
	return true, m.refreshCompletion()
}

func (m *Model) restoreHistoryEntry() {
	m.composer = []rune(m.history[m.historyIndex])
	m.composerCursor = len(m.composer)
}

// recordHistory appends a committed input to the per-session history, skipping
// consecutive duplicates so re-submitting a recalled entry does not grow the list.
func (m *Model) recordHistory(text string) {
	if text == "" {
		return
	}
	m.historyIndex = -1
	m.historyStash = nil
	if len(m.history) > 0 && m.history[len(m.history)-1] == text {
		return
	}
	m.history = append(m.history, text)
}

// resetHistory drops the per-session history when the active session changes.
func (m *Model) resetHistory() {
	m.history = nil
	m.historyIndex = -1
	m.historyStash = nil
}

// handleComposerEditKey implements cursor movement, word jumps, and line/word
// deletion for the composer buffer. Text mutations refresh completion; pure
// cursor movement does not.
func (m *Model) handleComposerEditKey(message tea.KeyPressMsg) (bool, tea.Cmd) {
	key := message.Key()
	switch key.Code {
	case tea.KeyLeft:
		if isControlKey(message, tea.KeyLeft) {
			m.composerCursor = prevWordBoundary(m.composer, m.composerCursor)
		} else {
			m.composerCursor = clampInt(m.composerCursor-1, 0, len(m.composer))
		}
		return true, nil
	case tea.KeyRight:
		if isControlKey(message, tea.KeyRight) {
			m.composerCursor = nextWordBoundary(m.composer, m.composerCursor)
		} else {
			m.composerCursor = clampInt(m.composerCursor+1, 0, len(m.composer))
		}
		return true, nil
	case tea.KeyHome:
		m.composerCursor = lineStart(m.composer, m.composerCursor)
		return true, nil
	case tea.KeyEnd:
		m.composerCursor = lineEnd(m.composer, m.composerCursor)
		return true, nil
	case tea.KeyBackspace:
		if m.composerCursor > 0 {
			m.composer = deleteRunes(m.composer, m.composerCursor-1, 1)
			m.composerCursor--
		}
		return true, m.refreshCompletion()
	case tea.KeyDelete:
		if m.composerCursor < len(m.composer) {
			m.composer = deleteRunes(m.composer, m.composerCursor, 1)
		}
		return true, m.refreshCompletion()
	}
	if isControlKey(message, 'k') {
		m.composer = deleteRunes(m.composer, m.composerCursor, lineEnd(m.composer, m.composerCursor)-m.composerCursor)
		return true, m.refreshCompletion()
	}
	if isControlKey(message, 'u') {
		start := lineStart(m.composer, m.composerCursor)
		m.composer = deleteRunes(m.composer, start, m.composerCursor-start)
		m.composerCursor = start
		return true, m.refreshCompletion()
	}
	return false, nil
}

// insertComposerText inserts runes at the cursor and refreshes completion.
func (m *Model) insertComposerText(input []rune) tea.Cmd {
	remaining := maxComposerRunes - len(m.composer)
	if remaining <= 0 {
		return nil
	}
	if len(input) > remaining {
		input = input[:remaining]
	}
	m.composer = insertRunes(m.composer, m.composerCursor, input)
	m.composerCursor += len(input)
	return m.refreshCompletion()
}

// insertRunes inserts value at index within dst, reallocating as needed.
func insertRunes(dst []rune, index int, value []rune) []rune {
	if index < 0 {
		index = 0
	}
	if index > len(dst) {
		index = len(dst)
	}
	if len(value) == 0 {
		return dst
	}
	dst = append(dst, make([]rune, len(value))...)
	copy(dst[index+len(value):], dst[index:])
	copy(dst[index:], value)
	return dst
}

// deleteRunes removes count runes starting at index and zeroes the freed tail
// so sensitive input is not retained in the backing array.
func deleteRunes(dst []rune, index int, count int) []rune {
	if index < 0 {
		index = 0
	}
	if index >= len(dst) || count <= 0 {
		return dst
	}
	if index+count > len(dst) {
		count = len(dst) - index
	}
	copy(dst[index:], dst[index+count:])
	for i := len(dst) - count; i < len(dst); i++ {
		dst[i] = 0
	}
	return dst[:len(dst)-count]
}

// prevWordBoundary returns the index of the first rune of the word at or left
// of cursor, clamped to [0, cursor].
func prevWordBoundary(text []rune, cursor int) int {
	if cursor <= 0 {
		return 0
	}
	i := cursor - 1
	for i >= 0 && unicode.IsSpace(text[i]) {
		i--
	}
	for i >= 0 && !unicode.IsSpace(text[i]) {
		i--
	}
	return i + 1
}

// nextWordBoundary returns the index one past the word at or right of cursor.
func nextWordBoundary(text []rune, cursor int) int {
	i := cursor
	for i < len(text) && unicode.IsSpace(text[i]) {
		i++
	}
	for i < len(text) && !unicode.IsSpace(text[i]) {
		i++
	}
	return i
}

// lineStart returns the index of the first rune of the line containing cursor.
func lineStart(text []rune, cursor int) int {
	for i := cursor - 1; i >= 0; i-- {
		if text[i] == '\n' {
			return i + 1
		}
	}
	return 0
}

// lineEnd returns the index one past the last rune of the line containing cursor.
func lineEnd(text []rune, cursor int) int {
	for i := cursor; i < len(text); i++ {
		if text[i] == '\n' {
			return i
		}
	}
	return len(text)
}
