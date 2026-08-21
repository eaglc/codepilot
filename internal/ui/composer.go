package ui

import "strings"

func composerView(input []rune, busy bool) string {
	if busy {
		return "Starting turn..."
	}
	value := strings.ReplaceAll(string(input), "\n", " ↵ ")
	if value == "" {
		value = "Ask CodePilot anything…"
	}
	return value
}
