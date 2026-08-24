package codingagent_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/eaglc/codepilot/internal/codingagent"
	workspaceinfra "github.com/eaglc/codepilot/internal/codingagent/workspace"
	codingmemory "github.com/eaglc/codepilot/internal/codingstore/memory"
)

type recordingCloser struct{ ids []string }

func (c *recordingCloser) CloseWorktree(_ context.Context, id string) error {
	c.ids = append(c.ids, id)
	return nil
}

func TestManagerListsUnavailableAndRelocatesMatchingHistory(t *testing.T) {
	ctx := context.Background()
	original := managerCommittedRepository(t)
	resolved, err := workspaceinfra.ResolveWorktree(ctx, original)
	if err != nil {
		t.Fatal(err)
	}
	repository := codingmemory.NewRepository()
	now := time.Now().UTC()
	workspace := codingagent.Workspace{ID: "workspace", DisplayName: "repo", GitCommonDir: resolved.GitCommonDir, RepositoryFingerprint: resolved.RepositoryFingerprint, Trusted: true, CreatedAt: now, UpdatedAt: now}
	worktree := codingagent.Worktree{ID: "worktree", WorkspaceID: workspace.ID, Root: resolved.Root, GitDir: resolved.GitDir, CreatedAt: now, LastUsedAt: now}
	if err := repository.SaveWorkspace(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	if err := repository.SaveWorktree(ctx, worktree); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(filepath.Dir(original), "moved")
	if err := os.Rename(original, moved); err != nil {
		t.Fatal(err)
	}
	closer := &recordingCloser{}
	manager, err := codingagent.NewWorkspaceManager(repository, closer)
	if err != nil {
		t.Fatal(err)
	}
	values, err := manager.ListWorkspaces(ctx)
	if err != nil || len(values) != 1 || len(values[0].Worktrees) != 1 || values[0].Worktrees[0].Availability != codingagent.WorktreeUnavailable {
		t.Fatalf("workspace catalog = %#v, %v", values, err)
	}
	relocated, err := manager.RelocateWorktree(ctx, codingagent.RelocateWorktreeRequest{WorktreeID: worktree.ID, NewPath: moved})
	if err != nil {
		t.Fatal(err)
	}
	if !managerSamePath(relocated.Root, moved) || len(closer.ids) != 1 || closer.ids[0] != string(worktree.ID) {
		t.Fatalf("relocated=%#v closed=%#v", relocated, closer.ids)
	}
	if _, err := manager.LoadWorktree(ctx, worktree.ID); err != nil {
		t.Fatalf("load relocated worktree: %v", err)
	}
}

func TestManagerRejectsRelocationToDifferentHistory(t *testing.T) {
	ctx := context.Background()
	original := managerCommittedRepository(t)
	resolved, _ := workspaceinfra.ResolveWorktree(ctx, original)
	repository := codingmemory.NewRepository()
	now := time.Now().UTC()
	workspace := codingagent.Workspace{ID: "workspace", DisplayName: "repo", GitCommonDir: resolved.GitCommonDir, RepositoryFingerprint: resolved.RepositoryFingerprint, Trusted: true, CreatedAt: now, UpdatedAt: now}
	worktree := codingagent.Worktree{ID: "worktree", WorkspaceID: workspace.ID, Root: resolved.Root, GitDir: resolved.GitDir, CreatedAt: now, LastUsedAt: now}
	_ = repository.SaveWorkspace(ctx, workspace)
	_ = repository.SaveWorktree(ctx, worktree)
	if err := os.Rename(original, filepath.Join(filepath.Dir(original), "gone")); err != nil {
		t.Fatal(err)
	}
	different := managerCommittedRepository(t)
	if err := os.WriteFile(filepath.Join(different, "main.go"), []byte("package different\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	managerRunGit(t, different, "add", "main.go")
	managerRunGit(t, different, "commit", "--quiet", "--amend", "--no-edit")
	manager, _ := codingagent.NewWorkspaceManager(repository, nil)
	if _, err := manager.RelocateWorktree(ctx, codingagent.RelocateWorktreeRequest{WorktreeID: worktree.ID, NewPath: different}); err == nil {
		t.Fatal("expected different-history relocation rejection")
	}
}

func TestManagerClosesPreviousWorktreeOnActivation(t *testing.T) {
	ctx := context.Background()
	repository := codingmemory.NewRepository()
	now := time.Now().UTC()
	closer := &recordingCloser{}
	manager, _ := codingagent.NewWorkspaceManager(repository, closer)
	for index, id := range []codingagent.WorktreeID{"one", "two"} {
		root := managerCommittedRepository(t)
		resolved, _ := workspaceinfra.ResolveWorktree(ctx, root)
		workspaceID := codingagent.WorkspaceID("workspace-" + string(rune('a'+index)))
		_ = repository.SaveWorkspace(ctx, codingagent.Workspace{ID: workspaceID, DisplayName: string(id), GitCommonDir: resolved.GitCommonDir, RepositoryFingerprint: resolved.RepositoryFingerprint, Trusted: true, CreatedAt: now, UpdatedAt: now})
		_ = repository.SaveWorktree(ctx, codingagent.Worktree{ID: id, WorkspaceID: workspaceID, Root: resolved.Root, GitDir: resolved.GitDir, CreatedAt: now, LastUsedAt: now})
	}
	if err := manager.ActivateWorktree(ctx, "one"); err != nil {
		t.Fatal(err)
	}
	if err := manager.ActivateWorktree(ctx, "two"); err != nil {
		t.Fatal(err)
	}
	if len(closer.ids) != 1 || closer.ids[0] != "one" {
		t.Fatalf("closed worktrees = %#v", closer.ids)
	}
}

func managerCommittedRepository(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	managerRunGit(t, root, "init", "--quiet")
	managerRunGit(t, root, "config", "user.name", "CodePilot Test")
	managerRunGit(t, root, "config", "user.email", "codepilot@example.invalid")
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	managerRunGit(t, root, "add", "main.go")
	managerRunGit(t, root, "commit", "--quiet", "-m", "initial")
	return filepath.Clean(root)
}

func managerRunGit(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, arguments...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", arguments, err, output)
	}
}

func managerSamePath(left, right string) bool {
	left, right = filepath.Clean(left), filepath.Clean(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}
