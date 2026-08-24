package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	agentsession "github.com/eaglc/codepilot/internal/agent/session"
	"github.com/eaglc/codepilot/internal/codingagent"
	codingfile "github.com/eaglc/codepilot/internal/codingstore/file"
	sessionfile "github.com/eaglc/codepilot/internal/sessionstore/file"
)

func TestDiagnoseMissingStateIsReadOnly(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "missing-state")
	report, err := DiagnoseState(context.Background(), MaintenanceOptions{StateDir: stateDir})
	if err != nil {
		t.Fatalf("diagnose missing state: %v", err)
	}
	if len(report.Issues) != 0 {
		t.Fatalf("report = %#v", report)
	}
	if _, err := os.Stat(stateDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only diagnosis created StateDir: %v", err)
	}
}

func TestDiagnoseRefusesConcurrentWriter(t *testing.T) {
	stateDir := t.TempDir()
	lease, err := sessionfile.AcquireStateLease(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	_, err = DiagnoseState(context.Background(), MaintenanceOptions{StateDir: stateDir})
	if !errors.Is(err, sessionfile.ErrStateInUse) {
		t.Fatalf("diagnose error = %v", err)
	}
}

func TestRepairStateCompletesFileBackedCreationIntent(t *testing.T) {
	ctx := context.Background()
	stateDir := t.TempDir()
	products, err := codingfile.NewRepository(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	agents, err := sessionfile.NewRepository(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	workspace := codingagent.Workspace{ID: "workspace-repair", DisplayName: "repo", GitCommonDir: filepath.Join(stateDir, ".git"), Trusted: true, CreatedAt: now, UpdatedAt: now}
	if err := products.SaveWorkspace(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	worktree := codingagent.Worktree{ID: "worktree-repair", WorkspaceID: workspace.ID, Root: stateDir, GitDir: workspace.GitCommonDir, CreatedAt: now, LastUsedAt: now}
	if err := products.SaveWorktree(ctx, worktree); err != nil {
		t.Fatal(err)
	}
	session := codingagent.Session{ID: "coding-repair", AgentSessionID: "agent-repair", WorkspaceID: workspace.ID, WorktreeID: worktree.ID, ProviderProfileID: "provider", ModelID: "model", PermissionMode: codingagent.PermissionAsk, CreatedAt: now, UpdatedAt: now}
	intent := codingagent.SessionCreationIntent{ID: codingagent.CreationIntentID(session.ID), Session: session, Status: codingagent.SessionCreationPending, CreatedAt: now, UpdatedAt: now}
	if err := products.BeginSessionCreation(ctx, intent); err != nil {
		t.Fatal(err)
	}
	if err := agents.Create(ctx, agentsession.Metadata{ID: session.AgentSessionID, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	report, err := RepairState(ctx, MaintenanceOptions{StateDir: stateDir})
	if err != nil {
		t.Fatalf("repair file state: %v", err)
	}
	if len(report.After.Issues) != 0 {
		t.Fatalf("remaining issues = %#v", report.After.Issues)
	}
	reopened, err := codingfile.OpenRepository(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.LoadSession(ctx, session.ID); err != nil {
		t.Fatalf("load reconciled product session: %v", err)
	}
	intents, err := reopened.ListSessionCreationIntents(ctx)
	if err != nil || len(intents) != 1 || intents[0].Status != codingagent.SessionCreationCompleted {
		t.Fatalf("reconciled intents = %#v, %v", intents, err)
	}
}
