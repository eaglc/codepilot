package ui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/eaglc/codepilot/internal/session"
)

func TestPickerWindowClampsAroundCursor(t *testing.T) {
	if start, end := pickerWindow(0, 5, 8); start != 0 || end != 5 {
		t.Fatalf("small list = %d,%d, want 0,5", start, end)
	}
	if start, end := pickerWindow(0, 20, 8); start != 0 || end != 8 {
		t.Fatalf("top = %d,%d, want 0,8", start, end)
	}
	if start, end := pickerWindow(19, 20, 8); start != 12 || end != 20 {
		t.Fatalf("bottom = %d,%d, want 12,20", start, end)
	}
	if start, end := pickerWindow(10, 20, 8); start != 6 || end != 14 {
		t.Fatalf("center = %d,%d, want 6,14", start, end)
	}
	if start, end := pickerWindow(5, 20, 0); start != 0 || end != 0 {
		t.Fatalf("zero visible = %d,%d, want 0,0", start, end)
	}
}

func TestSessionPickerKeepsCursorVisible(t *testing.T) {
	picker := NewSessionPicker(nil)
	picker.stage = SessionPickerChoosing
	picker.sessions = make([]session.SessionSummary, 20)
	for index := range picker.sessions {
		picker.sessions[index] = session.SessionSummary{
			ID:    session.SessionID(fmt.Sprintf("ses-%02d", index)),
			Title: fmt.Sprintf("S%02d", index),
		}
	}
	for cursor := 0; cursor < len(picker.sessions); cursor++ {
		picker.cursor = cursor
		view := picker.View("")
		if marker := fmt.Sprintf("> S%02d", cursor); !strings.Contains(view, marker) {
			t.Fatalf("session cursor %d not visible:\n%s", cursor, view)
		}
	}
}

func TestProviderPickerChooseModelKeepsCursorVisible(t *testing.T) {
	picker := NewProviderPicker(nil)
	picker.stage = ProviderPickerChooseModel
	picker.models = make([]session.ModelOption, 20)
	for index := range picker.models {
		picker.models[index] = session.ModelOption{ID: fmt.Sprintf("model-%02d", index)}
	}
	for cursor := 0; cursor < len(picker.models); cursor++ {
		picker.cursor = cursor
		view := picker.View()
		if marker := fmt.Sprintf("> model-%02d", cursor); !strings.Contains(view, marker) {
			t.Fatalf("model cursor %d not visible:\n%s", cursor, view)
		}
	}
}

func TestProviderPickerChooseProviderKeepsCursorVisible(t *testing.T) {
	picker := NewProviderPicker(nil)
	picker.stage = ProviderPickerChooseProvider
	picker.profiles = make([]session.ProviderProfile, 20)
	for index := range picker.profiles {
		picker.profiles[index] = session.ProviderProfile{
			ID:          session.ProviderProfileID(fmt.Sprintf("prv-%02d", index)),
			DisplayName: fmt.Sprintf("Provider %02d", index),
			ModelID:     fmt.Sprintf("model-%02d", index),
		}
	}
	total := len(picker.profiles) + len(providerChoices)
	for cursor := 0; cursor < total; cursor++ {
		picker.cursor = cursor
		view := picker.View()
		var marker string
		if cursor < len(picker.profiles) {
			marker = fmt.Sprintf("> Provider %02d  ·", cursor)
		} else {
			marker = "> " + providerChoices[cursor-len(picker.profiles)].DisplayName
		}
		if !strings.Contains(view, marker) {
			t.Fatalf("provider cursor %d not visible:\n%s", cursor, view)
		}
	}
}
