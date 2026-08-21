package ui

import "strings"

// composerCursorBlock is the cell rendered at the composer cursor position. A
// reverse-video space reads as a solid block on every terminal and never renders
// as an invisible zero-width cursor.
const composerCursorBlock = " "

// composerView renders the composer contents with a visible cursor at the given
// rune offset. Newlines are rendered as a ↵ glyph so the single-line input box
// stays intact. cursorOn controls whether the cursor cell is a highlighted block
// (input focused) or a plain space (input not focused, so the text stays put).
func composerView(input []rune, cursor int, busy bool, cursorOn bool) string {
	if busy {
		return codePilotStyles.muted.Render("Starting turn...")
	}
	if len(input) == 0 {
		return composerCursorCell(cursorOn) +
			codePilotStyles.placeholder.Render("Ask CodePilot anything…")
	}
	cursor = clampInt(cursor, 0, len(input))
	display := []rune(strings.ReplaceAll(string(input), "\n", " ↵ "))
	index := clampInt(composerDisplayIndex(input, cursor), 0, len(display))
	return codePilotStyles.text.Render(string(display[:index])) +
		composerCursorCell(cursorOn) +
		codePilotStyles.text.Render(string(display[index:]))
}

// composerCursorCell renders the single cursor cell: a reverse-video block when
// the input box owns focus, otherwise a plain space that preserves alignment.
func composerCursorCell(on bool) string {
	if on {
		return codePilotStyles.cursor.Render(composerCursorBlock)
	}
	return " "
}

// composerDisplayIndex maps a rune offset in the composer buffer to the matching
// offset in the rendered string, where every '\n' expands to three runes (" ↵ ").
func composerDisplayIndex(input []rune, cursor int) int {
	if cursor <= 0 {
		return 0
	}
	if cursor > len(input) {
		cursor = len(input)
	}
	index := 0
	for _, current := range input[:cursor] {
		if current == '\n' {
			index += 3
		} else {
			index++
		}
	}
	return index
}
