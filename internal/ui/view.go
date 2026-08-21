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
)

// LayoutMode identifies the responsive panel arrangement.
type LayoutMode string

const (
	// LayoutWide displays Conversation and Diff side by side.
	LayoutWide LayoutMode = "wide"
	// LayoutNarrow displays one full-width panel selected with Tab.
	LayoutNarrow LayoutMode = "narrow"
)

// PanelFocus identifies the visible panel in a narrow terminal.
type PanelFocus string

const (
	// FocusConversation selects conversation history and streaming output.
	FocusConversation PanelFocus = "conversation"
	// FocusDiff selects the active diff view.
	FocusDiff PanelFocus = "diff"
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
	layout := CalculateLayout(m.width, m.height)
	footer := m.footerView(layout)
	layout.BodyHeight = max(0, layout.BodyHeight-max(0, len(footer)-3))
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
	view.WindowTitle = "CodePilot"
	view.BackgroundColor = codePilotTheme.background
	view.ForegroundColor = codePilotTheme.foreground
	return view
}

func (m *Model) workspaceBody(layout ResponsiveLayout) []string {
	if layout.Mode == LayoutWide {
		conversation := renderPanel(
			"Conversation",
			conversationView(m.snapshot, m.assistant, layout.ConversationWidth-4, layout.BodyHeight-2),
			layout.ConversationWidth,
			layout.BodyHeight,
			m.focus == FocusConversation,
			styleConversationLine,
		)
		diff := renderPanel(
			diffTitle(m.diff, m.diffKind),
			diffView(m.diff, m.diffKind, layout.DiffWidth-4, layout.BodyHeight-2),
			layout.DiffWidth,
			layout.BodyHeight,
			m.focus == FocusDiff,
			styleDiffLine,
		)
		return joinPanels(conversation, diff, layout)
	}
	if m.focus == FocusDiff {
		return renderPanel(
			diffTitle(m.diff, m.diffKind),
			diffView(m.diff, m.diffKind, layout.Width-4, layout.BodyHeight-2),
			layout.Width,
			layout.BodyHeight,
			true,
			styleDiffLine,
		)
	}
	return renderPanel(
		"Conversation",
		conversationView(m.snapshot, m.assistant, layout.Width-4, layout.BodyHeight-2),
		layout.Width,
		layout.BodyHeight,
		true,
		styleConversationLine,
	)
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
		return renderInputBox("Navigate", hint, "selection owns the keyboard", layout.Width, false, false)
	}
	value := composerView(m.composer, m.inputBusy)
	help := "Enter send  •  Alt+Enter newline  •  /help commands"
	menu := m.completionView(layout.Width)
	if len(menu) > 0 {
		help = "↑/↓ choose  •  Enter/Tab insert  •  Esc close"
	}
	if layout.Mode == LayoutNarrow && len(menu) == 0 {
		help += "  •  Tab panel"
	}
	return append(menu, renderInputBox("Message", value, help, layout.Width, true, len(m.composer) == 0 && !m.inputBusy)...)
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

func renderPanel(title string, content []string, width int, height int, focused bool, styleLine func(string) string) []string {
	if width <= 0 || height <= 0 {
		return nil
	}
	if width < 4 || height < 2 {
		return tailBoundedLines(content, width, height)
	}
	borderStyle := codePilotStyles.border
	if focused {
		borderStyle = codePilotStyles.borderFocused
	}
	lines := make([]string, 0, height)
	lines = append(lines, panelBorderLine('╭', "─ "+title+" ", '╮', width, borderStyle))
	innerWidth := width - 2
	contentWidth := max(0, innerWidth-2)
	visible := tailBoundedLines(content, contentWidth, height-2)
	for index := 0; index < height-2; index++ {
		value := ""
		if index < len(visible) {
			value = visible[index]
			if styleLine != nil {
				value = styleLine(value)
			}
		}
		body := padLine(" "+value, innerWidth)
		lines = append(lines, borderStyle.Render("│")+body+borderStyle.Render("│"))
	}
	lines = append(lines, panelBorderLine('╰', "", '╯', width, borderStyle))
	return lines
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
	dialog := renderPanel(title, body, dialogWidth, dialogHeight, true, styleDialogLine)
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

func renderInputBox(title string, value string, help string, width int, prompt bool, placeholder bool) []string {
	if width < 4 {
		return []string{clipLine(value, width)}
	}
	top := panelBorderLine('╭', "─ "+title+" ", '╮', width, codePilotStyles.borderFocused)
	innerWidth := width - 2
	content := " "
	if prompt {
		content += codePilotStyles.composerPrompt.Render("›") + " "
	}
	if placeholder {
		content += codePilotStyles.placeholder.Render(value)
	} else {
		content += codePilotStyles.text.Render(value)
	}
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

func tailBoundedLines(lines []string, width int, height int) []string {
	if height <= 0 {
		return nil
	}
	if len(lines) > height {
		if height == 1 {
			lines = lines[:1]
		} else {
			visible := make([]string, 1, height)
			visible[0] = lines[0]
			visible = append(visible, lines[len(lines)-(height-1):]...)
			lines = visible
		}
	}
	result := make([]string, len(lines))
	for index, line := range lines {
		result[index] = clipLine(line, width)
	}
	return result
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
