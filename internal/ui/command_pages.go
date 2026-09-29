package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/eaglc/codepilot/internal/codingagent"
)

type permissionOption struct {
	mode        codingagent.PermissionMode
	label       string
	description string
}

var permissionOptions = []permissionOption{
	{mode: codingagent.PermissionReadOnly, label: "Read only", description: "Allow inspection only; block edits, checks, and language-server startup."},
	{mode: codingagent.PermissionAsk, label: "Ask before acting", description: "Ask before edits, checks, and language-server startup."},
	{mode: codingagent.PermissionAutoEdit, label: "Auto edit", description: "Apply policy-safe edits automatically; checks and process startup still ask."},
}

type permissionPicker struct {
	active  bool
	loading bool
	cursor  int
	error   string
}

type permissionModeChangedMsg struct {
	session    codingagent.Session
	err        error
	generation uint64
}

func newPermissionPicker(current codingagent.PermissionMode) permissionPicker {
	picker := permissionPicker{active: true}
	for index := range permissionOptions {
		if permissionOptions[index].mode == current {
			picker.cursor = index
			break
		}
	}
	return picker
}

func (m *Model) handleHelpKey(message tea.KeyPressMsg) tea.Cmd {
	key := message.Key()
	if key.Code == tea.KeyEscape || key.Code == tea.KeyEsc || key.Code == tea.KeyEnter || strings.EqualFold(key.Text, "q") {
		m.helpActive = false
	}
	return nil
}

func (m *Model) handleInstructionsKey(message tea.KeyPressMsg) tea.Cmd {
	key := message.Key()
	if key.Code == tea.KeyEscape || key.Code == tea.KeyEsc || key.Code == tea.KeyEnter || strings.EqualFold(key.Text, "q") {
		m.instructionsActive = false
		return nil
	}
	switch {
	case key.Code == tea.KeyUp || strings.EqualFold(key.Text, "k"):
		m.instructionsScroll = max(0, m.instructionsScroll-1)
	case key.Code == tea.KeyDown || strings.EqualFold(key.Text, "j"):
		m.instructionsScroll++
	case key.Code == tea.KeyPgUp:
		m.instructionsScroll = max(0, m.instructionsScroll-max(1, m.height-2))
	case key.Code == tea.KeyPgDown:
		m.instructionsScroll += max(1, m.height-2)
	case key.Code == tea.KeyHome:
		m.instructionsScroll = 0
	case key.Code == tea.KeyEnd:
		m.instructionsScroll = len(m.instructions)*4 + 16
	}
	return nil
}

func (m *Model) handleContextKey(message tea.KeyPressMsg) tea.Cmd {
	key := message.Key()
	if key.Code == tea.KeyEscape || key.Code == tea.KeyEsc || key.Code == tea.KeyEnter || strings.EqualFold(key.Text, "q") {
		m.contextActive = false
		return nil
	}
	switch {
	case key.Code == tea.KeyUp || strings.EqualFold(key.Text, "k"):
		m.contextScroll = max(0, m.contextScroll-1)
	case key.Code == tea.KeyDown || strings.EqualFold(key.Text, "j"):
		m.contextScroll++
	case key.Code == tea.KeyPgUp:
		m.contextScroll = max(0, m.contextScroll-max(1, m.height-2))
	case key.Code == tea.KeyPgDown:
		m.contextScroll += max(1, m.height-2)
	case key.Code == tea.KeyHome:
		m.contextScroll = 0
	case key.Code == tea.KeyEnd:
		m.contextScroll = len(m.contextReport.Categories)*3 + len(m.contextReport.Degradations)*2 + 24
	}
	return nil
}

func (m *Model) handlePermissionKey(message tea.KeyPressMsg) tea.Cmd {
	key := message.Key()
	if key.Code == tea.KeyEscape || key.Code == tea.KeyEsc {
		m.permissionPicker = permissionPicker{}
		return nil
	}
	if m.permissionPicker.loading {
		return nil
	}
	switch {
	case key.Code == tea.KeyUp || strings.EqualFold(key.Text, "k"):
		m.permissionPicker.cursor = max(0, m.permissionPicker.cursor-1)
	case key.Code == tea.KeyDown || strings.EqualFold(key.Text, "j"):
		m.permissionPicker.cursor = min(len(permissionOptions)-1, m.permissionPicker.cursor+1)
	case key.Code == tea.KeyEnter:
		selected := permissionOptions[m.permissionPicker.cursor]
		if selected.mode == m.snapshot.Session.PermissionMode {
			m.permissionPicker = permissionPicker{}
			return nil
		}
		m.permissionPicker.loading = true
		m.permissionPicker.error = ""
		client, ctx, sessionID, generation := m.client, m.ctx, m.sessionID, m.generation
		return func() tea.Msg {
			session, err := client.SetPermissionMode(ctx, sessionID, selected.mode)
			return permissionModeChangedMsg{session: session, err: err, generation: generation}
		}
	}
	return nil
}

func (m *Model) applyPermissionModeChanged(message permissionModeChangedMsg) {
	if message.generation != m.generation || !m.permissionPicker.active {
		return
	}
	if message.err != nil {
		m.permissionPicker.loading = false
		m.permissionPicker.error = safeError(message.err)
		return
	}
	m.snapshot.Session = message.session
	m.permissionPicker = permissionPicker{}
	m.status = "Permissions changed to " + permissionModeLabel(message.session.PermissionMode) + "."
}

func permissionModeLabel(mode codingagent.PermissionMode) string {
	for _, option := range permissionOptions {
		if option.mode == mode {
			return option.label
		}
	}
	return string(mode)
}

func (m *Model) helpView(width, height int) tea.View {
	lines := []string{theme.header.Render("Commands"), theme.muted.Render("Enter, q, or Esc closes this page"), ""}
	for _, command := range registeredCommands() {
		lines = append(lines, theme.user.Render(fmt.Sprintf("  %-20s", command.usage))+theme.muted.Render(command.description))
	}
	lines = append(lines,
		"",
		theme.muted.Render("Composer: Enter sends  •  Alt+Enter adds a line  •  ↑/↓ moves by visual line, then sent history"),
		theme.muted.Render("Conversation: drag selects text  •  y/Ctrl+C copies  •  Tab selects a message/tool  •  Alt+M toggles Markdown"),
	)
	return pageView("CodePilot Help", lines, width, height)
}

func (m *Model) instructionsView(width, height int) tea.View {
	target := m.instructionsPath
	if target == "" {
		target = "."
	}
	lines := []string{
		theme.header.Render("Project instructions"),
		theme.muted.Render("↑/↓ or j/k scroll  •  Enter, q, or Esc closes"),
		"",
		theme.user.Render("Scope: ") + theme.assistant.Render(target),
		"",
	}
	if m.instructionsLoading {
		lines = append(lines, theme.muted.Render("  Discovering project instructions..."), "")
	}
	if m.instructionsError != "" {
		lines = append(lines, theme.failure.Render("  "+m.instructionsError), "")
	}
	effective := make([]codingagent.InstructionSource, 0)
	for _, source := range m.instructions {
		status, style := instructionStatusPresentation(source, target)
		line := fmt.Sprintf("  %-12s %s", status, source.Source)
		lines = append(lines, style.Render(line))
		metadata := "    scope " + source.Scope
		if source.SHA256 != "" {
			digest := source.SHA256
			if len(digest) > 12 {
				digest = digest[:12]
			}
			metadata += "  •  sha256 " + digest
		}
		lines = append(lines, theme.muted.Render(metadata))
		if source.Diagnostic != "" {
			lines = append(lines, theme.muted.Render("    "+source.Diagnostic))
		}
		lines = append(lines, "")
		if source.Status == codingagent.InstructionLoaded && instructionApplies(source.Scope, target) {
			effective = append(effective, source)
		}
	}
	if !m.instructionsLoading && m.instructionsError == "" && len(m.instructions) == 0 {
		lines = append(lines, theme.muted.Render("  Instruction discovery is unavailable for this session."), "")
	}
	lines = append(lines, theme.header.Render("Effective root-to-leaf chain"))
	if len(effective) == 0 {
		lines = append(lines, theme.muted.Render("  No project instructions apply to this scope."))
	} else {
		for index, source := range effective {
			lines = append(lines, theme.success.Render(fmt.Sprintf("  %d. %s", index+1, source.Source)))
		}
	}
	lines = append(lines, "", theme.muted.Render("Only exact AGENTS.md files are loaded. Repository instructions cannot change permissions or product policy."))
	maxScroll := max(0, len(lines)-height)
	m.instructionsScroll = min(max(0, m.instructionsScroll), maxScroll)
	return pageView("CodePilot Instructions", lines[m.instructionsScroll:], width, height)
}

func instructionStatusPresentation(source codingagent.InstructionSource, target string) (string, lipgloss.Style) {
	if source.Status == codingagent.InstructionLoaded && !instructionApplies(source.Scope, target) {
		return "not matched", theme.muted
	}
	switch source.Status {
	case codingagent.InstructionLoaded:
		return "loaded", theme.success
	case codingagent.InstructionNotFound:
		return "not found", theme.muted
	case codingagent.InstructionIgnored:
		return "ignored", theme.warning
	case codingagent.InstructionFailed:
		return "failed", theme.failure
	default:
		return "unknown", theme.warning
	}
}

func instructionApplies(scope, target string) bool {
	return scope == "." || target == scope || strings.HasPrefix(target, scope+"/")
}

func (m *Model) contextView(width, height int) tea.View {
	lines := []string{
		theme.header.Render("Context budget"),
		theme.muted.Render("↑/↓ or j/k scroll  •  Enter, q, or Esc closes"),
		"",
	}
	if m.contextLoading {
		lines = append(lines, theme.muted.Render("  Loading the latest prepared request..."))
	} else if m.contextError != "" {
		lines = append(lines, theme.failure.Render("  "+m.contextError))
	} else if !m.contextReport.Available {
		lines = append(lines, theme.muted.Render("  No model request has been prepared for this conversation yet."))
	} else {
		report := m.contextReport
		lines = append(lines,
			theme.user.Render(fmt.Sprintf("Latest request: run %s  •  step %d", report.RunID, report.Attempt)),
			theme.assistant.Render(fmt.Sprintf("Estimated input: ~%d tokens", report.EstimatedInput)),
		)
		if report.ProviderExact {
			lines = append(lines, theme.success.Render(fmt.Sprintf("Provider input: %d tokens (exact)", report.ProviderInput)))
		} else {
			lines = append(lines, theme.muted.Render("Provider input: unavailable; local category counts are estimated"))
		}
		lines = append(lines, "")
		if report.InputBudget > 0 {
			lines = append(lines, theme.user.Render(fmt.Sprintf("Input budget: %d tokens", report.InputBudget)))
		} else {
			lines = append(lines, theme.warning.Render("Input budget: unknown; fallback policy is active"))
		}
		lines = append(lines,
			theme.muted.Render(fmt.Sprintf("Context window %d  •  reserved output %d  •  safety margin %d", report.ContextWindow, report.ReservedOutput, report.SafetyMargin)),
			theme.muted.Render(fmt.Sprintf("Compression threshold %d  •  hard limit %d  •  budget source %s", report.SummarizeThreshold, report.HardLimit, report.BudgetSource)),
			"",
			theme.header.Render("Estimated categories"),
		)
		for _, category := range report.Categories {
			lines = append(lines,
				theme.assistant.Render(fmt.Sprintf("  %-18s ~%d tokens  (%d items)", contextCategoryLabel(category.Category), category.Tokens, category.Items)),
				theme.muted.Render("    "+category.Source),
			)
		}
		lines = append(lines, "", theme.header.Render("Latest compression result"))
		if report.Compacted {
			lines = append(lines, theme.success.Render("  History was summarized for this request."))
		} else {
			lines = append(lines, theme.muted.Render("  No new history summary was needed for this request."))
		}
		if len(report.Degradations) == 0 {
			lines = append(lines, theme.muted.Render("  No context degradation was recorded."))
		} else {
			for _, degradation := range report.Degradations {
				lines = append(lines, theme.warning.Render("  "+degradation.Kind), theme.muted.Render("    "+degradation.Reason))
			}
		}
		lines = append(lines, "", theme.muted.Render("Counts expose sizes and safe source summaries only; prompt and file contents are never shown."))
	}
	maxScroll := max(0, len(lines)-height)
	m.contextScroll = min(max(0, m.contextScroll), maxScroll)
	return pageView("CodePilot Context", lines[m.contextScroll:], width, height)
}

func contextCategoryLabel(category codingagent.ContextCategory) string {
	switch category {
	case codingagent.ContextSystem:
		return "System"
	case codingagent.ContextTask:
		return "Task"
	case codingagent.ContextInstructions:
		return "Instructions"
	case codingagent.ContextSkills:
		return "Skills"
	case codingagent.ContextHistory:
		return "History"
	case codingagent.ContextToolResults:
		return "Tool Results"
	case codingagent.ContextArtifacts:
		return "Artifacts"
	case codingagent.ContextReservedOutput:
		return "Reserved Output"
	default:
		return "Unknown"
	}
}

func (m *Model) permissionView(width, height int) tea.View {
	lines := []string{
		theme.header.Render("Session permissions"),
		theme.muted.Render("↑/↓ or j/k choose  •  Enter apply  •  Esc close"),
		"",
	}
	if m.permissionPicker.loading {
		lines = append(lines, theme.muted.Render("Updating permission mode..."), "")
	}
	if m.permissionPicker.error != "" {
		lines = append(lines, theme.failure.Render(m.permissionPicker.error), "")
	}
	for index, option := range permissionOptions {
		marker := "  "
		if index == m.permissionPicker.cursor {
			marker = "❯ "
		}
		current := ""
		if option.mode == m.snapshot.Session.PermissionMode {
			current = "  • current"
		}
		lines = append(lines, theme.user.Render(marker+option.label+current))
		lines = append(lines, theme.muted.Render("    "+option.description), "")
	}
	lines = append(lines, theme.muted.Render("Changing modes revokes reusable approvals already granted by this session."))
	return pageView("CodePilot Permissions", lines, width, height)
}

func pageView(title string, lines []string, width, height int) tea.View {
	for index := range lines {
		lines[index] = truncateANSI(lines[index], width)
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	view := tea.NewView(strings.Join(lines[:min(len(lines), height)], "\n"))
	view.AltScreen = true
	view.WindowTitle = title
	return view
}
