package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/eaglc/codepilot/internal/session"
)

// SessionPickerStage describes the asynchronous session selection flow.
type SessionPickerStage string

const (
	// SessionPickerClosed indicates that normal composer input owns the keyboard.
	SessionPickerClosed SessionPickerStage = "closed"
	// SessionPickerLoading indicates that session summaries are being loaded.
	SessionPickerLoading SessionPickerStage = "loading"
	// SessionPickerChoosing indicates that the user can select a session.
	SessionPickerChoosing SessionPickerStage = "choosing"
	// SessionPickerConfirming indicates that a cross-worktree switch needs an
	// explicit confirmation before application state is rebound.
	SessionPickerConfirming SessionPickerStage = "confirming"
	// SessionPickerSwitching indicates that the selected session is activating.
	SessionPickerSwitching SessionPickerStage = "switching"
	// SessionPickerFailed indicates a safe, retryable picker error.
	SessionPickerFailed SessionPickerStage = "failed"
)

// SessionPicker coordinates listing and switching sessions without exposing
// persistence adapters to the UI.
type SessionPicker struct {
	controller       SessionController
	stage            SessionPickerStage
	filter           session.SessionFilter
	sessions         []session.SessionSummary
	cursor           int
	message          string
	generation       uint64
	activeWorktreeID session.WorktreeID
	pendingSessionID session.SessionID
}

// NewSessionPicker creates a closed picker bound to the application service.
func NewSessionPicker(controller SessionController) *SessionPicker {
	return &SessionPicker{controller: controller, stage: SessionPickerClosed}
}

// Open starts a fresh session listing operation.
func (p *SessionPicker) Open(ctx context.Context, filter session.SessionFilter) tea.Cmd {
	return p.OpenForWorktree(ctx, filter, "")
}

// OpenForWorktree starts listing and records the worktree used to identify a
// cross-worktree selection that needs confirmation.
func (p *SessionPicker) OpenForWorktree(ctx context.Context, filter session.SessionFilter, activeWorktreeID session.WorktreeID) tea.Cmd {
	if ctx == nil {
		ctx = context.Background()
	}
	p.generation++
	p.stage = SessionPickerLoading
	p.filter = filter
	p.sessions = nil
	p.cursor = 0
	p.message = ""
	p.activeWorktreeID = activeWorktreeID
	p.pendingSessionID = ""
	generation := p.generation
	return func() tea.Msg {
		if p.controller == nil {
			return sessionsLoadedMsg{generation: generation, message: "Session selection is unavailable."}
		}
		values, err := p.controller.ListSessions(ctx, filter)
		return sessionsLoadedMsg{generation: generation, sessions: values, message: SafeErrorMessage(err, "Sessions could not be loaded.")}
	}
}

// Cancel invalidates in-flight results and returns keyboard ownership to the composer.
func (p *SessionPicker) Cancel() {
	if p == nil {
		return
	}
	p.generation++
	p.stage = SessionPickerClosed
	p.sessions = nil
	p.cursor = 0
	p.message = ""
	p.activeWorktreeID = ""
	p.pendingSessionID = ""
}

// Update applies asynchronous listing and switching results.
func (p *SessionPicker) Update(message tea.Msg) tea.Cmd {
	if p == nil {
		return nil
	}
	switch value := message.(type) {
	case sessionsLoadedMsg:
		if value.generation != p.generation || p.stage == SessionPickerClosed {
			return nil
		}
		if value.message != "" {
			p.stage = SessionPickerFailed
			p.message = value.message
			return nil
		}
		p.sessions = append([]session.SessionSummary(nil), value.sessions...)
		p.cursor = 0
		p.stage = SessionPickerChoosing
	case sessionSwitchedMsg:
		if value.generation != p.generation || p.stage == SessionPickerClosed {
			return nil
		}
		if value.message != "" {
			p.stage = SessionPickerFailed
			p.message = value.message
			return nil
		}
		p.Cancel()
	}
	return nil
}

// HandleKey navigates the picker and starts an asynchronous switch.
func (p *SessionPicker) HandleKey(message tea.KeyPressMsg) tea.Cmd {
	if p == nil || p.stage == SessionPickerClosed {
		return nil
	}
	key := message.Key()
	if p.stage == SessionPickerConfirming {
		if key.Code == tea.KeyEscape || key.Code == tea.KeyEsc || strings.EqualFold(key.Text, "n") {
			p.stage = SessionPickerChoosing
			p.pendingSessionID = ""
			return nil
		}
		if strings.EqualFold(key.Text, "y") {
			return p.switchSession(p.pendingSessionID)
		}
		return nil
	}
	if key.Code == tea.KeyEscape || key.Code == tea.KeyEsc {
		p.Cancel()
		return nil
	}
	switch p.stage {
	case SessionPickerChoosing:
		if movePickerCursor(&p.cursor, message, len(p.sessions)) {
			return nil
		}
		if isEnterKey(key.Code) && len(p.sessions) > 0 {
			selected := p.sessions[p.cursor]
			if p.activeWorktreeID != "" && selected.WorktreeID != "" && selected.WorktreeID != p.activeWorktreeID {
				p.pendingSessionID = selected.ID
				p.stage = SessionPickerConfirming
				return nil
			}
			return p.switchSession(selected.ID)
		}
	case SessionPickerFailed:
		if isEnterKey(key.Code) {
			return p.OpenForWorktree(context.Background(), p.filter, p.activeWorktreeID)
		}
	}
	return nil
}

// View returns a compact accessible session selection overlay.
func (p *SessionPicker) View(activeID session.SessionID) string {
	if p == nil || p.stage == SessionPickerClosed {
		return ""
	}
	switch p.stage {
	case SessionPickerLoading:
		return "Sessions\nLoading sessions..."
	case SessionPickerSwitching:
		return "Sessions\nSwitching session..."
	case SessionPickerConfirming:
		return fmt.Sprintf("Switch to another worktree?\nSession: %s\nWorktree: %s", p.pendingSessionID, p.pendingWorktreeID())
	case SessionPickerFailed:
		return "Session selection failed\n" + p.message
	case SessionPickerChoosing:
		lines := []string{"Select session"}
		start, end := pickerWindow(p.cursor, len(p.sessions), maxVisiblePickerItems)
		if start > 0 {
			lines = append(lines, "  …")
		}
		for index := start; index < end; index++ {
			value := p.sessions[index]
			title := strings.TrimSpace(value.Title)
			if title == "" {
				title = string(value.ID)
			}
			age := ""
			if !value.UpdatedAt.IsZero() {
				age = " · " + value.UpdatedAt.Local().Format(time.DateTime)
			}
			active := ""
			if value.ID == activeID {
				active = " (active)"
			}
			lines = append(lines, pickerLine(index == p.cursor, fmt.Sprintf("%s%s · %s · %s%s", title, active, value.WorktreeID, value.PermissionMode, age)))
		}
		if end < len(p.sessions) {
			lines = append(lines, "  …")
		}
		if len(p.sessions) == 0 {
			lines = append(lines, "No matching sessions.")
		}
		return strings.Join(lines, "\n")
	default:
		return ""
	}
}

func (p *SessionPicker) switchSession(id session.SessionID) tea.Cmd {
	p.stage = SessionPickerSwitching
	generation := p.generation
	return func() tea.Msg {
		if p.controller == nil {
			return sessionSwitchedMsg{generation: generation, message: "Session selection is unavailable."}
		}
		err := p.controller.SwitchSession(context.Background(), id)
		return sessionSwitchedMsg{generation: generation, message: SafeErrorMessage(err, "The selected session could not be activated.")}
	}
}

func (p *SessionPicker) pendingWorktreeID() session.WorktreeID {
	for _, value := range p.sessions {
		if value.ID == p.pendingSessionID {
			return value.WorktreeID
		}
	}
	return ""
}

// Stage returns the current picker stage.
func (p *SessionPicker) Stage() SessionPickerStage {
	if p == nil {
		return SessionPickerClosed
	}
	return p.stage
}

// Sessions returns a defensive copy of the visible summaries.
func (p *SessionPicker) Sessions() []session.SessionSummary {
	if p == nil {
		return nil
	}
	return append([]session.SessionSummary(nil), p.sessions...)
}

type sessionsLoadedMsg struct {
	generation uint64
	sessions   []session.SessionSummary
	message    string
}

type sessionSwitchedMsg struct {
	generation uint64
	message    string
}
