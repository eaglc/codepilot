package ui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/eaglc/codepilot/internal/session"
)

func TestCalculateLayout_WideUsesSixtyFortySplit(t *testing.T) {
	layout := CalculateLayout(121, 30)
	if layout.Mode != LayoutWide {
		t.Fatalf("layout mode = %q, want wide", layout.Mode)
	}
	if layout.ConversationWidth+layout.DiffWidth+1 != layout.Width {
		t.Fatalf("panel widths = %d + %d, terminal width = %d", layout.ConversationWidth, layout.DiffWidth, layout.Width)
	}
	if layout.ConversationWidth != 72 || layout.DiffWidth != 48 {
		t.Fatalf("panel widths = %d/%d, want 72/48", layout.ConversationWidth, layout.DiffWidth)
	}
}

func TestModel_ViewNarrowTerminalSwitchesSingleVisiblePanel(t *testing.T) {
	snapshot := testSnapshot()
	model := NewModel(nil, nil, snapshot)
	model.diff = session.DiffResult{Kind: session.DiffSession, Text: "diff --git a/main.go b/main.go", Drifted: true}
	model.Update(tea.WindowSizeMsg{Width: 72, Height: 10})

	conversation := model.View().Content
	if !strings.Contains(conversation, "Conversation") || strings.Contains(conversation, "Diff (session)") {
		t.Fatalf("initial narrow view did not show only conversation:\n%s", conversation)
	}
	// Tab cycles input → conversation → diff, so two tabs reach the diff panel.
	model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab}))
	model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab}))
	diff := model.View().Content
	if !strings.Contains(diff, "Diff (session)") || strings.Contains(diff, "Conversation") {
		t.Fatalf("tabbed narrow view did not show only diff:\n%s", diff)
	}
	if !strings.Contains(diff, "DRIFTED") {
		t.Fatalf("diff drift warning is missing:\n%s", diff)
	}
	for _, line := range strings.Split(diff, "\n") {
		if width := ansi.StringWidth(line); width > 72 {
			t.Fatalf("rendered line width = %d, want <= 72: %q", width, line)
		}
	}
}

func TestModel_ViewSurfacesDirtyAndRecoveryState(t *testing.T) {
	snapshot := testSnapshot()
	snapshot.WorktreeState.Dirty = true
	snapshot.RecoveryWarnings = []session.RecoveryWarning{{
		Code:        session.RecoveryTruncatedLog,
		Stream:      "messages",
		UserMessage: "An incomplete final messages record was ignored.",
	}}
	model := NewModel(nil, nil, snapshot)
	model.Update(tea.WindowSizeMsg{Width: 120, Height: 8})

	view := model.View().Content
	if !strings.Contains(view, "DIRTY") {
		t.Fatalf("dirty marker is missing:\n%s", view)
	}
	if !strings.Contains(view, "Recovery warning: An incomplete final messages record was ignored.") {
		t.Fatalf("recovery warning is missing:\n%s", view)
	}
}

func TestModel_RejectsEventsForInactiveSessionOrTurn(t *testing.T) {
	model := NewModel(nil, nil, testSnapshot())
	model.activeTurn = "turn_active"
	model.snapshot.RuntimeState = session.RuntimeRunning

	model.applyEvent(session.Event{
		SessionID: "ses_other",
		TurnID:    "turn_active",
		Kind:      session.EventAssistantDelta,
		Payload:   session.EventPayload{Text: &session.TextEventPayload{Text: "secret session"}},
	})
	model.applyEvent(session.Event{
		SessionID: model.snapshot.Session.ID,
		TurnID:    "turn_old",
		Kind:      session.EventAssistantDelta,
		Payload:   session.EventPayload{Text: &session.TextEventPayload{Text: "stale turn"}},
	})
	model.applyEvent(session.Event{
		SessionID: model.snapshot.Session.ID,
		TurnID:    "turn_active",
		Kind:      session.EventAssistantDelta,
		Payload:   session.EventPayload{Text: &session.TextEventPayload{Text: "current"}},
	})

	if model.assistant != "current" || model.staleEvents != 2 {
		t.Fatalf("assistant = %q, stale events = %d", model.assistant, model.staleEvents)
	}
}

func TestSafeErrorMessage_DoesNotExposeDiagnosticCause(t *testing.T) {
	err := &session.AppError{
		Code:      session.ErrProviderUnavailable,
		Operation: "provider.validate",
		Cause:     errors.New("api key sk-live-secret was rejected"),
	}
	message := SafeErrorMessage(err, "fallback")
	if message != "The selected provider or model is unavailable." {
		t.Fatalf("safe message = %q", message)
	}
	if strings.Contains(message, "sk-live-secret") || strings.Contains(message, "provider.validate") {
		t.Fatalf("safe message exposed diagnostics: %q", message)
	}

	err.UserMessage = "Check the selected profile."
	if message := SafeErrorMessage(err, "fallback"); message != err.UserMessage {
		t.Fatalf("app user message = %q, want %q", message, err.UserMessage)
	}
}

func testSnapshot() session.SessionSnapshot {
	return session.SessionSnapshot{
		Session: session.Session{
			ID:             "ses_active",
			Title:          "Fix tests",
			ModelID:        "test-model",
			PermissionMode: session.PermissionAsk,
		},
		RuntimeState: session.RuntimeIdle,
		Messages: []session.Message{{
			ID:        "msg_user",
			SessionID: "ses_active",
			Role:      session.RoleUser,
			Content:   "Please fix the tests.",
		}},
		WorktreeState: session.WorktreeState{
			Root:      `H:\workspace\repo`,
			Branch:    "main",
			Available: true,
		},
	}
}
