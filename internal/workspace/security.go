package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/eaglc/codepilot/internal/session"
)

func canonicalExistingDirectory(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", workspaceAppError(session.ErrInvalidInput, "workspace.resolve_path", "A worktree path is required.", nil)
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", workspaceAppError(session.ErrWorkspaceUnavailable, "workspace.resolve_path", "The worktree path could not be resolved.", err)
	}
	realPath, err := filepath.EvalSymlinks(filepath.Clean(absolute))
	if err != nil {
		return "", workspaceAppError(session.ErrWorkspaceUnavailable, "workspace.resolve_path", "The worktree path is unavailable.", fmt.Errorf("evaluate symbolic links for %q: %w", absolute, err))
	}
	info, err := os.Stat(realPath)
	if err != nil {
		return "", workspaceAppError(session.ErrWorkspaceUnavailable, "workspace.resolve_path", "The worktree path is unavailable.", err)
	}
	if !info.IsDir() {
		return "", workspaceAppError(session.ErrInvalidInput, "workspace.resolve_path", "The worktree path must be a directory.", nil)
	}
	return filepath.Clean(realPath), nil
}

func secureExistingFile(root string, relativePath string) (string, string, error) {
	absolute, relative, err := securePath(root, relativePath, false)
	if err != nil {
		return "", "", err
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return "", "", workspaceAppError(session.ErrNotFound, "workspace.read_file", "The requested file is unavailable.", err)
	}
	if !info.Mode().IsRegular() {
		return "", "", workspaceAppError(session.ErrInvalidInput, "workspace.read_file", "The requested path is not a regular file.", nil)
	}
	return absolute, relative, nil
}

// ResolveSafeExistingFile returns a canonical worktree file and normalized
// relative path after applying the shared boundary and sensitive-file rules.
func ResolveSafeExistingFile(root string, relativePath string) (string, string, error) {
	return secureExistingFile(root, relativePath)
}

func securePath(root string, relativePath string, allowMissing bool) (string, string, error) {
	canonicalRoot, err := canonicalExistingDirectory(root)
	if err != nil {
		return "", "", err
	}
	if strings.TrimSpace(relativePath) == "" || filepath.IsAbs(relativePath) || filepath.VolumeName(relativePath) != "" {
		return "", "", unsafePathError("Only worktree-relative paths are allowed.")
	}
	cleanRelative := filepath.Clean(filepath.FromSlash(relativePath))
	if cleanRelative == "." || cleanRelative == ".." || strings.HasPrefix(cleanRelative, ".."+string(filepath.Separator)) {
		return "", "", unsafePathError("The requested path leaves the worktree.")
	}
	components := strings.FieldsFunc(cleanRelative, func(value rune) bool {
		return value == '/' || value == '\\'
	})
	for _, component := range components {
		if strings.EqualFold(component, ".git") {
			return "", "", unsafePathError("Git metadata cannot be accessed.")
		}
		if isSensitiveDirectory(component) {
			return "", "", unsafePathError("Sensitive credential directories cannot be accessed.")
		}
	}
	if isSensitiveFileName(filepath.Base(cleanRelative)) {
		return "", "", unsafePathError("Sensitive credential files cannot be accessed.")
	}

	absolute := filepath.Join(canonicalRoot, cleanRelative)
	lexicalRelative, err := filepath.Rel(canonicalRoot, absolute)
	if err != nil || pathLeavesRoot(lexicalRelative) {
		return "", "", unsafePathError("The requested path leaves the worktree.")
	}

	resolvedTarget := absolute
	if allowMissing {
		resolvedTarget, err = resolveExistingAncestor(absolute)
	} else {
		resolvedTarget, err = filepath.EvalSymlinks(absolute)
	}
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", "", workspaceAppError(session.ErrNotFound, "workspace.resolve_path", "The requested path is unavailable.", err)
		}
		return "", "", workspaceAppError(session.ErrWorkspaceUnavailable, "workspace.resolve_path", "The requested path could not be resolved safely.", err)
	}
	realRelative, err := filepath.Rel(canonicalRoot, resolvedTarget)
	if err != nil || pathLeavesRoot(realRelative) {
		return "", "", unsafePathError("A symbolic link leaves the worktree.")
	}

	return filepath.Clean(absolute), filepath.ToSlash(lexicalRelative), nil
}

func resolveExistingAncestor(value string) (string, error) {
	candidate := filepath.Clean(value)
	for {
		resolved, err := filepath.EvalSymlinks(candidate)
		if err == nil {
			return filepath.Clean(resolved), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(candidate)
		if parent == candidate {
			return "", err
		}
		candidate = parent
	}
}

func pathLeavesRoot(relative string) bool {
	return relative == ".." || filepath.IsAbs(relative) || strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func isSensitiveDirectory(name string) bool {
	switch strings.ToLower(name) {
	case ".git", ".agents", ".codex", ".ssh", ".aws", ".azure", ".kube", ".gnupg":
		return true
	default:
		return false
	}
}

func isSensitiveFileName(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	if lower == ".env" || strings.HasPrefix(lower, ".env.") {
		return true
	}
	switch lower {
	case ".netrc", ".npmrc", ".pypirc", "id_rsa", "id_dsa", "id_ecdsa", "id_ed25519":
		return true
	}
	switch strings.ToLower(filepath.Ext(lower)) {
	case ".pem", ".key", ".p12", ".pfx", ".crt", ".cer", ".der":
		return true
	}
	extension := strings.ToLower(filepath.Ext(lower))
	stem := strings.TrimSuffix(lower, extension)
	switch stem {
	case "credential", "credentials", "secret", "secrets", "token", "tokens", "access_token", "refresh_token":
		switch extension {
		case "", ".json", ".yaml", ".yml", ".toml", ".txt", ".ini", ".conf", ".config":
			return true
		}
	}
	return false
}

func unsafePathError(message string) error {
	return workspaceAppError(session.ErrPermissionDenied, "workspace.validate_path", message, nil)
}

func workspaceAppError(code session.ErrorCode, operation string, message string, cause error) error {
	return &session.AppError{
		Code:        code,
		Operation:   operation,
		UserMessage: message,
		Cause:       cause,
		Retryable:   code == session.ErrWorkspaceUnavailable,
	}
}

func wrapGitError(operation string, err error) error {
	return workspaceAppError(session.ErrWorkspaceUnavailable, operation, "The Git worktree is unavailable.", fmt.Errorf("%s: %w", operation, err))
}
