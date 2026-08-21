package ui

import (
	"context"
	"errors"
	"strings"

	"github.com/eaglc/codepilot/internal/session"
)

const defaultOperationError = "The operation could not be completed."

const helpText = `/model                         configure or switch provider/model
/permissions MODE              set read-only, ask, or auto-edit
/session create [TITLE]         create and activate a session (/session new is an alias)
/session list [--all]           open the session picker
/session switch ID              switch directly by ID
/session rename NAME            rename the active session
/session archive                archive it and create a replacement
/workspace open PATH            activate another Git worktree
/workspace list                 show registered worktrees
/status                         show active session and worktree state
/diff [proposed|session|workspace]
/clear                          start a new session with empty context
/help                           show this help
/exit                           save and exit

Ctrl+C cancel turn · Ctrl+D exit · Ctrl+N new session · Ctrl+O sessions · Tab switch panel`

type slashCommand struct {
	name      string
	arguments []string
}

type slashCommandDefinition struct {
	command        string
	usage          string
	insert         string
	description    string
	executeOnEnter bool
}

var slashCommandDefinitions = []slashCommandDefinition{
	{command: "model", usage: "/model", insert: "/model", description: "Configure or switch provider/model", executeOnEnter: true},
	{command: "permissions read-only", usage: "/permissions read-only", insert: "/permissions read-only", description: "Allow inspection only", executeOnEnter: true},
	{command: "permissions ask", usage: "/permissions ask", insert: "/permissions ask", description: "Ask before edits and checks", executeOnEnter: true},
	{command: "permissions auto-edit", usage: "/permissions auto-edit", insert: "/permissions auto-edit", description: "Allow validated edits", executeOnEnter: true},
	{command: "session create", usage: "/session create [TITLE]", insert: "/session create ", description: "Create and activate a session"},
	{command: "session list", usage: "/session list", insert: "/session list", description: "List sessions in this worktree", executeOnEnter: true},
	{command: "session list --all", usage: "/session list --all", insert: "/session list --all", description: "List sessions in all worktrees", executeOnEnter: true},
	{command: "session switch", usage: "/session switch ID", insert: "/session switch ", description: "Switch directly by session ID"},
	{command: "session rename", usage: "/session rename NAME", insert: "/session rename ", description: "Rename the active session"},
	{command: "session archive", usage: "/session archive", insert: "/session archive", description: "Archive the active session", executeOnEnter: true},
	{command: "workspace open", usage: "/workspace open PATH", insert: "/workspace open ", description: "Open another Git worktree"},
	{command: "workspace list", usage: "/workspace list", insert: "/workspace list", description: "List registered worktrees", executeOnEnter: true},
	{command: "status", usage: "/status", insert: "/status", description: "Show active session and worktree state", executeOnEnter: true},
	{command: "diff proposed", usage: "/diff proposed", insert: "/diff proposed", description: "Show the proposed diff", executeOnEnter: true},
	{command: "diff session", usage: "/diff session", insert: "/diff session", description: "Show the active session diff", executeOnEnter: true},
	{command: "diff workspace", usage: "/diff workspace", insert: "/diff workspace", description: "Show the worktree diff", executeOnEnter: true},
	{command: "clear", usage: "/clear", insert: "/clear", description: "Start a session with empty context", executeOnEnter: true},
	{command: "help", usage: "/help", insert: "/help", description: "Show command help", executeOnEnter: true},
	{command: "exit", usage: "/exit", insert: "/exit", description: "Save and exit", executeOnEnter: true},
}

func parseSlashCommand(input string) (slashCommand, error) {
	trimmed := strings.TrimSpace(input)
	if !strings.HasPrefix(trimmed, "/") {
		return slashCommand{}, errors.New("command must start with /")
	}
	fields := strings.Fields(strings.TrimPrefix(trimmed, "/"))
	if len(fields) == 0 {
		return slashCommand{}, errors.New("enter a command after /")
	}
	return slashCommand{name: strings.ToLower(fields[0]), arguments: fields[1:]}, nil
}

// SafeErrorMessage converts an application error to terminal-safe user copy.
// Wrapped diagnostic causes are intentionally never formatted or returned.
func SafeErrorMessage(err error, fallback string) string {
	if err == nil {
		return ""
	}
	if strings.TrimSpace(fallback) == "" {
		fallback = defaultOperationError
	}

	var appError *session.AppError
	if errors.As(err, &appError) {
		if message := strings.TrimSpace(appError.UserMessage); message != "" {
			return message
		}
		if message := errorCodeMessage(appError.Code); message != "" {
			return message
		}
	}
	if errors.Is(err, context.Canceled) {
		return "Operation was cancelled."
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "Operation timed out. Try again."
	}
	return fallback
}

func errorCodeMessage(code session.ErrorCode) string {
	switch code {
	case session.ErrInvalidInput:
		return "The request is invalid."
	case session.ErrInvalidState:
		return "That action is not available right now."
	case session.ErrNotFound:
		return "The requested item was not found."
	case session.ErrConflict:
		return "The workspace changed. Refresh and try again."
	case session.ErrWorkspaceUnavailable:
		return "The worktree is unavailable. Restore its original path and try again."
	case session.ErrProviderUnavailable:
		return "The selected provider or model is unavailable."
	case session.ErrPermissionDenied:
		return "The current permission mode does not allow that action."
	case session.ErrApprovalRequired:
		return "Approval is required before this action can run."
	case session.ErrCancelled:
		return "Operation was cancelled."
	case session.ErrTimeout:
		return "Operation timed out. Try again."
	case session.ErrCorruptedState:
		return "Stored session data is corrupted and could not be restored."
	case session.ErrPersistence:
		return "Session state could not be read or saved."
	case session.ErrInternal:
		return defaultOperationError
	default:
		return ""
	}
}
