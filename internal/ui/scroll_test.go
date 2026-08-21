package ui

import (
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/eaglc/codepilot/internal/session"
)

func TestPrefixedLinesWrapsLongContent(t *testing.T) {
	// Content width = 8 - len("You: ")=5 => 3 cells, so "abcdefghij" wraps into
	// four display lines with continuation lines indented by the prefix width.
	lines := prefixedLines("You: ", "abcdefghij", 8)
	want := []string{"You: abc", "     def", "     ghi", "     j"}
	if !reflect.DeepEqual(lines, want) {
		t.Fatalf("prefixedLines wrapped to %q, want %q", lines, want)
	}
}

func TestPrefixedLinesKeepsExplicitNewlinesIndented(t *testing.T) {
	lines := prefixedLines("Assistant: ", "first\nsecond", 80)
	want := []string{"Assistant: first", "           second"}
	if !reflect.DeepEqual(lines, want) {
		t.Fatalf("prefixedLines multi-line = %q, want %q", lines, want)
	}
}

func TestConversationViewWrapsMessagesWithinWidth(t *testing.T) {
	snapshot := testSnapshot()
	snapshot.Messages = []session.Message{{
		ID:        "msg_user",
		SessionID: snapshot.Session.ID,
		Role:      session.RoleUser,
		Content:   "This is a very long user message that must wrap across several display lines.",
	}}
	const width = 20
	lines := conversationView(snapshot, "", width)
	if len(lines) < 2 {
		t.Fatalf("long message did not wrap: %q", lines)
	}
	for _, line := range lines {
		if got := ansi.StringWidth(line); got > width {
			t.Fatalf("rendered line width = %d, want <= %d: %q", got, width, line)
		}
	}
}

func TestResolveScrollFollowsBottomAndClamps(t *testing.T) {
	if got := resolveScroll(scrollFollowBottom, 100, 10); got != 90 {
		t.Fatalf("follow-bottom offset = %d, want 90", got)
	}
	if got := resolveScroll(5, 100, 10); got != 5 {
		t.Fatalf("in-range offset = %d, want 5", got)
	}
	if got := resolveScroll(999, 100, 10); got != 90 {
		t.Fatalf("over-range offset = %d, want 90", got)
	}
	if got := resolveScroll(-3, 100, 10); got != 0 {
		t.Fatalf("under-range offset = %d, want 0", got)
	}
	if got := resolveScroll(scrollFollowBottom, 5, 10); got != 0 {
		t.Fatalf("content-fits offset = %d, want 0", got)
	}
}

func TestWindowLinesRespectsOffsetAndHeight(t *testing.T) {
	lines := []string{"a", "b", "c", "d", "e"}
	if got := windowLines(lines, 1, 2); !reflect.DeepEqual(got, []string{"b", "c"}) {
		t.Fatalf("window = %q, want [b c]", got)
	}
	if got := windowLines(lines, 10, 2); got != nil {
		t.Fatalf("offset beyond end returned %q, want nil", got)
	}
	if got := windowLines(lines, 0, 0); got != nil {
		t.Fatalf("zero height returned %q, want nil", got)
	}
}

func TestModelScrollKeysAdjustFocusedPanelWhileStreaming(t *testing.T) {
	model := NewModel(nil, nil, testSnapshot())
	model.Update(tea.WindowSizeMsg{Width: 60, Height: 12})
	model.snapshot.Messages = []session.Message{{
		ID:        "msg_assistant",
		SessionID: model.snapshot.Session.ID,
		Role:      session.RoleAssistant,
		Content:   strings.Repeat("line\n", 40),
	}}
	model.inputBusy = true // scrolling must still work while the assistant streams

	model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp}))
	if model.conversationScroll < 0 {
		t.Fatalf("KeyUp did not move conversation scroll: %d", model.conversationScroll)
	}
	model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyHome}))
	if model.conversationScroll != 0 {
		t.Fatalf("KeyHome conversation scroll = %d, want 0", model.conversationScroll)
	}
	model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnd}))
	if model.conversationScroll != scrollFollowBottom {
		t.Fatalf("KeyEnd conversation scroll = %d, want %d", model.conversationScroll, scrollFollowBottom)
	}
}

func TestModelDiffScrollKeysAdjustFocusedDiff(t *testing.T) {
	model := NewModel(nil, nil, testSnapshot())
	model.Update(tea.WindowSizeMsg{Width: 60, Height: 12})
	model.diff = session.DiffResult{Kind: session.DiffSession, Text: strings.Repeat("+changed line\n", 40)}
	model.focus = FocusDiff
	model.inputBusy = true // ↑/↓ scroll the panel only while the composer is disabled

	model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp}))
	if model.diffScroll < 0 {
		t.Fatalf("KeyUp did not move diff scroll: %d", model.diffScroll)
	}
	model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyHome}))
	if model.diffScroll != 0 {
		t.Fatalf("KeyHome diff scroll = %d, want 0", model.diffScroll)
	}
}

func TestModelScrollResetsOnSessionSwitch(t *testing.T) {
	model := NewModel(nil, nil, testSnapshot())
	model.Update(tea.WindowSizeMsg{Width: 60, Height: 12})
	model.snapshot.Messages = []session.Message{{
		ID:        "msg_assistant",
		SessionID: model.snapshot.Session.ID,
		Role:      session.RoleAssistant,
		Content:   strings.Repeat("line\n", 40),
	}}
	model.inputBusy = true
	model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp}))
	if model.conversationScroll < 0 {
		t.Fatalf("setup did not scroll conversation: %d", model.conversationScroll)
	}

	next := testSnapshot()
	next.Session.ID = "ses_other"
	model.Update(sessionLoadedMsg{snapshot: next})
	if model.conversationScroll != scrollFollowBottom || model.diffScroll != scrollFollowBottom {
		t.Fatalf("session switch did not reset scroll: conversation=%d diff=%d", model.conversationScroll, model.diffScroll)
	}
}
