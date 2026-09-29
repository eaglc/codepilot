package ui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/eaglc/codepilot/internal/codingagent"
)

func newComposerTestModel(t *testing.T, options ...Option) *Model {
	t.Helper()
	bridge, err := NewEventBridge(2)
	if err != nil {
		t.Fatalf("create bridge: %v", err)
	}
	t.Cleanup(func() {
		_ = bridge.Close()
	})
	snapshot := codingagent.Snapshot{Session: codingagent.Session{ID: "session-a", Title: "repo"}}
	model, err := NewModel(context.Background(), fakeClient{snapshot: snapshot}, bridge, snapshot, options...)
	if err != nil {
		t.Fatalf("create model: %v", err)
	}
	return model
}

func TestLayoutComposerUsesGraphemeCellsAndSoftWrap(t *testing.T) {
	input := []rune("界a\u0301🙂\n尾")
	document := layoutComposer(input, 3, 4)
	wantLines := []string{"界á", "🙂", "尾"}
	if strings.Join(document.lines, "|") != strings.Join(wantLines, "|") {
		t.Fatalf("lines = %#v want %#v", document.lines, wantLines)
	}
	if document.cursor.line != 1 || document.cursor.column != 0 {
		t.Fatalf("cursor = %#v want line 1 column 0", document.cursor)
	}
	if got := nearestGraphemeBoundary(input, 2); got != 1 {
		t.Fatalf("combining-mark boundary = %d want 1", got)
	}
}

func TestComposerDeletesWholeEmojiGrapheme(t *testing.T) {
	model := newComposerTestModel(t)
	model.replaceInput("a👨‍👩‍👧‍👦")
	model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyBackspace}))
	if got := string(model.input); got != "a" || model.cursor != 1 {
		t.Fatalf("input = %q cursor = %d", got, model.cursor)
	}
}

func TestComposerDefaultsToAltEnterNewlineAndEnterSubmit(t *testing.T) {
	model := newComposerTestModel(t)
	model.Update(tea.KeyPressMsg(tea.Key{Text: "first"}))
	if _, command := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter, Mod: tea.ModAlt})); command != nil {
		t.Fatal("Alt+Enter submitted instead of inserting a newline")
	}
	model.Update(tea.KeyPressMsg(tea.Key{Text: "second"}))
	if got := string(model.input); got != "first\nsecond" {
		t.Fatalf("input = %q", got)
	}
	if _, command := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})); command == nil {
		t.Fatal("Enter did not submit")
	}
	if len(model.input) != 0 || model.history[len(model.history)-1] != "first\nsecond" {
		t.Fatalf("input = %q history = %#v", string(model.input), model.history)
	}
}

func TestComposerKeymapCanBeOverridden(t *testing.T) {
	keymap := ComposerKeymap{
		Submit:  []ComposerKeyBinding{{Code: tea.KeyEnter, Mod: tea.ModCtrl}},
		Newline: []ComposerKeyBinding{{Code: tea.KeyEnter}},
	}
	model := newComposerTestModel(t, WithComposerKeymap(keymap))
	model.Update(tea.KeyPressMsg(tea.Key{Text: "line"}))
	if _, command := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})); command != nil || string(model.input) != "line\n" {
		t.Fatalf("custom newline input = %q command = %#v", string(model.input), command)
	}
	if _, command := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter, Mod: tea.ModCtrl})); command == nil {
		t.Fatal("custom submit binding did not submit")
	}
}

func TestComposerRejectsAmbiguousKeymap(t *testing.T) {
	bridge, _ := NewEventBridge(2)
	defer bridge.Close()
	snapshot := codingagent.Snapshot{Session: codingagent.Session{ID: "session", Title: "repo"}}
	binding := ComposerKeyBinding{Code: tea.KeyEnter}
	_, err := NewModel(context.Background(), fakeClient{snapshot: snapshot}, bridge, snapshot, WithComposerKeymap(ComposerKeymap{
		Submit: []ComposerKeyBinding{binding}, Newline: []ComposerKeyBinding{binding},
	}))
	if err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Fatalf("error = %v", err)
	}
}

func TestComposerVerticalMovementPrecedesHistoryNavigation(t *testing.T) {
	model := newComposerTestModel(t)
	model.history = []string{"sent prompt"}
	model.replaceInput("first\nsecond")
	model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp}))
	if string(model.input) != "first\nsecond" || model.historyIndex != -1 || model.cursor != len([]rune("first")) {
		t.Fatalf("first up input = %q cursor = %d history = %d", string(model.input), model.cursor, model.historyIndex)
	}
	model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp}))
	if string(model.input) != "sent prompt" || model.historyIndex != 0 {
		t.Fatalf("second up input = %q history = %d", string(model.input), model.historyIndex)
	}
	model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
	if string(model.input) != "first\nsecond" || model.historyIndex != -1 || model.cursor != len([]rune("first")) {
		t.Fatalf("restored draft input = %q cursor = %d history = %d", string(model.input), model.cursor, model.historyIndex)
	}
}

func TestComposerDraftsAreIsolatedPerSession(t *testing.T) {
	model := newComposerTestModel(t)
	model.replaceInput("draft A")
	model.cursor = 3
	model.syncDraft()
	model.activateSnapshot(codingagent.Snapshot{Session: codingagent.Session{ID: "session-b", Title: "B"}})
	if len(model.input) != 0 {
		t.Fatalf("session B inherited draft %q", string(model.input))
	}
	model.replaceInput("draft B")
	model.activateSnapshot(codingagent.Snapshot{Session: codingagent.Session{ID: "session-a", Title: "A"}})
	if string(model.input) != "draft A" || model.cursor != 3 {
		t.Fatalf("session A draft = %q cursor = %d", string(model.input), model.cursor)
	}
	model.activateSnapshot(codingagent.Snapshot{Session: codingagent.Session{ID: "session-b", Title: "B"}})
	if string(model.input) != "draft B" {
		t.Fatalf("session B draft = %q", string(model.input))
	}
}

func TestComposerPasteNormalizesAndCapsInput(t *testing.T) {
	model := newComposerTestModel(t)
	model.Update(tea.PasteMsg{Content: "a\r\n界\r🙂\x00"})
	if got := string(model.input); got != "a\n界\n🙂" {
		t.Fatalf("normalized paste = %q", got)
	}
	model.clearInput()
	model.Update(tea.PasteMsg{Content: strings.Repeat("x", maxInputRunes+100)})
	if len(model.input) != maxInputRunes || model.cursor != maxInputRunes {
		t.Fatalf("large paste length = %d cursor = %d", len(model.input), model.cursor)
	}
}

func TestComposerAdaptsBetweenTwoAndEightRows(t *testing.T) {
	model := newComposerTestModel(t)
	if got := len(model.renderComposer(40, maxComposerHeight).lines); got != minComposerHeight {
		t.Fatalf("empty height = %d", got)
	}
	model.replaceInput(strings.Repeat("line\n", 12))
	composer := model.renderComposer(40, maxComposerHeight)
	if len(composer.lines) != maxComposerHeight || composer.cursorY != maxComposerHeight-1 {
		t.Fatalf("expanded Composer = %#v", composer)
	}
	model.width, model.height = 20, 6
	view := model.View()
	lines := strings.Split(view.Content, "\n")
	if len(lines) != model.height || !strings.Contains(ansi.Strip(view.Content), "Ready") || !strings.Contains(ansi.Strip(view.Content), "line") {
		t.Fatalf("narrow view lines = %d content = %q", len(lines), ansi.Strip(view.Content))
	}
	if strings.Contains(ansi.Strip(view.Content), "Current context") || strings.Contains(ansi.Strip(view.Content), "──") {
		t.Fatalf("narrow view did not prioritize Composer and status: %q", ansi.Strip(view.Content))
	}
	input, cursor := string(model.input), model.cursor
	model.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	resized := model.View()
	if string(model.input) != input || model.cursor != cursor || resized.Cursor == nil || resized.Cursor.X < 0 || resized.Cursor.X >= model.width {
		t.Fatalf("resize changed input=%q cursor=%d native=%#v", string(model.input), model.cursor, resized.Cursor)
	}
}

func TestComposerDraftCanBeExplicitlyDiscarded(t *testing.T) {
	model := newComposerTestModel(t)
	model.replaceInput("discard me")
	if _, ok := model.drafts[model.sessionID]; !ok {
		t.Fatal("draft was not saved")
	}
	model.clearInput()
	if len(model.input) != 0 || len(model.drafts) != 0 {
		t.Fatalf("input = %q drafts = %#v", string(model.input), model.drafts)
	}
}
