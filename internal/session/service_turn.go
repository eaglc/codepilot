package session

import "strings"

// The functions in this file translate raw turn results, session records, and
// limits into the statuses, summaries, and configurations the rest of the
// service publishes. They are pure: no shared state, no side effects.

func validateRunLimits(limits RunLimits) error {
	if limits.MaxSteps <= 0 || limits.MaxTurnDuration <= 0 || limits.CommandTimeout <= 0 || limits.ToolResultMaxBytes <= 0 || limits.CommandOutputMaxBytes <= 0 {
		return applicationError(ErrInvalidInput, "session.validate_run_limits", "Run limits must be positive.", nil)
	}
	return nil
}

func validPermissionMode(mode PermissionMode) bool {
	return mode == PermissionReadOnly || mode == PermissionAsk || mode == PermissionAutoEdit
}

func classifyTurnStatus(cancelled bool, failed bool, hasPatch bool, outcome CheckOutcome) TurnStatus {
	if cancelled || outcome == CheckCancelled {
		return TurnCancelled
	}
	if failed {
		return TurnFailed
	}
	if !hasPatch {
		return TurnCompleted
	}

	switch outcome {
	case CheckPassed:
		return TurnVerified
	case CheckFailed:
		return TurnFailed
	default:
		return TurnUnverified
	}
}

func finalEventKind(status TurnStatus) EventKind {
	switch status {
	case TurnCancelled:
		return EventTurnCancelled
	case TurnFailed:
		return EventTurnFailed
	default:
		return EventTurnCompleted
	}
}

func finalTextForStatus(status TurnStatus) string {
	switch status {
	case TurnCompleted:
		return "The request completed without changing files."
	case TurnCancelled:
		return "Turn cancelled."
	case TurnFailed:
		return "The requested change could not be completed."
	case TurnVerified:
		return "The change was completed and verified."
	default:
		return "The change was completed but could not be verified."
	}
}

func codingAgentConfig(value Session, worktreeRoot string, limits RunLimits) CodingAgentConfig {
	return CodingAgentConfig{
		SessionID:         value.ID,
		WorkspaceID:       value.WorkspaceID,
		WorktreeID:        value.WorktreeID,
		WorktreeRoot:      worktreeRoot,
		ProviderProfileID: value.ProviderProfileID,
		ModelID:           value.ModelID,
		Limits:            limits,
	}
}

func sessionSummaryFromSession(value Session) SessionSummary {
	return SessionSummary{
		ID:                value.ID,
		WorkspaceID:       value.WorkspaceID,
		WorktreeID:        value.WorktreeID,
		Title:             value.Title,
		ProviderProfileID: value.ProviderProfileID,
		ModelID:           value.ModelID,
		PermissionMode:    value.PermissionMode,
		LastTurnStatus:    value.LastTurnStatus,
		Archived:          value.Archived,
		UpdatedAt:         value.UpdatedAt,
	}
}

func worktreeSummaryFromRecord(value WorktreeRecord, sessionID SessionID, available bool) WorktreeSummary {
	return WorktreeSummary{
		ID:            value.ID,
		WorkspaceID:   value.WorkspaceID,
		Root:          value.Root,
		LastSessionID: sessionID,
		Available:     available,
		LastUsedAt:    value.LastUsedAt,
	}
}

func titleFromMessage(message string) string {
	firstLine := message
	if newline := strings.IndexByte(firstLine, '\n'); newline >= 0 {
		firstLine = firstLine[:newline]
	}
	firstLine = strings.TrimSpace(firstLine)
	runes := []rune(firstLine)
	if len(runes) > 80 {
		runes = runes[:80]
	}
	return string(runes)
}
