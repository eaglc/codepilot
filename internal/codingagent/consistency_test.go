package codingagent_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/eaglc/codepilot/internal/agent"
	agentsession "github.com/eaglc/codepilot/internal/agent/session"
	"github.com/eaglc/codepilot/internal/codingagent"
	codingmemory "github.com/eaglc/codepilot/internal/codingstore/memory"
	"github.com/eaglc/codepilot/internal/contextmanager"
	"github.com/eaglc/codepilot/internal/llm"
)

type hiddenWorktreeReader struct {
	repository *codingmemory.Repository
	hidden     codingagent.WorktreeID
}

func (r hiddenWorktreeReader) LoadWorktree(ctx context.Context, id codingagent.WorktreeID) (codingagent.Worktree, error) {
	if id == r.hidden {
		return codingagent.Worktree{}, fmt.Errorf("load hidden worktree %q: %w", id, codingagent.ErrWorktreeNotFound)
	}
	return r.repository.LoadWorktree(ctx, id)
}

type failingSessionRepository struct {
	*codingmemory.Repository
	failCreate bool
}

func (r *failingSessionRepository) CreateSession(ctx context.Context, session codingagent.Session) error {
	if r.failCreate {
		return errors.New("injected product repository failure")
	}
	return r.Repository.CreateSession(ctx, session)
}

func TestCreateSessionPersistsIntentBeforeCrossRepositoryFailure(t *testing.T) {
	ctx := context.Background()
	productStore := &failingSessionRepository{Repository: codingmemory.NewRepository(), failCreate: true}
	seedMemoryWorktree(t, productStore.Repository, "workspace-1", "worktree-1")
	agentSessions := agentsession.NewMemoryRepository()
	contexts, err := contextmanager.NewManager()
	if err != nil {
		t.Fatalf("create context manager: %v", err)
	}
	runtime, err := agent.NewRuntime(agent.Dependencies{Models: finalModelFactory{}, Contexts: contexts, Sessions: agentSessions})
	if err != nil {
		t.Fatalf("create Agent runtime: %v", err)
	}
	service, err := codingagent.NewService(codingagent.Dependencies{
		Sessions: productStore, AgentSessions: agentSessions, Worktrees: productStore,
		Agent: runtime, Tools: emptyToolFactory{}, Prompts: staticPrompt{}, Events: &productEvents{},
	})
	if err != nil {
		t.Fatalf("create service: %v", err)
	}
	_, createErr := service.CreateSession(ctx, codingagent.Session{
		ID: "coding-failed", AgentSessionID: "agent-failed", WorkspaceID: "workspace-1", WorktreeID: "worktree-1",
		ProviderProfileID: "profile-1", ModelID: "model-1", PermissionMode: codingagent.PermissionAsk,
	})
	if createErr == nil {
		t.Fatal("expected injected create failure")
	}
	intents, err := productStore.ListSessionCreationIntents(ctx)
	if err != nil || len(intents) != 1 || intents[0].Status != codingagent.SessionCreationPending {
		t.Fatalf("creation intents = %#v, %v", intents, err)
	}
	if _, err := agentSessions.Load(ctx, "agent-failed"); err != nil {
		t.Fatalf("Agent session should be durable before injected product failure: %v", err)
	}
	if _, err := productStore.LoadSession(ctx, "coding-failed"); !errors.Is(err, codingagent.ErrSessionNotFound) {
		t.Fatalf("product session error = %v", err)
	}

	productStore.failCreate = false
	manager, err := codingagent.NewConsistencyManager(codingagent.ConsistencyDependencies{Sessions: productStore, AgentSessions: agentSessions, Worktrees: productStore})
	if err != nil {
		t.Fatalf("create consistency manager: %v", err)
	}
	repair, err := manager.Repair(ctx)
	if err != nil {
		t.Fatalf("repair creation: %v", err)
	}
	if len(repair.After.Issues) != 0 {
		t.Fatalf("remaining issues = %#v", repair.After.Issues)
	}
	if _, err := productStore.LoadSession(ctx, "coding-failed"); err != nil {
		t.Fatalf("load repaired product session: %v", err)
	}
}

func TestRepairCreationIntentAtEveryRepositoryWriteBoundary(t *testing.T) {
	for _, test := range []struct {
		name          string
		createAgent   bool
		createProduct bool
	}{
		{name: "intent-only"},
		{name: "agent-written", createAgent: true},
		{name: "product-written", createAgent: true, createProduct: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			products := codingmemory.NewRepository()
			seedMemoryWorktree(t, products, "workspace", "worktree")
			agents := agentsession.NewMemoryRepository()
			now := time.Now().UTC().Truncate(time.Millisecond)
			session := codingagent.Session{
				ID: "coding", AgentSessionID: "agent", WorkspaceID: "workspace", WorktreeID: "worktree", Title: "test",
				ProviderProfileID: "profile", ModelID: "model", PermissionMode: codingagent.PermissionAsk, CreatedAt: now, UpdatedAt: now,
			}
			intent := codingagent.SessionCreationIntent{ID: codingagent.CreationIntentID(session.ID), Session: session, Status: codingagent.SessionCreationPending, CreatedAt: now, UpdatedAt: now}
			if err := products.BeginSessionCreation(ctx, intent); err != nil {
				t.Fatalf("begin intent: %v", err)
			}
			if test.createAgent {
				if err := agents.Create(ctx, agentsession.Metadata{ID: session.AgentSessionID, Name: session.Title, CreatedAt: now, UpdatedAt: now}); err != nil {
					t.Fatalf("create Agent session: %v", err)
				}
			}
			if test.createProduct {
				if err := products.CreateSession(ctx, session); err != nil {
					t.Fatalf("create product session: %v", err)
				}
			}
			manager, _ := codingagent.NewConsistencyManager(codingagent.ConsistencyDependencies{Sessions: products, AgentSessions: agents, Worktrees: products})
			repair, err := manager.Repair(ctx)
			if err != nil {
				t.Fatalf("repair: %v", err)
			}
			if len(repair.After.Issues) != 0 {
				t.Fatalf("remaining issues = %#v", repair.After.Issues)
			}
			intents, _ := products.ListSessionCreationIntents(ctx)
			if len(intents) != 1 || intents[0].Status != codingagent.SessionCreationCompleted {
				t.Fatalf("intents = %#v", intents)
			}
		})
	}
}

func TestDiagnoseAndRepairOrphanDanglingAndMissingWorktree(t *testing.T) {
	ctx := context.Background()
	products := codingmemory.NewRepository()
	seedMemoryWorktree(t, products, "workspace", "worktree")
	agents := agentsession.NewMemoryRepository()
	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := agents.Create(ctx, agentsession.Metadata{ID: "agent-orphan", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := agents.AppendEntry(ctx, "agent-orphan", agentsession.MainLane, agentsession.Entry{
		ID: "orphan-user", RunID: "orphan-run", Type: agentsession.EntryMessage,
		Message: &llm.Message{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentText, Text: "preserve me"}}},
	}); err != nil {
		t.Fatalf("append orphan journal: %v", err)
	}
	if err := agents.Create(ctx, agentsession.Metadata{ID: "agent-missing-worktree", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	missingRoot := t.TempDir()
	if err := products.SaveWorktree(ctx, codingagent.Worktree{ID: "worktree-absent", WorkspaceID: "workspace", Root: missingRoot, GitDir: filepath.Join(missingRoot, ".git"), CreatedAt: now, LastUsedAt: now}); err != nil {
		t.Fatalf("seed hidden worktree: %v", err)
	}
	for _, session := range []codingagent.Session{
		{ID: "coding-dangling", AgentSessionID: "agent-absent", WorkspaceID: "workspace", WorktreeID: "worktree", ProviderProfileID: "profile", ModelID: "model", PermissionMode: codingagent.PermissionAsk, CreatedAt: now, UpdatedAt: now},
		{ID: "coding-missing-worktree", AgentSessionID: "agent-missing-worktree", WorkspaceID: "workspace", WorktreeID: "worktree-absent", ProviderProfileID: "profile", ModelID: "model", PermissionMode: codingagent.PermissionAsk, CreatedAt: now, UpdatedAt: now},
	} {
		if err := products.CreateSession(ctx, session); err != nil {
			t.Fatalf("create fixture session: %v", err)
		}
	}
	manager, _ := codingagent.NewConsistencyManager(codingagent.ConsistencyDependencies{Sessions: products, AgentSessions: agents, Worktrees: hiddenWorktreeReader{repository: products, hidden: "worktree-absent"}})
	report, err := manager.Diagnose(ctx)
	if err != nil {
		t.Fatalf("diagnose: %v", err)
	}
	want := map[codingagent.ConsistencyIssueKind]bool{
		codingagent.IssueOrphanAgentSession: false, codingagent.IssueDanglingCodingSession: false, codingagent.IssueMissingWorktree: false,
	}
	for _, issue := range report.Issues {
		want[issue.Kind] = true
	}
	for kind, found := range want {
		if !found {
			t.Fatalf("missing issue %q in %#v", kind, report.Issues)
		}
	}
	repair, err := manager.Repair(ctx)
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	if len(repair.After.Issues) != 0 {
		t.Fatalf("remaining issues = %#v", repair.After.Issues)
	}
	metadata, _ := agents.List(ctx)
	for _, value := range metadata {
		if value.ID == "agent-orphan" && !value.Archived {
			t.Fatal("orphan Agent session was not archived")
		}
	}
	orphan, err := agents.Load(ctx, "agent-orphan")
	if err != nil || len(orphan.Log) != 1 {
		t.Fatalf("orphan journal was not preserved: %#v, %v", orphan, err)
	}
	for _, id := range []codingagent.SessionID{"coding-dangling", "coding-missing-worktree"} {
		session, _ := products.LoadSession(ctx, id)
		if !session.Archived {
			t.Fatalf("broken Coding session %q was not archived", id)
		}
	}
}

func seedMemoryWorktree(t *testing.T, repository *codingmemory.Repository, workspaceID codingagent.WorkspaceID, worktreeID codingagent.WorktreeID) {
	t.Helper()
	now := time.Now().UTC()
	root := t.TempDir()
	gitDir := filepath.Join(root, ".git")
	if err := repository.SaveWorkspace(context.Background(), codingagent.Workspace{ID: workspaceID, DisplayName: string(workspaceID), GitCommonDir: gitDir, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("save workspace: %v", err)
	}
	if err := repository.SaveWorktree(context.Background(), codingagent.Worktree{ID: worktreeID, WorkspaceID: workspaceID, Root: root, GitDir: gitDir, CreatedAt: now, LastUsedAt: now}); err != nil {
		t.Fatalf("save worktree: %v", err)
	}
}
