package codingagent

import (
	"context"
	"path/filepath"
	"strings"
	"time"
)

func (s *Service) projectWorkspaceSnapshot(ctx context.Context, snapshot *Snapshot) {
	if snapshot == nil {
		return
	}
	value := WorkspaceSnapshot{
		WorkspaceID: snapshot.Session.WorkspaceID,
		WorktreeID:  snapshot.Session.WorktreeID,
		DisplayName: string(snapshot.Session.WorkspaceID),
		ObservedAt:  time.Now().UTC(),
	}
	if s == nil || s.deps.Worktrees == nil || snapshot.Session.WorktreeID == "" {
		snapshot.Workspace = value
		return
	}
	worktree, err := s.deps.Worktrees.LoadWorktree(ctx, snapshot.Session.WorktreeID)
	if err != nil {
		snapshot.Workspace = value
		return
	}
	if displayName := safeWorkspaceLabel(filepath.Base(worktree.Root)); displayName != "" && displayName != "." {
		value.DisplayName = displayName
	}
	branch, branchErr := runReadOnlyGit(ctx, worktree.Root, maxWorkspaceStatusBytes, "symbolic-ref", "--quiet", "--short", "HEAD")
	if branchErr != nil {
		value.Detached = true
		branch, branchErr = runReadOnlyGit(ctx, worktree.Root, maxWorkspaceStatusBytes, "rev-parse", "--short", "HEAD")
	}
	if branchErr != nil {
		snapshot.Workspace = value
		return
	}
	head, headErr := runReadOnlyGit(ctx, worktree.Root, maxWorkspaceStatusBytes, "rev-parse", "--short", "HEAD")
	status, statusErr := runReadOnlyGit(ctx, worktree.Root, maxWorkspaceStatusBytes, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if headErr != nil || statusErr != nil {
		snapshot.Workspace = value
		return
	}
	changed := parseGitStatusPaths(status)
	value.Branch = safeWorkspaceLabel(strings.TrimSpace(branch))
	value.Head = safeWorkspaceLabel(strings.TrimSpace(head))
	value.Available = true
	value.Dirty = len(changed) != 0
	value.ChangedFiles = len(changed)
	snapshot.Workspace = value
}

func safeWorkspaceLabel(value string) string {
	value = strings.Map(func(character rune) rune {
		if character < ' ' || character == '\x7f' {
			return -1
		}
		return character
	}, strings.TrimSpace(value))
	return boundedUTF8(value, 256)
}
