package app

import (
	"context"
	"io"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestNewBuildsRunnableApplicationAndCloseIsIdempotent(t *testing.T) {
	worktree := t.TempDir()
	command := exec.Command("git", "init", "--quiet")
	command.Dir = worktree
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("initialize Git fixture: %v: %s", err, output)
	}
	root := t.TempDir()
	application, err := New(context.Background(), Options{
		WorkingDirectory: worktree,
		ConfigDir:        filepath.Join(root, "config"),
		StateDir:         filepath.Join(root, "state"),
		TrustWorkspace:   true,
	})
	if err != nil {
		t.Fatalf("build application: %v", err)
	}
	if application.model == nil || application.runtime.sessions == nil || application.runtime.snapshot.Session.ID == "" {
		t.Fatal("application did not activate a session and presentation")
	}
	if err := application.Close(); err != nil {
		t.Fatalf("close application: %v", err)
	}
	if err := application.Close(); err != nil {
		t.Fatalf("close application again: %v", err)
	}
}

func TestAppRunStartsAndHonorsContextCancellation(t *testing.T) {
	worktree := t.TempDir()
	command := exec.Command("git", "init", "--quiet")
	command.Dir = worktree
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("initialize Git fixture: %v: %s", err, output)
	}
	root := t.TempDir()
	application, err := New(context.Background(), Options{
		WorkingDirectory: worktree,
		ConfigDir:        filepath.Join(root, "config"),
		StateDir:         filepath.Join(root, "state"),
		TrustWorkspace:   true,
		DisableInput:     true,
		Output:           io.Discard,
	})
	if err != nil {
		t.Fatalf("build application: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := application.Run(ctx); err != nil {
		t.Fatalf("run application: %v", err)
	}
	if err := application.Close(); err != nil {
		t.Fatalf("close application: %v", err)
	}
}

func TestNewRequiresTrustBeforeRegisteringWorktree(t *testing.T) {
	worktree := t.TempDir()
	command := exec.Command("git", "init", "--quiet")
	command.Dir = worktree
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("initialize Git fixture: %v: %s", err, output)
	}
	root := t.TempDir()
	_, err := New(context.Background(), Options{
		WorkingDirectory: worktree,
		ConfigDir:        filepath.Join(root, "config"),
		StateDir:         filepath.Join(root, "state"),
	})
	path, required := WorkspaceTrustRequired(err)
	if !required || path == "" {
		t.Fatalf("trust requirement was not returned: path=%q err=%v", path, err)
	}
}

func TestNewRejectsNonGitDirectoryWithSafeMessage(t *testing.T) {
	root := t.TempDir()
	_, err := New(context.Background(), Options{
		WorkingDirectory: root,
		ConfigDir:        filepath.Join(root, "config"),
		StateDir:         filepath.Join(root, "state"),
	})
	if err == nil {
		t.Fatal("non-Git directory was accepted")
	}
	if message := UserMessage(err); message != "The Git worktree is unavailable." {
		t.Fatalf("safe startup message = %q", message)
	}
}
