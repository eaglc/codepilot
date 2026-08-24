package codingagent

import (
	"errors"
	"path"
	"sort"
	"strings"
	"time"
)

// PermissionGrantScope describes how long an approval is intended to apply.
// Once is represented by the durable Agent interrupt resolution; only Session
// grants are stored in Coding product metadata.
type PermissionGrantScope string

const (
	PermissionGrantOnce    PermissionGrantScope = "once"
	PermissionGrantSession PermissionGrantScope = "session"
)

const (
	PermissionActionModify  = "modify"
	PermissionActionExecute = "execute"
)

// PermissionExecutePlanAction scopes execution permission to one trusted plan.
func PermissionExecutePlanAction(planID string) string {
	return PermissionActionExecute + ":" + strings.TrimSpace(planID)
}

// PermissionStartLanguageServerAction scopes execution to one allowlisted language server.
func PermissionStartLanguageServerAction(languageID string) string {
	return PermissionActionExecute + ":lsp:" + strings.TrimSpace(languageID)
}

// PermissionGrant is an append-only, secret-free authorization audit record.
// Paths are exact worktree-relative resources, never globs or absolute paths.
type PermissionGrant struct {
	ID                string               `json:"id"`
	Scope             PermissionGrantScope `json:"scope"`
	ToolName          string               `json:"tool_name"`
	Action            string               `json:"action"`
	Paths             []string             `json:"paths,omitempty"`
	SourceTurnID      TurnID               `json:"source_turn_id"`
	SourceInterruptID string               `json:"source_interrupt_id"`
	CreatedAt         time.Time            `json:"created_at"`
	ExpiresAt         time.Time            `json:"expires_at"`
	RevokedAt         time.Time            `json:"revoked_at,omitempty"`
}

// PermissionRequest is the immutable authorization fact consumed by tools.
type PermissionRequest struct {
	ToolName string
	Action   string
	Paths    []string
}

// PermissionGranted reports whether an active exact-scope grant covers every
// requested path. A pathless grant covers only a pathless request.
func PermissionGranted(grants []PermissionGrant, request PermissionRequest, at time.Time) bool {
	requested, ok := normalizeGrantPaths(request.Paths)
	if !ok || strings.TrimSpace(request.ToolName) == "" || strings.TrimSpace(request.Action) == "" {
		return false
	}
	for _, grant := range grants {
		if grant.Scope != PermissionGrantSession || grant.ToolName != request.ToolName || grant.Action != request.Action || !grant.RevokedAt.IsZero() || at.Before(grant.CreatedAt) || !at.Before(grant.ExpiresAt) {
			continue
		}
		granted, valid := normalizeGrantPaths(grant.Paths)
		if !valid || len(granted) != 0 && len(requested) == 0 || len(granted) == 0 && len(requested) != 0 {
			continue
		}
		allowed := make(map[string]struct{}, len(granted))
		for _, value := range granted {
			allowed[value] = struct{}{}
		}
		covered := true
		for _, value := range requested {
			if _, found := allowed[value]; !found {
				covered = false
				break
			}
		}
		if covered {
			return true
		}
	}
	return false
}

// ValidatePermissionGrants validates persisted session authorization audit.
func ValidatePermissionGrants(grants []PermissionGrant) error {
	seen := make(map[string]struct{}, len(grants))
	for _, grant := range grants {
		if strings.TrimSpace(grant.ID) == "" || grant.Scope != PermissionGrantSession || strings.TrimSpace(grant.ToolName) == "" || strings.TrimSpace(grant.Action) == "" || grant.SourceTurnID == "" || strings.TrimSpace(grant.SourceInterruptID) == "" || grant.CreatedAt.IsZero() || !grant.ExpiresAt.After(grant.CreatedAt) || grant.ExpiresAt.Sub(grant.CreatedAt) > 24*time.Hour {
			return errors.New("permission grant identity, scope, source, and lifetime are required")
		}
		if len(grant.ID) > 128 || len(grant.ToolName) > 256 || len(grant.Action) > 256 || len(grant.SourceTurnID) > 256 || len(grant.SourceInterruptID) > 256 || len(grant.Paths) > 128 {
			return errors.New("permission grant exceeds its metadata bounds")
		}
		if strings.ContainsAny(grant.ID+grant.ToolName+grant.Action+grant.SourceInterruptID, "\r\n\x00") {
			return errors.New("permission grant contains invalid control characters")
		}
		if _, exists := seen[grant.ID]; exists {
			return errors.New("permission grant ids must be unique")
		}
		seen[grant.ID] = struct{}{}
		normalized, valid := normalizeGrantPaths(grant.Paths)
		if !valid || len(normalized) != len(grant.Paths) {
			return errors.New("permission grant paths must be normalized worktree-relative paths")
		}
		for index := range normalized {
			if normalized[index] != grant.Paths[index] || len(grant.Paths[index]) > 4096 {
				return errors.New("permission grant paths must be normalized worktree-relative paths")
			}
		}
		if !grant.RevokedAt.IsZero() && grant.RevokedAt.Before(grant.CreatedAt) {
			return errors.New("permission grant revocation precedes creation")
		}
	}
	return nil
}

func normalizeGrantPaths(values []string) ([]string, bool) {
	normalized := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.ReplaceAll(strings.TrimSpace(value), "\\", "/")
		clean := path.Clean(value)
		if value == "" || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") || strings.ContainsRune(clean, 0) || clean != value {
			return nil, false
		}
		if _, exists := seen[clean]; exists {
			continue
		}
		seen[clean] = struct{}{}
		normalized = append(normalized, clean)
	}
	sort.Strings(normalized)
	return normalized, true
}
