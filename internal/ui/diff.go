package ui

import (
	"fmt"
	"strings"

	"github.com/eaglc/codepilot/internal/session"
)

func diffView(result session.DiffResult, selectedKind session.DiffKind, width int, height int) []string {
	lines := make([]string, 0, len(result.Files)+2)
	if result.Drifted {
		lines = append(lines, "DRIFTED: Session Diff no longer matches current files; inspect Workspace Diff.")
	}
	if strings.TrimSpace(result.Text) != "" {
		lines = append(lines, strings.Split(strings.ReplaceAll(result.Text, "\r\n", "\n"), "\n")...)
	} else {
		for _, file := range result.Files {
			lines = append(lines, fmt.Sprintf("%s %s +%d -%d", file.Status, file.Path, file.Additions, file.Deletions))
		}
	}
	if len(lines) == 0 {
		lines = append(lines, "No changes.")
	}
	if result.Truncated {
		lines = append(lines, "Diff output was truncated.")
	}
	return tailBoundedLines(lines, width, height)
}

func diffTitle(result session.DiffResult, selectedKind session.DiffKind) string {
	kind := result.Kind
	if kind == "" {
		kind = selectedKind
	}
	title := fmt.Sprintf("Diff (%s)", kind)
	if result.Drifted {
		title += " [DRIFTED]"
	}
	return title
}
