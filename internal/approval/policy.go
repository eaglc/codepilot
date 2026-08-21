package approval

import "github.com/eaglc/codepilot/internal/session"

func basePolicy(mode session.PermissionMode, kind session.ActionKind) session.AuthorizationOutcome {
	if kind == session.ActionRead {
		return session.AuthorizationAllow
	}
	if kind == session.ActionStartLanguageServer {
		// A language server is read-oriented but still executes an external binary,
		// so every permission mode requires an explicit first-use decision.
		return session.AuthorizationPrompt
	}
	switch mode {
	case session.PermissionReadOnly:
		return session.AuthorizationDeny
	case session.PermissionAsk:
		return session.AuthorizationPrompt
	case session.PermissionAutoEdit:
		if kind == session.ActionApplyPatch {
			return session.AuthorizationAllow
		}
		return session.AuthorizationPrompt
	default:
		return session.AuthorizationDeny
	}
}

func denialReason(mode session.PermissionMode, kind session.ActionKind) string {
	if mode == session.PermissionReadOnly && kind == session.ActionApplyPatch {
		return "Read-only mode does not permit patches."
	}
	if mode == session.PermissionReadOnly && kind == session.ActionRunCheck {
		return "Read-only mode does not permit running project code."
	}
	if kind == session.ActionStartLanguageServer {
		return "Starting the language server was not approved."
	}
	return "The current permission policy denied this action."
}
