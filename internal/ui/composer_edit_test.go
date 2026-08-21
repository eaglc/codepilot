package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestInsertRunesAtCursor(t *testing.T) {
	if got := insertRunes([]rune("ace"), 1, []rune("bd")); string(got) != "abdce" {
		t.Fatalf("insertRunes = %q, want %q", got, "abdce")
	}
	if got := insertRunes([]rune("ab"), 2, []rune("c")); string(got) != "abc" {
		t.Fatalf("append insert = %q, want %q", got, "abc")
	}
	if got := insertRunes(nil, 0, []rune("hi")); string(got) != "hi" {
		t.Fatalf("nil insert = %q, want %q", got, "hi")
	}
	if got := insertRunes([]rune("ab"), 99, []rune("c")); string(got) != "abc" {
		t.Fatalf("clamped insert = %q, want %q", got, "abc")
	}
}

func TestDeleteRunesRemovesRange(t *testing.T) {
	if got := deleteRunes([]rune("abcde"), 1, 2); string(got) != "ade" {
		t.Fatalf("deleteRunes = %q, want %q", got, "ade")
	}
	if got := deleteRunes([]rune("ab"), 0, 5); string(got) != "" {
		t.Fatalf("over-delete = %q, want empty", got)
	}
	if got := deleteRunes([]rune("ab"), 5, 1); string(got) != "ab" {
		t.Fatalf("out-of-range delete = %q, want %q", got, "ab")
	}
	if got := deleteRunes([]rune("ab"), 0, 0); string(got) != "ab" {
		t.Fatalf("zero-count delete = %q, want %q", got, "ab")
	}
}

func TestWordBoundaries(t *testing.T) {
	text := []rune("hello world foo")
	if got := prevWordBoundary(text, len(text)); got != 12 {
		t.Fatalf("prev from end = %d, want 12", got)
	}
	if got := prevWordBoundary(text, 6); got != 0 {
		t.Fatalf("prev from space = %d, want 0", got)
	}
	if got := nextWordBoundary(text, 0); got != 5 {
		t.Fatalf("next from start = %d, want 5", got)
	}
	if got := nextWordBoundary(text, 6); got != 11 {
		t.Fatalf("next from space = %d, want 11", got)
	}
}

func TestLineBoundaries(t *testing.T) {
	text := []rune("ab\ncd\nef")
	if got := lineStart(text, 4); got != 3 {
		t.Fatalf("lineStart(4) = %d, want 3", got)
	}
	if got := lineEnd(text, 4); got != 5 {
		t.Fatalf("lineEnd(4) = %d, want 5", got)
	}
	if got := lineStart(text, 2); got != 0 {
		t.Fatalf("lineStart(2) = %d, want 0", got)
	}
	if got := lineEnd(text, 7); got != 8 {
		t.Fatalf("lineEnd(7) = %d, want 8", got)
	}
}

func TestComposerDisplayIndexMapsNewlines(t *testing.T) {
	input := []rune("a\nb")
	if got := composerDisplayIndex(input, 0); got != 0 {
		t.Fatalf("index 0 = %d, want 0", got)
	}
	if got := composerDisplayIndex(input, 1); got != 1 {
		t.Fatalf("index 1 = %d, want 1", got)
	}
	if got := composerDisplayIndex(input, 2); got != 4 {
		t.Fatalf("index 2 = %d, want 4", got)
	}
	if got := composerDisplayIndex(input, 3); got != 5 {
		t.Fatalf("index 3 = %d, want 5", got)
	}
}

func TestComposerViewRendersVisibleCursor(t *testing.T) {
	if plain := ansi.Strip(composerView([]rune("hi"), 1, false, true)); plain != "h i" {
		t.Fatalf("composerView stripped = %q, want %q", plain, "h i")
	}
	if empty := ansi.Strip(composerView(nil, 0, false, true)); !strings.Contains(empty, "Ask CodePilot anything") {
		t.Fatalf("empty composerView = %q", empty)
	}
	if busy := ansi.Strip(composerView([]rune("hi"), 1, true, true)); !strings.Contains(busy, "Starting turn") {
		t.Fatalf("busy composerView = %q", busy)
	}
}

func TestComposerViewHidesCursorWhenUnfocused(t *testing.T) {
	block := codePilotStyles.cursor.Render(composerCursorBlock)
	if on := composerView([]rune("hi"), 1, false, true); !strings.Contains(on, block) {
		t.Fatalf("focused composer missing cursor block: %q", on)
	}
	off := composerView([]rune("hi"), 1, false, false)
	if strings.Contains(off, block) {
		t.Fatalf("unfocused composer still shows cursor block: %q", off)
	}
	if plain := ansi.Strip(off); plain != "h i" {
		t.Fatalf("unfocused composer stripped = %q, want %q (cursor cell stays a space)", plain, "h i")
	}
}

func TestModelComposerCursorMovesAndInsertsAtPosition(t *testing.T) {
	model := NewModel(nil, nil, testSnapshot())
	model.Update(tea.KeyPressMsg(tea.Key{Text: "ab"}))
	model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyLeft}))
	model.Update(tea.KeyPressMsg(tea.Key{Text: "x"}))
	if string(model.composer) != "axb" || model.composerCursor != 2 {
		t.Fatalf("composer = %q, cursor = %d", model.composer, model.composerCursor)
	}
}

func TestModelComposerHomeEndJumpLine(t *testing.T) {
	model := NewModel(nil, nil, testSnapshot())
	model.composer = []rune("one\ntwo")
	model.composerCursor = len(model.composer)

	model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyHome}))
	if model.composerCursor != 4 {
		t.Fatalf("Home cursor = %d, want 4", model.composerCursor)
	}
	model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnd}))
	if model.composerCursor != 7 {
		t.Fatalf("End cursor = %d, want 7", model.composerCursor)
	}
}

func TestModelComposerDeleteAndBackspaceRespectCursor(t *testing.T) {
	model := NewModel(nil, nil, testSnapshot())
	model.composer = []rune("abcde")
	model.composerCursor = 2

	model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyBackspace}))
	if string(model.composer) != "acde" || model.composerCursor != 1 {
		t.Fatalf("after backspace composer=%q cursor=%d", model.composer, model.composerCursor)
	}
	model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyDelete}))
	if string(model.composer) != "ade" || model.composerCursor != 1 {
		t.Fatalf("after delete composer=%q cursor=%d", model.composer, model.composerCursor)
	}
}

func TestModelComposerCtrlWordJump(t *testing.T) {
	model := NewModel(nil, nil, testSnapshot())
	model.composer = []rune("hello world")
	model.composerCursor = len(model.composer)

	model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyLeft, Mod: tea.ModCtrl}))
	if model.composerCursor != 6 {
		t.Fatalf("ctrl-left cursor = %d, want 6", model.composerCursor)
	}
	model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyRight, Mod: tea.ModCtrl}))
	if model.composerCursor != 11 {
		t.Fatalf("ctrl-right cursor = %d, want 11", model.composerCursor)
	}
}

func TestModelComposerCtrlKUDeleteToLineEnds(t *testing.T) {
	model := NewModel(nil, nil, testSnapshot())
	model.composer = []rune("hello world")
	model.composerCursor = 5

	model.Update(tea.KeyPressMsg(tea.Key{Code: 'k', Mod: tea.ModCtrl}))
	if string(model.composer) != "hello" {
		t.Fatalf("ctrl-k composer = %q, want %q", model.composer, "hello")
	}
	model.Update(tea.KeyPressMsg(tea.Key{Code: 'u', Mod: tea.ModCtrl}))
	if string(model.composer) != "" {
		t.Fatalf("ctrl-u composer = %q, want empty", model.composer)
	}
}

func TestModelComposerHistoryRecall(t *testing.T) {
	model := NewModel(nil, nil, testSnapshot())
	model.history = []string{"first", "second"}
	model.historyIndex = -1

	model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp}))
	if string(model.composer) != "second" {
		t.Fatalf("first ↑ = %q, want %q", model.composer, "second")
	}
	model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp}))
	if string(model.composer) != "first" {
		t.Fatalf("second ↑ = %q, want %q", model.composer, "first")
	}
	model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
	if string(model.composer) != "second" {
		t.Fatalf("↓ = %q, want %q", model.composer, "second")
	}
	model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
	if string(model.composer) != "" {
		t.Fatalf("↓ past newest = %q, want empty stash", model.composer)
	}
}

func TestModelSubmitRecordsHistoryAndDeduplicates(t *testing.T) {
	model := NewModel(nil, nil, testSnapshot())
	model.composer = []rune("hello")
	model.submitComposer()
	model.composer = []rune("hello")
	model.submitComposer()
	model.composer = []rune("world")
	model.submitComposer()
	if len(model.history) != 2 || model.history[0] != "hello" || model.history[1] != "world" {
		t.Fatalf("history = %#v", model.history)
	}
}
