package ui

import (
	"image/color"

	"charm.land/lipgloss/v2"
)

// tuiTheme is immutable after initialization so every view uses the same
// semantic colors instead of accumulating component-specific ANSI values.
type tuiTheme struct {
	background color.Color
	foreground color.Color
	primary    color.Color
	surface    color.Color
	border     color.Color
	muted      color.Color
	success    color.Color
	warning    color.Color
	error      color.Color
	accent     color.Color
}

var codePilotTheme = tuiTheme{
	background: lipgloss.Color("#11111B"),
	foreground: lipgloss.Color("#CDD6F4"),
	primary:    lipgloss.Color("#CBA6F7"),
	surface:    lipgloss.Color("#313244"),
	border:     lipgloss.Color("#45475A"),
	muted:      lipgloss.Color("#7F849C"),
	success:    lipgloss.Color("#A6E3A1"),
	warning:    lipgloss.Color("#F9E2AF"),
	error:      lipgloss.Color("#F38BA8"),
	accent:     lipgloss.Color("#89B4FA"),
}

var codePilotStyles = struct {
	brand          lipgloss.Style
	header         lipgloss.Style
	badge          lipgloss.Style
	border         lipgloss.Style
	borderFocused  lipgloss.Style
	panelTitle     lipgloss.Style
	text           lipgloss.Style
	muted          lipgloss.Style
	selected       lipgloss.Style
	user           lipgloss.Style
	assistant      lipgloss.Style
	statusSuccess  lipgloss.Style
	statusWarning  lipgloss.Style
	statusError    lipgloss.Style
	statusInfo     lipgloss.Style
	diffAdd        lipgloss.Style
	diffDelete     lipgloss.Style
	diffHunk       lipgloss.Style
	diffMeta       lipgloss.Style
	composerPrompt lipgloss.Style
	placeholder    lipgloss.Style
	cursor         lipgloss.Style
	focusBar       lipgloss.Style
	scrollbarThumb lipgloss.Style
	scrollbarTrack lipgloss.Style
}{
	brand:          lipgloss.NewStyle().Bold(true).Foreground(codePilotTheme.background).Background(codePilotTheme.primary).Padding(0, 1),
	header:         lipgloss.NewStyle().Foreground(codePilotTheme.foreground),
	badge:          lipgloss.NewStyle().Foreground(codePilotTheme.primary).Background(codePilotTheme.surface).Padding(0, 1),
	border:         lipgloss.NewStyle().Foreground(codePilotTheme.border),
	borderFocused:  lipgloss.NewStyle().Foreground(codePilotTheme.primary).Bold(true),
	panelTitle:     lipgloss.NewStyle().Foreground(codePilotTheme.primary).Bold(true),
	text:           lipgloss.NewStyle().Foreground(codePilotTheme.foreground),
	muted:          lipgloss.NewStyle().Foreground(codePilotTheme.muted),
	selected:       lipgloss.NewStyle().Foreground(codePilotTheme.background).Background(codePilotTheme.primary).Bold(true).Padding(0, 1),
	user:           lipgloss.NewStyle().Foreground(codePilotTheme.accent).Bold(true),
	assistant:      lipgloss.NewStyle().Foreground(codePilotTheme.primary).Bold(true),
	statusSuccess:  lipgloss.NewStyle().Foreground(codePilotTheme.success),
	statusWarning:  lipgloss.NewStyle().Foreground(codePilotTheme.warning),
	statusError:    lipgloss.NewStyle().Foreground(codePilotTheme.error),
	statusInfo:     lipgloss.NewStyle().Foreground(codePilotTheme.accent),
	diffAdd:        lipgloss.NewStyle().Foreground(codePilotTheme.success),
	diffDelete:     lipgloss.NewStyle().Foreground(codePilotTheme.error),
	diffHunk:       lipgloss.NewStyle().Foreground(codePilotTheme.primary),
	diffMeta:       lipgloss.NewStyle().Foreground(codePilotTheme.muted),
	composerPrompt: lipgloss.NewStyle().Foreground(codePilotTheme.primary).Bold(true),
	placeholder:    lipgloss.NewStyle().Foreground(codePilotTheme.muted).Italic(true),
	cursor:         lipgloss.NewStyle().Foreground(codePilotTheme.background).Background(codePilotTheme.foreground),
	focusBar:       lipgloss.NewStyle().Foreground(codePilotTheme.success).Bold(true),
	scrollbarThumb: lipgloss.NewStyle().Foreground(codePilotTheme.primary),
	scrollbarTrack: lipgloss.NewStyle().Foreground(codePilotTheme.border),
}
