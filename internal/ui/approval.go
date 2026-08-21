package ui

import (
	"fmt"
	"strings"

	"github.com/eaglc/codepilot/internal/session"
)

func approvalStatus(request *session.ApprovalRequest) string {
	if request == nil {
		return ""
	}
	summary := strings.TrimSpace(request.Action.Summary)
	if summary == "" {
		summary = string(request.Action.Kind)
	}
	return "Approval required: " + summary + " [allow once / allow session / deny]"
}

func approvalView(request *session.ApprovalRequest) string {
	if request == nil {
		return ""
	}
	lines := []string{"Approval required", approvalStatus(request)}
	if request.Action.Command != nil {
		command := request.Action.Command.Program
		if len(request.Action.Command.Args) > 0 {
			command += " " + strings.Join(request.Action.Command.Args, " ")
		}
		lines = append(lines, "Command: "+command, "Timeout: "+request.Action.Command.Timeout.String())
	}
	if request.Action.Patch != nil && len(request.Action.Patch.Files) > 0 {
		lines = append(lines, "Files: "+strings.Join(request.Action.Patch.Files, ", "))
	}
	lines = append(lines,
		fmt.Sprintf("Action: %s", request.Action.Kind),
	)
	return strings.Join(lines, "\n")
}
