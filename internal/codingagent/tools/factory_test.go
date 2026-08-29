package codingtools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/eaglc/codepilot/internal/codingagent"
	"github.com/eaglc/codepilot/internal/codingagent/language"
	"github.com/eaglc/codepilot/internal/codingagent/lsp"
	"github.com/eaglc/codepilot/internal/llm"
	"github.com/eaglc/codepilot/internal/tool"
)

func TestBoundedTextPreservesUTF8AtByteLimit(t *testing.T) {
	got := boundedText("ab世界", 5)
	if !utf8.ValidString(got) || got != "ab世...[truncated]" {
		t.Fatalf("boundedText() = %q", got)
	}
}

func TestBoundedBufferSupportsConcurrentWriters(t *testing.T) {
	buffer := &boundedBuffer{limit: 128}
	var writers sync.WaitGroup
	for index := 0; index < 8; index++ {
		writers.Add(1)
		go func() {
			defer writers.Done()
			for attempt := 0; attempt < 32; attempt++ {
				if written, err := buffer.Write([]byte("abcd")); err != nil || written != 4 {
					t.Errorf("Write() = %d, %v", written, err)
				}
			}
		}()
	}
	writers.Wait()
	if got := len(buffer.Bytes()); got != 128 || !buffer.Truncated() {
		t.Fatalf("bounded buffer length=%d truncated=%v", got, buffer.Truncated())
	}
}

type fakeNavigator struct {
	ready           bool
	definitionCalls int
}

func (n *fakeNavigator) Ready(lsp.Scope) bool { return n.ready }
func (n *fakeNavigator) Definition(context.Context, lsp.Scope, string, lsp.Position, int) ([]lsp.Location, error) {
	n.definitionCalls++
	n.ready = true
	return []lsp.Location{{Path: "main.go", Range: lsp.Range{Start: lsp.Position{Line: 3, Column: 1}, End: lsp.Position{Line: 3, Column: 5}}}}, nil
}
func (*fakeNavigator) References(context.Context, lsp.Scope, string, lsp.Position, bool, int) ([]lsp.Location, error) {
	return nil, nil
}
func (*fakeNavigator) Diagnostics(context.Context, lsp.Scope, string, int) ([]lsp.Diagnostic, error) {
	return nil, nil
}
func (*fakeNavigator) DocumentSymbols(context.Context, lsp.Scope, string, int) ([]lsp.Symbol, error) {
	return nil, nil
}
func (*fakeNavigator) CloseWorktree(context.Context, string) error { return nil }
func (*fakeNavigator) Close() error                                { return nil }

func TestFactoryCreatesBoundedReadOnlyTools(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	factory := NewFactory(Options{})
	registry, err := factory.CreateTools(context.Background(), codingagent.ToolScope{SessionID: "session", WorkspaceID: "workspace", WorktreeID: "worktree", WorktreeRoot: root})
	if err != nil {
		t.Fatalf("create tools: %v", err)
	}
	if got := len(registry.Definitions()); got != 14 {
		t.Fatalf("tool definitions = %d", got)
	}
	result, err := registry.Execute(context.Background(), tool.Call{ID: "call-1", Name: "read_file", Arguments: json.RawMessage(`{"path":"main.go","start_line":1,"end_line":2}`)}, nil)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if result.Status != tool.ResultCompleted || !strings.Contains(result.Content[0].Text, "1: package main") {
		t.Fatalf("read result = %#v", result)
	}
}

func TestFactoryPlanProfilesGateWorkspaceReads(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	initial, err := NewFactory(Options{}).CreateTools(context.Background(), codingagent.ToolScope{
		Profile: codingagent.CapabilityPlan, SessionID: "session", WorkspaceID: "workspace", WorktreeID: "worktree", WorktreeRoot: root,
	})
	if err != nil {
		t.Fatalf("create initial Plan tools: %v", err)
	}
	if len(initial.Definitions()) != 0 {
		t.Fatalf("initial Plan profile exposed workspace tools: %#v", initial.Definitions())
	}
	registry, err := NewFactory(Options{}).CreateTools(context.Background(), codingagent.ToolScope{
		Profile: codingagent.CapabilityPlanWorkspace, SessionID: "session", WorkspaceID: "workspace", WorktreeID: "worktree", WorktreeRoot: root,
	})
	if err != nil {
		t.Fatalf("create workspace Plan tools: %v", err)
	}
	allowed := map[string]bool{
		"read_file": true, "list_files": true, "search_code": true,
		"git_status": true, "git_diff": true, "git_log": true,
		"git_branches": true, "git_show_commit": true,
	}
	if len(registry.Definitions()) != len(allowed) {
		t.Fatalf("workspace Plan tools = %#v", registry.Definitions())
	}
	for _, definition := range registry.Definitions() {
		if !allowed[definition.Name] {
			t.Fatalf("Plan profile exposed %q", definition.Name)
		}
	}
	for _, forbidden := range []string{"apply_patch", "create_file", "edit_file", "replace_file", "run_checks", "language_server"} {
		if _, found := registry.Lookup(forbidden); found {
			t.Fatalf("Plan profile exposed side-effect tool %q", forbidden)
		}
	}
}

func TestFactoryRegistersToolResultReaderWithReadableArtifactStore(t *testing.T) {
	registry, err := NewFactory(Options{Artifacts: &memoryArtifactStore{}}).CreateTools(context.Background(), codingagent.ToolScope{
		SessionID: "session", WorkspaceID: "workspace", WorktreeID: "worktree", WorktreeRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("create tools: %v", err)
	}
	for _, definition := range registry.Definitions() {
		if definition.Name == "read_tool_result" {
			return
		}
	}
	t.Fatal("read_tool_result was not registered for a readable artifact store")
}

func TestEditFileUsesExactReplacementAndGeneratedDiff(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "main.go")
	if err := os.WriteFile(path, []byte("package main\n\nvar answer = 41\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	registry, err := NewFactory(Options{}).CreateTools(context.Background(), codingagent.ToolScope{
		SessionID: "session", WorkspaceID: "workspace", WorktreeID: "worktree", WorktreeRoot: root,
		PermissionMode: codingagent.PermissionAutoEdit,
	})
	if err != nil {
		t.Fatalf("create tools: %v", err)
	}
	call := tool.Call{ID: "edit", Name: "edit_file", Arguments: json.RawMessage(`{"path":"main.go","old_text":"var answer = 41","new_text":"var answer = 42","intent":"Fix answer"}`)}
	result, err := registry.Execute(context.Background(), call, nil)
	if err != nil || result.Status != tool.ResultCompleted || !strings.Contains(string(result.Details), `"kind":"coding_patch_v1"`) || !strings.Contains(string(result.Details), "+var answer = 42") {
		t.Fatalf("edit result=%#v err=%v", result, err)
	}
	content, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(content), "var answer = 42") {
		t.Fatalf("edited content=%q err=%v", content, err)
	}
	missing, err := registry.Execute(context.Background(), tool.Call{ID: "missing", Name: "edit_file", Arguments: call.Arguments}, nil)
	if err != nil || missing.Status != tool.ResultInvalid || !strings.Contains(missing.Content[0].Text, "not found exactly once") {
		t.Fatalf("stale exact edit result=%#v err=%v", missing, err)
	}
}

func TestCreateFileBuildsNestedStructureInEmptyWorktree(t *testing.T) {
	root := t.TempDir()
	registry, err := NewFactory(Options{}).CreateTools(context.Background(), codingagent.ToolScope{
		SessionID: "session", WorkspaceID: "workspace", WorktreeID: "worktree", WorktreeRoot: root,
		PermissionMode: codingagent.PermissionAutoEdit,
	})
	if err != nil {
		t.Fatalf("create tools: %v", err)
	}
	call := tool.Call{ID: "create", Name: "create_file", Arguments: mustJSON(map[string]string{
		"path": "cmd/app/main.go", "content": "package main\n\nfunc main() {}\n", "intent": "Create application entrypoint",
	})}
	result, err := registry.Execute(context.Background(), call, nil)
	if err != nil || result.Status != tool.ResultCompleted || !strings.Contains(string(result.Details), `"kind":"coding_patch_v1"`) {
		t.Fatalf("create result=%#v err=%v", result, err)
	}
	content, err := os.ReadFile(filepath.Join(root, "cmd", "app", "main.go"))
	if err != nil || string(content) != "package main\n\nfunc main() {}\n" {
		t.Fatalf("created content=%q err=%v", content, err)
	}
	again, err := registry.Execute(context.Background(), call, nil)
	if err != nil || again.Status != tool.ResultInvalid || !strings.Contains(again.Content[0].Text, "already exists") {
		t.Fatalf("duplicate create=%#v err=%v", again, err)
	}
	empty, err := registry.Execute(context.Background(), tool.Call{ID: "empty", Name: "create_file", Arguments: json.RawMessage(`{"path":"testdata/.gitkeep","content":""}`)}, nil)
	if err != nil || empty.Status != tool.ResultCompleted {
		t.Fatalf("empty placeholder=%#v err=%v", empty, err)
	}
	if info, err := os.Stat(filepath.Join(root, "testdata", ".gitkeep")); err != nil || info.Size() != 0 {
		t.Fatalf("empty placeholder info=%#v err=%v", info, err)
	}
}

func TestCreateFileApprovalPreventsOverwriteAndSupportsResume(t *testing.T) {
	root := t.TempDir()
	registry, err := NewFactory(Options{}).CreateTools(context.Background(), codingagent.ToolScope{
		SessionID: "session", WorkspaceID: "workspace", WorktreeID: "worktree", WorktreeRoot: root,
		PermissionMode: codingagent.PermissionAsk,
	})
	if err != nil {
		t.Fatalf("create tools: %v", err)
	}
	call := tool.Call{ID: "create", Name: "create_file", IdempotencyKey: "turn:create", Arguments: mustJSON(map[string]string{
		"path": "internal/config/config.go", "content": "package config\n", "intent": "Create configuration package",
	})}
	pending, err := registry.Execute(context.Background(), call, nil)
	if err != nil || pending.Status != tool.ResultInterrupted || pending.Interrupt == nil {
		t.Fatalf("pending creation=%#v err=%v", pending, err)
	}
	if _, err := os.Stat(filepath.Join(root, "internal")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("creation changed worktree before approval: %v", err)
	}
	completed, err := registry.Resume(context.Background(), call, *pending.Interrupt, tool.Result{
		Status: tool.ResultCompleted, Content: []llm.Content{{Type: llm.ContentText, Text: "approved"}},
	}, nil)
	if err != nil || completed.Status != tool.ResultCompleted {
		t.Fatalf("resumed creation=%#v err=%v", completed, err)
	}
	if content, err := os.ReadFile(filepath.Join(root, "internal", "config", "config.go")); err != nil || string(content) != "package config\n" {
		t.Fatalf("approved creation content=%q err=%v", content, err)
	}
	driftCall := tool.Call{ID: "drift", Name: "create_file", IdempotencyKey: "turn:drift", Arguments: mustJSON(map[string]string{
		"path": "README.md", "content": "agent content\n", "intent": "Create readme",
	})}
	driftPending, err := registry.Execute(context.Background(), driftCall, nil)
	if err != nil || driftPending.Status != tool.ResultInterrupted || driftPending.Interrupt == nil {
		t.Fatalf("pending drift creation=%#v err=%v", driftPending, err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("user content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	drifted, err := registry.Resume(context.Background(), driftCall, *driftPending.Interrupt, tool.Result{
		Status: tool.ResultCompleted, Content: []llm.Content{{Type: llm.ContentText, Text: "approved"}},
	}, nil)
	if err != nil || drifted.Status != tool.ResultFailed {
		t.Fatalf("drifted creation=%#v err=%v", drifted, err)
	}
	if content, _ := os.ReadFile(filepath.Join(root, "README.md")); string(content) != "user content\n" {
		t.Fatalf("drifted creation overwrote user file: %q", content)
	}
}

func TestCreateFileSessionGrantCoversNewPathsButNotOtherWriteTools(t *testing.T) {
	root := t.TempDir()
	now := time.Now().UTC()
	grant := codingagent.PermissionGrant{
		ID: "grant-create", Scope: codingagent.PermissionGrantSession, ToolName: "create_file", Action: codingagent.PermissionActionModify, AllPaths: true,
		SourceTurnID: "turn", SourceInterruptID: "approval", CreatedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour),
	}
	registry, err := NewFactory(Options{}).CreateTools(context.Background(), codingagent.ToolScope{
		SessionID: "session", WorkspaceID: "workspace", WorktreeID: "worktree", WorktreeRoot: root,
		PermissionMode: codingagent.PermissionAsk, PermissionGrants: []codingagent.PermissionGrant{grant},
	})
	if err != nil {
		t.Fatalf("create tools: %v", err)
	}
	for index, target := range []string{"cmd/app/main.go", "internal/config/config.go"} {
		result, executeErr := registry.Execute(context.Background(), tool.Call{
			ID: fmt.Sprintf("create-%d", index), Name: "create_file",
			Arguments: mustJSON(map[string]string{"path": target, "content": "package generated\n"}),
		}, nil)
		if executeErr != nil || result.Status != tool.ResultCompleted || result.Interrupt != nil {
			t.Fatalf("session-granted create %q = %#v err=%v", target, result, executeErr)
		}
	}
	result, err := registry.Execute(context.Background(), tool.Call{
		ID: "edit", Name: "edit_file",
		Arguments: json.RawMessage(`{"path":"cmd/app/main.go","old_text":"generated","new_text":"changed"}`),
	}, nil)
	if err != nil || result.Status != tool.ResultInterrupted || result.Interrupt == nil {
		t.Fatalf("create_file grant leaked to edit_file: result=%#v err=%v", result, err)
	}
}

func TestCreateFileRejectsSensitiveTraversalAndSecretContent(t *testing.T) {
	root := t.TempDir()
	registry, err := NewFactory(Options{}).CreateTools(context.Background(), codingagent.ToolScope{
		SessionID: "session", WorkspaceID: "workspace", WorktreeID: "worktree", WorktreeRoot: root,
		PermissionMode: codingagent.PermissionAutoEdit,
	})
	if err != nil {
		t.Fatalf("create tools: %v", err)
	}
	for name, arguments := range map[string]json.RawMessage{
		"traversal": json.RawMessage(`{"path":"../outside.txt","content":"safe"}`),
		"sensitive": json.RawMessage(`{"path":".env","content":"PUBLIC=true"}`),
		"secret":    json.RawMessage(`{"path":"config.txt","content":"API_KEY=top-secret-value"}`),
	} {
		result, executeErr := registry.Execute(context.Background(), tool.Call{ID: name, Name: "create_file", Arguments: arguments}, nil)
		if executeErr != nil || result.Status == tool.ResultCompleted || result.Interrupt != nil {
			t.Fatalf("%s creation=%#v err=%v", name, result, executeErr)
		}
	}
}

func TestEditFilePreservesLineEndingsAndEmbeddedCarriageReturns(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "README.md")
	before := "heading\r\nconst marker = \"left\rright\";\r\nfooter\r\n"
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	registry, err := NewFactory(Options{}).CreateTools(context.Background(), codingagent.ToolScope{
		SessionID: "session", WorkspaceID: "workspace", WorktreeID: "worktree", WorktreeRoot: root,
		PermissionMode: codingagent.PermissionAutoEdit,
	})
	if err != nil {
		t.Fatalf("create tools: %v", err)
	}
	arguments := mustJSON(map[string]string{
		"path": "README.md", "old_text": "left\rright", "new_text": "fixed", "intent": "Repair malformed sample",
	})
	result, err := registry.Execute(context.Background(), tool.Call{ID: "edit-crlf", Name: "edit_file", Arguments: arguments}, nil)
	if err != nil || result.Status != tool.ResultCompleted {
		t.Fatalf("edit result=%#v err=%v", result, err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read edited file: %v", err)
	}
	want := "heading\r\nconst marker = \"fixed\";\r\nfooter\r\n"
	if string(content) != want {
		t.Fatalf("line endings changed: got %q want %q", content, want)
	}
}

func TestEditFileMatchesLFArgumentsAcrossCRLFLineBoundaries(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "README.md")
	if err := os.WriteFile(path, []byte("first\r\nsecond\r\nthird\r\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	registry, err := NewFactory(Options{}).CreateTools(context.Background(), codingagent.ToolScope{
		SessionID: "session", WorkspaceID: "workspace", WorktreeID: "worktree", WorktreeRoot: root,
		PermissionMode: codingagent.PermissionAutoEdit,
	})
	if err != nil {
		t.Fatalf("create tools: %v", err)
	}
	arguments := mustJSON(map[string]string{
		"path": "README.md", "old_text": "first\nsecond", "new_text": "FIRST\nSECOND", "intent": "Rewrite section",
	})
	result, err := registry.Execute(context.Background(), tool.Call{ID: "edit-multiline", Name: "edit_file", Arguments: arguments}, nil)
	if err != nil || result.Status != tool.ResultCompleted {
		t.Fatalf("multiline edit result=%#v err=%v", result, err)
	}
	content, _ := os.ReadFile(path)
	if string(content) != "FIRST\r\nSECOND\r\nthird\r\n" {
		t.Fatalf("multiline edit changed line endings: %q", content)
	}
}

func TestReadFileReportsAndNormalizesLineEndingMetadata(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "README.md")
	if err := os.WriteFile(path, []byte("first\r\nsecond\r\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	registry, err := NewFactory(Options{}).CreateTools(context.Background(), codingagent.ToolScope{
		SessionID: "session", WorkspaceID: "workspace", WorktreeID: "worktree", WorktreeRoot: root,
	})
	if err != nil {
		t.Fatalf("create tools: %v", err)
	}
	result, err := registry.Execute(context.Background(), tool.Call{ID: "read", Name: "read_file", Arguments: json.RawMessage(`{"path":"README.md"}`)}, nil)
	if err != nil || result.Status != tool.ResultCompleted || strings.Contains(result.Content[0].Text, "\r") {
		t.Fatalf("read result=%#v err=%v", result, err)
	}
	var details struct {
		SHA256          string `json:"sha256"`
		LineEnding      string `json:"line_ending"`
		EndsWithNewline bool   `json:"ends_with_newline"`
	}
	if err := json.Unmarshal(result.Details, &details); err != nil || len(details.SHA256) != 64 || details.LineEnding != "crlf" || !details.EndsWithNewline {
		t.Fatalf("read details=%#v err=%v", details, err)
	}
}

func TestReplaceFileGeneratesDiffPreservesCRLFAndChecksExpectedDigest(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "README.md")
	if err := os.WriteFile(path, []byte("old title\r\nold body\r\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	registry, err := NewFactory(Options{}).CreateTools(context.Background(), codingagent.ToolScope{
		SessionID: "session", WorkspaceID: "workspace", WorktreeID: "worktree", WorktreeRoot: root,
		PermissionMode: codingagent.PermissionAutoEdit,
	})
	if err != nil {
		t.Fatalf("create tools: %v", err)
	}
	read, err := registry.Execute(context.Background(), tool.Call{ID: "read", Name: "read_file", Arguments: json.RawMessage(`{"path":"README.md"}`)}, nil)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	var metadata struct {
		SHA256 string `json:"sha256"`
	}
	if err := json.Unmarshal(read.Details, &metadata); err != nil {
		t.Fatalf("decode read metadata: %v", err)
	}
	arguments := mustJSON(map[string]string{
		"path": "README.md", "content": "# Short\n\nConcise.\n", "expected_sha256": metadata.SHA256, "intent": "Simplify README",
	})
	result, err := registry.Execute(context.Background(), tool.Call{ID: "replace", Name: "replace_file", Arguments: arguments}, nil)
	if err != nil || result.Status != tool.ResultCompleted || !strings.Contains(string(result.Details), `"kind":"coding_patch_v1"`) {
		t.Fatalf("replace result=%#v err=%v", result, err)
	}
	var replacementDetails patchToolDetails
	if err := json.Unmarshal(result.Details, &replacementDetails); err != nil {
		t.Fatalf("decode replacement details: %v", err)
	}
	if replacementDetails.Diff == nil || strings.Contains(replacementDetails.Diff.Text, "\r") {
		t.Fatalf("replacement diff contains terminal carriage returns: %#v", replacementDetails.Diff)
	}
	content, _ := os.ReadFile(path)
	if string(content) != "# Short\r\n\r\nConcise.\r\n" {
		t.Fatalf("replacement did not preserve CRLF: %q", content)
	}
	stale, err := registry.Execute(context.Background(), tool.Call{ID: "stale", Name: "replace_file", Arguments: arguments}, nil)
	if err != nil || stale.Status != tool.ResultFailed || !strings.Contains(stale.Content[0].Text, "changed since it was read") {
		t.Fatalf("stale replacement result=%#v err=%v", stale, err)
	}
}

func TestReplaceFileWaitsForApprovalAndRejectsApprovalDrift(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "README.md")
	if err := os.WriteFile(path, []byte("old\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	registry, err := NewFactory(Options{}).CreateTools(context.Background(), codingagent.ToolScope{
		SessionID: "session", WorkspaceID: "workspace", WorktreeID: "worktree", WorktreeRoot: root,
		PermissionMode: codingagent.PermissionAsk,
	})
	if err != nil {
		t.Fatalf("create tools: %v", err)
	}
	call := tool.Call{
		ID: "replace", Name: "replace_file", IdempotencyKey: "turn:replace",
		Arguments: mustJSON(map[string]string{"path": "README.md", "content": "new\n", "intent": "Simplify README"}),
	}
	pending, err := registry.Execute(context.Background(), call, nil)
	if err != nil || pending.Status != tool.ResultInterrupted || pending.Interrupt == nil {
		t.Fatalf("pending replacement=%#v err=%v", pending, err)
	}
	if content, _ := os.ReadFile(path); string(content) != "old\n" {
		t.Fatalf("file changed before approval: %q", content)
	}
	if err := os.WriteFile(path, []byte("user change\n"), 0o600); err != nil {
		t.Fatalf("write drift: %v", err)
	}
	result, err := registry.Resume(context.Background(), call, *pending.Interrupt, tool.Result{
		Status: tool.ResultCompleted, Content: []llm.Content{{Type: llm.ContentText, Text: "approved"}},
	}, nil)
	if err != nil || result.Status != tool.ResultFailed || !strings.Contains(result.Content[0].Text, "changed after approval") {
		t.Fatalf("drifted replacement=%#v err=%v", result, err)
	}
	if content, _ := os.ReadFile(path); string(content) != "user change\n" {
		t.Fatalf("drifted file was overwritten: %q", content)
	}
}

func TestReplaceFileApprovalResumesWholeFileReplacement(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "README.md")
	if err := os.WriteFile(path, []byte("old\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	registry, err := NewFactory(Options{}).CreateTools(context.Background(), codingagent.ToolScope{
		SessionID: "session", WorkspaceID: "workspace", WorktreeID: "worktree", WorktreeRoot: root,
		PermissionMode: codingagent.PermissionAsk,
	})
	if err != nil {
		t.Fatalf("create tools: %v", err)
	}
	call := tool.Call{
		ID: "replace", Name: "replace_file", IdempotencyKey: "turn:replace",
		Arguments: mustJSON(map[string]string{"path": "README.md", "content": "new\n", "intent": "Simplify README"}),
	}
	pending, err := registry.Execute(context.Background(), call, nil)
	if err != nil || pending.Status != tool.ResultInterrupted || pending.Interrupt == nil {
		t.Fatalf("pending replacement=%#v err=%v", pending, err)
	}
	result, err := registry.Resume(context.Background(), call, *pending.Interrupt, tool.Result{
		Status: tool.ResultCompleted, Content: []llm.Content{{Type: llm.ContentText, Text: "approved"}},
	}, nil)
	if err != nil || result.Status != tool.ResultCompleted {
		t.Fatalf("approved replacement=%#v err=%v", result, err)
	}
	if content, _ := os.ReadFile(path); string(content) != "new\n" {
		t.Fatalf("approved content=%q", content)
	}
}

func TestEditFileApprovalResumesOriginalExactEdit(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "main.go")
	if err := os.WriteFile(path, []byte("old\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	registry, err := NewFactory(Options{}).CreateTools(context.Background(), codingagent.ToolScope{
		SessionID: "session", WorkspaceID: "workspace", WorktreeID: "worktree", WorktreeRoot: root,
		PermissionMode: codingagent.PermissionAsk,
	})
	if err != nil {
		t.Fatalf("create tools: %v", err)
	}
	call := tool.Call{ID: "edit", Name: "edit_file", IdempotencyKey: "turn:edit", Arguments: json.RawMessage(`{"path":"main.go","old_text":"old","new_text":"new"}`)}
	pending, err := registry.Execute(context.Background(), call, nil)
	if err != nil || pending.Status != tool.ResultInterrupted || pending.Interrupt == nil {
		t.Fatalf("pending edit=%#v err=%v", pending, err)
	}
	completed, err := registry.Resume(context.Background(), call, *pending.Interrupt, tool.Result{
		Status: tool.ResultCompleted, Content: []llm.Content{{Type: llm.ContentText, Text: "approved"}},
	}, nil)
	if err != nil || completed.Status != tool.ResultCompleted {
		t.Fatalf("resumed edit=%#v err=%v", completed, err)
	}
	content, _ := os.ReadFile(path)
	if string(content) != "new\n" {
		t.Fatalf("resumed edit content=%q", content)
	}
}

func TestReadFileRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	registry, err := NewFactory(Options{}).CreateTools(context.Background(), codingagent.ToolScope{SessionID: "session", WorkspaceID: "workspace", WorktreeID: "worktree", WorktreeRoot: root})
	if err != nil {
		t.Fatalf("create tools: %v", err)
	}
	result, err := registry.Execute(context.Background(), tool.Call{ID: "call-1", Name: "read_file", Arguments: json.RawMessage(`{"path":"../secret"}`)}, nil)
	if err != nil {
		t.Fatalf("execute read: %v", err)
	}
	if result.Status != tool.ResultDenied {
		t.Fatalf("traversal result = %#v", result)
	}
}

func TestSensitiveReadRequiresApprovalAndRemainsRedacted(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("API_KEY=top-secret-value\nPUBLIC=true\n"), 0o600); err != nil {
		t.Fatalf("write sensitive fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".env.example"), []byte("API_KEY=example-placeholder\n"), 0o600); err != nil {
		t.Fatalf("write example fixture: %v", err)
	}
	registry, err := NewFactory(Options{}).CreateTools(context.Background(), codingagent.ToolScope{SessionID: "session", WorkspaceID: "workspace", WorktreeID: "worktree", WorktreeRoot: root})
	if err != nil {
		t.Fatalf("create tools: %v", err)
	}
	call := tool.Call{ID: "read-secret", Name: "read_file", Arguments: json.RawMessage(`{"path":".env"}`), IdempotencyKey: "turn:read-secret"}
	pending, err := registry.Execute(context.Background(), call, nil)
	if err != nil || pending.Status != tool.ResultInterrupted || pending.Interrupt == nil || strings.Contains(pending.Content[0].Text, "top-secret-value") {
		t.Fatalf("sensitive read boundary: result=%#v err=%v", pending, err)
	}
	completed, err := registry.Resume(context.Background(), call, *pending.Interrupt, tool.Result{
		Status: tool.ResultCompleted, Content: []llm.Content{{Type: llm.ContentText, Text: "approved"}},
	}, nil)
	if err != nil || completed.Status != tool.ResultCompleted || strings.Contains(completed.Content[0].Text, "top-secret-value") || !strings.Contains(completed.Content[0].Text, codingagent.RedactedValue) {
		t.Fatalf("approved sensitive read was not redacted: result=%#v err=%v", completed, err)
	}
	example, err := registry.Execute(context.Background(), tool.Call{ID: "example", Name: "read_file", Arguments: json.RawMessage(`{"path":".env.example"}`)}, nil)
	if err != nil || example.Status != tool.ResultCompleted || example.Interrupt != nil || strings.Contains(example.Content[0].Text, "example-placeholder") {
		t.Fatalf("template read/redaction: result=%#v err=%v", example, err)
	}
}

func TestListAndSearchExcludeBuiltInAndConfiguredSensitivePaths(t *testing.T) {
	root := t.TempDir()
	runGitFixture(t, root, "init", "--quiet")
	fixtures := map[string]string{
		"main.go":                      "package main // ordinary-marker\n",
		".gitignore":                   "ignored.go\n",
		"ignored.go":                   "package ignored // ignored-marker\n",
		"vendor/dependency.go":         "package dependency // vendor-marker\n",
		"node_modules/module/index.js": "// module-marker\n",
		".env":                         "TOKEN=top-secret-value\n",
		"private-data/credential.txt":  "top-secret-value\n",
	}
	for name, content := range fixtures {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("create fixture parent: %v", err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("write fixture %s: %v", name, err)
		}
	}
	registry, err := NewFactory(Options{}).CreateTools(context.Background(), codingagent.ToolScope{
		SessionID: "session", WorkspaceID: "workspace", WorktreeID: "worktree", WorktreeRoot: root, SensitivePaths: []string{"private-data"},
	})
	if err != nil {
		t.Fatalf("create tools: %v", err)
	}
	listed, err := registry.Execute(context.Background(), tool.Call{ID: "list", Name: "list_files", Arguments: json.RawMessage(`{}`)}, nil)
	if err != nil || listed.Status != tool.ResultCompleted || strings.Contains(listed.Content[0].Text, ".env") || strings.Contains(listed.Content[0].Text, "private-data") || !strings.Contains(listed.Content[0].Text, "main.go") {
		t.Fatalf("sensitive listing: result=%#v err=%v", listed, err)
	}
	if strings.Contains(listed.Content[0].Text, "ignored.go") || strings.Contains(listed.Content[0].Text, "vendor/") || strings.Contains(listed.Content[0].Text, "node_modules/") {
		t.Fatalf("ignored/dependency files entered listing: %#v", listed)
	}
	searched, err := registry.Execute(context.Background(), tool.Call{ID: "search", Name: "search_code", Arguments: json.RawMessage(`{"query":"top-secret-value"}`)}, nil)
	if err != nil || searched.Status != tool.ResultCompleted || strings.Contains(searched.Content[0].Text, "top-secret-value") || !strings.Contains(searched.Content[0].Text, "No matches") {
		t.Fatalf("sensitive search: result=%#v err=%v", searched, err)
	}
	ignoredSearch, err := registry.Execute(context.Background(), tool.Call{ID: "ignored", Name: "search_code", Arguments: json.RawMessage(`{"query":"marker"}`)}, nil)
	if err != nil || strings.Contains(ignoredSearch.Content[0].Text, "ignored-marker") || strings.Contains(ignoredSearch.Content[0].Text, "vendor-marker") || strings.Contains(ignoredSearch.Content[0].Text, "module-marker") || !strings.Contains(ignoredSearch.Content[0].Text, "ordinary-marker") {
		t.Fatalf("ignored/dependency search result=%#v err=%v", ignoredSearch, err)
	}
	direct, err := registry.Execute(context.Background(), tool.Call{ID: "direct", Name: "search_code", Arguments: json.RawMessage(`{"query":"secret","path":"private-data"}`)}, nil)
	if err != nil || direct.Status != tool.ResultDenied {
		t.Fatalf("direct sensitive search was not denied: result=%#v err=%v", direct, err)
	}
}

func TestApplyPatchBlocksSensitiveTargetsAndRecognizedSecretValues(t *testing.T) {
	root := t.TempDir()
	registry, err := NewFactory(Options{}).CreateTools(context.Background(), codingagent.ToolScope{
		SessionID: "session", WorkspaceID: "workspace", WorktreeID: "worktree", WorktreeRoot: root, PermissionMode: codingagent.PermissionAsk,
	})
	if err != nil {
		t.Fatalf("create tools: %v", err)
	}
	for name, patch := range map[string]string{
		"path":   "--- /dev/null\n+++ b/.env\n@@ -0,0 +1 @@\n+PUBLIC=true\n",
		"secret": "--- /dev/null\n+++ b/config.txt\n@@ -0,0 +1 @@\n+API_KEY=top-secret-value\n",
	} {
		result, executeErr := registry.Execute(context.Background(), tool.Call{ID: name, Name: "apply_patch", Arguments: mustJSON(map[string]string{"patch": patch})}, nil)
		if executeErr != nil || result.Status != tool.ResultDenied || result.Interrupt != nil {
			t.Fatalf("%s sensitive patch: result=%#v err=%v", name, result, executeErr)
		}
	}
}

func TestApplyPatchWaitsForApprovalThenReturnsInlineDiff(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "main.go")
	if err := os.WriteFile(path, []byte("package old\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	registry, err := NewFactory(Options{}).CreateTools(context.Background(), codingagent.ToolScope{
		SessionID: "session", WorkspaceID: "workspace", WorktreeID: "worktree", WorktreeRoot: root,
		PermissionMode: codingagent.PermissionAsk,
	})
	if err != nil {
		t.Fatalf("create tools: %v", err)
	}
	patch := "--- a/main.go\n+++ b/main.go\n@@ -1 +1 @@\n-package old\n+package main\n"
	arguments, _ := json.Marshal(map[string]string{"patch": patch, "intent": "Fix package name"})
	call := tool.Call{ID: "call-patch", Name: "apply_patch", Arguments: arguments, IdempotencyKey: "turn:call-patch"}
	pending, err := registry.Execute(context.Background(), call, nil)
	if err != nil {
		t.Fatalf("prepare patch: %v", err)
	}
	if pending.Status != tool.ResultInterrupted || pending.Interrupt == nil || pending.Interrupt.Kind != "approval" {
		t.Fatalf("pending result = %#v", pending)
	}
	var approval struct {
		Kind        string            `json:"kind"`
		Patch       string            `json:"patch"`
		Files       []string          `json:"files"`
		BeforeState map[string]string `json:"before_state"`
		Digest      string            `json:"digest"`
	}
	if err := json.Unmarshal(pending.Interrupt.Payload, &approval); err != nil {
		t.Fatalf("decode approval payload: %v", err)
	}
	if approval.Kind != "coding_patch_approval_v1" || approval.Patch != patch || len(approval.Files) != 1 || len(approval.BeforeState) != 1 || approval.Digest == "" {
		t.Fatalf("approval payload = %#v", approval)
	}
	unchanged, _ := os.ReadFile(path)
	if string(unchanged) != "package old\n" {
		t.Fatalf("patch changed before approval: %q", unchanged)
	}
	completed, err := registry.Resume(context.Background(), call, *pending.Interrupt, tool.Result{
		Status: tool.ResultCompleted, Content: []llm.Content{{Type: llm.ContentText, Text: "approved"}},
	}, nil)
	if err != nil {
		t.Fatalf("resume patch: %v", err)
	}
	if completed.Status != tool.ResultCompleted || !strings.Contains(string(completed.Details), `"kind":"coding_patch_v1"`) {
		t.Fatalf("completed result = %#v", completed)
	}
	changed, _ := os.ReadFile(path)
	if strings.ReplaceAll(string(changed), "\r\n", "\n") != "package main\n" {
		t.Fatalf("applied content = %q", changed)
	}
}

func TestApplyPatchApprovalRejectsWorktreeDrift(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "main.go")
	if err := os.WriteFile(path, []byte("old\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	registry, err := NewFactory(Options{}).CreateTools(context.Background(), codingagent.ToolScope{
		SessionID: "session", WorkspaceID: "workspace", WorktreeID: "worktree", WorktreeRoot: root,
		PermissionMode: codingagent.PermissionAsk,
	})
	if err != nil {
		t.Fatalf("create tools: %v", err)
	}
	arguments := json.RawMessage(`{"patch":"--- a/main.go\n+++ b/main.go\n@@ -1 +1 @@\n-old\n+new\n"}`)
	call := tool.Call{ID: "call-patch", Name: "apply_patch", Arguments: arguments, IdempotencyKey: "turn:call-patch"}
	pending, err := registry.Execute(context.Background(), call, nil)
	if err != nil || pending.Interrupt == nil {
		t.Fatalf("prepare patch: result=%#v err=%v", pending, err)
	}
	if err := os.WriteFile(path, []byte("user change\n"), 0o600); err != nil {
		t.Fatalf("write drift: %v", err)
	}
	result, err := registry.Resume(context.Background(), call, *pending.Interrupt, tool.Result{
		Status: tool.ResultCompleted, Content: []llm.Content{{Type: llm.ContentText, Text: "approved"}},
	}, nil)
	if err != nil {
		t.Fatalf("resume patch: %v", err)
	}
	if result.Status != tool.ResultFailed || !strings.Contains(result.Content[0].Text, "changed after approval") {
		t.Fatalf("drift result = %#v", result)
	}
	content, _ := os.ReadFile(path)
	if string(content) != "user change\n" {
		t.Fatalf("drifted content was overwritten: %q", content)
	}
}

func TestApplyPatchSessionGrantIsExactAndSkipsRepeatedApproval(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"main.go", "other.go"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("old\n"), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	now := time.Now().UTC()
	grant := codingagent.PermissionGrant{
		ID: "grant-main", Scope: codingagent.PermissionGrantSession, ToolName: "apply_patch", Action: codingagent.PermissionActionModify,
		Paths: []string{"main.go"}, SourceTurnID: "turn", SourceInterruptID: "approval", CreatedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour),
	}
	registry, err := NewFactory(Options{}).CreateTools(context.Background(), codingagent.ToolScope{
		SessionID: "session", WorkspaceID: "workspace", WorktreeID: "worktree", WorktreeRoot: root,
		PermissionMode: codingagent.PermissionAsk, PermissionGrants: []codingagent.PermissionGrant{grant},
	})
	if err != nil {
		t.Fatalf("create tools: %v", err)
	}
	mainCall := tool.Call{ID: "main", Name: "apply_patch", Arguments: json.RawMessage(`{"patch":"--- a/main.go\n+++ b/main.go\n@@ -1 +1 @@\n-old\n+new\n"}`)}
	result, err := registry.Execute(context.Background(), mainCall, nil)
	if err != nil || result.Status != tool.ResultCompleted {
		t.Fatalf("granted patch: result=%#v err=%v", result, err)
	}
	otherCall := tool.Call{ID: "other", Name: "apply_patch", Arguments: json.RawMessage(`{"patch":"--- a/other.go\n+++ b/other.go\n@@ -1 +1 @@\n-old\n+new\n"}`)}
	result, err = registry.Execute(context.Background(), otherCall, nil)
	if err != nil || result.Status != tool.ResultInterrupted {
		t.Fatalf("out-of-scope patch did not request approval: result=%#v err=%v", result, err)
	}
}

func TestAutoEditAppliesSafeTextButRequiresApprovalForExcludedTargets(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("old\n"), 0o600); err != nil {
		t.Fatalf("write safe target: %v", err)
	}
	workflowDir := filepath.Join(root, ".github", "workflows")
	if err := os.MkdirAll(workflowDir, 0o700); err != nil {
		t.Fatalf("create workflow directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workflowDir, "ci.yml"), []byte("old\n"), 0o600); err != nil {
		t.Fatalf("write workflow: %v", err)
	}
	registry, err := NewFactory(Options{}).CreateTools(context.Background(), codingagent.ToolScope{
		SessionID: "session", WorkspaceID: "workspace", WorktreeID: "worktree", WorktreeRoot: root, PermissionMode: codingagent.PermissionAutoEdit,
	})
	if err != nil {
		t.Fatalf("create tools: %v", err)
	}
	safe, err := registry.Execute(context.Background(), tool.Call{ID: "safe", Name: "apply_patch", Arguments: json.RawMessage(`{"patch":"--- a/main.go\n+++ b/main.go\n@@ -1 +1 @@\n-old\n+new\n"}`)}, nil)
	if err != nil || safe.Status != tool.ResultCompleted {
		t.Fatalf("safe automatic edit: result=%#v err=%v", safe, err)
	}
	guarded, err := registry.Execute(context.Background(), tool.Call{ID: "guarded", Name: "apply_patch", Arguments: json.RawMessage(`{"patch":"--- a/.github/workflows/ci.yml\n+++ b/.github/workflows/ci.yml\n@@ -1 +1 @@\n-old\n+new\n"}`)}, nil)
	if err != nil || guarded.Status != tool.ResultInterrupted || guarded.Interrupt == nil {
		t.Fatalf("excluded automatic target did not require approval: result=%#v err=%v", guarded, err)
	}
	content, _ := os.ReadFile(filepath.Join(workflowDir, "ci.yml"))
	if string(content) != "old\n" {
		t.Fatalf("excluded target changed without approval: %q", content)
	}
}

func TestLanguageNavigationRequiresProcessApprovalThenReturnsBoundedResult(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	navigator := &fakeNavigator{}
	registry, err := NewFactory(Options{Languages: language.NewDefaultRegistry(), Navigator: navigator}).CreateTools(context.Background(), codingagent.ToolScope{
		SessionID: "session", WorkspaceID: "workspace", WorktreeID: "worktree", WorktreeRoot: root, PermissionMode: codingagent.PermissionAsk,
	})
	if err != nil {
		t.Fatalf("create tools: %v", err)
	}
	if len(registry.Definitions()) != 18 {
		t.Fatalf("tool definitions = %d, want 18", len(registry.Definitions()))
	}
	call := tool.Call{ID: "definition", Name: "find_definition", Arguments: json.RawMessage(`{"path":"main.go","line":1,"column":1}`), IdempotencyKey: "turn:definition"}
	pending, err := registry.Execute(context.Background(), call, nil)
	if err != nil || pending.Status != tool.ResultInterrupted || pending.Interrupt == nil || navigator.definitionCalls != 0 {
		t.Fatalf("language-server approval: result=%#v calls=%d err=%v", pending, navigator.definitionCalls, err)
	}
	result, err := registry.Resume(context.Background(), call, *pending.Interrupt, tool.Result{Status: tool.ResultCompleted, Content: []llm.Content{{Type: llm.ContentText, Text: "approved"}}}, nil)
	if err != nil || result.Status != tool.ResultCompleted || navigator.definitionCalls != 1 || !strings.Contains(result.Content[0].Text, "main.go:3:1-3:5") {
		t.Fatalf("approved definition: result=%#v calls=%d err=%v", result, navigator.definitionCalls, err)
	}
}

func TestLanguageNavigationHonorsReadOnlyGrantAndSensitivePathBoundaries(t *testing.T) {
	root := t.TempDir()
	for name, content := range map[string]string{"go.mod": "module test\n", "main.go": "package main\n", "private/main.go": "package private\n"} {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	create := func(mode codingagent.PermissionMode, grants []codingagent.PermissionGrant) (*tool.Registry, *fakeNavigator) {
		t.Helper()
		navigator := &fakeNavigator{}
		registry, err := NewFactory(Options{Languages: language.NewDefaultRegistry(), Navigator: navigator}).CreateTools(context.Background(), codingagent.ToolScope{
			SessionID: "session", WorkspaceID: "workspace", WorktreeID: "worktree", WorktreeRoot: root,
			PermissionMode: mode, PermissionGrants: grants, SensitivePaths: []string{"private"},
		})
		if err != nil {
			t.Fatal(err)
		}
		return registry, navigator
	}
	call := tool.Call{ID: "definition", Name: "find_definition", Arguments: json.RawMessage(`{"path":"main.go","line":1,"column":1}`)}
	readOnly, _ := create(codingagent.PermissionReadOnly, nil)
	result, err := readOnly.Execute(context.Background(), call, nil)
	if err != nil || result.Status != tool.ResultDenied {
		t.Fatalf("read-only navigation: result=%#v err=%v", result, err)
	}
	now := time.Now().UTC()
	grant := codingagent.PermissionGrant{ID: "lsp-grant", Scope: codingagent.PermissionGrantSession, ToolName: "language_server", Action: codingagent.PermissionStartLanguageServerAction("go"), SourceTurnID: "turn", SourceInterruptID: "approval", CreatedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour)}
	granted, navigator := create(codingagent.PermissionAsk, []codingagent.PermissionGrant{grant})
	result, err = granted.Execute(context.Background(), call, nil)
	if err != nil || result.Status != tool.ResultCompleted || navigator.definitionCalls != 1 {
		t.Fatalf("granted navigation: result=%#v calls=%d err=%v", result, navigator.definitionCalls, err)
	}
	sensitive := call
	sensitive.ID = "sensitive"
	sensitive.Arguments = json.RawMessage(`{"path":"private/main.go","line":1,"column":1}`)
	result, err = granted.Execute(context.Background(), sensitive, nil)
	if err != nil || result.Status != tool.ResultDenied || navigator.definitionCalls != 1 {
		t.Fatalf("sensitive navigation: result=%#v calls=%d err=%v", result, navigator.definitionCalls, err)
	}
}
