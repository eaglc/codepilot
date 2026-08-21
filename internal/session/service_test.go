package session_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/eaglc/codepilot/internal/session"
	"github.com/eaglc/codepilot/internal/sessionstore"
)

func TestService_StartTurnPersistsInputBeforeAgentAndCommitsVerifiedResult(t *testing.T) {
	fixture := newServiceFixture(t)
	fixture.agent.run = func(ctx context.Context, request session.TurnRequest, events session.EventSink) (session.TurnResult, error) {
		snapshot, err := fixture.store.LoadSession(ctx, request.Scope.SessionID)
		if err != nil {
			t.Fatalf("load session from agent: %v", err)
		}
		if len(snapshot.Messages) != 1 || snapshot.Messages[0].ID != request.UserMessage.ID {
			t.Fatalf("user message was not persisted before agent run: %#v", snapshot.Messages)
		}
		if len(request.History) != 0 {
			t.Fatalf("history contains current user message: %#v", request.History)
		}
		if request.Scope.PermissionMode != session.PermissionAsk || request.Scope.Limits != fixture.limits {
			t.Fatalf("unexpected immutable scope: %#v", request.Scope)
		}

		return session.TurnResult{
			FinalText:         "Fixed the bug.",
			Steps:             4,
			TerminationReason: "final",
			CheckSummary: session.CheckSummary{
				Outcome: session.CheckPassed,
				Summary: "go test ./... passed",
			},
			AppliedPatches: []session.PatchRecord{
				{
					ID:        "patch_test",
					SessionID: request.Scope.SessionID,
					TurnID:    request.Scope.TurnID,
					Patch:     "diff",
					Files: []session.PatchedFile{
						{Path: "main.go", BeforeHash: "before", AfterHash: "after"},
					},
					AppliedAt: time.Now().UTC(),
				},
			},
		}, nil
	}

	turnID, err := fixture.service.StartTurn(context.Background(), "  Fix the bug.\nMore details  ")
	if err != nil {
		t.Fatalf("start turn: %v", err)
	}
	fixture.events.waitFor(t, session.EventTurnCompleted)
	waitForIdle(t, fixture.service)

	snapshot, err := fixture.store.LoadSession(context.Background(), fixture.session.ID)
	if err != nil {
		t.Fatalf("load completed session: %v", err)
	}
	if snapshot.Session.Title != "Fix the bug." {
		t.Fatalf("title = %q, want first message line", snapshot.Session.Title)
	}
	if snapshot.Session.LastTurnStatus != session.TurnVerified {
		t.Fatalf("status = %s, want verified", snapshot.Session.LastTurnStatus)
	}
	if len(snapshot.Messages) != 2 || len(snapshot.Turns) != 1 || len(snapshot.Patches) != 1 {
		t.Fatalf("unexpected records: messages=%d turns=%d patches=%d", len(snapshot.Messages), len(snapshot.Turns), len(snapshot.Patches))
	}
	if snapshot.Turns[0].ID != turnID || snapshot.Turns[0].CheckSummary.Outcome != session.CheckPassed {
		t.Fatalf("unexpected turn record: %#v", snapshot.Turns[0])
	}
	if _, err := fixture.service.ReadDiff(context.Background(), session.DiffSession); err != nil {
		t.Fatalf("read session diff: %v", err)
	}
	request := fixture.workspaceReader.diffRequest()
	if len(request.Files) != 1 || request.Files[0] != "main.go" || request.ExpectedHashes["main.go"] != "after" {
		t.Fatalf("session diff did not include latest patch evidence: %#v", request)
	}

	events := fixture.events.all()
	if events[0].Kind != session.EventSessionActivated || events[1].Kind != session.EventTurnStarted {
		t.Fatalf("unexpected initial event order: %#v", events)
	}
	if events[len(events)-1].Kind != session.EventTurnCompleted {
		t.Fatalf("last event = %s, want turn completed", events[len(events)-1].Kind)
	}
}

func TestService_RunTurnRecordsWarningWhenFinalEventDeliveryFails(t *testing.T) {
	store := sessionstore.NewMemoryStore()
	events := &selectiveEventSink{failKinds: map[session.EventKind]bool{session.EventTurnCompleted: true}}
	agent := &fakeCodingAgent{}
	service := newTurnServiceHarness(t, store, store, events, agent)
	agent.run = func(context.Context, session.TurnRequest, session.EventSink) (session.TurnResult, error) {
		return session.TurnResult{FinalText: "done"}, nil
	}

	if _, err := service.StartTurn(context.Background(), "Finish"); err != nil {
		t.Fatalf("start turn: %v", err)
	}
	waitForIdle(t, service)

	snapshot, err := service.CurrentSession(context.Background())
	if err != nil {
		t.Fatalf("current session: %v", err)
	}
	if !hasRecoveryWarning(snapshot.RecoveryWarnings, session.RecoveryTurnUnrecorded) {
		t.Fatalf("recovery warnings = %#v, want a turn-unrecorded warning", snapshot.RecoveryWarnings)
	}
}

func TestService_RunTurnRecordsWarningWhenSaveFailureEventDeliveryFails(t *testing.T) {
	store := sessionstore.NewMemoryStore()
	events := &selectiveEventSink{failKinds: map[session.EventKind]bool{session.EventSessionSaveFailed: true}}
	agent := &fakeCodingAgent{}
	service := newTurnServiceHarness(t, store, &commitFailingStore{MemoryStore: store}, events, agent)
	agent.run = func(context.Context, session.TurnRequest, session.EventSink) (session.TurnResult, error) {
		return session.TurnResult{FinalText: "done"}, nil
	}

	if _, err := service.StartTurn(context.Background(), "Finish"); err != nil {
		t.Fatalf("start turn: %v", err)
	}
	waitForIdle(t, service)

	snapshot, err := service.CurrentSession(context.Background())
	if err != nil {
		t.Fatalf("current session: %v", err)
	}
	if !hasRecoveryWarning(snapshot.RecoveryWarnings, session.RecoveryTurnUnrecorded) {
		t.Fatalf("recovery warnings = %#v, want a turn-unrecorded warning", snapshot.RecoveryWarnings)
	}
}

func TestService_CancelTurnWaitsForAgentAndPreservesPatch(t *testing.T) {
	fixture := newServiceFixture(t)
	started := make(chan struct{})
	cancelObserved := make(chan struct{})
	release := make(chan struct{})
	fixture.agent.run = func(ctx context.Context, request session.TurnRequest, events session.EventSink) (session.TurnResult, error) {
		patch := session.PatchRecord{
			ID:        "patch_before_cancel",
			SessionID: request.Scope.SessionID,
			TurnID:    request.Scope.TurnID,
			Patch:     "diff",
			Files:     []session.PatchedFile{{Path: "main.go"}},
			AppliedAt: time.Now().UTC(),
		}
		if err := events.Publish(ctx, session.Event{
			SessionID: request.Scope.SessionID,
			TurnID:    request.Scope.TurnID,
			Kind:      session.EventPatchApplied,
			Payload: session.EventPayload{
				Patch: &session.PatchEventPayload{Record: patch},
			},
		}); err != nil {
			return session.TurnResult{}, err
		}
		close(started)
		<-ctx.Done()
		close(cancelObserved)
		<-release
		return session.TurnResult{
			CheckSummary: session.CheckSummary{Outcome: session.CheckCancelled},
		}, ctx.Err()
	}

	if _, err := fixture.service.StartTurn(context.Background(), "Cancel this turn"); err != nil {
		t.Fatalf("start turn: %v", err)
	}
	<-started

	cancelResult := make(chan error, 1)
	go func() {
		cancelResult <- fixture.service.CancelTurn(context.Background())
	}()
	<-cancelObserved

	select {
	case err := <-cancelResult:
		t.Fatalf("cancel returned before agent exit: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	if err := <-cancelResult; err != nil {
		t.Fatalf("cancel turn: %v", err)
	}
	waitForIdle(t, fixture.service)

	snapshot, err := fixture.store.LoadSession(context.Background(), fixture.session.ID)
	if err != nil {
		t.Fatalf("load cancelled session: %v", err)
	}
	if snapshot.Session.LastTurnStatus != session.TurnCancelled {
		t.Fatalf("status = %s, want cancelled", snapshot.Session.LastTurnStatus)
	}
	if len(snapshot.Patches) != 1 || snapshot.Patches[0].ID != "patch_before_cancel" {
		t.Fatalf("patch was not preserved: %#v", snapshot.Patches)
	}
}

func TestService_RejectsStateChangesWhileTurnRuns(t *testing.T) {
	fixture := newServiceFixture(t)
	started := make(chan struct{})
	release := make(chan struct{})
	fixture.agent.run = func(ctx context.Context, request session.TurnRequest, events session.EventSink) (session.TurnResult, error) {
		close(started)
		select {
		case <-release:
			return session.TurnResult{}, nil
		case <-ctx.Done():
			return session.TurnResult{}, ctx.Err()
		}
	}

	if _, err := fixture.service.StartTurn(context.Background(), "Keep running"); err != nil {
		t.Fatalf("start turn: %v", err)
	}
	<-started

	if err := fixture.service.SetPermissionMode(context.Background(), session.PermissionReadOnly); !hasErrorCode(err, session.ErrInvalidState) {
		t.Fatalf("permission change error = %v, want invalid state", err)
	}
	if err := fixture.service.SwitchSession(context.Background(), fixture.session.ID); !hasErrorCode(err, session.ErrInvalidState) {
		t.Fatalf("session switch error = %v, want invalid state", err)
	}
	if err := fixture.service.SwitchModel(context.Background(), session.ModelSelection{
		ProviderProfileID: fixture.session.ProviderProfileID,
		ModelID:           fixture.session.ModelID,
	}); !hasErrorCode(err, session.ErrInvalidState) {
		t.Fatalf("model switch error = %v, want invalid state", err)
	}

	close(release)
	fixture.events.waitFor(t, session.EventTurnCompleted)
	waitForIdle(t, fixture.service)
}

func TestService_ListWorkspaceFilesUsesActiveWorktree(t *testing.T) {
	fixture := newServiceFixture(t)
	fixture.workspaceReader.files = session.WorkspaceFileList{Files: []session.WorkspaceFile{{Path: "internal/ui/model.go", Size: 42}}}

	files, err := fixture.service.ListWorkspaceFiles(context.Background(), 25)
	if err != nil {
		t.Fatal(err)
	}
	if len(files.Files) != 1 || files.Files[0].Path != "internal/ui/model.go" {
		t.Fatalf("workspace files = %#v", files)
	}
	root, limit := fixture.workspaceReader.fileRequest()
	if root != fixture.sessionRoot() || limit != 25 {
		t.Fatalf("file request = %q/%d", root, limit)
	}
}

func TestService_RestoresPersistedDirtySessionAcrossRestart(t *testing.T) {
	ctx := context.Background()
	baseDir := t.TempDir()
	root := filepath.Join(baseDir, "repo")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := sessionstore.NewFileStore(filepath.Join(baseDir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 21, 9, 0, 0, 0, time.UTC)
	workspace := session.WorkspaceRecord{ID: "ws_restore", DisplayName: "repo", GitCommonDir: filepath.Join(root, ".git"), Trusted: true, CreatedAt: now, LastUsedAt: now}
	worktree := session.WorktreeRecord{ID: "wt_restore", WorkspaceID: workspace.ID, Root: root, GitDir: filepath.Join(root, ".git"), LastSessionID: "ses_restore", CreatedAt: now, LastUsedAt: now}
	storedSession := session.Session{
		ID: worktree.LastSessionID, WorkspaceID: workspace.ID, WorktreeID: worktree.ID, Title: "Persisted bugfix",
		ProviderProfileID: "prv_restore", ModelID: "model-restore", PermissionMode: session.PermissionAutoEdit,
		BaseCommit: "old-head", LastTurnStatus: session.TurnUnverified, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.SaveWorkspace(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveWorktree(ctx, worktree); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateSession(ctx, storedSession); err != nil {
		t.Fatal(err)
	}
	for _, message := range []session.Message{
		{ID: "msg_restore_user", SessionID: storedSession.ID, TurnID: "turn_restore", Role: session.RoleUser, Content: "Fix the existing dirty worktree.", CreatedAt: now},
		{ID: "msg_restore_assistant", SessionID: storedSession.ID, TurnID: "turn_restore", Role: session.RoleAssistant, Content: "A patch was applied but not verified.", CreatedAt: now.Add(time.Minute)},
	} {
		if err := store.AppendMessage(ctx, message); err != nil {
			t.Fatal(err)
		}
	}
	patch := session.PatchRecord{
		ID: "patch_restore", SessionID: storedSession.ID, TurnID: "turn_restore", Patch: "diff", AppliedAt: now,
		Files: []session.PatchedFile{{Path: "answer.go", BeforeHash: "before", AfterHash: "after"}},
	}
	if err := store.AppendPatch(ctx, patch); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveLastActiveSession(ctx, storedSession.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := sessionstore.NewFileStore(filepath.Join(baseDir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	reader := &fakeWorkspaceReader{
		resolved: session.ResolvedWorktree{DisplayName: workspace.DisplayName, Root: root, GitDir: worktree.GitDir, GitCommonDir: workspace.GitCommonDir},
		state:    session.WorktreeState{Root: root, Branch: "main", HeadCommit: "new-head", Dirty: true, Available: true},
	}
	agent := &fakeCodingAgent{}
	agent.run = func(_ context.Context, request session.TurnRequest, _ session.EventSink) (session.TurnResult, error) {
		if len(request.History) != 2 || request.Scope.PermissionMode != session.PermissionAutoEdit || request.Scope.WorktreeRoot != root {
			t.Fatalf("restored turn request = %#v", request)
		}
		return session.TurnResult{FinalText: "No additional patch.", CheckSummary: session.CheckSummary{Outcome: session.CheckNotRun}}, nil
	}
	events := newRecordingEventSink()
	service, err := session.NewService(session.Dependencies{
		CodingAgents: &fakeCodingAgentFactory{agent: agent}, SessionStore: reopened, WorkspaceRegistry: reopened,
		WorkspaceReader: reader, ModelCatalog: &fakeModelCatalog{}, Authorizer: &fakeAuthorizer{}, Events: events,
		Limits: session.RunLimits{MaxSteps: 20, MaxTurnDuration: time.Minute, CommandTimeout: time.Minute, ToolResultMaxBytes: 1 << 20, CommandOutputMaxBytes: 1 << 20},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })

	restored, err := service.Activate(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Session.ID != storedSession.ID || restored.Session.BaseCommit != "old-head" || restored.Session.PermissionMode != session.PermissionAutoEdit {
		t.Fatalf("restored session = %#v", restored.Session)
	}
	if !restored.WorktreeState.Dirty || restored.WorktreeState.HeadCommit != "new-head" || len(restored.Messages) != 2 || len(restored.Patches) != 1 {
		t.Fatalf("restored snapshot = %#v", restored)
	}
	if _, err := service.ReadDiff(ctx, session.DiffSession); err != nil {
		t.Fatal(err)
	}
	diffRequest := reader.diffRequest()
	if len(diffRequest.Files) != 1 || diffRequest.ExpectedHashes["answer.go"] != "after" {
		t.Fatalf("restored diff request = %#v", diffRequest)
	}
	if _, err := service.StartTurn(ctx, "Continue from restored history."); err != nil {
		t.Fatal(err)
	}
	events.waitFor(t, session.EventTurnCompleted)
	waitForIdle(t, service)
}

func TestService_ActivateSkipsArchivedLastSession(t *testing.T) {
	ctx := context.Background()
	store := sessionstore.NewMemoryStore()
	root := filepath.Clean(t.TempDir())
	now := time.Now().UTC()
	workspace := session.WorkspaceRecord{ID: "ws_archived", DisplayName: "repo", GitCommonDir: filepath.Join(root, ".git"), Trusted: true, CreatedAt: now, LastUsedAt: now}
	worktree := session.WorktreeRecord{ID: "wt_archived", WorkspaceID: workspace.ID, Root: root, GitDir: filepath.Join(root, ".git"), LastSessionID: "ses_archived", CreatedAt: now, LastUsedAt: now}
	archived := session.Session{ID: worktree.LastSessionID, WorkspaceID: workspace.ID, WorktreeID: worktree.ID, PermissionMode: session.PermissionAsk, Archived: true, CreatedAt: now, UpdatedAt: now}
	if err := store.SaveWorkspace(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveWorktree(ctx, worktree); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateSession(ctx, archived); err != nil {
		t.Fatal(err)
	}
	reader := &fakeWorkspaceReader{
		resolved: session.ResolvedWorktree{DisplayName: workspace.DisplayName, Root: root, GitDir: worktree.GitDir, GitCommonDir: workspace.GitCommonDir},
		state:    session.WorktreeState{Root: root, Branch: "main", HeadCommit: "head", Available: true},
	}
	service, err := session.NewService(session.Dependencies{
		CodingAgents: &fakeCodingAgentFactory{agent: &fakeCodingAgent{}}, SessionStore: store, WorkspaceRegistry: store,
		WorkspaceReader: reader, ModelCatalog: &fakeModelCatalog{}, Authorizer: &fakeAuthorizer{}, Events: newRecordingEventSink(),
		Limits: session.RunLimits{MaxSteps: 10, MaxTurnDuration: time.Minute, CommandTimeout: time.Minute, ToolResultMaxBytes: 1024, CommandOutputMaxBytes: 1024},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })

	snapshot, err := service.Activate(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Session.ID == archived.ID || snapshot.Session.Archived {
		t.Fatalf("archived session was restored: %#v", snapshot.Session)
	}
	loadedWorktree, err := store.LoadWorktree(ctx, worktree.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loadedWorktree.LastSessionID != snapshot.Session.ID {
		t.Fatalf("last session = %q, want %q", loadedWorktree.LastSessionID, snapshot.Session.ID)
	}
}

func TestService_NewWorktreeUsesMostRecentlyValidatedModel(t *testing.T) {
	ctx := context.Background()
	store := sessionstore.NewMemoryStore()
	root := filepath.Clean(t.TempDir())
	now := time.Now().UTC()
	reader := &fakeWorkspaceReader{
		resolved: session.ResolvedWorktree{DisplayName: "new-repo", Root: root, GitDir: filepath.Join(root, ".git"), GitCommonDir: filepath.Join(root, ".git")},
		state:    session.WorktreeState{Root: root, Branch: "main", HeadCommit: "head", Available: true},
	}
	catalog := &fakeModelCatalog{profiles: []session.ProviderProfile{
		{ID: "prv_old", ModelID: "old-model", ValidatedAt: now.Add(-time.Hour)},
		{ID: "prv_latest", ModelID: "latest-model", ValidatedAt: now},
		{ID: "prv_missing", ModelID: "missing-model", CredentialLocation: "missing", ValidatedAt: now.Add(time.Hour)},
	}}
	service, err := session.NewService(session.Dependencies{
		CodingAgents: &fakeCodingAgentFactory{agent: &fakeCodingAgent{}}, SessionStore: store, WorkspaceRegistry: store,
		WorkspaceReader: reader, ModelCatalog: catalog, Authorizer: &fakeAuthorizer{}, Events: newRecordingEventSink(),
		Limits: session.RunLimits{MaxSteps: 10, MaxTurnDuration: time.Minute, CommandTimeout: time.Minute, ToolResultMaxBytes: 1024, CommandOutputMaxBytes: 1024},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })

	snapshot, err := service.Activate(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Session.ProviderProfileID != "prv_latest" || snapshot.Session.ModelID != "latest-model" {
		t.Fatalf("new worktree model = %q/%q", snapshot.Session.ProviderProfileID, snapshot.Session.ModelID)
	}
}

type serviceFixture struct {
	service         *session.Service
	store           *sessionstore.MemoryStore
	agent           *fakeCodingAgent
	events          *recordingEventSink
	workspaceReader *fakeWorkspaceReader
	session         session.Session
	limits          session.RunLimits
}

func (f *serviceFixture) sessionRoot() string {
	return f.workspaceReader.resolved.Root
}

func newServiceFixture(t *testing.T) *serviceFixture {
	t.Helper()
	ctx := context.Background()
	store := sessionstore.NewMemoryStore()
	root := "H:\\workspace\\repo"
	now := time.Date(2026, 8, 20, 8, 0, 0, 0, time.UTC)
	workspace := session.WorkspaceRecord{
		ID:           "ws_test",
		DisplayName:  "repo",
		GitCommonDir: root + "\\.git",
		Trusted:      true,
		CreatedAt:    now,
		LastUsedAt:   now,
	}
	worktree := session.WorktreeRecord{
		ID:            "wt_test",
		WorkspaceID:   workspace.ID,
		Root:          root,
		GitDir:        root + "\\.git",
		LastSessionID: "ses_test",
		CreatedAt:     now,
		LastUsedAt:    now,
	}
	activeSession := session.Session{
		ID:                worktree.LastSessionID,
		WorkspaceID:       workspace.ID,
		WorktreeID:        worktree.ID,
		ProviderProfileID: "prv_test",
		ModelID:           "model-test",
		PermissionMode:    session.PermissionAsk,
		BaseCommit:        "abcdef",
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	if err := store.SaveWorkspace(ctx, workspace); err != nil {
		t.Fatalf("save workspace: %v", err)
	}
	if err := store.SaveWorktree(ctx, worktree); err != nil {
		t.Fatalf("save worktree: %v", err)
	}
	if err := store.CreateSession(ctx, activeSession); err != nil {
		t.Fatalf("create session: %v", err)
	}

	agent := &fakeCodingAgent{}
	factory := &fakeCodingAgentFactory{agent: agent}
	events := newRecordingEventSink()
	limits := session.RunLimits{
		MaxSteps:              30,
		MaxTurnDuration:       20 * time.Minute,
		CommandTimeout:        5 * time.Minute,
		ToolResultMaxBytes:    64 * 1024,
		CommandOutputMaxBytes: 256 * 1024,
	}
	workspaceReader := &fakeWorkspaceReader{
		resolved: session.ResolvedWorktree{
			DisplayName:  workspace.DisplayName,
			Root:         root,
			GitDir:       worktree.GitDir,
			GitCommonDir: workspace.GitCommonDir,
		},
		state: session.WorktreeState{
			Root:       root,
			Branch:     "main",
			HeadCommit: activeSession.BaseCommit,
			Available:  true,
		},
	}
	service, err := session.NewService(session.Dependencies{
		CodingAgents:      factory,
		SessionStore:      store,
		WorkspaceRegistry: store,
		WorkspaceReader:   workspaceReader,
		ModelCatalog:      &fakeModelCatalog{},
		Authorizer:        &fakeAuthorizer{},
		Events:            events,
		Limits:            limits,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	if _, err := service.Activate(ctx, root); err != nil {
		t.Fatalf("activate service: %v", err)
	}
	t.Cleanup(func() {
		if err := service.Close(); err != nil {
			t.Errorf("close service: %v", err)
		}
	})

	return &serviceFixture{
		service:         service,
		store:           store,
		agent:           agent,
		events:          events,
		workspaceReader: workspaceReader,
		session:         activeSession,
		limits:          limits,
	}
}

type fakeCodingAgent struct {
	run func(context.Context, session.TurnRequest, session.EventSink) (session.TurnResult, error)

	mu     sync.Mutex
	closed bool
}

func (a *fakeCodingAgent) RunTurn(ctx context.Context, request session.TurnRequest, events session.EventSink) (session.TurnResult, error) {
	return a.run(ctx, request, events)
}

func (a *fakeCodingAgent) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.closed = true
	return nil
}

type fakeCodingAgentFactory struct {
	agent session.CodingAgent
}

func (f *fakeCodingAgentFactory) CreateCodingAgent(context.Context, session.CodingAgentConfig) (session.CodingAgent, error) {
	return f.agent, nil
}

type fakeWorkspaceReader struct {
	resolved session.ResolvedWorktree
	state    session.WorktreeState

	mu          sync.Mutex
	lastRequest session.DiffRequest
	files       session.WorkspaceFileList
	fileRoot    string
	fileLimit   int
}

func (r *fakeWorkspaceReader) ResolveWorktree(context.Context, string) (session.ResolvedWorktree, error) {
	return r.resolved, nil
}

func (r *fakeWorkspaceReader) ReadWorktreeState(context.Context, string) (session.WorktreeState, error) {
	return r.state, nil
}

func (r *fakeWorkspaceReader) ReadDiff(_ context.Context, request session.DiffRequest) (session.DiffResult, error) {
	r.mu.Lock()
	r.lastRequest = request
	r.mu.Unlock()
	return session.DiffResult{}, nil
}

func (r *fakeWorkspaceReader) ListWorkspaceFiles(_ context.Context, root string, limit int) (session.WorkspaceFileList, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fileRoot = root
	r.fileLimit = limit
	return r.files, nil
}

func (r *fakeWorkspaceReader) fileRequest() (string, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.fileRoot, r.fileLimit
}

func (r *fakeWorkspaceReader) diffRequest() session.DiffRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastRequest
}

type fakeModelCatalog struct {
	profiles []session.ProviderProfile
}

func (c *fakeModelCatalog) ListProviderProfiles(context.Context) ([]session.ProviderProfile, error) {
	return append([]session.ProviderProfile(nil), c.profiles...), nil
}

func (c *fakeModelCatalog) ConfigureProvider(context.Context, session.ConfigureProviderRequest) (session.ProviderProfile, error) {
	return session.ProviderProfile{}, nil
}

func (c *fakeModelCatalog) ListModels(context.Context, session.ProviderProfileID) ([]session.ModelOption, error) {
	return nil, nil
}

func (c *fakeModelCatalog) ValidateSelection(context.Context, session.ModelSelection) (session.ModelValidation, error) {
	return session.ModelValidation{Valid: true}, nil
}

type fakeAuthorizer struct{}

func (a *fakeAuthorizer) Authorize(context.Context, session.PermissionMode, session.Action) (session.Authorization, error) {
	return session.Authorization{Outcome: session.AuthorizationAllow}, nil
}

func (a *fakeAuthorizer) WaitDecision(context.Context, session.ApprovalRequestID) (session.ApprovalDecision, error) {
	return session.ApprovalDecision{}, nil
}

func (a *fakeAuthorizer) Resolve(context.Context, session.ApprovalResolution) error {
	return nil
}

func (a *fakeAuthorizer) ClearSession(context.Context, session.SessionID) error {
	return nil
}

type recordingEventSink struct {
	mu     sync.Mutex
	events []session.Event
	notify chan struct{}
}

func newRecordingEventSink() *recordingEventSink {
	return &recordingEventSink{notify: make(chan struct{}, 1)}
}

func (s *recordingEventSink) Publish(ctx context.Context, event session.Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	s.events = append(s.events, event)
	s.mu.Unlock()
	select {
	case s.notify <- struct{}{}:
	default:
	}
	return nil
}

func (s *recordingEventSink) all() []session.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]session.Event(nil), s.events...)
}

func (s *recordingEventSink) waitFor(t *testing.T, kind session.EventKind) session.Event {
	t.Helper()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for {
		for _, event := range s.all() {
			if event.Kind == kind {
				return event
			}
		}
		select {
		case <-s.notify:
		case <-timer.C:
			t.Fatalf("timed out waiting for event %s; got %#v", kind, s.all())
		}
	}
}

func waitForIdle(t *testing.T, service *session.Service) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		snapshot, err := service.CurrentSession(context.Background())
		if err != nil {
			t.Fatalf("read current session: %v", err)
		}
		if snapshot.RuntimeState == session.RuntimeIdle {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for idle session")
}

func hasErrorCode(err error, code session.ErrorCode) bool {
	var appError *session.AppError
	return errors.As(err, &appError) && appError.Code == code
}

func hasRecoveryWarning(warnings []session.RecoveryWarning, code session.RecoveryWarningCode) bool {
	for _, warning := range warnings {
		if warning.Code == code {
			return true
		}
	}
	return false
}

// newTurnServiceHarness seeds and activates a service whose single turn uses the
// given agent. `store` is the SessionStore (may be a failing wrapper) while
// `base` remains the WorkspaceRegistry and seed target.
func newTurnServiceHarness(t *testing.T, base *sessionstore.MemoryStore, store session.SessionStore, events session.EventSink, agent *fakeCodingAgent) *session.Service {
	t.Helper()
	ctx := context.Background()
	root := "H:\\workspace\\repo"
	now := time.Date(2026, 8, 20, 8, 0, 0, 0, time.UTC)
	workspace := session.WorkspaceRecord{ID: "ws_turn", DisplayName: "repo", GitCommonDir: root + "\\.git", Trusted: true, CreatedAt: now, LastUsedAt: now}
	worktree := session.WorktreeRecord{ID: "wt_turn", WorkspaceID: workspace.ID, Root: root, GitDir: root + "\\.git", LastSessionID: "ses_turn", CreatedAt: now, LastUsedAt: now}
	activeSession := session.Session{ID: worktree.LastSessionID, WorkspaceID: workspace.ID, WorktreeID: worktree.ID, ProviderProfileID: "prv_turn", ModelID: "model-turn", PermissionMode: session.PermissionAsk, CreatedAt: now, UpdatedAt: now}
	if err := base.SaveWorkspace(ctx, workspace); err != nil {
		t.Fatalf("save workspace: %v", err)
	}
	if err := base.SaveWorktree(ctx, worktree); err != nil {
		t.Fatalf("save worktree: %v", err)
	}
	if err := base.CreateSession(ctx, activeSession); err != nil {
		t.Fatalf("create session: %v", err)
	}
	reader := &fakeWorkspaceReader{
		resolved: session.ResolvedWorktree{DisplayName: "repo", Root: root, GitDir: worktree.GitDir, GitCommonDir: workspace.GitCommonDir},
		state:    session.WorktreeState{Root: root, Branch: "main", HeadCommit: "abcdef", Available: true},
	}
	service, err := session.NewService(session.Dependencies{
		CodingAgents:      &fakeCodingAgentFactory{agent: agent},
		SessionStore:      store,
		WorkspaceRegistry: base,
		WorkspaceReader:   reader,
		ModelCatalog:      &fakeModelCatalog{},
		Authorizer:        &fakeAuthorizer{},
		Events:            events,
		Limits:            session.RunLimits{MaxSteps: 30, MaxTurnDuration: time.Minute, CommandTimeout: time.Minute, ToolResultMaxBytes: 64 << 10, CommandOutputMaxBytes: 256 << 10},
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	if _, err := service.Activate(ctx, root); err != nil {
		t.Fatalf("activate: %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })
	return service
}

// commitFailingStore fails CommitTurn while delegating everything else to a
// real in-memory store.
type commitFailingStore struct {
	*sessionstore.MemoryStore
}

func (s *commitFailingStore) CommitTurn(context.Context, session.TurnCommit) error {
	return errors.New("commit turn failed")
}

// selectiveEventSink records every event but fails delivery for configured kinds.
type selectiveEventSink struct {
	mu        sync.Mutex
	events    []session.Event
	failKinds map[session.EventKind]bool
}

func (s *selectiveEventSink) Publish(ctx context.Context, event session.Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	s.events = append(s.events, event)
	s.mu.Unlock()
	if s.failKinds[event.Kind] {
		return errors.New("event delivery failed")
	}
	return nil
}
