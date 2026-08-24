package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentsession "github.com/eaglc/codepilot/internal/agent/session"
	"github.com/eaglc/codepilot/internal/codingagent"
	"github.com/eaglc/codepilot/internal/codingagent/workspace"
	codingfile "github.com/eaglc/codepilot/internal/codingstore/file"
	"github.com/eaglc/codepilot/internal/llm"
	"github.com/eaglc/codepilot/internal/provider"
	providerfile "github.com/eaglc/codepilot/internal/provider/file"
	sessionfile "github.com/eaglc/codepilot/internal/sessionstore/file"
)

func TestSelectedPermissionAcceptsHyphenAndUnderscoreSpellings(t *testing.T) {
	t.Setenv("CODEPILOT_PERMISSION", "")
	tests := []struct {
		value string
		want  codingagent.PermissionMode
	}{
		{value: "read-only", want: codingagent.PermissionReadOnly},
		{value: "read_only", want: codingagent.PermissionReadOnly},
		{value: "auto-edit", want: codingagent.PermissionAutoEdit},
		{value: "auto_edit", want: codingagent.PermissionAutoEdit},
		{value: "ask", want: codingagent.PermissionAsk},
	}
	for _, test := range tests {
		mode, forced, err := selectedPermission(test.value)
		if err != nil || !forced || mode != test.want {
			t.Fatalf("selectedPermission(%q) = %q, %v, %v", test.value, mode, forced, err)
		}
	}
}

func TestPrepareWorkspaceBindingRequiresAndAppliesExplicitRelocation(t *testing.T) {
	ctx := context.Background()
	state := filepath.Join(t.TempDir(), "state")
	repository, err := codingfile.NewRepository(state)
	if err != nil {
		t.Fatal(err)
	}
	original := appCommittedRepository(t)
	resolved, err := workspace.ResolveWorktree(ctx, original)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	first, err := prepareWorkspaceBinding(ctx, repository, resolved, Options{TrustWorkspace: true}, now)
	if err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(filepath.Dir(original), "moved")
	if err := os.Rename(original, moved); err != nil {
		t.Fatal(err)
	}
	movedResolved, err := workspace.ResolveWorktree(ctx, moved)
	if err != nil {
		t.Fatal(err)
	}
	_, err = prepareWorkspaceBinding(ctx, repository, movedResolved, Options{TrustWorkspace: true}, now.Add(time.Minute))
	id, previous, next, required := WorktreeRelocationRequired(err)
	if !required || id != first.worktree.ID || previous != first.worktree.Root || !sameLocalPath(next, movedResolved.Root) {
		t.Fatalf("relocation prompt: id=%q previous=%q next=%q required=%v err=%v", id, previous, next, required, err)
	}
	relocated, err := prepareWorkspaceBinding(ctx, repository, movedResolved, Options{TrustWorkspace: true, RelocateWorktree: id}, now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if relocated.workspace.ID != first.workspace.ID || relocated.worktree.ID != first.worktree.ID || !sameLocalPath(relocated.worktree.Root, movedResolved.Root) {
		t.Fatalf("relocated binding = %#v", relocated)
	}
}

func appCommittedRepository(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	commands := [][]string{{"init", "--quiet"}, {"config", "user.name", "CodePilot Test"}, {"config", "user.email", "codepilot@example.invalid"}}
	for _, arguments := range commands {
		command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", arguments, err, output)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, arguments := range [][]string{{"add", "main.go"}, {"commit", "--quiet", "-m", "initial"}} {
		command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", arguments, err, output)
		}
	}
	return filepath.Clean(root)
}

func TestNewComposesAndRestoresTrustedProductState(t *testing.T) {
	root := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/tags" {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"models":[{"name":"qwen-coder:latest"}]}`))
	}))
	defer server.Close()
	t.Setenv("OLLAMA_HOST", server.URL)
	t.Setenv("OPENAI_API_KEY", "test-secret-that-must-not-be-persisted")
	options := Options{
		WorkingDirectory: ".", ConfigDir: filepath.Join(root, "config"), StateDir: filepath.Join(root, "state"),
		TrustWorkspace: true, ProviderProfile: "ollama", Model: "qwen-coder",
	}
	first, err := New(context.Background(), options)
	if err != nil {
		t.Fatalf("compose application: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first application: %v", err)
	}
	options.TrustWorkspace = false
	second, err := New(context.Background(), options)
	if err != nil {
		t.Fatalf("restore trusted application: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("close restored application: %v", err)
	}
	for _, directory := range []string{"coding-workspaces", "coding-worktrees", "coding-sessions", "agent-sessions"} {
		entries, err := os.ReadDir(filepath.Join(root, "state", directory))
		if err != nil || len(entries) == 0 {
			t.Fatalf("durable directory %s: entries=%d err=%v", directory, len(entries), err)
		}
	}
	profiles, err := os.ReadFile(filepath.Join(root, "config", "provider-profiles.json"))
	if err != nil {
		t.Fatalf("read durable Provider profiles: %v", err)
	}
	content := string(profiles)
	for _, id := range []string{`"id": "openai"`, `"id": "deepseek"`, `"id": "ollama"`} {
		if !strings.Contains(content, id) {
			t.Fatalf("Provider profiles do not contain %s: %s", id, content)
		}
	}
	if strings.Contains(content, "test-secret-that-must-not-be-persisted") {
		t.Fatalf("Provider profile file contains credential material: %s", content)
	}
}

func TestSeedBuiltinProfilesDoesNotOverwriteExistingProfile(t *testing.T) {
	profiles := provider.NewMemoryProfileRepository()
	custom := provider.Profile{
		ID: "ollama", Kind: provider.KindOllama, DisplayName: "Local Lab",
		BaseURL: "http://127.0.0.1:22434", DefaultModel: "custom-model",
	}
	if err := profiles.SaveProfile(context.Background(), custom); err != nil {
		t.Fatalf("save custom profile: %v", err)
	}
	if err := seedBuiltinProfiles(context.Background(), profiles); err != nil {
		t.Fatalf("seed built-in profiles: %v", err)
	}
	stored, err := profiles.LoadProfile(context.Background(), "ollama")
	if err != nil {
		t.Fatalf("load existing profile: %v", err)
	}
	if stored != custom {
		t.Fatalf("existing profile was overwritten: got %#v want %#v", stored, custom)
	}
	values, err := profiles.ListProfiles(context.Background())
	if err != nil {
		t.Fatalf("list profiles: %v", err)
	}
	if len(values) != 3 {
		t.Fatalf("expected three built-in IDs after seeding, got %d", len(values))
	}
}

func TestBuildProvidersUsesPersistedCustomProfile(t *testing.T) {
	profiles, err := providerfile.NewRepository(t.TempDir())
	if err != nil {
		t.Fatalf("create profile repository: %v", err)
	}
	custom := provider.Profile{
		ID: "lab", Kind: provider.KindOllama, DisplayName: "Local Lab",
		BaseURL: "http://127.0.0.1:22434", DefaultModel: "custom-model",
	}
	if err := profiles.SaveProfile(context.Background(), custom); err != nil {
		t.Fatalf("save custom profile: %v", err)
	}
	_, selection, forced, err := buildProviders(context.Background(), profiles, "lab", "")
	if err != nil {
		t.Fatalf("build providers: %v", err)
	}
	if !forced || selection.profile != custom.ID || selection.model != custom.DefaultModel {
		t.Fatalf("unexpected selection: forced=%v selection=%#v", forced, selection)
	}
}

func TestNewKeepsTUIAvailableWhenProviderPreflightNeedsConfiguration(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"models":[]}`))
	}))
	defer server.Close()
	t.Setenv("OLLAMA_HOST", server.URL)
	root := t.TempDir()
	application, err := New(context.Background(), Options{
		WorkingDirectory: ".", ConfigDir: filepath.Join(root, "config"), StateDir: filepath.Join(root, "state"),
		TrustWorkspace: true, ProviderProfile: "ollama", Model: "missing-model",
	})
	if err != nil || application == nil {
		t.Fatalf("compose application with Provider setup issue: application=%v err=%v", application, err)
	}
	if err := application.Close(); err != nil {
		t.Fatalf("close application: %v", err)
	}
}

func TestNewRejectsConcurrentStateWriterAndReopensAfterClose(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"models":[{"name":"qwen-coder:latest"}]}`))
	}))
	defer server.Close()
	t.Setenv("OLLAMA_HOST", server.URL)
	root := t.TempDir()
	options := Options{
		WorkingDirectory: ".", ConfigDir: filepath.Join(root, "config"), StateDir: filepath.Join(root, "state"),
		TrustWorkspace: true, ProviderProfile: "ollama", Model: "qwen-coder",
	}
	first, err := New(context.Background(), options)
	if err != nil {
		t.Fatalf("compose first writer: %v", err)
	}
	second, err := New(context.Background(), options)
	if second != nil || !errors.Is(err, sessionfile.ErrStateInUse) {
		t.Fatalf("second writer = %#v, err = %v", second, err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first writer: %v", err)
	}
	third, err := New(context.Background(), options)
	if err != nil {
		t.Fatalf("reopen after release: %v", err)
	}
	if err := third.Close(); err != nil {
		t.Fatalf("close reopened writer: %v", err)
	}
}

func TestNewRunsRecoveryCoordinatorBeforeOpeningTUI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/tags" {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"models":[{"name":"qwen-coder:latest"}]}`))
	}))
	defer server.Close()
	t.Setenv("OLLAMA_HOST", server.URL)
	root := t.TempDir()
	options := Options{
		WorkingDirectory: ".", ConfigDir: filepath.Join(root, "config"), StateDir: filepath.Join(root, "state"),
		TrustWorkspace: true, ProviderProfile: "ollama", Model: "qwen-coder",
	}
	first, err := New(context.Background(), options)
	if err != nil {
		t.Fatalf("compose initial application: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close initial application: %v", err)
	}
	productStore, err := codingfile.NewRepository(options.StateDir)
	if err != nil {
		t.Fatalf("open Coding store: %v", err)
	}
	sessions, err := productStore.ListSessions(context.Background())
	if err != nil || len(sessions) != 1 {
		t.Fatalf("list Coding sessions: sessions=%#v err=%v", sessions, err)
	}
	agentStore, err := sessionfile.NewRepository(options.StateDir)
	if err != nil {
		t.Fatalf("open Agent store: %v", err)
	}
	sessionID := sessions[0].AgentSessionID
	appendFileRecoveryRecord(t, agentStore, sessionID, agentsession.Record{ID: "recovery-operation", Type: agentsession.RecordOperationStarted, RunID: "recovery-turn", Operation: &agentsession.OperationData{Intent: agentsession.OperationRun}})
	appendFileRecoveryEntry(t, agentStore, sessionID, agentsession.Entry{ID: "recovery-user", RunID: "recovery-turn", Type: agentsession.EntryMessage, Message: &llm.Message{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentText, Text: "read go.mod"}}}})
	appendFileRecoveryRecord(t, agentStore, sessionID, agentsession.Record{ID: "recovery-step-start", Type: agentsession.RecordStepStarted, RunID: "recovery-turn", Step: &agentsession.StepData{Attempt: 1}})
	appendFileRecoveryEntry(t, agentStore, sessionID, agentsession.Entry{ID: "recovery-assistant", RunID: "recovery-turn", Type: agentsession.EntryMessage, Message: &llm.Message{
		Role: llm.RoleAssistant, StopReason: llm.StopReasonToolUse,
		Content: []llm.Content{{Type: llm.ContentToolCall, ToolCall: &llm.ToolCall{ID: "recovery-call", Name: "read_file", Arguments: json.RawMessage(`{"path":"go.mod","start_line":1,"end_line":1}`)}}},
	}})
	appendFileRecoveryRecord(t, agentStore, sessionID, agentsession.Record{ID: "recovery-step-finish", Type: agentsession.RecordStepFinished, RunID: "recovery-turn", Step: &agentsession.StepData{Attempt: 1, AssistantEntryID: "recovery-assistant", StopReason: string(llm.StopReasonToolUse)}})
	appendFileRecoveryRecord(t, agentStore, sessionID, agentsession.Record{ID: "recovery-tool-start", Type: agentsession.RecordToolStarted, RunID: "recovery-turn", Tool: &agentsession.ToolData{
		AssistantEntryID: "recovery-assistant", ToolCallID: "recovery-call", ToolName: "read_file",
		EffectiveArgs: json.RawMessage(`{"path":"go.mod","start_line":1,"end_line":1}`), IdempotencyKey: "recovery-turn:recovery-call",
		ResultEntryID: "recovery-result", ReplayPolicy: "safe",
	}})
	before, err := agentStore.Load(context.Background(), sessionID)
	seededPlan := agentsession.BuildRecoveryPlan(before)
	if err != nil || len(seededPlan.Actions) != 1 || seededPlan.Actions[0].Kind != agentsession.RecoveryRetryTool {
		t.Fatalf("seeded recovery plan = %#v, err=%v", seededPlan, err)
	}
	options.TrustWorkspace = false
	second, err := New(context.Background(), options)
	if err != nil {
		t.Fatalf("compose recovered application: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("close recovered application: %v", err)
	}
	after, err := agentStore.Load(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("load coordinated recovery: %v", err)
	}
	plan := agentsession.BuildRecoveryPlan(after)
	if len(plan.Actions) != 1 || plan.Actions[0].Kind != agentsession.RecoveryContinueRun || plan.Actions[0].Automatic {
		t.Fatalf("startup coordinator did not stop at manual continuation: %#v", plan)
	}
	var resultEntry, finishRecord bool
	for _, entry := range after.Entries {
		resultEntry = resultEntry || entry.ID == "recovery-result"
	}
	for _, record := range after.Records {
		finishRecord = finishRecord || record.Type == agentsession.RecordToolFinished && record.RunID == "recovery-turn"
	}
	if !resultEntry || !finishRecord {
		t.Fatalf("automatic safe replay was not durably completed: result=%v finish=%v", resultEntry, finishRecord)
	}
}

func appendFileRecoveryEntry(t *testing.T, repository agentsession.Repository, sessionID agentsession.ID, entry agentsession.Entry) {
	t.Helper()
	if _, err := repository.AppendEntry(context.Background(), sessionID, agentsession.MainLane, entry); err != nil {
		t.Fatalf("append file recovery entry %q: %v", entry.ID, err)
	}
}

func appendFileRecoveryRecord(t *testing.T, repository agentsession.Repository, sessionID agentsession.ID, record agentsession.Record) {
	t.Helper()
	if _, err := repository.AppendRecord(context.Background(), sessionID, agentsession.MainLane, record); err != nil {
		t.Fatalf("append file recovery record %q: %v", record.ID, err)
	}
}
