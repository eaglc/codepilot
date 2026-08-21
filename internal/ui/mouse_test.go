package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/eaglc/codepilot/internal/session"
)

func TestRegionAtMapsHeaderStatusAndOutOfBounds(t *testing.T) {
	layout := CalculateLayout(120, 24)
	// BodyHeight = 24-5 = 19, so body rows are 1..19, status row 20, footer 21..23.
	if _, ok := regionAt(0, 0, layout, FocusConversation); ok {
		t.Fatalf("header row should not be focusable")
	}
	if _, ok := regionAt(10, 20, layout, FocusConversation); ok {
		t.Fatalf("status row should not be focusable")
	}
	if _, ok := regionAt(-1, 5, layout, FocusConversation); ok {
		t.Fatalf("negative x should not be focusable")
	}
	if _, ok := regionAt(10, 24, layout, FocusConversation); ok {
		t.Fatalf("row at/above height should not be focusable")
	}
	if _, ok := regionAt(10, -1, layout, FocusConversation); ok {
		t.Fatalf("negative y should not be focusable")
	}
}

func TestRegionAtWideSplitsConversationAndDiff(t *testing.T) {
	layout := CalculateLayout(120, 24)
	// ConversationWidth = 71, so x < 71 is conversation, else diff.
	if region, ok := regionAt(10, 5, layout, FocusConversation); !ok || region != FocusConversation {
		t.Fatalf("left body = (%v, %v), want conversation", region, ok)
	}
	if region, ok := regionAt(80, 5, layout, FocusConversation); !ok || region != FocusDiff {
		t.Fatalf("right body = (%v, %v), want diff", region, ok)
	}
	if region, ok := regionAt(71, 5, layout, FocusConversation); !ok || region != FocusDiff {
		t.Fatalf("boundary x=71 = (%v, %v), want diff", region, ok)
	}
	if region, ok := regionAt(10, 19, layout, FocusConversation); !ok || region != FocusConversation {
		t.Fatalf("last body row = (%v, %v), want conversation", region, ok)
	}
}

func TestRegionAtNarrowShowsVisiblePanel(t *testing.T) {
	layout := CalculateLayout(72, 24)
	if region, ok := regionAt(50, 5, layout, FocusConversation); !ok || region != FocusConversation {
		t.Fatalf("narrow conversation = (%v, %v)", region, ok)
	}
	if region, ok := regionAt(50, 5, layout, FocusDiff); !ok || region != FocusDiff {
		t.Fatalf("narrow diff = (%v, %v)", region, ok)
	}
	if region, ok := regionAt(50, 5, layout, FocusInput); !ok || region != FocusConversation {
		t.Fatalf("narrow input-focus panel = (%v, %v), want conversation", region, ok)
	}
}

func TestRegionAtFooterFocussesInput(t *testing.T) {
	layout := CalculateLayout(120, 24)
	if region, ok := regionAt(10, 21, layout, FocusConversation); !ok || region != FocusInput {
		t.Fatalf("footer row = (%v, %v), want input", region, ok)
	}
	if region, ok := regionAt(10, 23, layout, FocusConversation); !ok || region != FocusInput {
		t.Fatalf("last footer row = (%v, %v), want input", region, ok)
	}
}

func TestScrollbarThumbGeometry(t *testing.T) {
	if _, _, ok := scrollbarThumb(0, 5, 10); ok {
		t.Fatalf("content that fits should not need a scrollbar")
	}
	if _, _, ok := scrollbarThumb(0, 100, 0); ok {
		t.Fatalf("zero-height track should not need a scrollbar")
	}
	start, length, ok := scrollbarThumb(90, 100, 10)
	if !ok || start != 9 || length != 1 {
		t.Fatalf("bottom thumb = (%d, %d, %v), want (9, 1, true)", start, length, ok)
	}
	start, length, ok = scrollbarThumb(0, 100, 10)
	if !ok || start != 0 || length != 1 {
		t.Fatalf("top thumb = (%d, %d, %v), want (0, 1, true)", start, length, ok)
	}
	start, length, ok = scrollbarThumb(0, 20, 10)
	if !ok || start != 0 || length != 5 {
		t.Fatalf("half track thumb = (%d, %d, %v), want (0, 5, true)", start, length, ok)
	}
	start, length, ok = scrollbarThumb(10, 20, 10)
	if !ok || start != 5 || length != 5 {
		t.Fatalf("bottom half track thumb = (%d, %d, %v), want (5, 5, true)", start, length, ok)
	}
}

func TestModelDefaultFocusIsInput(t *testing.T) {
	model := NewModel(nil, nil, testSnapshot())
	if model.focus != FocusInput {
		t.Fatalf("default focus = %q, want input", model.focus)
	}
}

func TestModelTabCyclesFocusThreeWays(t *testing.T) {
	model := NewModel(nil, nil, testSnapshot())
	if model.focus != FocusInput {
		t.Fatalf("start focus = %q, want input", model.focus)
	}
	model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab}))
	if model.focus != FocusConversation {
		t.Fatalf("after tab1 = %q, want conversation", model.focus)
	}
	model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab}))
	if model.focus != FocusDiff {
		t.Fatalf("after tab2 = %q, want diff", model.focus)
	}
	model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab}))
	if model.focus != FocusInput {
		t.Fatalf("after tab3 = %q, want input", model.focus)
	}
}

func TestModelPanelFocusArrowKeysScrollConversation(t *testing.T) {
	model := NewModel(nil, nil, testSnapshot())
	model.Update(tea.WindowSizeMsg{Width: 60, Height: 12})
	model.snapshot.Messages = []session.Message{{
		ID:        "msg_assistant",
		SessionID: model.snapshot.Session.ID,
		Role:      session.RoleAssistant,
		Content:   strings.Repeat("line\n", 40),
	}}
	model.focus = FocusConversation

	model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp}))
	if model.conversationScroll < 0 {
		t.Fatalf("KeyUp did not scroll conversation: %d", model.conversationScroll)
	}
	model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyHome}))
	if model.conversationScroll != 0 {
		t.Fatalf("KeyHome conversation scroll = %d, want 0", model.conversationScroll)
	}
	model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnd}))
	if model.conversationScroll != scrollFollowBottom {
		t.Fatalf("KeyEnd conversation scroll = %d, want follow-bottom", model.conversationScroll)
	}
}

func TestModelTypingWhilePanelFocusedFocusesInput(t *testing.T) {
	model := NewModel(nil, nil, testSnapshot())
	model.focus = FocusConversation

	model.Update(tea.KeyPressMsg(tea.Key{Text: "x"}))
	if model.focus != FocusInput {
		t.Fatalf("typing while panel focused left focus = %q, want input", model.focus)
	}
	if string(model.composer) != "x" {
		t.Fatalf("composer = %q, want %q", model.composer, "x")
	}
}

func TestModelInputFocusArrowRecallsHistoryNotScroll(t *testing.T) {
	model := NewModel(nil, nil, testSnapshot())
	model.Update(tea.WindowSizeMsg{Width: 60, Height: 12})
	model.snapshot.Messages = []session.Message{{
		ID:        "msg_assistant",
		SessionID: model.snapshot.Session.ID,
		Role:      session.RoleAssistant,
		Content:   strings.Repeat("line\n", 40),
	}}
	model.focus = FocusInput
	model.history = []string{"first"}
	before := model.conversationScroll

	model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp}))
	if model.conversationScroll != before {
		t.Fatalf("↑ while input focused scrolled conversation: %d -> %d", before, model.conversationScroll)
	}
	if string(model.composer) != "first" {
		t.Fatalf("↑ while input focused composer = %q, want %q", model.composer, "first")
	}
}

func TestModelInputFocusPageKeysScrollConversation(t *testing.T) {
	model := NewModel(nil, nil, testSnapshot())
	model.Update(tea.WindowSizeMsg{Width: 60, Height: 12})
	model.snapshot.Messages = []session.Message{{
		ID:        "msg_assistant",
		SessionID: model.snapshot.Session.ID,
		Role:      session.RoleAssistant,
		Content:   strings.Repeat("line\n", 40),
	}}
	model.focus = FocusInput

	model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyPgUp}))
	if model.conversationScroll < 0 {
		t.Fatalf("PgUp did not scroll conversation while input focused: %d", model.conversationScroll)
	}
}

func TestModelMouseWheelScrollsActivePanel(t *testing.T) {
	model := NewModel(nil, nil, testSnapshot())
	model.Update(tea.WindowSizeMsg{Width: 60, Height: 12})
	model.snapshot.Messages = []session.Message{{
		ID:        "msg_assistant",
		SessionID: model.snapshot.Session.ID,
		Role:      session.RoleAssistant,
		Content:   strings.Repeat("line\n", 40),
	}}
	model.focus = FocusConversation

	model.Update(tea.MouseWheelMsg(tea.Mouse{X: 10, Y: 5, Button: tea.MouseWheelUp}))
	if model.conversationScroll < 0 {
		t.Fatalf("wheel up did not scroll conversation: %d", model.conversationScroll)
	}
	if !model.scrollbarVisible {
		t.Fatalf("wheel did not show scrollbar")
	}
}

func TestModelMouseWheelScrollsDiffWhenFocused(t *testing.T) {
	model := NewModel(nil, nil, testSnapshot())
	model.Update(tea.WindowSizeMsg{Width: 60, Height: 12})
	model.diff = session.DiffResult{Kind: session.DiffSession, Text: strings.Repeat("+line\n", 40)}
	model.focus = FocusDiff

	model.Update(tea.MouseWheelMsg(tea.Mouse{X: 10, Y: 5, Button: tea.MouseWheelUp}))
	if model.diffScroll < 0 {
		t.Fatalf("wheel up did not scroll diff: %d", model.diffScroll)
	}
}

func TestModelMouseClickSetsFocus(t *testing.T) {
	model := NewModel(nil, nil, testSnapshot())
	model.Update(tea.WindowSizeMsg{Width: 120, Height: 24})

	// Right half body -> diff.
	model.Update(tea.MouseClickMsg(tea.Mouse{X: 80, Y: 5, Button: tea.MouseLeft}))
	if model.focus != FocusDiff {
		t.Fatalf("right-body click focus = %q, want diff", model.focus)
	}
	// Left half body -> conversation.
	model.Update(tea.MouseClickMsg(tea.Mouse{X: 10, Y: 5, Button: tea.MouseLeft}))
	if model.focus != FocusConversation {
		t.Fatalf("left-body click focus = %q, want conversation", model.focus)
	}
	// Footer -> input.
	model.Update(tea.MouseClickMsg(tea.Mouse{X: 10, Y: 22, Button: tea.MouseLeft}))
	if model.focus != FocusInput {
		t.Fatalf("footer click focus = %q, want input", model.focus)
	}
}

func TestModelScrollbarHidesAfterTick(t *testing.T) {
	model := NewModel(nil, nil, testSnapshot())
	model.scrollbarVisible = true
	model.Update(scrollbarHideMsg{})
	if model.scrollbarVisible {
		t.Fatalf("scrollbar still visible after hide tick")
	}
}

func TestModelFocusTogglesCursorBlink(t *testing.T) {
	model := NewModel(nil, nil, testSnapshot())
	if !model.cursorOn {
		t.Fatalf("default cursor should be on")
	}
	model.setFocus(FocusConversation)
	if model.cursorOn {
		t.Fatalf("cursor should be off when a panel is focused")
	}
	model.setFocus(FocusInput)
	if !model.cursorOn {
		t.Fatalf("cursor should be on when input is focused")
	}
}

func TestModelBlinkTickTogglesCursor(t *testing.T) {
	model := NewModel(nil, nil, testSnapshot())
	model.cursorOn = true
	model.Update(blinkTickMsg{})
	if model.cursorOn {
		t.Fatalf("blink tick did not turn cursor off")
	}
	model.Update(blinkTickMsg{})
	if !model.cursorOn {
		t.Fatalf("blink tick did not turn cursor back on")
	}
}

func TestRenderPanelShowsGreenBarWhenActive(t *testing.T) {
	active := strings.Join(renderPanel("Conversation", []string{"a", "b", "c"}, 20, 6, false, true, false, 0, styleConversationLine), "\n")
	if !strings.Contains(active, codePilotStyles.focusBar.Render("│")) {
		t.Fatalf("active panel missing green bar:\n%s", active)
	}
	inactive := strings.Join(renderPanel("Conversation", []string{"a"}, 20, 4, false, false, false, 0, styleConversationLine), "\n")
	if strings.Contains(inactive, codePilotStyles.focusBar.Render("│")) {
		t.Fatalf("inactive panel should not show green bar:\n%s", inactive)
	}
}

func TestRenderPanelShowsScrollbarWhenRequested(t *testing.T) {
	content := make([]string, 20)
	for index := range content {
		content[index] = "line"
	}
	shown := strings.Join(renderPanel("Conversation", content, 20, 10, true, true, true, 5, styleConversationLine), "\n")
	if !strings.Contains(shown, codePilotStyles.scrollbarThumb.Render("█")) {
		t.Fatalf("scrollbar thumb missing:\n%s", shown)
	}
	hidden := strings.Join(renderPanel("Conversation", content, 20, 10, true, true, false, 5, styleConversationLine), "\n")
	if strings.Contains(hidden, codePilotStyles.scrollbarThumb.Render("█")) {
		t.Fatalf("scrollbar thumb shown when hidden:\n%s", hidden)
	}
}
