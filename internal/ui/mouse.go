package ui

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

// regionAt maps a zero-based terminal cell to the focusable region it belongs
// to, mirroring View's row layout: the header (row 0) and status line are not
// focusable; the panel body (rows 1..BodyHeight) is conversation/diff; the
// footer (below the status line) is the input box.
func regionAt(x int, y int, layout ResponsiveLayout, current PanelFocus) (PanelFocus, bool) {
	if x < 0 || y < 0 || y >= layout.Height {
		return current, false
	}
	if y == 0 {
		return current, false
	}
	if y <= layout.BodyHeight {
		if layout.Mode == LayoutWide {
			if x < layout.ConversationWidth {
				return FocusConversation, true
			}
			return FocusDiff, true
		}
		if current == FocusDiff {
			return FocusDiff, true
		}
		return FocusConversation, true
	}
	if y == layout.BodyHeight+1 {
		return current, false
	}
	return FocusInput, true
}

// scrollbarHideDelay is how long the transient scrollbar stays visible after
// the last mouse wheel event.
const scrollbarHideDelay = time.Second

// scrollbarHideMsg is delivered scrollbarHideDelay after a wheel event to hide
// the transient scrollbar.
type scrollbarHideMsg struct{}

// blinkInterval toggles the composer cursor visibility when the input box is
// focused, producing a terminal cursor blink.
const blinkInterval = 530 * time.Millisecond

// blinkTickMsg toggles the composer cursor visibility.
type blinkTickMsg struct{}

// handleMouseWheel scrolls the active panel (diff when focused, otherwise the
// conversation) by the wheel delta and shows the transient scrollbar.
func (m *Model) handleMouseWheel(up bool) tea.Cmd {
	if m.completion.active() || m.approval != nil || m.overlayText != "" {
		return nil
	}
	if m.providerPicker != nil && m.providerPicker.Stage() != ProviderPickerClosed {
		return nil
	}
	if m.sessionPicker != nil && m.sessionPicker.Stage() != SessionPickerClosed {
		return nil
	}
	layout := m.layout()
	panelHeight := layout.BodyHeight - 2
	if panelHeight <= 0 {
		return nil
	}
	scroll := &m.conversationScroll
	content := conversationView(m.snapshot, m.assistant, conversationContentWidth(layout))
	if m.focus == FocusDiff {
		scroll = &m.diffScroll
		content = diffView(m.diff)
	}
	total := len(content)
	maxOffset := max(0, total-panelHeight)
	current := resolveScroll(*scroll, total, panelHeight)
	step := 3
	if !up {
		step = -step
	}
	next := clampInt(current-step, 0, maxOffset)
	if next >= maxOffset {
		*scroll = scrollFollowBottom
	} else {
		*scroll = next
	}
	m.scrollbarVisible = true
	return scrollbarHideCmd()
}

// handleMouseClick sets focus to the region under the click, if any.
func (m *Model) handleMouseClick(x int, y int) tea.Cmd {
	if m.approval != nil || m.overlayText != "" {
		return nil
	}
	if m.providerPicker != nil && m.providerPicker.Stage() != ProviderPickerClosed {
		return nil
	}
	if m.sessionPicker != nil && m.sessionPicker.Stage() != SessionPickerClosed {
		return nil
	}
	region, ok := regionAt(x, y, m.layout(), m.focus)
	if !ok {
		return nil
	}
	m.setFocus(region)
	return nil
}

// setFocus switches the active region, starting or stopping the cursor blink
// as the input box gains or loses focus.
func (m *Model) setFocus(next PanelFocus) {
	if next == m.focus {
		return
	}
	m.focus = next
	if next == FocusInput {
		m.cursorOn = true
	} else {
		m.cursorOn = false
	}
}

// scrollbarHideCmd schedules the transient scrollbar to disappear.
func scrollbarHideCmd() tea.Cmd {
	return tea.Tick(scrollbarHideDelay, func(time.Time) tea.Msg {
		return scrollbarHideMsg{}
	})
}

// blinkCmd schedules the next cursor visibility toggle while the input box is
// focused.
func blinkCmd() tea.Cmd {
	return tea.Tick(blinkInterval, func(time.Time) tea.Msg {
		return blinkTickMsg{}
	})
}
