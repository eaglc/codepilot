package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/eaglc/codepilot/internal/codingagent"
)

type commandSpec struct {
	name        string
	usage       string
	description string
	aliases     []string
	takesArg    bool
	requiresArg bool
	run         func(*Model, string) tea.Cmd
}

func registeredCommands() []commandSpec {
	return []commandSpec{
		{name: "/help", usage: "/help", description: "Show this command guide", run: runHelpCommand},
		{name: "/plan", usage: "/plan [request]", description: "Start a read-only Plan task", takesArg: true, run: runPlanCommand},
		{name: "/workspace", usage: "/workspace", description: "Choose or repair a workspace", run: runWorkspaceCommand},
		{name: "/provider", usage: "/provider", description: "Configure a provider and choose a model", aliases: []string{"/model"}, run: runModelCommand},
		{name: "/permissions", usage: "/permissions", description: "Choose the active session's safety mode", run: runPermissionsCommand},
		{name: "/session", usage: "/session", description: "Choose, create, or archive a session", run: runSessionCommand},
		{name: "/clear", usage: "/clear", description: "Start a clean session and preserve this conversation", run: runClearCommand},
		{name: "/rename", usage: "/rename <title>", description: "Rename the active session", takesArg: true, requiresArg: true, run: runRenameCommand},
		{name: "/fork", usage: "/fork", description: "Continue from a historical message", run: runForkCommand},
		{name: "/md", usage: "/md [on|off]", description: "Toggle Markdown rendering for assistant messages", aliases: []string{"/markdown"}, takesArg: true, run: runMarkdownCommand},
		{name: "/exit", usage: "/exit", description: "Exit CodePilot", aliases: []string{"/quit"}, run: runExitCommand},
	}
}

func (m *Model) submitCommand(text string) tea.Cmd {
	spec, arguments, found := lookupCommand(text)
	if !found {
		m.clearInput()
		m.errorMessage = "Unknown command. Use /help to see available commands."
		return nil
	}
	m.clearInput()
	m.errorMessage = ""
	return spec.run(m, arguments)
}

func lookupCommand(value string) (commandSpec, string, bool) {
	value = strings.TrimSpace(value)
	for _, spec := range registeredCommands() {
		names := append([]string{spec.name}, spec.aliases...)
		for _, name := range names {
			if strings.EqualFold(value, name) {
				return spec, "", true
			}
			prefix := name + " "
			if spec.takesArg && len(value) > len(prefix) && strings.EqualFold(value[:len(prefix)], prefix) {
				return spec, strings.TrimSpace(value[len(prefix):]), true
			}
		}
	}
	return commandSpec{}, "", false
}

func filterCommands(prefix string) []commandSpec {
	prefix = strings.ToLower(strings.TrimSpace(prefix))
	if !strings.HasPrefix(prefix, "/") || strings.ContainsAny(prefix, " \t\r\n") {
		return nil
	}
	var matches []commandSpec
	for _, spec := range registeredCommands() {
		matched := strings.HasPrefix(spec.name, prefix)
		for _, alias := range spec.aliases {
			matched = matched || strings.HasPrefix(alias, prefix)
		}
		if matched {
			matches = append(matches, spec)
		}
	}
	return matches
}

func (m *Model) commandMatches() []commandSpec {
	return filterCommands(string(m.input))
}

func (m *Model) completionActive() bool {
	return !m.busy && !m.picker.active && !m.sessionPicker.active && !m.workspacePicker.active && !m.permissionPicker.active && !m.forkPicker.active && !m.helpActive &&
		m.pendingApproval() == nil && m.pendingRecovery() == nil && !m.completionDismissed && len(m.commandMatches()) != 0
}

func (m *Model) moveCompletionSelection(delta int) {
	matches := m.commandMatches()
	if len(matches) == 0 {
		m.completionCursor = 0
		return
	}
	m.completionCursor = (m.completionCursor + delta + len(matches)) % len(matches)
}

func (m *Model) completeCommand() {
	matches := m.commandMatches()
	if len(matches) == 0 {
		return
	}
	m.completionCursor = min(max(0, m.completionCursor), len(matches)-1)
	selected := matches[m.completionCursor]
	value := selected.name
	if selected.takesArg {
		value += " "
	}
	m.replaceInput(value)
	m.completionDismissed = true
}

func (m *Model) submitSelectedCommand() tea.Cmd {
	matches := m.commandMatches()
	if len(matches) == 0 {
		return nil
	}
	m.completionCursor = min(max(0, m.completionCursor), len(matches)-1)
	selected := matches[m.completionCursor]
	if selected.requiresArg {
		m.completeCommand()
		return nil
	}
	return m.submitCommand(selected.name)
}

func (m *Model) commandCompletionLines(width, limit int) []string {
	if !m.completionActive() {
		return nil
	}
	matches := m.commandMatches()
	if len(matches) == 0 || limit <= 0 {
		return nil
	}
	m.completionCursor = min(max(0, m.completionCursor), len(matches)-1)
	start, end := pickerWindow(m.completionCursor, len(matches), limit)
	lines := make([]string, 0, end-start)
	for index := start; index < end; index++ {
		if index == m.completionCursor {
			line := theme.user.Render(fmt.Sprintf("❯ %-22s", matches[index].usage)) + theme.muted.Render("— "+matches[index].description)
			lines = append(lines, truncateANSI(line, width))
			continue
		}
		line := theme.muted.Render(fmt.Sprintf("  %-22s— %s", matches[index].usage, matches[index].description))
		lines = append(lines, truncateANSI(line, width))
	}
	return lines
}

func noCommandArguments(m *Model, arguments, usage string) bool {
	if arguments == "" {
		return true
	}
	m.errorMessage = "Usage: " + usage
	return false
}

func runHelpCommand(m *Model, arguments string) tea.Cmd {
	if noCommandArguments(m, arguments, "/help") {
		m.helpActive = true
	}
	return nil
}

func runPlanCommand(m *Model, arguments string) tea.Cmd {
	if strings.TrimSpace(arguments) == "" {
		m.planInput = true
		m.status = "Plan mode: enter a request for read-only planning."
		return nil
	}
	return m.submitTurn(strings.TrimSpace(arguments), codingagent.TurnModePlan)
}

func runWorkspaceCommand(m *Model, arguments string) tea.Cmd {
	if !noCommandArguments(m, arguments, "/workspace") {
		return nil
	}
	m.workspacePicker = newWorkspacePicker()
	return m.loadWorkspaces()
}

func runModelCommand(m *Model, arguments string) tea.Cmd {
	if !noCommandArguments(m, arguments, "/model") {
		return nil
	}
	m.picker = newProviderPicker("", false)
	return m.loadProviderProfiles()
}

func runPermissionsCommand(m *Model, arguments string) tea.Cmd {
	if noCommandArguments(m, arguments, "/permissions") {
		m.permissionPicker = newPermissionPicker(m.snapshot.Session.PermissionMode)
	}
	return nil
}

func runSessionCommand(m *Model, arguments string) tea.Cmd {
	if !noCommandArguments(m, arguments, "/session") {
		return nil
	}
	m.sessionPicker = newSessionPicker()
	return m.loadSessions()
}

func runRenameCommand(m *Model, arguments string) tea.Cmd {
	if arguments == "" {
		m.errorMessage = "Usage: /rename <title>"
		return nil
	}
	return m.renameSession(arguments)
}

func runForkCommand(m *Model, arguments string) tea.Cmd {
	if !noCommandArguments(m, arguments, "/fork") {
		return nil
	}
	m.forkPicker = newForkPicker(m.snapshot.Transcript)
	return nil
}

func runClearCommand(m *Model, arguments string) tea.Cmd {
	if !noCommandArguments(m, arguments, "/clear") {
		return nil
	}
	return m.createSession("")
}

func runMarkdownCommand(m *Model, arguments string) tea.Cmd {
	switch strings.ToLower(arguments) {
	case "", "toggle":
		m.setMarkdownEnabled(!m.markdownEnabled)
	case "on":
		m.setMarkdownEnabled(true)
	case "off":
		m.setMarkdownEnabled(false)
	default:
		m.errorMessage = "Usage: /md [on|off]"
	}
	return nil
}

func runExitCommand(m *Model, arguments string) tea.Cmd {
	if !noCommandArguments(m, arguments, "/exit") {
		return nil
	}
	return tea.Quit
}
