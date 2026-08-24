package codingstore_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/eaglc/codepilot/internal/codingagent"
	filecodingstore "github.com/eaglc/codepilot/internal/codingstore/file"
	memorycodingstore "github.com/eaglc/codepilot/internal/codingstore/memory"
)

type repository interface {
	codingagent.WorkspaceRepository
	codingagent.SessionRepository
}

func TestRepositoriesEnforceTheSamePersistenceContract(t *testing.T) {
	tests := []struct {
		name string
		new  func(*testing.T) repository
	}{
		{name: "memory", new: func(*testing.T) repository { return memorycodingstore.NewRepository() }},
		{name: "file", new: func(t *testing.T) repository {
			value, err := filecodingstore.NewRepository(filepath.Join(t.TempDir(), "state"))
			if err != nil {
				t.Fatalf("new file repository: %v", err)
			}
			return value
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			testRepositoryContract(t, test.new(t))
		})
	}
}

func testRepositoryContract(t *testing.T, repository repository) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	root := filepath.Join(t.TempDir(), "worktree")
	gitDir := filepath.Join(root, ".git")
	workspace := codingagent.Workspace{
		ID: "workspace", DisplayName: "repository", GitCommonDir: gitDir,
		RepositoryFingerprint: "git-anchor-v1:sha1:0123456789abcdef0123456789abcdef01234567",
		Trusted:               true, CreatedAt: now, UpdatedAt: now,
	}
	invalidWorkspace := workspace
	invalidWorkspace.DisplayName = ""
	if err := repository.SaveWorkspace(ctx, invalidWorkspace); err == nil {
		t.Fatal("SaveWorkspace accepted incomplete workspace")
	}
	if err := repository.SaveWorkspace(ctx, workspace); err != nil {
		t.Fatalf("SaveWorkspace: %v", err)
	}
	changedWorkspace := workspace
	changedWorkspace.CreatedAt = now.Add(time.Second)
	if err := repository.SaveWorkspace(ctx, changedWorkspace); err == nil {
		t.Fatal("SaveWorkspace accepted immutable creation-time change")
	}

	worktree := codingagent.Worktree{ID: "worktree", WorkspaceID: workspace.ID, Root: root, GitDir: gitDir, CreatedAt: now, LastUsedAt: now}
	invalidWorktree := worktree
	invalidWorktree.Root = "relative"
	if err := repository.SaveWorktree(ctx, invalidWorktree); err == nil {
		t.Fatal("SaveWorktree accepted a relative root")
	}
	orphanWorktree := worktree
	orphanWorktree.ID = "orphan"
	orphanWorktree.WorkspaceID = "missing"
	if err := repository.SaveWorktree(ctx, orphanWorktree); err == nil {
		t.Fatal("SaveWorktree accepted a missing workspace")
	}
	if err := repository.SaveWorktree(ctx, worktree); err != nil {
		t.Fatalf("SaveWorktree: %v", err)
	}
	changedWorktree := worktree
	changedWorktree.Root = filepath.Join(t.TempDir(), "changed")
	changedWorktree.GitDir = filepath.Join(changedWorktree.Root, ".git")
	if err := repository.SaveWorktree(ctx, changedWorktree); err == nil {
		t.Fatal("SaveWorktree accepted immutable path change")
	}

	session := codingagent.Session{
		ID: "session", AgentSessionID: "agent", WorkspaceID: workspace.ID, WorktreeID: worktree.ID,
		ProviderProfileID: "provider", ModelID: "model", PermissionMode: codingagent.PermissionAsk,
		CreatedAt: now, UpdatedAt: now,
	}
	invalidSession := session
	invalidSession.ModelID = ""
	if err := repository.CreateSession(ctx, invalidSession); err == nil {
		t.Fatal("CreateSession accepted an incomplete model binding")
	}
	wrongBinding := session
	wrongBinding.ID = "wrong-binding"
	wrongBinding.WorkspaceID = "another"
	if err := repository.CreateSession(ctx, wrongBinding); err == nil {
		t.Fatal("CreateSession accepted a mismatched worktree binding")
	}
	if err := repository.CreateSession(ctx, session); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	changedSession := session
	changedSession.WorktreeID = "another"
	if err := repository.SaveSession(ctx, changedSession); err == nil {
		t.Fatal("SaveSession accepted immutable binding change")
	}

	intent := codingagent.SessionCreationIntent{
		ID: "not-deterministic", Session: session, Status: codingagent.SessionCreationPending,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := repository.BeginSessionCreation(ctx, intent); err == nil {
		t.Fatal("BeginSessionCreation accepted a non-deterministic identity")
	}
}
