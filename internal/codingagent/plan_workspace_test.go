package codingagent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestWorkspaceRevisionDetectsRelevantContentChangeWhenGitStatusIsUnchanged(t *testing.T) {
	root := initializePlanWorkspaceGit(t)
	writePlanWorkspaceFile(t, root, "planned.txt", "first modification\n")
	baseline, err := capturePlanWorkspaceRevision(context.Background(), "worktree-1", root, []string{"planned.txt"})
	if err != nil {
		t.Fatal(err)
	}
	writePlanWorkspaceFile(t, root, "planned.txt", "second modification with the same Git status\n")
	current, err := capturePlanWorkspaceRevision(context.Background(), "worktree-1", root, []string{"planned.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if baseline.StatusDigest != current.StatusDigest {
		t.Fatalf("status digest changed even though porcelain state stayed M: before=%s after=%s", baseline.StatusDigest, current.StatusDigest)
	}
	plan := workspaceDriftTestPlan(t, baseline)
	drift, err := classifyWorkspaceDrift(plan, current, WorkspaceDriftAtApproval, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if drift.Severity != WorkspaceDriftMaterial || drift.Reason != "relevant_path_changed" || len(drift.Paths) != 1 || drift.Paths[0] != "planned.txt" {
		t.Fatalf("relevant drift = %#v", drift)
	}
}

func TestWorkspaceRevisionAllowsUnrelatedWorktreeChange(t *testing.T) {
	root := initializePlanWorkspaceGit(t)
	baseline, err := capturePlanWorkspaceRevision(context.Background(), "worktree-1", root, []string{"planned.txt"})
	if err != nil {
		t.Fatal(err)
	}
	writePlanWorkspaceFile(t, root, "unrelated.txt", "changed outside Plan scope\n")
	current, err := capturePlanWorkspaceRevision(context.Background(), "worktree-1", root, []string{"planned.txt"})
	if err != nil {
		t.Fatal(err)
	}
	plan := workspaceDriftTestPlan(t, baseline)
	drift, err := classifyWorkspaceDrift(plan, current, WorkspaceDriftAtExecution, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if drift.Severity != WorkspaceDriftInformational || drift.Reason != "unrelated_workspace_changed" || len(drift.Paths) != 1 || drift.Paths[0] != "unrelated.txt" {
		t.Fatalf("unrelated drift = %#v", drift)
	}
}

func TestWorkspaceRevisionTreatsHeadAndWorktreeIdentityAsMaterial(t *testing.T) {
	root := initializePlanWorkspaceGit(t)
	baseline, err := capturePlanWorkspaceRevision(context.Background(), "worktree-1", root, []string{"planned.txt"})
	if err != nil {
		t.Fatal(err)
	}
	plan := workspaceDriftTestPlan(t, baseline)
	writePlanWorkspaceFile(t, root, "unrelated.txt", "new commit\n")
	runPlanWorkspaceGit(t, root, "add", "unrelated.txt")
	runPlanWorkspaceGit(t, root, "commit", "--quiet", "-m", "advance")
	current, err := capturePlanWorkspaceRevision(context.Background(), "worktree-1", root, []string{"planned.txt"})
	if err != nil {
		t.Fatal(err)
	}
	drift, err := classifyWorkspaceDrift(plan, current, WorkspaceDriftAtApproval, time.Now().UTC())
	if err != nil || drift.Severity != WorkspaceDriftMaterial || drift.Reason != "git_head_changed" {
		t.Fatalf("HEAD drift = %#v, %v", drift, err)
	}
	current = baseline
	current.IdentityDigest = digestText("different worktree")
	drift, err = classifyWorkspaceDrift(plan, current, WorkspaceDriftAtApproval, time.Now().UTC())
	if err != nil || drift.Severity != WorkspaceDriftMaterial || drift.Reason != "worktree_identity_changed" {
		t.Fatalf("identity drift = %#v, %v", drift, err)
	}
}

func initializePlanWorkspaceGit(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	runPlanWorkspaceGit(t, root, "init", "--quiet")
	runPlanWorkspaceGit(t, root, "config", "user.name", "CodePilot Test")
	runPlanWorkspaceGit(t, root, "config", "user.email", "test@example.invalid")
	writePlanWorkspaceFile(t, root, "planned.txt", "baseline\n")
	writePlanWorkspaceFile(t, root, "unrelated.txt", "baseline\n")
	runPlanWorkspaceGit(t, root, "add", "planned.txt", "unrelated.txt")
	runPlanWorkspaceGit(t, root, "commit", "--quiet", "-m", "initial")
	return root
}

func writePlanWorkspaceFile(t *testing.T, root, relative, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runPlanWorkspaceGit(t *testing.T, root string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", arguments, err, output)
	}
}

func workspaceDriftTestPlan(t *testing.T, revision WorkspaceRevision) Plan {
	t.Helper()
	now := time.Now().UTC()
	plan := Plan{
		ID: "plan-drift", TurnID: "turn-drift", Version: 1, Goal: "Change the planned file.",
		Scope: PlanScope{Included: []string{"planned.txt"}}, Findings: []string{"The file exists."}, Risks: []string{"Workspace drift could invalidate the change."},
		Steps:              []PlanStep{{ID: "change", Goal: "Change the planned file.", Files: []string{"planned.txt"}, Validation: []string{"Verify the result."}}},
		AcceptanceCriteria: []string{"The file is updated."}, RecommendedStrategy: ExecutionSingle,
		WorkspaceRelevant: true, CompletionMode: PlanCompletionExecute, WorkspaceRevision: revision, CreatedAt: now,
	}
	plan.Digest, _ = ComputePlanDigest(plan)
	if err := ValidatePlan(plan); err != nil {
		t.Fatalf("drift test Plan: %v", err)
	}
	return plan
}
