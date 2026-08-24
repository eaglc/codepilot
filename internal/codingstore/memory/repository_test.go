package memory

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eaglc/codepilot/internal/codingagent"
)

func TestRelocateWorktreeRejectsMissingWorkspaceWithoutMutation(t *testing.T) {
	now := time.Now().UTC()
	root := filepath.Join(t.TempDir(), "original")
	newRoot := filepath.Join(t.TempDir(), "relocated")
	repository := NewRepository()
	worktree := codingagent.Worktree{
		ID: "worktree", WorkspaceID: "missing", Root: root, GitDir: filepath.Join(root, ".git"),
		CreatedAt: now, LastUsedAt: now,
	}
	// Seed the inconsistent state directly because SaveWorktree now prevents it;
	// RelocateWorktree must still fail safely for state written by older builds.
	repository.worktrees[worktree.ID] = worktree
	_, err := repository.RelocateWorktree(
		context.Background(), worktree.ID, root, newRoot, filepath.Join(newRoot, ".git"), filepath.Join(newRoot, ".git"), now.Add(time.Minute),
	)
	if err == nil || !strings.Contains(err.Error(), "workspace \"missing\" not found") {
		t.Fatalf("RelocateWorktree() error = %v", err)
	}
	loaded, loadErr := repository.LoadWorktree(context.Background(), worktree.ID)
	if loadErr != nil {
		t.Fatalf("load worktree: %v", loadErr)
	}
	if loaded.Root != root || loaded.GitDir != worktree.GitDir || !loaded.LastUsedAt.Equal(now) {
		t.Fatalf("failed relocation mutated worktree: %#v", loaded)
	}
	workspaces, listErr := repository.ListWorkspaces(context.Background())
	if listErr != nil || len(workspaces) != 0 {
		t.Fatalf("failed relocation created workspace: values=%#v err=%v", workspaces, listErr)
	}
}

func TestSessionPermissionAuditIsDefensivelyCopied(t *testing.T) {
	now := time.Now().UTC()
	root := filepath.Join(t.TempDir(), "worktree")
	session := codingagent.Session{
		ID: "session", AgentSessionID: "agent", WorkspaceID: "workspace", WorktreeID: "worktree",
		ProviderProfileID: "provider", ModelID: "model", PermissionMode: codingagent.PermissionAsk,
		CreatedAt: now, UpdatedAt: now,
		SensitivePaths: []string{"private-data"},
		PermissionGrants: []codingagent.PermissionGrant{{
			ID: "grant", Scope: codingagent.PermissionGrantSession, ToolName: "apply_patch", Action: codingagent.PermissionActionModify,
			Paths: []string{"main.go"}, SourceTurnID: "turn", SourceInterruptID: "approval", CreatedAt: now, ExpiresAt: now.Add(time.Hour),
		}},
	}
	repository := NewRepository()
	if err := repository.SaveWorkspace(context.Background(), codingagent.Workspace{ID: "workspace", DisplayName: "repository", GitCommonDir: filepath.Join(root, ".git"), CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("save workspace: %v", err)
	}
	if err := repository.SaveWorktree(context.Background(), codingagent.Worktree{ID: "worktree", WorkspaceID: "workspace", Root: root, GitDir: filepath.Join(root, ".git"), CreatedAt: now, LastUsedAt: now}); err != nil {
		t.Fatalf("save worktree: %v", err)
	}
	if err := repository.CreateSession(context.Background(), session); err != nil {
		t.Fatalf("create session: %v", err)
	}
	session.PermissionGrants[0].Paths[0] = "caller-mutated.go"
	session.SensitivePaths[0] = "caller-mutated"
	loaded, err := repository.LoadSession(context.Background(), session.ID)
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	if loaded.PermissionGrants[0].Paths[0] != "main.go" || loaded.SensitivePaths[0] != "private-data" {
		t.Fatalf("create retained caller slice: %#v", loaded)
	}
	loaded.PermissionGrants[0].Paths[0] = "load-mutated.go"
	loaded.SensitivePaths[0] = "load-mutated"
	reloaded, err := repository.LoadSession(context.Background(), session.ID)
	if err != nil || reloaded.PermissionGrants[0].Paths[0] != "main.go" || reloaded.SensitivePaths[0] != "private-data" {
		t.Fatalf("load exposed repository slice: session=%#v err=%v", reloaded, err)
	}
}
