package sessionstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/eaglc/codepilot/internal/session"
)

func TestFileStore_PersistsIdempotentSessionRecordsAcrossReopen(t *testing.T) {
	fixture := newFileStoreFixture(t)
	ctx := context.Background()
	userMessage := session.Message{
		ID:        "msg_user",
		SessionID: fixture.session.ID,
		TurnID:    "turn_test",
		Role:      session.RoleUser,
		Content:   "Fix the bug.",
		CreatedAt: fixture.now.Add(time.Minute),
	}
	patch := session.PatchRecord{
		ID:        "patch_test",
		SessionID: fixture.session.ID,
		TurnID:    userMessage.TurnID,
		Patch:     "diff --git a/main.go b/main.go",
		Files: []session.PatchedFile{
			{Path: "main.go", BeforeHash: "before", AfterHash: "after"},
		},
		AppliedAt: fixture.now.Add(2 * time.Minute),
	}

	if err := fixture.store.AppendMessage(ctx, userMessage); err != nil {
		t.Fatalf("append user message: %v", err)
	}
	if err := fixture.store.AppendMessage(ctx, userMessage); err != nil {
		t.Fatalf("repeat user message: %v", err)
	}
	if err := fixture.store.AppendPatch(ctx, patch); err != nil {
		t.Fatalf("append patch: %v", err)
	}
	if err := fixture.store.AppendPatch(ctx, patch); err != nil {
		t.Fatalf("repeat patch: %v", err)
	}

	completed := fixture.session
	completed.Title = "Fix the bug"
	completed.LastTurnStatus = session.TurnVerified
	completed.UpdatedAt = fixture.now.Add(3 * time.Minute)
	commit := session.TurnCommit{
		Session: completed,
		AssistantMessage: session.Message{
			ID:        "msg_assistant",
			SessionID: completed.ID,
			TurnID:    userMessage.TurnID,
			Role:      session.RoleAssistant,
			Content:   "Fixed.",
			CreatedAt: completed.UpdatedAt,
		},
		Turn: session.TurnRecord{
			ID:                userMessage.TurnID,
			SessionID:         completed.ID,
			UserMessageID:     userMessage.ID,
			Status:            session.TurnVerified,
			TerminationReason: "final",
			Steps:             4,
			CheckSummary: session.CheckSummary{
				Outcome: session.CheckPassed,
				Summary: "go test ./... passed",
			},
			StartedAt:   userMessage.CreatedAt,
			CompletedAt: completed.UpdatedAt,
		},
	}
	if err := fixture.store.CommitTurn(ctx, commit); err != nil {
		t.Fatalf("commit turn: %v", err)
	}
	if err := fixture.store.CommitTurn(ctx, commit); err != nil {
		t.Fatalf("repeat turn commit: %v", err)
	}
	if err := fixture.store.SaveLastActiveSession(ctx, completed.ID); err != nil {
		t.Fatalf("save last active session: %v", err)
	}
	if err := fixture.store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	reopened, err := NewFileStore(fixture.stateDir)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	snapshot, err := reopened.LoadSession(ctx, completed.ID)
	if err != nil {
		t.Fatalf("load reopened session: %v", err)
	}
	if snapshot.Session.Title != completed.Title || snapshot.Session.LastTurnStatus != session.TurnVerified {
		t.Fatalf("unexpected session metadata: %#v", snapshot.Session)
	}
	if len(snapshot.Messages) != 2 || len(snapshot.Turns) != 1 || len(snapshot.Patches) != 1 {
		t.Fatalf("unexpected record counts: messages=%d turns=%d patches=%d", len(snapshot.Messages), len(snapshot.Turns), len(snapshot.Patches))
	}
	if snapshot.Patches[0].Files[0].Path != "main.go" || snapshot.Turns[0].CheckSummary.Outcome != session.CheckPassed {
		t.Fatalf("unexpected persisted records: %#v %#v", snapshot.Patches, snapshot.Turns)
	}

	worktrees, err := reopened.ListWorktrees(ctx)
	if err != nil {
		t.Fatalf("list worktrees: %v", err)
	}
	if len(worktrees) != 1 || !worktrees[0].Available || worktrees[0].DisplayName != fixture.workspace.DisplayName {
		t.Fatalf("unexpected worktrees: %#v", worktrees)
	}
}

func TestFileStore_ArchiveKeepsHistoryAndCloseRejectsOperations(t *testing.T) {
	fixture := newFileStoreFixture(t)
	ctx := context.Background()
	message := session.Message{
		ID:        "msg_test",
		SessionID: fixture.session.ID,
		TurnID:    "turn_test",
		Role:      session.RoleUser,
		Content:   "Keep me.",
		CreatedAt: fixture.now,
	}
	if err := fixture.store.AppendMessage(ctx, message); err != nil {
		t.Fatalf("append message: %v", err)
	}
	if err := fixture.store.ArchiveSession(ctx, fixture.session.ID, fixture.now.Add(time.Minute)); err != nil {
		t.Fatalf("archive session: %v", err)
	}

	snapshot, err := fixture.store.LoadSession(ctx, fixture.session.ID)
	if err != nil {
		t.Fatalf("load archived session: %v", err)
	}
	if !snapshot.Session.Archived || len(snapshot.Messages) != 1 {
		t.Fatalf("archive removed history or flag is false: %#v", snapshot)
	}
	listed, err := fixture.store.ListSessions(ctx, session.SessionFilter{})
	if err != nil || len(listed) != 0 {
		t.Fatalf("default archived listing = %#v, %v", listed, err)
	}
	listed, err = fixture.store.ListSessions(ctx, session.SessionFilter{IncludeArchived: true})
	if err != nil || len(listed) != 1 {
		t.Fatalf("included archived listing = %#v, %v", listed, err)
	}

	if err := fixture.store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	if _, err := fixture.store.LoadSession(ctx, fixture.session.ID); !errors.Is(err, ErrStoreClosed) {
		t.Fatalf("load after close error = %v, want closed", err)
	}
}

func TestFileStore_ListWorktreesMarksMovedRootUnavailable(t *testing.T) {
	fixture := newFileStoreFixture(t)
	movedRoot := fixture.worktree.Root + "-moved"
	if err := os.Rename(fixture.worktree.Root, movedRoot); err != nil {
		t.Fatalf("move worktree fixture: %v", err)
	}

	worktrees, err := fixture.store.ListWorktrees(context.Background())
	if err != nil {
		t.Fatalf("list worktrees: %v", err)
	}
	if len(worktrees) != 1 || worktrees[0].Available {
		t.Fatalf("moved worktree availability = %#v, want unavailable", worktrees)
	}
}

func TestFileStore_RecoversTruncatedFinalJSONLRecordBeforeAppend(t *testing.T) {
	fixture := newFileStoreFixture(t)
	ctx := context.Background()
	first := session.Message{
		ID:        "msg_first",
		SessionID: fixture.session.ID,
		TurnID:    "turn_first",
		Role:      session.RoleUser,
		Content:   "First.",
		CreatedAt: fixture.now,
	}
	if err := fixture.store.AppendMessage(ctx, first); err != nil {
		t.Fatalf("append first message: %v", err)
	}

	messagePath := filepath.Join(filepath.Dir(fixture.store.sessionPaths[fixture.session.ID]), "messages.jsonl")
	appendTestBytes(t, messagePath, []byte(`{"id":`))
	snapshot, err := fixture.store.LoadSession(ctx, fixture.session.ID)
	if err != nil || len(snapshot.Messages) != 1 {
		t.Fatalf("load with truncated tail = %#v, %v", snapshot.Messages, err)
	}
	if len(snapshot.RecoveryWarnings) != 1 || snapshot.RecoveryWarnings[0].Code != session.RecoveryTruncatedLog || snapshot.RecoveryWarnings[0].Stream != "messages" {
		t.Fatalf("recovery warnings = %#v", snapshot.RecoveryWarnings)
	}

	second := first
	second.ID = "msg_second"
	second.TurnID = "turn_second"
	second.Content = "Second."
	if err := fixture.store.AppendMessage(ctx, second); err != nil {
		t.Fatalf("append after truncated tail: %v", err)
	}
	snapshot, err = fixture.store.LoadSession(ctx, fixture.session.ID)
	if err != nil || len(snapshot.Messages) != 2 {
		t.Fatalf("repaired message log = %#v, %v", snapshot.Messages, err)
	}
	if len(snapshot.RecoveryWarnings) != 0 {
		t.Fatalf("warnings remained after repair: %#v", snapshot.RecoveryWarnings)
	}
}

func TestFileStore_RejectsMiddleJSONLCorruption(t *testing.T) {
	fixture := newFileStoreFixture(t)
	ctx := context.Background()
	message := session.Message{
		ID:        "msg_first",
		SessionID: fixture.session.ID,
		TurnID:    "turn_first",
		Role:      session.RoleUser,
		Content:   "First.",
		CreatedAt: fixture.now,
	}
	if err := fixture.store.AppendMessage(ctx, message); err != nil {
		t.Fatalf("append first message: %v", err)
	}
	messagePath := filepath.Join(filepath.Dir(fixture.store.sessionPaths[fixture.session.ID]), "messages.jsonl")
	appendTestBytes(t, messagePath, []byte("not-json\n"))

	if _, err := fixture.store.LoadSession(ctx, fixture.session.ID); !hasErrorCode(err, session.ErrCorruptedState) {
		t.Fatalf("load corrupt log error = %v, want corrupted state", err)
	}
}

func TestProcessLock_AllowsOnlyOneOwner(t *testing.T) {
	stateDir := t.TempDir()
	first, err := AcquireProcessLock(stateDir)
	if err != nil {
		t.Fatalf("acquire first lock: %v", err)
	}
	t.Cleanup(func() { _ = first.Close() })

	if _, err := AcquireProcessLock(stateDir); !errors.Is(err, ErrStateInUse) {
		t.Fatalf("acquire second lock error = %v, want state in use", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("release first lock: %v", err)
	}
	second, err := AcquireProcessLock(stateDir)
	if err != nil {
		t.Fatalf("reacquire lock: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("release second lock: %v", err)
	}
}

type fileStoreFixture struct {
	stateDir  string
	store     *FileStore
	now       time.Time
	workspace session.WorkspaceRecord
	worktree  session.WorktreeRecord
	session   session.Session
}

func newFileStoreFixture(t *testing.T) fileStoreFixture {
	t.Helper()
	ctx := context.Background()
	baseDir := t.TempDir()
	stateDir := filepath.Join(baseDir, "state")
	worktreeRoot := filepath.Join(baseDir, "repo")
	if err := os.MkdirAll(filepath.Join(worktreeRoot, ".git"), 0o700); err != nil {
		t.Fatalf("create worktree fixture: %v", err)
	}
	store, err := NewFileStore(stateDir)
	if err != nil {
		t.Fatalf("create file store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Date(2026, 8, 20, 8, 0, 0, 0, time.UTC)
	workspace := session.WorkspaceRecord{
		ID:           "ws_test",
		DisplayName:  "fixture",
		GitCommonDir: filepath.Join(worktreeRoot, ".git"),
		Trusted:      true,
		CreatedAt:    now,
		LastUsedAt:   now,
	}
	worktree := session.WorktreeRecord{
		ID:          "wt_test",
		WorkspaceID: workspace.ID,
		Root:        worktreeRoot,
		GitDir:      filepath.Join(worktreeRoot, ".git"),
		CreatedAt:   now,
		LastUsedAt:  now,
	}
	storedSession := session.Session{
		ID:             "ses_test",
		WorkspaceID:    workspace.ID,
		WorktreeID:     worktree.ID,
		PermissionMode: session.PermissionAsk,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := store.SaveWorkspace(ctx, workspace); err != nil {
		t.Fatalf("save workspace: %v", err)
	}
	if err := store.SaveWorktree(ctx, worktree); err != nil {
		t.Fatalf("save worktree: %v", err)
	}
	if err := store.CreateSession(ctx, storedSession); err != nil {
		t.Fatalf("create session: %v", err)
	}

	return fileStoreFixture{
		stateDir:  stateDir,
		store:     store,
		now:       now,
		workspace: workspace,
		worktree:  worktree,
		session:   storedSession,
	}
}

func appendTestBytes(t *testing.T, path string, content []byte) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("open append fixture: %v", err)
	}
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		t.Fatalf("append fixture: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close append fixture: %v", err)
	}
}
