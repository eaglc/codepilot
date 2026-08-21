package sessionstore

import (
	"errors"
	"path/filepath"
	"strings"
)

const currentStoreVersion = 1

type storeLayout struct {
	stateDir string
}

func newStoreLayout(stateDir string) storeLayout {
	return storeLayout{stateDir: stateDir}
}

func (l storeLayout) registryPath() string {
	return filepath.Join(l.stateDir, "registry.json")
}

func (l storeLayout) workspacesDir() string {
	return filepath.Join(l.stateDir, "workspaces")
}

func (l storeLayout) workspaceDir(id string) string {
	return filepath.Join(l.workspacesDir(), id)
}

func (l storeLayout) workspacePath(id string) string {
	return filepath.Join(l.workspaceDir(id), "workspace.json")
}

func (l storeLayout) worktreesDir(workspaceID string) string {
	return filepath.Join(l.workspaceDir(workspaceID), "worktrees")
}

func (l storeLayout) worktreeDir(workspaceID string, worktreeID string) string {
	return filepath.Join(l.worktreesDir(workspaceID), worktreeID)
}

func (l storeLayout) worktreePath(workspaceID string, worktreeID string) string {
	return filepath.Join(l.worktreeDir(workspaceID, worktreeID), "worktree.json")
}

func (l storeLayout) sessionsDir(workspaceID string, worktreeID string) string {
	return filepath.Join(l.worktreeDir(workspaceID, worktreeID), "sessions")
}

func (l storeLayout) sessionDir(workspaceID string, worktreeID string, sessionID string) string {
	return filepath.Join(l.sessionsDir(workspaceID, worktreeID), sessionID)
}

func (l storeLayout) sessionPath(workspaceID string, worktreeID string, sessionID string) string {
	return filepath.Join(l.sessionDir(workspaceID, worktreeID, sessionID), "session.json")
}

func validateStoreID(value string) error {
	if strings.TrimSpace(value) == "" {
		return errors.New("ID is empty")
	}
	if value == "." || value == ".." || filepath.Base(value) != value {
		return errors.New("ID contains a path component")
	}
	for _, character := range value {
		isLowercase := character >= 'a' && character <= 'z'
		isDigit := character >= '0' && character <= '9'
		if !isLowercase && !isDigit && character != '_' && character != '-' {
			return errors.New("ID contains an unsupported character")
		}
	}

	return nil
}
