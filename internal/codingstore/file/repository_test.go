package file

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

type legacyPlanV1Fixture struct {
	ID                  codingagent.PlanID            `json:"id"`
	TurnID              codingagent.TurnID            `json:"turn_id"`
	Version             uint64                        `json:"version"`
	Goal                string                        `json:"goal"`
	Scope               codingagent.PlanScope         `json:"scope"`
	Findings            []string                      `json:"findings"`
	Assumptions         []string                      `json:"assumptions,omitempty"`
	Risks               []string                      `json:"risks"`
	Steps               []codingagent.PlanStep        `json:"steps"`
	AcceptanceCriteria  []string                      `json:"acceptance_criteria"`
	RecommendedStrategy codingagent.ExecutionStrategy `json:"recommended_strategy"`
	WorkspaceRevision   codingagent.WorkspaceRevision `json:"workspace_revision"`
	Digest              string                        `json:"digest"`
	CreatedAt           time.Time                     `json:"created_at"`
}

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
	turn := codingagent.Turn{
		ID: "turn-1", SessionID: session.ID, RequestText: "inspect", Phase: codingagent.TurnPhaseDirect,
		Status: codingagent.TurnPending, Strategy: codingagent.ExecutionSingle, Revision: 1,
		Runs:      []codingagent.RunBinding{{RunID: "run-1", UserEntryID: "entry-1", Phase: codingagent.TurnPhaseDirect, Profile: codingagent.CapabilityDirect, Status: codingagent.RunBindingPending}},
		CreatedAt: now, UpdatedAt: now,
	}
	if err := reopened.CreateTurn(context.Background(), turn); err != nil {
		t.Fatalf("create turn: %v", err)
	}
	metadataPath, err := reopened.sessionMetadataPath(session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(metadataPath); err != nil {
		t.Fatalf("session metadata missing from aggregate directory: %v", err)
	}
	turn.Runs[0].Status = codingagent.RunBindingRunning
	turn.Runs[0].StartedAt = now
	turn.Status = codingagent.TurnRunning
	turn.Revision = 2
	if err := reopened.SaveTurn(context.Background(), turn, 1); err != nil {
		t.Fatalf("save turn: %v", err)
	}
	plan := codingagent.Plan{
		ID: "plan-1", TurnID: turn.ID, Version: 1, Goal: "Persist Plan versions.",
		Scope:    codingagent.PlanScope{Included: []string{"internal/codingagent"}},
		Findings: []string{"The Product Turn is durable."}, Risks: []string{"Plan versions must be immutable."},
		Steps:              []codingagent.PlanStep{{ID: "persist", Goal: "Persist the Plan.", Files: []string{"internal/codingagent/plan.go"}, Validation: []string{"Reopen the repository."}}},
		AcceptanceCriteria: []string{"The exact version survives restart."}, RecommendedStrategy: codingagent.ExecutionSingle,
		WorkspaceRelevant: true, CompletionMode: codingagent.PlanCompletionExecute,
		WorkspaceRevision: codingagent.WorkspaceRevision{WorktreeID: worktree.ID, StatusDigest: strings.Repeat("a", 64), RecordedAt: now}, CreatedAt: now,
	}
	plan.Digest, _ = codingagent.ComputePlanDigest(plan)
	if err := reopened.CreatePlanVersion(context.Background(), plan); err != nil {
		t.Fatalf("create Plan version: %v", err)
	}
	planStore, err := NewRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	persistedPlan, err := planStore.LoadPlan(context.Background(), plan.ID, plan.Version)
	if err != nil || persistedPlan.Digest != plan.Digest {
		t.Fatalf("reopened Plan = %#v, %v", persistedPlan, err)
	}
	turnsPath, err := reopened.turnsPath(session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(turnsPath); err != nil {
		t.Fatalf("session turn journal missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "coding-turns")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy per-turn directory exists: %v", err)
	}
	journal, err := os.OpenFile(turnsPath, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := journal.WriteString(`{"version":1,"kind":"saved"`); err != nil {
		_ = journal.Close()
		t.Fatal(err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	finishedAt := now.Add(time.Second)
	turn.Runs[0].Status = codingagent.RunBindingCompleted
	turn.Runs[0].FinishedAt = finishedAt
	turn.Status = codingagent.TurnCompleted
	turn.UpdatedAt = finishedAt
	turn.CompletedAt = finishedAt
	turn.Revision = 3
	if err := reopened.SaveTurn(context.Background(), turn, 2); err != nil {
		t.Fatalf("save after truncated tail: %v", err)
	}
	replayed, err := OpenRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	turns, err := replayed.ListTurns(context.Background(), session.ID)
	if err != nil || len(turns) != 1 || turns[0].Revision != 3 || turns[0].Status != codingagent.TurnCompleted {
		t.Fatalf("replayed turns = %#v, %v", turns, err)
	}
}

func TestRepositoryMigratesLegacyFlatSessionMetadata(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	legacyDirectory := filepath.Join(root, "coding-sessions")
	if err := os.MkdirAll(legacyDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	legacy := codingagent.Session{
		ID: "legacy-session", AgentSessionID: "legacy-agent", WorkspaceID: "legacy-workspace", WorktreeID: "legacy-worktree",
		ProviderProfileID: "profile", ModelID: "model", PermissionMode: codingagent.PermissionAsk,
		CreatedAt: now, UpdatedAt: now,
	}
	legacyPath := filepath.Join(legacyDirectory, string(legacy.ID)+".json")
	if err := writeEnvelope(legacyPath, legacy); err != nil {
		t.Fatal(err)
	}
	repository, err := NewRepository(root)
	if err != nil {
		t.Fatalf("migrate repository: %v", err)
	}
	loaded, err := repository.LoadSession(context.Background(), legacy.ID)
	if err != nil || !reflect.DeepEqual(loaded, legacy) {
		t.Fatalf("migrated session = %#v, %v", loaded, err)
	}
	metadataPath, err := repository.sessionMetadataPath(legacy.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(metadataPath); err != nil {
		t.Fatalf("migrated metadata missing: %v", err)
	}
	if _, err := os.Stat(legacyPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy metadata remains after migration: %v", err)
	}
}

func TestRepositoryLoadsLegacyPlanWithoutNewCompletionFields(t *testing.T) {
	root := t.TempDir()
	repository, err := NewRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	legacy := legacyPlanV1Fixture{
		ID: "plan-legacy", TurnID: "turn-legacy", Version: 1, Goal: "Load the original Plan format.",
		Scope:    codingagent.PlanScope{Included: []string{"internal/codingagent"}},
		Findings: []string{"The stored Plan predates completion modes."}, Risks: []string{"Its immutable digest must remain valid."},
		Steps:              []codingagent.PlanStep{{ID: "load", Goal: "Load the Plan.", Files: []string{"internal/codingagent/plan.go"}, Validation: []string{"Open the existing session."}}},
		AcceptanceCriteria: []string{"Startup succeeds without rewriting the Plan."}, RecommendedStrategy: codingagent.ExecutionSingle,
		WorkspaceRevision: codingagent.WorkspaceRevision{WorktreeID: "worktree-legacy", StatusDigest: strings.Repeat("b", 64), RecordedAt: now},
		CreatedAt:         now,
	}
	canonical, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(canonical)
	legacy.Digest = hex.EncodeToString(digest[:])
	path, err := repository.planVersionPath(legacy.ID, legacy.Version)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeEnvelope(path, legacy); err != nil {
		t.Fatal(err)
	}
	versions, err := repository.ListPlanVersions(context.Background(), legacy.ID)
	if err != nil || len(versions) != 1 {
		t.Fatalf("list legacy Plan versions = %#v, %v", versions, err)
	}
	loaded := versions[0]
	if loaded.CompletionMode != codingagent.PlanCompletionExecute || !loaded.WorkspaceRelevant || loaded.Digest != legacy.Digest {
		t.Fatalf("loaded legacy Plan = %#v", loaded)
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
