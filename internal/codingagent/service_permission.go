package codingagent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	agentsession "github.com/eaglc/codepilot/internal/agent/session"
)

const (
	defaultSessionGrantTTL = 8 * time.Hour
	maxPermissionGrants    = 1024
)

func deriveSessionGrant(product Session, durable agentsession.Snapshot, request ResumeTurnRequest, now time.Time) (PermissionGrant, error) {
	state := agentsession.AnalyzeRecovery(durable)
	var pending *agentsession.PendingInterrupt
	for index := range state.PendingInterrupts {
		candidate := &state.PendingInterrupts[index]
		if candidate.RunID == agentsession.RunID(request.TurnID) && candidate.InterruptID == request.InterruptID {
			pending = candidate
			break
		}
	}
	if pending == nil || pending.Kind != "approval" || strings.TrimSpace(pending.ToolCallID) == "" {
		return PermissionGrant{}, errors.New("the requested approval is not pending")
	}
	toolName := ""
	for _, candidate := range state.PendingTools {
		if candidate.RunID == pending.RunID && candidate.ToolCallID == pending.ToolCallID {
			toolName = candidate.ToolName
			break
		}
	}
	var payload struct {
		Kind          string   `json:"kind"`
		Version       int      `json:"version"`
		Files         []string `json:"files"`
		PlanID        string   `json:"plan_id"`
		Digest        string   `json:"digest"`
		GrantToolName string   `json:"grant_tool_name"`
		RequestedTool string   `json:"requested_tool"`
		Language      string   `json:"language"`
	}
	if json.Unmarshal(pending.Payload, &payload) != nil || payload.Version != 1 || strings.TrimSpace(payload.Digest) == "" {
		return PermissionGrant{}, errors.New("the pending approval cannot create a session grant")
	}
	grant := PermissionGrant{
		Scope: PermissionGrantSession, ToolName: toolName,
		SourceTurnID: request.TurnID, SourceInterruptID: request.InterruptID,
		CreatedAt: now, ExpiresAt: now.Add(defaultSessionGrantTTL),
	}
	switch {
	case payload.Kind == "coding_patch_approval_v1" && (toolName == "apply_patch" || toolName == "edit_file" || toolName == "replace_file"):
		paths, valid := normalizeGrantPaths(payload.Files)
		if !valid || len(paths) == 0 {
			return PermissionGrant{}, errors.New("the pending patch has no grantable paths")
		}
		grant.Action, grant.Paths = PermissionActionModify, paths
	case payload.Kind == "coding_check_approval_v1" && toolName == "run_checks" && strings.TrimSpace(payload.PlanID) != "":
		grant.Action = PermissionExecutePlanAction(payload.PlanID)
	case payload.Kind == "coding_lsp_start_approval_v1" && isNavigationTool(toolName) && payload.RequestedTool == toolName && payload.GrantToolName == "language_server" && validLanguageGrant(payload.Language):
		grant.ToolName = payload.GrantToolName
		grant.Action = PermissionStartLanguageServerAction(payload.Language)
	default:
		return PermissionGrant{}, errors.New("the pending approval does not support session grants")
	}
	seed, _ := json.Marshal(struct {
		SessionID   SessionID
		TurnID      TurnID
		InterruptID string
		ToolName    string
		Action      string
		Paths       []string
	}{SessionID: product.ID, TurnID: request.TurnID, InterruptID: request.InterruptID, ToolName: grant.ToolName, Action: grant.Action, Paths: grant.Paths})
	digest := sha256.Sum256(seed)
	grant.ID = "grant_" + hex.EncodeToString(digest[:16])
	if err := ValidatePermissionGrants([]PermissionGrant{grant}); err != nil {
		return PermissionGrant{}, err
	}
	return grant, nil
}

func isNavigationTool(value string) bool {
	switch value {
	case "find_definition", "find_references", "get_diagnostics", "document_symbols":
		return true
	default:
		return false
	}
}

func validLanguageGrant(value string) bool {
	return value == "go" || value == "python" || value == "node"
}

func appendPermissionGrant(product *Session, grant PermissionGrant) (bool, error) {
	if product == nil {
		return false, errors.New("permission grant session is unavailable")
	}
	for _, existing := range product.PermissionGrants {
		if existing.ID == grant.ID {
			return false, nil
		}
	}
	if len(product.PermissionGrants) >= maxPermissionGrants {
		return false, errors.New("the session permission audit limit has been reached")
	}
	grant.Paths = append([]string(nil), grant.Paths...)
	product.PermissionGrants = append(product.PermissionGrants, grant)
	return true, nil
}

func clonePermissionGrants(values []PermissionGrant) []PermissionGrant {
	cloned := append([]PermissionGrant(nil), values...)
	for index := range cloned {
		cloned[index].Paths = append([]string(nil), cloned[index].Paths...)
	}
	return cloned
}

func toolScope(product Session, worktree Worktree) ToolScope {
	return ToolScope{
		SessionID: product.ID, WorkspaceID: product.WorkspaceID, WorktreeID: product.WorktreeID,
		WorktreeRoot: worktree.Root, PermissionMode: product.PermissionMode,
		PermissionGrants: clonePermissionGrants(product.PermissionGrants), SensitivePaths: append([]string(nil), product.SensitivePaths...),
	}
}
