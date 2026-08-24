package codingagent

import (
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
)

// ValidateWorkspace checks the shared persistence contract for a workspace.
func ValidateWorkspace(value Workspace) error {
	if value.ID == "" || value.DisplayName == "" || value.GitCommonDir == "" || value.CreatedAt.IsZero() || value.UpdatedAt.IsZero() {
		return errors.New("Coding workspace identity, name, Git directory, and timestamps are required")
	}
	if value.RepositoryFingerprint != "" {
		parts := strings.Split(value.RepositoryFingerprint, ":")
		if len(parts) != 3 || parts[0] != "git-anchor-v1" || (parts[1] != "sha1" && parts[1] != "sha256") {
			return errors.New("Coding workspace repository fingerprint is invalid")
		}
		expectedLength := 40
		if parts[1] == "sha256" {
			expectedLength = 64
		}
		if len(parts[2]) != expectedLength {
			return errors.New("Coding workspace repository fingerprint is invalid")
		}
		if _, err := hex.DecodeString(parts[2]); err != nil {
			return errors.New("Coding workspace repository fingerprint is invalid")
		}
	}
	return nil
}

// ValidateWorktree checks the shared persistence contract for a worktree.
func ValidateWorktree(value Worktree) error {
	if value.ID == "" || value.WorkspaceID == "" || value.Root == "" || value.GitDir == "" || value.CreatedAt.IsZero() || value.LastUsedAt.IsZero() {
		return errors.New("Coding worktree identity, paths, and timestamps are required")
	}
	if !filepath.IsAbs(value.Root) || !filepath.IsAbs(value.GitDir) || filepath.Clean(value.Root) != value.Root || filepath.Clean(value.GitDir) != value.GitDir {
		return errors.New("Coding worktree paths must be normalized and absolute")
	}
	return nil
}

// SameLocation compares normalized filesystem locations using platform case semantics.
func SameLocation(left, right string) bool {
	left, right = filepath.Clean(left), filepath.Clean(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

// ValidateSession checks the shared persistence contract for a Coding session.
func ValidateSession(value Session) error {
	if value.ID == "" || value.AgentSessionID == "" || value.WorkspaceID == "" || value.WorktreeID == "" || value.ProviderProfileID == "" || value.ModelID == "" || value.CreatedAt.IsZero() || value.UpdatedAt.IsZero() {
		return errors.New("Coding session bindings, model, and timestamps are required")
	}
	if value.PermissionMode != PermissionReadOnly && value.PermissionMode != PermissionAsk && value.PermissionMode != PermissionAutoEdit {
		return fmt.Errorf("Coding session permission mode %q is unsupported", value.PermissionMode)
	}
	if err := ValidatePermissionGrants(value.PermissionGrants); err != nil {
		return err
	}
	if err := ValidateSensitivePaths(value.SensitivePaths); err != nil {
		return err
	}
	return nil
}

// ValidateSessionCreationIntent checks an initial pending creation transaction.
func ValidateSessionCreationIntent(value SessionCreationIntent) error {
	if value.ID == "" || value.ID != CreationIntentID(value.Session.ID) {
		return errors.New("Coding session creation intent requires deterministic identity")
	}
	if value.Status != SessionCreationPending || value.CreatedAt.IsZero() || value.UpdatedAt.IsZero() || !value.CompletedAt.IsZero() {
		return errors.New("Coding session creation intent requires pending status and timestamps")
	}
	return ValidateSession(value.Session)
}
