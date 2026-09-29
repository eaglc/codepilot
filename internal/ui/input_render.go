package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
)

type inputViewport struct {
	text         string
	cursorOffset int
}

type composerPoint struct {
	index  int
	line   int
	column int
}

type composerDocument struct {
	lines  []string
	points []composerPoint
	cursor composerPoint
}

// layoutComposer lays out editable text in terminal cells. Cursor points are
// grapheme boundaries so combining marks and emoji sequences remain atomic.
func layoutComposer(value []rune, cursor, width int) composerDocument {
	width = max(1, width)
	cursor = nearestGraphemeBoundary(value, cursor)
	document := composerDocument{lines: []string{""}}
	line, column, runeIndex := 0, 0, 0
	graphemes := uniseg.NewGraphemes(string(value))
	for graphemes.Next() {
		cluster := graphemes.Str()
		clusterRunes := graphemes.Runes()
		wrapped := false
		if column >= width {
			line++
			column = 0
			document.lines = append(document.lines, "")
			wrapped = true
		}
		document.points = append(document.points, composerPoint{index: runeIndex, line: line, column: column})
		if cluster == "\n" {
			if !wrapped {
				line++
				column = 0
				document.lines = append(document.lines, "")
			}
			runeIndex += len(clusterRunes)
			continue
		}
		display := cluster
		if cluster == "\t" {
			display = "    "
		}
		clusterWidth := max(1, ansi.StringWidth(display))
		if column > 0 && column+clusterWidth > width {
			line++
			column = 0
			document.lines = append(document.lines, "")
			document.points[len(document.points)-1] = composerPoint{index: runeIndex, line: line, column: column}
		}
		document.lines[line] += display
		column += clusterWidth
		runeIndex += len(clusterRunes)
	}
	if column >= width {
		line++
		column = 0
		document.lines = append(document.lines, "")
	}
	document.points = append(document.points, composerPoint{index: len(value), line: line, column: column})
	document.cursor = document.points[len(document.points)-1]
	for _, point := range document.points {
		if point.index == cursor {
			document.cursor = point
			break
		}
	}
	return document
}

func graphemeBoundaries(value []rune) []int {
	boundaries := []int{0}
	runeIndex := 0
	graphemes := uniseg.NewGraphemes(string(value))
	for graphemes.Next() {
		runeIndex += len(graphemes.Runes())
		boundaries = append(boundaries, runeIndex)
	}
	return boundaries
}

func nearestGraphemeBoundary(value []rune, cursor int) int {
	cursor = min(max(0, cursor), len(value))
	boundaries := graphemeBoundaries(value)
	previous := 0
	for _, boundary := range boundaries {
		if boundary >= cursor {
			if cursor-previous <= boundary-cursor {
				return previous
			}
			return boundary
		}
		previous = boundary
	}
	return len(value)
}

func previousGraphemeBoundary(value []rune, cursor int) int {
	cursor = nearestGraphemeBoundary(value, cursor)
	previous := 0
	for _, boundary := range graphemeBoundaries(value) {
		if boundary >= cursor {
			return previous
		}
		previous = boundary
	}
	return previous
}

func nextGraphemeBoundary(value []rune, cursor int) int {
	cursor = nearestGraphemeBoundary(value, cursor)
	for _, boundary := range graphemeBoundaries(value) {
		if boundary > cursor {
			return boundary
		}
	}
	return len(value)
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
