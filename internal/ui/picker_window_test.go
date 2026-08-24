package ui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/eaglc/codepilot/internal/codingagent"
)

func TestPickerWindowKeepsCursorInBoundedRange(t *testing.T) {
	tests := []struct {
		name                string
		cursor, count, size int
		start, end          int
	}{
		{name: "empty", cursor: 0, count: 0, size: 8, start: 0, end: 0},
		{name: "small", cursor: 2, count: 5, size: 8, start: 0, end: 5},
		{name: "top", cursor: 0, count: 20, size: 8, start: 0, end: 8},
		{name: "middle", cursor: 10, count: 20, size: 8, start: 6, end: 14},
		{name: "bottom", cursor: 19, count: 20, size: 8, start: 12, end: 20},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			start, end := pickerWindow(test.cursor, test.count, test.size)
			if start != test.start || end != test.end {
				t.Fatalf("pickerWindow() = %d,%d, want %d,%d", start, end, test.start, test.end)
			}
		})
	}
}

func TestSessionAndWorkspaceViewsKeepSelectedItemVisible(t *testing.T) {
	model := &Model{sessionID: "session-19"}
	model.sessionPicker = sessionPicker{active: true, cursor: 19, sessions: make([]codingagent.Session, 20)}
	model.workspacePicker = workspacePicker{active: true, cursor: 19, items: make([]workspacePickerItem, 20)}
	for index := 0; index < 20; index++ {
		model.sessionPicker.sessions[index] = codingagent.Session{ID: codingagent.SessionID(fmt.Sprintf("session-%02d", index)), Title: fmt.Sprintf("Session %02d", index)}
		model.workspacePicker.items[index] = workspacePickerItem{
			workspace: codingagent.WorkspaceSummary{ID: codingagent.WorkspaceID(fmt.Sprintf("workspace-%02d", index)), DisplayName: fmt.Sprintf("Workspace %02d", index)},
			worktree:  codingagent.WorktreeSummary{ID: codingagent.WorktreeID(fmt.Sprintf("worktree-%02d", index)), Root: fmt.Sprintf("/worktree/%02d", index), Availability: codingagent.WorktreeAvailable},
		}
	}
	if view := model.sessionView(100, 16).Content; !strings.Contains(view, "Session 19") || !strings.Contains(view, "earlier sessions") {
		t.Fatalf("selected session is not windowed into view: %q", view)
	}
	if view := model.workspaceView(100, 16).Content; !strings.Contains(view, "Workspace 19") || !strings.Contains(view, "earlier worktrees") {
		t.Fatalf("selected worktree is not windowed into view: %q", view)
	}
}
