package ui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/eaglc/codepilot/internal/session"
)

func conversationView(snapshot session.SessionSnapshot, assistant string, width int) []string {
	var lines []string
	for _, message := range snapshot.Messages {
		role := "Assistant"
		if message.Role == session.RoleUser {
			role = "You"
		}
		lines = append(lines, prefixedLines(role+": ", message.Content, width)...)
	}
	if assistant != "" {
		lines = append(lines, prefixedLines("Assistant: ", assistant, width)...)
	}
	if len(lines) == 0 {
		lines = append(lines, "No messages yet.")
	}
	return lines
}

// prefixedLines splits value into display lines and hard-wraps each paragraph
// to the available content width so long messages are not silently truncated.
// The first line carries prefix; continuation lines are indented to align with
// the start of the content so wrapped text reads as a single block.
func prefixedLines(prefix string, value string, width int) []string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	prefixWidth := ansi.StringWidth(prefix)
	contentWidth := max(0, width-prefixWidth)
	paragraphs := strings.Split(value, "\n")
	lines := make([]string, 0, len(paragraphs))
	first := true
	for _, paragraph := range paragraphs {
		wrapped := ansi.Hardwrap(paragraph, contentWidth, true)
		for _, line := range strings.Split(wrapped, "\n") {
			if first {
				lines = append(lines, prefix+line)
				first = false
				continue
			}
			lines = append(lines, strings.Repeat(" ", prefixWidth)+line)
		}
	}
	return lines
}
