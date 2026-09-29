package codingagent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

type snapshotWorktreeReader struct {
	worktree Worktree
	err      error
}

func (r snapshotWorktreeReader) LoadWorktree(context.Context, WorktreeID) (Worktree, error) {
	return r.worktree, r.err
}

func TestProjectWorkspaceSnapshotReportsBoundedGitState(t *testing.T) {
	root := initializePlanWorkspaceGit(t)
	if err := os.WriteFile(filepath.Join(root, "planned.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "untracked secret.txt"), []byte("not projected\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	service := &Service{deps: Dependencies{Worktrees: snapshotWorktreeReader{worktree: Worktree{ID: "worktree", WorkspaceID: "workspace", Root: root}}}}
	snapshot := Snapshot{Session: Session{WorkspaceID: "workspace", WorktreeID: "worktree"}}

	service.projectWorkspaceSnapshot(context.Background(), &snapshot)

	workspace := snapshot.Workspace
	if !workspace.Available || !workspace.Dirty || workspace.ChangedFiles != 2 || workspace.Branch == "" || workspace.Head == "" {
		t.Fatalf("workspace snapshot = %#v", workspace)
	}
	if workspace.DisplayName != filepath.Base(root) || workspace.WorkspaceID != "workspace" || workspace.WorktreeID != "worktree" || workspace.ObservedAt.IsZero() {
		t.Fatalf("workspace identity = %#v", workspace)
	}
}

func TestProjectWorkspaceSnapshotReportsDetachedHeadWithoutFailingSnapshot(t *testing.T) {
	root := initializePlanWorkspaceGit(t)
	runPlanWorkspaceGit(t, root, "checkout", "--quiet", "--detach", "HEAD")
	service := &Service{deps: Dependencies{Worktrees: snapshotWorktreeReader{worktree: Worktree{ID: "worktree", WorkspaceID: "workspace", Root: root}}}}
	snapshot := Snapshot{Session: Session{WorkspaceID: "workspace", WorktreeID: "worktree"}}

	service.projectWorkspaceSnapshot(context.Background(), &snapshot)

	if !snapshot.Workspace.Available || !snapshot.Workspace.Detached || snapshot.Workspace.Branch != snapshot.Workspace.Head || snapshot.Workspace.Dirty {
		t.Fatalf("detached workspace snapshot = %#v", snapshot.Workspace)
	}
}
