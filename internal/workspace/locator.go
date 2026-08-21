package workspace

import (
	"context"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/eaglc/codepilot/internal/session"
)

// ResolveWorktree discovers canonical Git paths without changing repository state.
func (s *Service) ResolveWorktree(ctx context.Context, pathValue string) (session.ResolvedWorktree, error) {
	directory, err := canonicalExistingDirectory(pathValue)
	if err != nil {
		return session.ResolvedWorktree{}, err
	}
	inside, _, err := gitLine(ctx, directory, s.limits.MaxGitOutputBytes, []int{0}, "rev-parse", "--is-inside-work-tree")
	if err != nil || inside != "true" {
		return session.ResolvedWorktree{}, wrapGitError("workspace.resolve_worktree", firstError(err, errNotGitWorktree))
	}
	root, _, err := gitLine(ctx, directory, s.limits.MaxGitOutputBytes, []int{0}, "rev-parse", "--path-format=absolute", "--show-toplevel")
	if err != nil {
		return session.ResolvedWorktree{}, wrapGitError("workspace.resolve_worktree", err)
	}
	gitDirectory, _, err := gitLine(ctx, directory, s.limits.MaxGitOutputBytes, []int{0}, "rev-parse", "--path-format=absolute", "--absolute-git-dir")
	if err != nil {
		return session.ResolvedWorktree{}, wrapGitError("workspace.resolve_worktree", err)
	}
	commonDirectory, _, err := gitLine(ctx, directory, s.limits.MaxGitOutputBytes, []int{0}, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return session.ResolvedWorktree{}, wrapGitError("workspace.resolve_worktree", err)
	}

	canonicalRoot, err := canonicalExistingDirectory(root)
	if err != nil {
		return session.ResolvedWorktree{}, err
	}
	canonicalGitDirectory, err := canonicalExistingDirectory(gitDirectory)
	if err != nil {
		return session.ResolvedWorktree{}, err
	}
	canonicalCommonDirectory, err := canonicalExistingDirectory(commonDirectory)
	if err != nil {
		return session.ResolvedWorktree{}, err
	}
	displayName := filepath.Base(canonicalRoot)
	if strings.TrimSpace(displayName) == "" {
		displayName = canonicalRoot
	}
	return session.ResolvedWorktree{
		DisplayName:  displayName,
		Root:         canonicalRoot,
		GitDir:       canonicalGitDirectory,
		GitCommonDir: canonicalCommonDirectory,
	}, nil
}

func (s *Service) verifiedWorktreeRoot(ctx context.Context, root string) (string, error) {
	canonicalRoot, err := canonicalExistingDirectory(root)
	if err != nil {
		return "", err
	}
	resolved, err := s.ResolveWorktree(ctx, canonicalRoot)
	if err != nil {
		return "", err
	}
	if !samePath(canonicalRoot, resolved.Root) {
		return "", workspaceAppError(session.ErrInvalidInput, "workspace.verify_root", "The path is inside a worktree but is not its root.", nil)
	}
	return resolved.Root, nil
}

func samePath(left string, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

var errNotGitWorktree = workspaceSentinelError("path is not inside a Git worktree")

type workspaceSentinelError string

func (e workspaceSentinelError) Error() string {
	return string(e)
}

func firstError(first error, fallback error) error {
	if first != nil {
		return first
	}
	return fallback
}
