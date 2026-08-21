package ui

import (
	"fmt"
	"strings"

	"github.com/eaglc/codepilot/internal/session"
)

func conversationView(snapshot session.SessionSnapshot, assistant string, width int, height int) []string {
	var lines []string
	for _, message := range snapshot.Messages {
		role := "Assistant"
		if message.Role == session.RoleUser {
			role = "You"
		}
		lines = append(lines, prefixedLines(role+": ", message.Content)...)
	}
	if assistant != "" {
		lines = append(lines, prefixedLines("Assistant: ", assistant)...)
	}
	if len(lines) == 0 {
		lines = append(lines, "No messages yet.")
	}
	return tailBoundedLines(lines, width, height)
}

func prefixedLines(prefix string, value string) []string {
	parts := strings.Split(strings.ReplaceAll(value, "\r\n", "\n"), "\n")
	if len(parts) == 0 {
		return []string{prefix}
	}
	lines := make([]string, 0, len(parts))
	for index, part := range parts {
		if index == 0 {
			lines = append(lines, fmt.Sprintf("%s%s", prefix, part))
			continue
		}
		lines = append(lines, part)
	}
	return lines
}
