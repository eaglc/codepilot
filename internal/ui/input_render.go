package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

type inputViewport struct {
	text         string
	cursorOffset int
}

// renderInputViewport keeps the logical cursor visible without inserting a
// fake cursor cell into the content. Widths are terminal cells, not rune
// counts, so CJK and emoji remain aligned.
func renderInputViewport(value []rune, cursor, width int) inputViewport {
	width = max(1, width)
	cursor = min(max(0, cursor), len(value))
	before := displayInputRunes(value[:cursor])
	after := displayInputRunes(value[cursor:])
	visibleBefore := before
	beforeWidth := ansi.StringWidth(before)
	if beforeWidth >= width {
		visibleBefore = ansi.TruncateLeft(before, beforeWidth-(width-1), "")
	}
	cursorOffset := min(width-1, ansi.StringWidth(visibleBefore))
	visibleAfter := ansi.Truncate(after, width-cursorOffset, "")
	return inputViewport{text: visibleBefore + visibleAfter, cursorOffset: cursorOffset}
}

func displayInputRunes(value []rune) string {
	return strings.ReplaceAll(string(value), "\n", " ↵ ")
}

func nativeTextCursor(x, y int) *tea.Cursor {
	cursor := tea.NewCursor(max(0, x), max(0, y))
	cursor.Shape = tea.CursorBar
	cursor.Blink = true
	return cursor
}
