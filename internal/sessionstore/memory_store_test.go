package sessionstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/eaglc/codepilot/internal/session"
)

func TestMemoryStore_SessionRecordsAreIdempotentAndIsolated(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	now := time.Date(2026, 8, 20, 8, 0, 0, 0, time.UTC)
	value := session.Session{
		ID:             "ses_test",
		WorkspaceID:    "ws_test",
		WorktreeID:     "wt_test",
		PermissionMode: session.PermissionAsk,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	if err := store.CreateSession(ctx, value); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := store.CreateSession(ctx, value); !hasErrorCode(err, session.ErrConflict) {
		t.Fatalf("duplicate create error = %v, want conflict", err)
	}

	userMessage := session.Message{
		ID:        "msg_user",
		SessionID: value.ID,
		TurnID:    "turn_test",
		Role:      session.RoleUser,
		Content:   "Fix the bug.",
		CreatedAt: now,
	}
	if err := store.AppendMessage(ctx, userMessage); err != nil {
		t.Fatalf("append user message: %v", err)
	}
	if err := store.AppendMessage(ctx, userMessage); err != nil {
		t.Fatalf("repeat user message: %v", err)
	}

	patch := session.PatchRecord{
		ID:        "patch_test",
		SessionID: value.ID,
		TurnID:    userMessage.TurnID,
		Patch:     "diff",
		Files: []session.PatchedFile{
			{
				Path:       "main.go",
				BeforeHash: "before",
				AfterHash:  "after",
			},
		},
		AppliedAt: now,
	}
	if err := store.AppendPatch(ctx, patch); err != nil {
		t.Fatalf("append patch: %v", err)
	}
	if err := store.AppendPatch(ctx, patch); err != nil {
		t.Fatalf("repeat patch: %v", err)
	}
	patch.Files[0].Path = "mutated.go"

	value.Title = "Fix the bug"
	value.LastTurnStatus = session.TurnVerified
	value.UpdatedAt = now.Add(time.Minute)
	commit := session.TurnCommit{
		Session: value,
		AssistantMessage: session.Message{
			ID:        "msg_assistant",
			SessionID: value.ID,
			TurnID:    userMessage.TurnID,
			Role:      session.RoleAssistant,
			Content:   "Fixed.",
			CreatedAt: value.UpdatedAt,
		},
		Turn: session.TurnRecord{
			ID:            userMessage.TurnID,
			SessionID:     value.ID,
			UserMessageID: userMessage.ID,
			Status:        session.TurnVerified,
			CompletedAt:   value.UpdatedAt,
		},
	}
	if err := store.CommitTurn(ctx, commit); err != nil {
		t.Fatalf("commit turn: %v", err)
	}
	if err := store.CommitTurn(ctx, commit); err != nil {
		t.Fatalf("repeat turn commit: %v", err)
	}

	snapshot, err := store.LoadSession(ctx, value.ID)
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	if len(snapshot.Messages) != 2 || len(snapshot.Turns) != 1 || len(snapshot.Patches) != 1 {
		t.Fatalf("unexpected record counts: messages=%d turns=%d patches=%d", len(snapshot.Messages), len(snapshot.Turns), len(snapshot.Patches))
	}
	if got := snapshot.Patches[0].Files[0].Path; got != "main.go" {
		t.Fatalf("stored patch was mutated: %q", got)
	}

	snapshot.Messages[0].Content = "mutated"
	snapshot.Patches[0].Files[0].Path = "mutated-again.go"
	reloaded, err := store.LoadSession(ctx, value.ID)
	if err != nil {
		t.Fatalf("reload session: %v", err)
	}
	if reloaded.Messages[0].Content != userMessage.Content {
		t.Fatal("loaded messages share storage with the caller")
	}
	if reloaded.Patches[0].Files[0].Path != "main.go" {
		t.Fatal("loaded patches share storage with the caller")
	}
}

func TestMemoryStore_ListSessionsFiltersAndSorts(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	baseTime := time.Date(2026, 8, 20, 8, 0, 0, 0, time.UTC)

	values := []session.Session{
		{ID: "ses_b", WorkspaceID: "ws_1", WorktreeID: "wt_1", UpdatedAt: baseTime},
		{ID: "ses_a", WorkspaceID: "ws_1", WorktreeID: "wt_1", UpdatedAt: baseTime},
		{ID: "ses_new", WorkspaceID: "ws_1", WorktreeID: "wt_2", UpdatedAt: baseTime.Add(time.Minute)},
		{ID: "ses_archived", WorkspaceID: "ws_1", WorktreeID: "wt_1", Archived: true, UpdatedAt: baseTime.Add(2 * time.Minute)},
		{ID: "ses_other", WorkspaceID: "ws_2", WorktreeID: "wt_3", UpdatedAt: baseTime.Add(3 * time.Minute)},
	}
	for _, value := range values {
		if err := store.CreateSession(ctx, value); err != nil {
			t.Fatalf("create session %s: %v", value.ID, err)
		}
	}

	listed, err := store.ListSessions(ctx, session.SessionFilter{WorkspaceID: "ws_1"})
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	want := []session.SessionID{"ses_new", "ses_a", "ses_b"}
	if len(listed) != len(want) {
		t.Fatalf("listed %d sessions, want %d", len(listed), len(want))
	}
	for index, id := range want {
		if listed[index].ID != id {
			t.Fatalf("listed[%d] = %s, want %s", index, listed[index].ID, id)
		}
	}

	archived, err := store.ListSessions(ctx, session.SessionFilter{
		WorkspaceID:     "ws_1",
		WorktreeID:      "wt_1",
		IncludeArchived: true,
	})
	if err != nil {
		t.Fatalf("list archived sessions: %v", err)
	}
	if len(archived) != 3 || archived[0].ID != "ses_archived" {
		t.Fatalf("unexpected archived listing: %#v", archived)
	}
}

func TestMemoryStore_WorkspaceRegistry(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	now := time.Date(2026, 8, 20, 8, 0, 0, 0, time.UTC)
	workspace := session.WorkspaceRecord{
		ID:          "ws_test",
		DisplayName: "codepilot",
		Trusted:     true,
		LastUsedAt:  now,
	}
	worktree := session.WorktreeRecord{
		ID:          "wt_test",
		WorkspaceID: workspace.ID,
		Root:        "H:\\workspace_github\\codepilot",
		LastUsedAt:  now,
	}

	if err := store.SaveWorkspace(ctx, workspace); err != nil {
		t.Fatalf("save workspace: %v", err)
	}
	if err := store.SaveWorktree(ctx, worktree); err != nil {
		t.Fatalf("save worktree: %v", err)
	}

	loaded, found, err := store.FindWorktreeByRoot(ctx, worktree.Root)
	if err != nil {
		t.Fatalf("find worktree: %v", err)
	}
	if !found || loaded != worktree {
		t.Fatalf("got (%#v, %t), want worktree", loaded, found)
	}

	listed, err := store.ListWorktrees(ctx)
	if err != nil {
		t.Fatalf("list worktrees: %v", err)
	}
	if len(listed) != 1 || listed[0].DisplayName != workspace.DisplayName {
		t.Fatalf("unexpected worktree summaries: %#v", listed)
	}
}

func TestMemoryStore_ResetAndContextCancellation(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	value := session.Session{ID: "ses_test"}
	if err := store.CreateSession(ctx, value); err != nil {
		t.Fatalf("create session: %v", err)
	}

	store.Reset()
	if _, err := store.LoadSession(ctx, value.ID); !hasErrorCode(err, session.ErrNotFound) {
		t.Fatalf("load after reset error = %v, want not found", err)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := store.CreateSession(cancelled, value); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled create error = %v, want context cancellation", err)
	}
}

func hasErrorCode(err error, code session.ErrorCode) bool {
	var appError *session.AppError
	return errors.As(err, &appError) && appError.Code == code
}
