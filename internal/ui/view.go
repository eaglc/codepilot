package ui

import (
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/eaglc/codepilot/internal/session"
)

const (
	narrowTerminalBreakpoint = 100
	uiChromeHeight           = 5

	// scrollFollowBottom is a sentinel scroll offset meaning "pin to the newest
	// content". A panel in follow mode auto-scrolls as new lines arrive.
	scrollFollowBottom = -1
	// scrollPageSize is the number of lines moved by PageUp/PageDown.
	scrollPageSize = 10
)

// LayoutMode identifies the responsive panel arrangement.
type LayoutMode string

const (
	// LayoutWide displays Conversation and Diff side by side.
	LayoutWide LayoutMode = "wide"
	// LayoutNarrow displays one full-width panel selected with Tab.
	LayoutNarrow LayoutMode = "narrow"
)

// PanelFocus identifies the region that currently owns keyboard and mouse
// input: the conversation panel, the diff panel, or the input box.
type PanelFocus string

const (
	// FocusConversation selects conversation history and streaming output.
	FocusConversation PanelFocus = "conversation"
	// FocusDiff selects the active diff view.
	FocusDiff PanelFocus = "diff"
	// FocusInput selects the composer input box.
	FocusInput PanelFocus = "input"
)

// ResponsiveLayout contains terminal-cell dimensions for the current layout.
type ResponsiveLayout struct {
	Mode              LayoutMode
	Width             int
	Height            int
	BodyHeight        int
	ConversationWidth int
	DiffWidth         int
}

// CalculateLayout selects a 60/40 split for wide terminals and a single panel
// for narrow terminals. The header, status line, and input box consume five rows.
func CalculateLayout(width int, height int) ResponsiveLayout {
	if width < 1 {
		width = 1
	}
	if height < 1 {
		height = 1
	}
	bodyHeight := max(0, height-uiChromeHeight)
	layout := ResponsiveLayout{Width: width, Height: height, BodyHeight: bodyHeight}
	if width < narrowTerminalBreakpoint {
		layout.Mode = LayoutNarrow
		layout.ConversationWidth = width
		layout.DiffWidth = width
		return layout
	}

	layout.Mode = LayoutWide
	contentWidth := width - 1
	layout.ConversationWidth = contentWidth * 3 / 5
	layout.DiffWidth = contentWidth - layout.ConversationWidth
	return layout
}

// View renders the current state and enables Bubble Tea's alternate screen.
func (m *Model) View() tea.View {
	if m == nil {
		return tea.NewView("")
	}
	layout := m.layout()
	footer := m.footerView(layout)
	overlay := m.overlayView()
	lines := []string{headerView(m.snapshot, layout.Width)}
	if layout.BodyHeight > 0 {
		if overlay != "" {
			lines = append(lines, renderDialog(overlay, layout.Width, layout.BodyHeight)...)
		} else {
			lines = append(lines, m.workspaceBody(layout)...)
		}
	}
	if layout.Height >= 2 {
		lines = append(lines, renderStatus(m.statusView(), layout.Width))
	}
	if layout.Height >= 3 {
		remaining := layout.Height - len(lines)
		if remaining > 0 && len(footer) > remaining {
			footer = footer[len(footer)-remaining:]
		}
		lines = append(lines, footer...)
	}
	if len(lines) > layout.Height {
		lines = lines[:layout.Height]
	}

	view := tea.NewView(strings.Join(lines, "\n"))
	view.AltScreen = true
	view.MouseMode = tea.MouseModeCellMotion
	view.WindowTitle = "CodePilot"
	view.BackgroundColor = codePilotTheme.background
	view.ForegroundColor = codePilotTheme.foreground
	return view
}

// layout computes the responsive layout for the current terminal size after
// accounting for the footer (completion menu and input box) height.
func (m *Model) layout() ResponsiveLayout {
	layout := CalculateLayout(m.width, m.height)
	footer := m.footerView(layout)
	layout.BodyHeight = max(0, layout.BodyHeight-max(0, len(footer)-3))
	return layout
}

func (m *Model) workspaceBody(layout ResponsiveLayout) []string {
	if layout.Mode == LayoutWide {
		conversation := m.conversationPanel(layout, m.focus == FocusConversation)
		diff := m.diffPanel(layout, m.focus == FocusDiff)
		return joinPanels(conversation, diff, layout)
	}
	if m.focus == FocusDiff {
		return m.diffPanel(layout, true)
	}
	return m.conversationPanel(layout, true)
}

func (m *Model) conversationPanel(layout ResponsiveLayout, focused bool) []string {
	content := conversationView(m.snapshot, m.assistant, conversationContentWidth(layout))
	offset := resolveScroll(m.conversationScroll, len(content), layout.BodyHeight-2)
	showScrollbar := m.scrollbarVisible && m.focus != FocusDiff
	return renderPanel("Conversation", content, layout.ConversationWidth, layout.BodyHeight, focused, focused, showScrollbar, offset, styleConversationLine)
}

func (m *Model) diffPanel(layout ResponsiveLayout, focused bool) []string {
	content := diffView(m.diff)
	offset := resolveScroll(m.diffScroll, len(content), layout.BodyHeight-2)
	showScrollbar := m.scrollbarVisible && m.focus == FocusDiff
	return renderPanel(diffTitle(m.diff, m.diffKind), content, layout.DiffWidth, layout.BodyHeight, focused, focused, showScrollbar, offset, styleDiffLine)
}

// conversationContentWidth returns the width available for conversation body
// text: the panel width minus the two border columns and one interior padding
// column on each side.
func conversationContentWidth(layout ResponsiveLayout) int {
	if layout.Mode == LayoutWide {
		return layout.ConversationWidth - 4
	}
	return layout.Width - 4
}

func (m *Model) overlayView() string {
	if value := approvalView(m.approval); value != "" {
		return value
	}
	if m.providerPicker != nil {
		if value := m.providerPicker.View(); value != "" {
			return value
		}
	}
	if m.sessionPicker != nil {
		if value := m.sessionPicker.View(m.snapshot.Session.ID); value != "" {
			return value
		}
	}
	if m.overlayText != "" {
		if m.overlayTitle == "" {
			return m.overlayText
		}
		return m.overlayTitle + "\n" + m.overlayText
	}
	return ""
}

func (m *Model) overlayHint() string {
	if m.approval != nil {
		return "Y allow once  •  S allow session  •  N/Esc deny"
	}
	if m.providerPicker != nil {
		switch m.providerPicker.Stage() {
		case ProviderPickerChooseProvider, ProviderPickerChooseModel:
			return "↑/↓ move  •  Enter select  •  Esc close"
		case ProviderPickerEnteringConfig:
			return "Paste or type value  •  Enter continue  •  Esc cancel"
		case ProviderPickerFailed:
			return "Enter retry  •  Esc close"
		case ProviderPickerConfiguring:
			return "Checking credentials  •  Esc cancel"
		case ProviderPickerSwitching:
			return "Validating selected model  •  Esc cancel"
		case ProviderPickerLoadingProfiles, ProviderPickerLoadingModels:
			return "Please wait  •  Esc cancel"
		}
	}
	if m.sessionPicker != nil {
		switch m.sessionPicker.Stage() {
		case SessionPickerChoosing:
			return "↑/↓ move  •  Enter switch  •  Esc close"
		case SessionPickerConfirming:
			return "Y switch  •  N/Esc cancel"
		case SessionPickerFailed:
			return "Enter retry  •  Esc close"
		case SessionPickerLoading, SessionPickerSwitching:
			return "Please wait  •  Esc cancel"
		}
	}
	if m.overlayText != "" {
		if m.pendingWorkspace != "" {
			return "Y open worktree  •  N/Esc cancel"
		}
		return "Enter/Esc close"
	}
	return ""
}

func (m *Model) footerView(layout ResponsiveLayout) []string {
	if hint := m.overlayHint(); hint != "" {
		return renderInputBox("Navigate", codePilotStyles.text.Render(hint), "selection owns the keyboard", layout.Width, false)
	}
	value := composerView(m.composer, m.composerCursor, m.inputBusy, m.cursorOn)
	help := "Enter send  •  Alt+Enter newline  •  /help commands"
	menu := m.completionView(layout.Width)
	if len(menu) > 0 {
		help = "↑/↓ choose  •  Enter/Tab insert  •  Esc close"
	}
	if layout.Mode == LayoutNarrow && len(menu) == 0 {
		help += "  •  Tab panel"
	}
	return append(menu, renderInputBox("Message", value, help, layout.Width, true)...)
}

func headerView(snapshot session.SessionSnapshot, width int) string {
	state := "clean"
	stateStyle := codePilotStyles.statusSuccess
	if snapshot.WorktreeState.Dirty {
		state = "DIRTY"
		stateStyle = codePilotStyles.statusWarning
	}
	if !snapshot.WorktreeState.Available {
		state = "UNAVAILABLE"
		stateStyle = codePilotStyles.statusError
	}
	rootName := filepath.Base(snapshot.WorktreeState.Root)
	if rootName == "." || rootName == "" {
		rootName = "workspace"
	}
	branch := snapshot.WorktreeState.Branch
	if branch == "" {
		branch = "detached"
	}
	title := snapshot.Session.Title
	if title == "" {
		title = "New session"
	}
	model := snapshot.Session.ModelID
	if model == "" {
		model = "no model"
	}
	separator := codePilotStyles.muted.Render("  •  ")
	line := codePilotStyles.brand.Render("◆ CODEPILOT") + "  " +
		stateStyle.Render("● "+state) + separator +
		codePilotStyles.header.Render(rootName+":"+branch) + separator +
		codePilotStyles.header.Render(title) + "  " +
		codePilotStyles.badge.Render(model) + " " +
		codePilotStyles.badge.Render(string(snapshot.Session.PermissionMode))
	return padLine(line, width)
}

func (m *Model) statusView() string {
	if m.errorMessage != "" {
		return "Error: " + m.errorMessage
	}
	if len(m.snapshot.RecoveryWarnings) > 0 {
		return "Recovery warning: " + m.snapshot.RecoveryWarnings[0].UserMessage
	}
	if message := approvalStatus(m.approval); message != "" {
		return message
	}
	if m.status != "" {
		return m.status
	}
	return "Ready."
}

func renderStatus(value string, width int) string {
	style := codePilotStyles.statusInfo
	switch {
	case strings.HasPrefix(value, "Error:"):
		style = codePilotStyles.statusError
	case strings.HasPrefix(value, "Recovery warning:"), strings.HasPrefix(value, "Approval required:"):
		style = codePilotStyles.statusWarning
	case value == "Ready.", strings.Contains(value, "created"), strings.Contains(value, "switched"):
		style = codePilotStyles.statusSuccess
	}
	return padLine(style.Render("● "+value), width)
}

func renderPanel(title string, content []string, width int, height int, focused bool, active bool, showScrollbar bool, offset int, styleLine func(string) string) []string {
	if width <= 0 || height <= 0 {
		return nil
	}
	if width < 4 || height < 2 {
		return clipLines(windowLines(content, offset, height), width)
	}
	borderStyle := codePilotStyles.border
	if focused {
		borderStyle = codePilotStyles.borderFocused
	}
	leftStyle := borderStyle
	if active {
		leftStyle = codePilotStyles.focusBar
	}
	lines := make([]string, 0, height)
	lines = append(lines, panelBorderLine('╭', "─ "+title+" ", '╮', width, borderStyle))
	innerWidth := width - 2
	contentWidth := max(0, innerWidth-2)
	panelHeight := height - 2
	visible := windowLines(content, offset, panelHeight)
	thumbStart, thumbLen, scrollable := scrollbarThumb(offset, len(content), panelHeight)
	if !showScrollbar {
		scrollable = false
	}
	for index := 0; index < panelHeight; index++ {
		value := ""
		if index < len(visible) {
			value = clipLine(visible[index], contentWidth)
			if styleLine != nil {
				value = styleLine(value)
			}
		}
		body := padLine(" "+value, innerWidth)
		right := borderStyle.Render("│")
		if scrollable {
			right = scrollbarCell(index, thumbStart, thumbLen)
		}
		lines = append(lines, leftStyle.Render("│")+body+right)
	}
	lines = append(lines, panelBorderLine('╰', "", '╯', width, borderStyle))
	return lines
}

// scrollbarCell returns the rightmost cell for a panel content row: the thumb
// glyph for rows inside the thumb range, otherwise the track glyph.
func scrollbarCell(row int, thumbStart int, thumbLen int) string {
	if row >= thumbStart && row < thumbStart+thumbLen {
		return codePilotStyles.scrollbarThumb.Render("█")
	}
	return codePilotStyles.scrollbarTrack.Render("│")
}

func renderDialog(value string, width int, height int) []string {
	if width <= 0 || height <= 0 {
		return nil
	}
	parts := strings.Split(value, "\n")
	title := "CodePilot"
	body := parts
	if len(parts) > 0 && strings.TrimSpace(parts[0]) != "" {
		title = parts[0]
		body = parts[1:]
	}
	maxContentWidth := ansi.StringWidth(title)
	for _, line := range body {
		maxContentWidth = max(maxContentWidth, ansi.StringWidth(line))
	}
	dialogWidth := min(width, max(44, min(76, maxContentWidth+6)))
	dialogHeight := min(height, max(4, len(body)+2))
	dialog := renderPanel(title, body, dialogWidth, dialogHeight, true, false, false, 0, styleDialogLine)
	topPadding := max(0, (height-dialogHeight)/2)
	leftPadding := max(0, (width-dialogWidth)/2)
	lines := make([]string, 0, height)
	for range topPadding {
		lines = append(lines, strings.Repeat(" ", width))
	}
	for _, line := range dialog {
		lines = append(lines, padLine(strings.Repeat(" ", leftPadding)+line, width))
	}
	for len(lines) < height {
		lines = append(lines, strings.Repeat(" ", width))
	}
	return lines
}

func renderInputBox(title string, value string, help string, width int, prompt bool) []string {
	if width < 4 {
		return []string{clipLine(value, width)}
	}
	top := panelBorderLine('╭', "─ "+title+" ", '╮', width, codePilotStyles.borderFocused)
	innerWidth := width - 2
	content := " "
	if prompt {
		content += codePilotStyles.composerPrompt.Render("›") + " "
	}
	content += value
	middle := codePilotStyles.borderFocused.Render("│") + padLine(content, innerWidth) + codePilotStyles.borderFocused.Render("│")
	bottom := panelBorderLine('╰', "─ "+help+" ", '╯', width, codePilotStyles.border)
	return []string{top, middle, bottom}
}

func panelBorderLine(left rune, label string, right rune, width int, style lipgloss.Style) string {
	if width <= 1 {
		return clipLine(string(left), width)
	}
	innerWidth := width - 2
	label = clipLine(label, innerWidth)
	fill := strings.Repeat("─", max(0, innerWidth-ansi.StringWidth(label)))
	return style.Render(string(left) + label + fill + string(right))
}

func joinPanels(left []string, right []string, layout ResponsiveLayout) []string {
	lines := make([]string, layout.BodyHeight)
	for index := range lines {
		leftLine := ""
		if index < len(left) {
			leftLine = left[index]
		}
		rightLine := ""
		if index < len(right) {
			rightLine = right[index]
		}
		lines[index] = padLine(leftLine, layout.ConversationWidth) + " " + padLine(rightLine, layout.DiffWidth)
	}
	return lines
}

func styleConversationLine(value string) string {
	switch {
	case strings.HasPrefix(value, "You: "):
		return codePilotStyles.user.Render("You") + codePilotStyles.text.Render(value[len("You"):])
	case strings.HasPrefix(value, "Assistant: "):
		return codePilotStyles.assistant.Render("Assistant") + codePilotStyles.text.Render(value[len("Assistant"):])
	case value == "No messages yet.":
		return codePilotStyles.muted.Render(value)
	default:
		return codePilotStyles.text.Render(value)
	}
}

func styleDiffLine(value string) string {
	switch {
	case strings.HasPrefix(value, "+") && !strings.HasPrefix(value, "+++"):
		return codePilotStyles.diffAdd.Render(value)
	case strings.HasPrefix(value, "-") && !strings.HasPrefix(value, "---"):
		return codePilotStyles.diffDelete.Render(value)
	case strings.HasPrefix(value, "@@"):
		return codePilotStyles.diffHunk.Render(value)
	case strings.HasPrefix(value, "diff "), strings.HasPrefix(value, "index "), strings.HasPrefix(value, "---"), strings.HasPrefix(value, "+++"):
		return codePilotStyles.diffMeta.Render(value)
	case strings.HasPrefix(value, "DRIFTED:"):
		return codePilotStyles.statusWarning.Render(value)
	default:
		return codePilotStyles.text.Render(value)
	}
}

func styleDialogLine(value string) string {
	switch {
	case strings.HasPrefix(value, "> "):
		return codePilotStyles.selected.Render("› " + strings.TrimPrefix(value, "> "))
	case strings.HasPrefix(value, "Error:"), strings.Contains(value, " failed:"):
		return codePilotStyles.statusError.Render(value)
	case strings.HasPrefix(value, "Validation may"), strings.HasPrefix(value, "Switch to another worktree?"):
		return codePilotStyles.statusWarning.Render(value)
	case strings.Contains(value, "Loading"), strings.Contains(value, "Switching"), strings.Contains(value, "Validating"):
		return codePilotStyles.muted.Render(value)
	default:
		return codePilotStyles.text.Render(value)
	}
}

// resolveScroll maps a stored scroll position to a concrete top line offset.
// The scrollFollowBottom sentinel pins to the newest content; other values are
// clamped to the valid offset range.
func resolveScroll(scroll int, total int, visible int) int {
	if visible <= 0 {
		return 0
	}
	maxOffset := max(0, total-visible)
	if scroll == scrollFollowBottom {
		return maxOffset
	}
	return clampInt(scroll, 0, maxOffset)
}

// scrollbarThumb computes the thumb's start row and length within a track of
// height rows for content of total lines scrolled to offset. ok is false when
// the content fits, so no scrollbar is needed.
func scrollbarThumb(offset int, total int, height int) (start int, length int, ok bool) {
	if height <= 0 || total <= height {
		return 0, 0, false
	}
	length = max(1, height*height/total)
	maxOffset := total - height
	if maxOffset <= 0 {
		return 0, length, true
	}
	start = offset * (height - length) / maxOffset
	return start, length, true
}

// windowLines returns up to height lines starting at offset without reading
// past the end of lines.
func windowLines(lines []string, offset int, height int) []string {
	if height <= 0 || offset >= len(lines) {
		return nil
	}
	return lines[offset:min(len(lines), offset+height)]
}

func clipLines(lines []string, width int) []string {
	result := make([]string, len(lines))
	for index, line := range lines {
		result[index] = clipLine(line, width)
	}
	return result
}

func clampInt(value int, low int, high int) int {
	return min(max(value, low), high)
}

func clipLine(value string, width int) string {
	if width <= 0 {
		return ""
	}
	return ansi.Truncate(value, width, "")
}

func padLine(value string, width int) string {
	value = clipLine(value, width)
	return value + strings.Repeat(" ", max(0, width-ansi.StringWidth(value)))
}
