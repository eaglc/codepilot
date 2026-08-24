package codingagent_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/eaglc/codepilot/internal/agent"
	agentsession "github.com/eaglc/codepilot/internal/agent/session"
	"github.com/eaglc/codepilot/internal/codingagent"
	codingmemory "github.com/eaglc/codepilot/internal/codingstore/memory"
	"github.com/eaglc/codepilot/internal/contextmanager"
	"github.com/eaglc/codepilot/internal/llm"
)

func TestSetPermissionModeRevokesActiveSessionGrantsAndPublishesUpdate(t *testing.T) {
	service, products, _, events := newSessionManagementService(t)
	ctx := context.Background()
	now := time.Now().UTC()
	created, err := service.CreateSession(ctx, managedSession("coding-permissions", "agent-permissions", "Permissions"))
	if err != nil {
		t.Fatal(err)
	}
	created.PermissionGrants = []codingagent.PermissionGrant{
		{
			ID: "active", Scope: codingagent.PermissionGrantSession, ToolName: "apply_patch", Action: codingagent.PermissionActionModify,
			Paths: []string{"main.go"}, SourceTurnID: "turn-active", SourceInterruptID: "approval-active",
			CreatedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour),
		},
		{
			ID: "expired", Scope: codingagent.PermissionGrantSession, ToolName: "run_checks", Action: codingagent.PermissionActionExecute,
			SourceTurnID: "turn-expired", SourceInterruptID: "approval-expired",
			CreatedAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-time.Hour),
		},
	}
	if err := products.SaveSession(ctx, created); err != nil {
		t.Fatal(err)
	}

	updated, err := service.SetPermissionMode(ctx, created.ID, codingagent.PermissionReadOnly)
	if err != nil {
		t.Fatalf("set permission mode: %v", err)
	}
	if updated.PermissionMode != codingagent.PermissionReadOnly || updated.PermissionGrants[0].RevokedAt.IsZero() {
		t.Fatalf("updated session = %#v", updated)
	}
	if !updated.PermissionGrants[1].RevokedAt.IsZero() {
		t.Fatalf("expired grant was rewritten: %#v", updated.PermissionGrants[1])
	}
	stored, err := products.LoadSession(ctx, created.ID)
	if err != nil || stored.PermissionMode != codingagent.PermissionReadOnly || stored.PermissionGrants[0].RevokedAt.IsZero() {
		t.Fatalf("stored session = %#v, %v", stored, err)
	}
	last := events.values[len(events.values)-1]
	if last.Kind != codingagent.EventSessionUpdated || last.Payload.Session == nil || last.Payload.Session.PermissionMode != codingagent.PermissionReadOnly {
		t.Fatalf("last event = %#v", last)
	}
}

func TestSetPermissionModeRejectsUnsupportedMode(t *testing.T) {
	service, _, _, _ := newSessionManagementService(t)
	if _, err := service.SetPermissionMode(context.Background(), "session", codingagent.PermissionMode("unsafe")); err == nil {
		t.Fatal("unsupported permission mode was accepted")
	}
}

func TestSessionLifecycleListSwitchRenameAndArchive(t *testing.T) {
	service, products, _, events := newSessionManagementService(t)
	ctx := context.Background()
	first, err := service.CreateSession(ctx, managedSession("coding-first", "agent-first", "First"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.CreateSession(ctx, managedSession("coding-second", "agent-second", "Second"))
	if err != nil {
		t.Fatal(err)
	}
	values, err := service.ListSessions(ctx, codingagent.SessionListOptions{WorktreeID: "worktree"})
	if err != nil || len(values) != 2 {
		t.Fatalf("sessions = %#v, %v", values, err)
	}
	if _, err := service.SwitchSession(ctx, first.ID); err != nil {
		t.Fatalf("switch first: %v", err)
	}
	renamed, err := service.RenameSession(ctx, first.ID, "Renamed first")
	if err != nil || renamed.Title != "Renamed first" {
		t.Fatalf("renamed = %#v, %v", renamed, err)
	}
	if _, err := service.ArchiveSession(ctx, first.ID); err == nil || !strings.Contains(err.Error(), "switch") {
		t.Fatalf("archive active error = %v", err)
	}
	if _, err := service.SwitchSession(ctx, second.ID); err != nil {
		t.Fatalf("switch second: %v", err)
	}
	archived, err := service.ArchiveSession(ctx, first.ID)
	if err != nil || !archived.Archived {
		t.Fatalf("archived = %#v, %v", archived, err)
	}
	values, err = service.ListSessions(ctx, codingagent.SessionListOptions{WorktreeID: "worktree"})
	if err != nil || len(values) != 1 || values[0].ID != second.ID {
		t.Fatalf("active sessions = %#v, %v", values, err)
	}
	all, err := service.ListSessions(ctx, codingagent.SessionListOptions{WorktreeID: "worktree", IncludeArchived: true})
	if err != nil || len(all) != 2 {
		t.Fatalf("all sessions = %#v, %v", all, err)
	}
	stored, _ := products.LoadSession(ctx, first.ID)
	if !stored.Archived || stored.Title != "Renamed first" {
		t.Fatalf("stored first = %#v", stored)
	}
	var activated, updated bool
	for _, event := range events.values {
		activated = activated || event.Kind == codingagent.EventSessionActivated
		updated = updated || event.Kind == codingagent.EventSessionUpdated
		if event.Payload.Session != nil && event.Payload.Session.SessionID == "" {
			t.Fatalf("session event omitted product id: %#v", event)
		}
	}
	if !activated || !updated {
		t.Fatalf("lifecycle events = %#v", events.values)
	}
}

func TestForkLaneProjectsOnlySelectedHistoryAndContinuesOnBranch(t *testing.T) {
	service, _, agents, _ := newSessionManagementService(t)
	ctx := context.Background()
	created, err := service.CreateSession(ctx, managedSession("coding-branch", "agent-branch", "Branch"))
	if err != nil {
		t.Fatal(err)
	}
	appendBranchMessage(t, agents, created.AgentSessionID, "user-one", llm.RoleUser, "one")
	appendBranchMessage(t, agents, created.AgentSessionID, "assistant-one", llm.RoleAssistant, "answer one")
	appendBranchMessage(t, agents, created.AgentSessionID, "user-main-later", llm.RoleUser, "main later")
	if _, err := service.SwitchSession(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	forked, err := service.ForkLane(ctx, codingagent.ForkLaneRequest{SessionID: created.ID, FromEntryID: "assistant-one"})
	if err != nil {
		t.Fatalf("fork lane: %v", err)
	}
	if forked.Session.ActiveLane == "" || forked.Session.ActiveLane == agentsession.MainLane {
		t.Fatalf("active lane = %q", forked.Session.ActiveLane)
	}
	if transcriptText(forked.Transcript) != "one|answer one" {
		t.Fatalf("fork transcript = %q", transcriptText(forked.Transcript))
	}
	if _, err := service.StartTurn(ctx, codingagent.TurnRequest{SessionID: created.ID, Text: "branch prompt"}); err != nil {
		t.Fatalf("continue branch: %v", err)
	}
	snapshot, err := service.Snapshot(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	text := transcriptText(snapshot.Transcript)
	if text != "one|answer one|branch prompt|done" || strings.Contains(text, "main later") {
		t.Fatalf("continued branch transcript = %q", text)
	}
	// Retrying the same deterministic fork is idempotent and resets the branch leaf.
	repeated, err := service.ForkLane(ctx, codingagent.ForkLaneRequest{SessionID: created.ID, FromEntryID: "assistant-one"})
	if err != nil || transcriptText(repeated.Transcript) != "one|answer one|branch prompt|done" {
		t.Fatalf("repeat fork = %q, %v", transcriptText(repeated.Transcript), err)
	}
}

func newSessionManagementService(t *testing.T) (*codingagent.Service, *codingmemory.Repository, *agentsession.MemoryRepository, *productEvents) {
	t.Helper()
	products := codingmemory.NewRepository()
	seedMemoryWorktree(t, products, "workspace", "worktree")
	agents := agentsession.NewMemoryRepository()
	contexts, err := contextmanager.NewManager()
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := agent.NewRuntime(agent.Dependencies{Models: finalModelFactory{}, Contexts: contexts, Sessions: agents})
	if err != nil {
		t.Fatal(err)
	}
	events := &productEvents{}
	service, err := codingagent.NewService(codingagent.Dependencies{
		Sessions: products, AgentSessions: agents, Worktrees: products, Agent: runtime,
		Tools: emptyToolFactory{}, Prompts: staticPrompt{}, Events: events,
	})
	if err != nil {
		t.Fatal(err)
	}
	return service, products, agents, events
}

func managedSession(id codingagent.SessionID, agentID agentsession.ID, title string) codingagent.Session {
	return codingagent.Session{
		ID: id, AgentSessionID: agentID, WorkspaceID: "workspace", WorktreeID: "worktree", Title: title,
		ProviderProfileID: "profile-1", ModelID: "model-1", PermissionMode: codingagent.PermissionAsk,
	}
}

func appendBranchMessage(t *testing.T, repository agentsession.Repository, id agentsession.ID, entryID agentsession.EntryID, role llm.Role, text string) {
	t.Helper()
	if _, err := repository.AppendEntry(context.Background(), id, agentsession.MainLane, agentsession.Entry{
		ID: entryID, RunID: agentsession.RunID("run-" + string(entryID)), Type: agentsession.EntryMessage,
		Message: &llm.Message{Role: role, Content: []llm.Content{{Type: llm.ContentText, Text: text}}},
	}); err != nil {
		t.Fatalf("append %s: %v", entryID, err)
	}
}

func transcriptText(items []codingagent.TranscriptItem) string {
	var values []string
	for _, item := range items {
		if item.Kind == codingagent.TranscriptText {
			values = append(values, item.Text)
		}
	}
	return strings.Join(values, "|")
}
