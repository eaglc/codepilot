// Package buildinfo formats metadata embedded in CodePilot binaries.
package buildinfo

import (
	"fmt"
	"strings"
)

const maximumValueLength = 160

// Format returns the canonical single-line CodePilot version string.
func Format(version, commit, buildDate string) string {
	return fmt.Sprintf(
		"codepilot %s (commit %s, built %s)",
		safeValue(version, "dev"),
		safeValue(commit, "unknown"),
		safeValue(buildDate, "unknown"),
	)
}

func safeValue(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	value = strings.Map(func(character rune) rune {
		if character < ' ' || character == 0x7f {
			return '?'
		}
		return character
	}, value)
	characters := []rune(value)
	if len(characters) > maximumValueLength {
		value = string(characters[:maximumValueLength])
	}
	return value
}
