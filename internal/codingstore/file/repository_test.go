package file

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	agentsession "github.com/eaglc/codepilot/internal/agent/session"
	"github.com/eaglc/codepilot/internal/codingagent"
)

func TestRepositoryPersistsProductBindingsAcrossRestart(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	repository, err := NewRepository(root)
	if err != nil {
		t.Fatalf("new repository: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	workspace := codingagent.Workspace{ID: "workspace-1", DisplayName: "repo", GitCommonDir: filepath.Join(root, ".git"), Trusted: true, CreatedAt: now, UpdatedAt: now}
	if err := repository.SaveWorkspace(context.Background(), workspace); err != nil {
		t.Fatalf("save workspace: %v", err)
	}
	worktree := codingagent.Worktree{ID: "worktree-1", WorkspaceID: workspace.ID, Root: root, GitDir: workspace.GitCommonDir, CreatedAt: now, LastUsedAt: now}
	if err := repository.SaveWorktree(context.Background(), worktree); err != nil {
		t.Fatalf("save worktree: %v", err)
	}
	session := codingagent.Session{
		ID: "coding-1", AgentSessionID: agentsession.ID("agent-1"), WorkspaceID: workspace.ID, WorktreeID: worktree.ID,
		ProviderProfileID: "provider-1", ModelID: "model-1", PermissionMode: codingagent.PermissionAsk, CreatedAt: now, UpdatedAt: now,
		SensitivePaths: []string{"private-data"},
		PermissionGrants: []codingagent.PermissionGrant{{
			ID: "grant-1", Scope: codingagent.PermissionGrantSession, ToolName: "apply_patch", Action: codingagent.PermissionActionModify,
			Paths: []string{"main.go"}, SourceTurnID: "turn-1", SourceInterruptID: "approval-1", CreatedAt: now, ExpiresAt: now.Add(time.Hour),
		}},
	}
	if err := repository.CreateSession(context.Background(), session); err != nil {
		t.Fatalf("create session: %v", err)
	}
	reopened, err := NewRepository(root)
	if err != nil {
		t.Fatalf("reopen repository: %v", err)
	}
	loaded, err := reopened.LoadSession(context.Background(), session.ID)
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	if loaded.AgentSessionID != session.AgentSessionID || loaded.WorktreeID != session.WorktreeID || len(loaded.PermissionGrants) != 1 || loaded.PermissionGrants[0].Paths[0] != "main.go" || len(loaded.SensitivePaths) != 1 || loaded.SensitivePaths[0] != "private-data" {
		t.Fatalf("loaded session = %#v", loaded)
	}
	sessions, err := reopened.ListSessions(context.Background())
	if err != nil || len(sessions) != 1 {
		t.Fatalf("list sessions = %#v, %v", sessions, err)
	}
	loaded.WorktreeID = "different"
	if err := reopened.SaveSession(context.Background(), loaded); err == nil {
		t.Fatal("expected immutable binding error")
	}
}

func TestRepositoryRelocatesWorktreeExplicitlyAndIdempotently(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	repository, err := NewRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	workspace := codingagent.Workspace{ID: "workspace-relocate", DisplayName: "repo", GitCommonDir: filepath.Join(root, "old", ".git"), RepositoryFingerprint: "git-anchor-v1:sha1:" + strings.Repeat("a", 40), Trusted: true, CreatedAt: now, UpdatedAt: now}
	if err := repository.SaveWorkspace(context.Background(), workspace); err != nil {
		t.Fatal(err)
	}
	worktree := codingagent.Worktree{ID: "worktree-relocate", WorkspaceID: workspace.ID, Root: filepath.Join(root, "old"), GitDir: workspace.GitCommonDir, CreatedAt: now, LastUsedAt: now}
	if err := repository.SaveWorktree(context.Background(), worktree); err != nil {
		t.Fatal(err)
	}
	newRoot := filepath.Join(root, "new")
	newGitDir := filepath.Join(newRoot, ".git")
	updatedAt := now.Add(time.Minute)
	updated, err := repository.RelocateWorktree(context.Background(), worktree.ID, worktree.Root, newRoot, newGitDir, newGitDir, updatedAt)
	if err != nil || updated.Root != newRoot || updated.GitDir != newGitDir {
		t.Fatalf("relocate = %#v, %v", updated, err)
	}
	if _, err := repository.RelocateWorktree(context.Background(), worktree.ID, worktree.Root, newRoot, newGitDir, newGitDir, updatedAt); err != nil {
		t.Fatalf("idempotent relocate: %v", err)
	}
	loadedWorkspace, err := repository.LoadWorkspace(context.Background(), workspace.ID)
	if err != nil || loadedWorkspace.GitCommonDir != newGitDir || loadedWorkspace.RepositoryFingerprint != workspace.RepositoryFingerprint {
		t.Fatalf("relocated workspace = %#v, %v", loadedWorkspace, err)
	}
	workspace = loadedWorkspace
	workspace.RepositoryFingerprint = "git-anchor-v1:sha1:" + strings.Repeat("b", 40)
	if err := repository.SaveWorkspace(context.Background(), workspace); err == nil {
		t.Fatal("expected immutable fingerprint rejection")
	}
}

func TestRepositoryRecoversInterruptedWorktreeRelocation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	repository, err := NewRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	workspace := codingagent.Workspace{ID: "workspace-recovery", DisplayName: "repo", GitCommonDir: filepath.Join(root, "old", ".git"), Trusted: true, CreatedAt: now, UpdatedAt: now}
	worktree := codingagent.Worktree{ID: "worktree-recovery", WorkspaceID: workspace.ID, Root: filepath.Join(root, "old"), GitDir: workspace.GitCommonDir, CreatedAt: now, LastUsedAt: now}
	if err := repository.SaveWorkspace(context.Background(), workspace); err != nil {
		t.Fatal(err)
	}
	if err := repository.SaveWorktree(context.Background(), worktree); err != nil {
		t.Fatal(err)
	}
	afterWorktree := worktree
	afterWorktree.Root = filepath.Join(root, "new")
	afterWorktree.GitDir = filepath.Join(afterWorktree.Root, ".git")
	afterWorktree.LastUsedAt = now.Add(time.Minute)
	afterWorkspace := workspace
	afterWorkspace.GitCommonDir = afterWorktree.GitDir
	afterWorkspace.UpdatedAt = afterWorktree.LastUsedAt
	intent := worktreeRelocationIntent{
		ID: worktree.ID, BeforeWorktree: worktree, AfterWorktree: afterWorktree,
		BeforeWorkspace: workspace, AfterWorkspace: afterWorkspace,
	}
	intentPath, _ := repository.path(worktreeRelocationDirectory, string(worktree.ID))
	if err := writeEnvelope(intentPath, intent); err != nil {
		t.Fatal(err)
	}
	worktreePath, _ := repository.path("coding-worktrees", string(worktree.ID))
	if err := writeEnvelope(worktreePath, afterWorktree); err != nil {
		t.Fatal(err)
	}

	reopened, err := NewRepository(root)
	if err != nil {
		t.Fatalf("reopen and recover: %v", err)
	}
	loadedWorktree, err := reopened.LoadWorktree(context.Background(), worktree.ID)
	if err != nil || !reflect.DeepEqual(loadedWorktree, afterWorktree) {
		t.Fatalf("recovered worktree = %#v, %v", loadedWorktree, err)
	}
	loadedWorkspace, err := reopened.LoadWorkspace(context.Background(), workspace.ID)
	if err != nil || !reflect.DeepEqual(loadedWorkspace, afterWorkspace) {
		t.Fatalf("recovered workspace = %#v, %v", loadedWorkspace, err)
	}
	if _, err := os.Stat(intentPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("completed intent still exists: %v", err)
	}
}

func TestRepositoryPersistsAndCompletesSessionCreationIntent(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	repository, err := NewRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	workspace := codingagent.Workspace{ID: "workspace-intent", DisplayName: "repo", GitCommonDir: filepath.Join(root, ".git"), Trusted: true, CreatedAt: now, UpdatedAt: now}
	if err := repository.SaveWorkspace(context.Background(), workspace); err != nil {
		t.Fatal(err)
	}
	worktree := codingagent.Worktree{ID: "worktree-intent", WorkspaceID: workspace.ID, Root: root, GitDir: workspace.GitCommonDir, CreatedAt: now, LastUsedAt: now}
	if err := repository.SaveWorktree(context.Background(), worktree); err != nil {
		t.Fatal(err)
	}
	session := codingagent.Session{ID: "coding-intent", AgentSessionID: "agent-intent", WorkspaceID: workspace.ID, WorktreeID: worktree.ID, ProviderProfileID: "provider", ModelID: "model", PermissionMode: codingagent.PermissionAsk, CreatedAt: now, UpdatedAt: now}
	intent := codingagent.SessionCreationIntent{ID: codingagent.CreationIntentID(session.ID), Session: session, Status: codingagent.SessionCreationPending, CreatedAt: now, UpdatedAt: now}
	if err := repository.BeginSessionCreation(context.Background(), intent); err != nil {
		t.Fatalf("begin intent: %v", err)
	}
	reopened, err := NewRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	values, err := reopened.ListSessionCreationIntents(context.Background())
	if err != nil || len(values) != 1 || values[0].Status != codingagent.SessionCreationPending {
		t.Fatalf("persisted intents = %#v, %v", values, err)
	}
	completedAt := now.Add(time.Second)
	if err := reopened.CompleteSessionCreation(context.Background(), intent.ID, completedAt); err != nil {
		t.Fatalf("complete intent: %v", err)
	}
	values, err = reopened.ListSessionCreationIntents(context.Background())
	if err != nil || len(values) != 1 || values[0].Status != codingagent.SessionCreationCompleted || !values[0].CompletedAt.Equal(completedAt) {
		t.Fatalf("completed intents = %#v, %v", values, err)
	}
}
