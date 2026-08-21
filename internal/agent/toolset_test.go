package agent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/eaglc/codepilot/internal/language"
	"github.com/eaglc/codepilot/internal/session"
	"github.com/eaglc/codepilot/internal/tool"
)

func TestBuildToolRegistryRegistersSevenToolsInStableOrder(t *testing.T) {
	registry, _ := newToolTestRegistry(t, &fakeWorkspaceTools{})
	definitions := registry.Definitions()
	names := make([]string, 0, len(definitions))
	for _, value := range definitions {
		names = append(names, value.Name)
		var schema map[string]json.RawMessage
		if err := json.Unmarshal(value.InputSchema, &schema); err != nil {
			t.Fatalf("decode %s schema: %v", value.Name, err)
		}
		if string(schema["type"]) != `"object"` || string(schema["additionalProperties"]) != "false" {
			t.Fatalf("%s schema is not a closed object: %s", value.Name, value.InputSchema)
		}
	}
	want := []string{"list_files", "search_code", "read_file", "git_status", "git_diff", "apply_patch", "run_checks"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("tool order = %#v, want %#v", names, want)
	}
	var runSchema struct {
		Properties struct {
			PlanID struct {
				Enum []string `json:"enum"`
			} `json:"plan_id"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(definitions[6].InputSchema, &runSchema); err != nil {
		t.Fatalf("decode run_checks schema: %v", err)
	}
	if !reflect.DeepEqual(runSchema.Properties.PlanID.Enum, []string{"go-test-all"}) {
		t.Fatalf("run_checks plan enum = %#v", runSchema.Properties.PlanID.Enum)
	}
}

func TestBuildToolRegistryAppendsCodeNavigationToolsWhenAvailable(t *testing.T) {
	navigator := &fakeCodeNavigator{}
	registry, err := buildToolRegistry(toolTestScope(4096), language.LanguageProfile{ID: language.LanguageGo}, toolsetDependencies{
		Workspaces: &fakeWorkspaceTools{}, CodeIntel: navigator,
	})
	if err != nil {
		t.Fatalf("build registry: %v", err)
	}
	names := make([]string, 0, len(registry.Definitions()))
	for _, value := range registry.Definitions() {
		names = append(names, value.Name)
	}
	want := []string{"list_files", "search_code", "read_file", "git_status", "git_diff", "apply_patch", "run_checks", "definition", "references", "symbols", "diagnostics"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("tool order = %#v, want %#v", names, want)
	}

	registry, err = buildToolRegistry(toolTestScope(4096), language.LanguageProfile{ID: language.LanguageGeneric}, toolsetDependencies{
		Workspaces: &fakeWorkspaceTools{}, CodeIntel: navigator,
	})
	if err != nil {
		t.Fatalf("build generic registry: %v", err)
	}
	if len(registry.Definitions()) != 7 {
		t.Fatalf("generic tool count = %d, want 7", len(registry.Definitions()))
	}
}

func TestCodeNavigationToolsCaptureScopeAndReturnBoundedData(t *testing.T) {
	navigator := &fakeCodeNavigator{
		definitions: []Location{{Path: "main.go", Range: CodeRange{Start: CodePosition{Line: 3, Column: 2}, End: CodePosition{Line: 3, Column: 8}}}},
		references:  []Location{{Path: "other.go", Range: CodeRange{Start: CodePosition{Line: 7, Column: 1}, End: CodePosition{Line: 7, Column: 4}}}},
		symbols:     []Symbol{{Name: "Answer", Kind: "function", Location: Location{Path: "main.go", Range: CodeRange{Start: CodePosition{Line: 2, Column: 1}, End: CodePosition{Line: 4, Column: 2}}}}},
		diagnostics: []Diagnostic{{Path: "main.go", Range: CodeRange{Start: CodePosition{Line: 9, Column: 3}, End: CodePosition{Line: 9, Column: 5}}, Severity: DiagnosticError, Message: "broken"}},
	}
	registry, err := buildToolRegistry(toolTestScope(4096), language.LanguageProfile{ID: language.LanguageGo}, toolsetDependencies{
		Workspaces: &fakeWorkspaceTools{}, CodeIntel: navigator,
	})
	if err != nil {
		t.Fatalf("build registry: %v", err)
	}
	for _, invocation := range []struct {
		name      string
		arguments string
		contains  string
	}{
		{name: "definition", arguments: `{"path":"main.go","line":2,"column":4}`, contains: `"path":"main.go"`},
		{name: "references", arguments: `{"path":"main.go","line":2,"column":4,"include_declaration":true,"limit":10}`, contains: `"path":"other.go"`},
		{name: "symbols", arguments: `{"query":"Answer","limit":10}`, contains: `"name":"Answer"`},
		{name: "diagnostics", arguments: `{"path":"main.go","limit":10}`, contains: `"severity":"error"`},
	} {
		result := invokeCompletedTool(t, registry, invocation.name, invocation.arguments)
		if !strings.Contains(result.Content, invocation.contains) {
			t.Fatalf("%s result = %s", invocation.name, result.Content)
		}
	}
	for name, scope := range map[string]NavigationScope{
		"definition":  navigator.definitionRequest.Scope,
		"references":  navigator.referencesRequest.Scope,
		"symbols":     navigator.symbolsRequest.Scope,
		"diagnostics": navigator.diagnosticsRequest.Scope,
	} {
		if scope.WorktreeRoot != `C:\trusted\repo` || scope.WorktreeID != "worktree_test" || scope.SessionID != "session_test" || scope.TurnID != "turn_test" || scope.PermissionMode != session.PermissionAsk || scope.Language != language.LanguageGo {
			t.Fatalf("%s scope = %#v", name, scope)
		}
	}
}

func TestCodeNavigationToolRejectsForgedScopeAndDegradesWhenUnavailable(t *testing.T) {
	navigator := &fakeCodeNavigator{}
	registry, err := buildToolRegistry(toolTestScope(4096), language.LanguageProfile{ID: language.LanguageGo}, toolsetDependencies{
		Workspaces: &fakeWorkspaceTools{}, CodeIntel: navigator,
	})
	if err != nil {
		t.Fatalf("build registry: %v", err)
	}
	invalidCalls := []struct {
		name      string
		arguments string
	}{
		{name: "definition", arguments: `{"path":"main.go","line":1,"column":1,"worktree_root":"C:\\other"}`},
		{name: "definition", arguments: `{"path":"main.go","line":0,"column":1}`},
		{name: "references", arguments: `{"path":"main.go","line":1,"column":1,"limit":201}`},
		{name: "references", arguments: `{"path":"main.go","line":1,"column":1,"session_id":"other"}`},
		{name: "symbols", arguments: `{"query":"","limit":1}`},
		{name: "symbols", arguments: `{"query":"Answer","worktree_id":"other"}`},
		{name: "diagnostics", arguments: `{}`},
		{name: "diagnostics", arguments: `{"path":"main.go","language":"python"}`},
	}
	for _, invocation := range invalidCalls {
		result, err := invokeRegisteredTool(context.Background(), registry, invocation.name, json.RawMessage(invocation.arguments))
		if err != nil || result.Status != tool.ResultInvalid || navigator.calls != 0 {
			t.Fatalf("invalid %s result=%#v calls=%d err=%v", invocation.name, result, navigator.calls, err)
		}
	}

	navigator.err = ErrCodeNavigationUnavailable
	result, err := invokeRegisteredTool(context.Background(), registry, "definition", json.RawMessage(`{"path":"main.go","line":1,"column":1}`))
	if err != nil || result.Status != tool.ResultFailed || !strings.Contains(result.Content, "search_code and read_file") {
		t.Fatalf("fallback result=%#v err=%v", result, err)
	}
}

func TestBuildToolRegistryRejectsInvalidDependenciesAndPlans(t *testing.T) {
	validScope := toolTestScope(4096)
	validPlan := language.CheckPlan{
		ID: "go-test-all", Description: "Run Go tests.",
		Command: language.CheckCommand{ID: "go-test-all", Program: "go", Args: []string{"test", "./..."}},
	}
	var typedNil *fakeWorkspaceTools
	tests := []struct {
		name      string
		scope     session.TurnScope
		workspace WorkspaceTools
		plans     []language.CheckPlan
	}{
		{name: "missing scope", workspace: &fakeWorkspaceTools{}, plans: []language.CheckPlan{validPlan}},
		{name: "nil workspace", scope: validScope, plans: []language.CheckPlan{validPlan}},
		{name: "typed nil workspace", scope: validScope, workspace: typedNil, plans: []language.CheckPlan{validPlan}},
		{
			name:      "mismatched command ID",
			scope:     validScope,
			workspace: &fakeWorkspaceTools{},
			plans:     []language.CheckPlan{{ID: "one", Description: "one", Command: language.CheckCommand{ID: "two"}}},
		},
		{name: "duplicate plan", scope: validScope, workspace: &fakeWorkspaceTools{}, plans: []language.CheckPlan{validPlan, validPlan}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := buildToolRegistry(test.scope, language.LanguageProfile{ID: language.LanguageGo, CheckPlans: test.plans}, toolsetDependencies{Workspaces: test.workspace}); err == nil {
				t.Fatal("expected registry build error")
			}
		})
	}
}

func TestBuildToolRegistryCopiesLanguageCheckPlans(t *testing.T) {
	workspace := &fakeWorkspaceTools{checkResult: RunChecksResult{Outcome: session.CheckPassed}}
	profile := language.LanguageProfile{ID: language.LanguageGo, CheckPlans: []language.CheckPlan{{
		ID: "go-test-all", Description: "Run Go tests.",
		Command: language.CheckCommand{ID: "go-test-all", Program: "go", Args: []string{"test", "./..."}, EnvAllowlist: []string{"GOCACHE"}},
	}}}
	registry, err := buildToolRegistry(toolTestScope(4096), profile, toolsetDependencies{Workspaces: workspace})
	if err != nil {
		t.Fatalf("build registry: %v", err)
	}
	profile.CheckPlans[0].Command.Program = "powershell"
	profile.CheckPlans[0].Command.Args[0] = "run"
	profile.CheckPlans[0].Command.EnvAllowlist[0] = "API_TOKEN"
	invokeCompletedTool(t, registry, "run_checks", `{"plan_id":"go-test-all"}`)
	if workspace.checkRequest.Command.Program != "go" || !reflect.DeepEqual(workspace.checkRequest.Command.Args, []string{"test", "./..."}) || !reflect.DeepEqual(workspace.checkRequest.Command.EnvAllowlist, []string{"GOCACHE"}) {
		t.Fatalf("registry retained mutable language plan: %#v", workspace.checkRequest.Command)
	}
}

func TestCodingToolsUseCapturedTurnScopeAndTrustedCheckPlan(t *testing.T) {
	workspace := &fakeWorkspaceTools{
		listResult:   ListFilesResult{Files: []FileInfo{{Path: "main.go", Size: 42}}},
		searchResult: SearchCodeResult{Matches: []SearchMatch{{Path: "main.go", Line: 3, Column: 1, Text: "answer"}}},
		readResult:   ReadFileResult{Path: "main.go", Content: "package main", StartLine: 1, EndLine: 1, TotalLines: 1, TotalLinesKnown: true},
		statusResult: GitStatusResult{Branch: "main", HeadCommit: "abc", Dirty: true},
		diffResult:   session.DiffResult{Kind: session.DiffSession, Text: "diff", Files: []session.DiffFile{{Path: "main.go", Status: "M"}}},
		applyResult: ApplyPatchResult{
			Applied:      true,
			ProposedDiff: session.DiffResult{Kind: session.DiffProposed, Text: "patch", Files: []session.DiffFile{{Path: "main.go", Status: "M"}}},
			PatchRecord: session.PatchRecord{
				ID: "patch_test", SessionID: "session_test", TurnID: "turn_test", AppliedAt: time.Now().UTC(),
				Files: []session.PatchedFile{{Path: "main.go", BeforeHash: "before", AfterHash: "after"}},
			},
		},
		checkResult: RunChecksResult{PlanID: "go-test-all", Outcome: session.CheckPassed, Summary: "passed", ExitCode: 0},
	}
	registry, state := newToolTestRegistry(t, workspace)

	invokeCompletedTool(t, registry, "list_files", `{"pattern":"*.go","limit":10}`)
	invokeCompletedTool(t, registry, "search_code", `{"query":"answer","regex":false,"glob":"*.go","limit":10}`)
	invokeCompletedTool(t, registry, "read_file", `{"path":"main.go","start_line":1,"line_count":20}`)
	invokeCompletedTool(t, registry, "git_status", `{}`)
	invokeCompletedTool(t, registry, "apply_patch", `{"patch":"diff --git a/main.go b/main.go","intent":"Fix answer"}`)
	invokeCompletedTool(t, registry, "git_diff", `{"kind":"session","files":["main.go"]}`)
	invokeCompletedTool(t, registry, "run_checks", `{"plan_id":"go-test-all"}`)

	for operation, root := range map[string]string{
		"list": workspace.listRequest.WorktreeRoot, "search": workspace.searchRequest.WorktreeRoot,
		"read": workspace.readRequest.WorktreeRoot, "status": workspace.statusRequest.WorktreeRoot,
		"diff": workspace.diffRequest.WorktreeRoot, "patch": workspace.applyRequest.WorktreeRoot,
		"check": workspace.checkRequest.WorktreeRoot,
	} {
		if root != `C:\trusted\repo` {
			t.Fatalf("%s used root %q", operation, root)
		}
	}
	if workspace.applyRequest.SessionID != "session_test" || workspace.applyRequest.TurnID != "turn_test" || workspace.applyRequest.PermissionMode != session.PermissionAsk {
		t.Fatalf("apply patch did not use captured scope: %#v", workspace.applyRequest)
	}
	if workspace.checkRequest.SessionID != "session_test" || workspace.checkRequest.Command.Program != "go" || !reflect.DeepEqual(workspace.checkRequest.Command.Args, []string{"test", "./..."}) {
		t.Fatalf("run checks did not use trusted plan: %#v", workspace.checkRequest)
	}
	if workspace.checkRequest.Command.Timeout != 30*time.Second || workspace.checkRequest.Command.MaxOutputBytes != 4096 {
		t.Fatalf("turn limits did not constrain check plan: %#v", workspace.checkRequest.Command)
	}
	if workspace.diffRequest.Kind != session.DiffSession || workspace.diffRequest.ExpectedHashes["main.go"] != "after" {
		t.Fatalf("session diff omitted current-turn hashes: %#v", workspace.diffRequest)
	}
	patches, summary := state.snapshot()
	if len(patches) != 1 || patches[0].ID != "patch_test" || summary.Outcome != session.CheckPassed {
		t.Fatalf("turn tool state = patches %#v summary %#v", patches, summary)
	}
}

func TestCodingToolsRejectForgedTrustedFieldsAndUnknownArguments(t *testing.T) {
	tests := []struct {
		name      string
		toolName  string
		arguments string
	}{
		{name: "list root", toolName: "list_files", arguments: `{"root":"C:\\other"}`},
		{name: "search session", toolName: "search_code", arguments: `{"query":"x","session_id":"other"}`},
		{name: "read worktree", toolName: "read_file", arguments: `{"path":"main.go","worktree_root":"C:\\other"}`},
		{name: "status command", toolName: "git_status", arguments: `{"command":"git reset --hard"}`},
		{name: "diff hashes", toolName: "git_diff", arguments: `{"kind":"session","expected_hashes":{"main.go":"forged"}}`},
		{name: "patch permission", toolName: "apply_patch", arguments: `{"patch":"diff","intent":"change","permission_mode":"auto-edit"}`},
		{name: "check command", toolName: "run_checks", arguments: `{"plan_id":"go-test-all","command":"powershell"}`},
		{name: "check environment", toolName: "run_checks", arguments: `{"plan_id":"go-test-all","env":["API_TOKEN"]}`},
		{name: "check timeout", toolName: "run_checks", arguments: `{"plan_id":"go-test-all","timeout":999999}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace := &fakeWorkspaceTools{}
			registry, _ := newToolTestRegistry(t, workspace)
			result, err := invokeRegisteredTool(context.Background(), registry, test.toolName, json.RawMessage(test.arguments))
			if err != nil || result.Status != tool.ResultInvalid {
				t.Fatalf("result=%#v err=%v", result, err)
			}
			if workspace.calls != 0 {
				t.Fatalf("forged arguments invoked workspace %d times", workspace.calls)
			}
		})
	}
}

func TestCodingToolsRejectMissingTrailingAndBoundaryArguments(t *testing.T) {
	tests := []struct {
		name      string
		toolName  string
		arguments string
	}{
		{name: "empty document", toolName: "list_files", arguments: ``},
		{name: "null", toolName: "list_files", arguments: `null`},
		{name: "array", toolName: "list_files", arguments: `[]`},
		{name: "trailing object", toolName: "list_files", arguments: `{} {}`},
		{name: "negative list limit", toolName: "list_files", arguments: `{"limit":-1}`},
		{name: "missing search query", toolName: "search_code", arguments: `{}`},
		{name: "oversized search limit", toolName: "search_code", arguments: `{"query":"x","limit":201}`},
		{name: "missing read path", toolName: "read_file", arguments: `{}`},
		{name: "negative start", toolName: "read_file", arguments: `{"path":"main.go","start_line":-1}`},
		{name: "invalid diff kind", toolName: "git_diff", arguments: `{"kind":"all"}`},
		{name: "missing patch intent", toolName: "apply_patch", arguments: `{"patch":"diff"}`},
		{name: "unknown check plan", toolName: "run_checks", arguments: `{"plan_id":"not-available"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace := &fakeWorkspaceTools{}
			registry, _ := newToolTestRegistry(t, workspace)
			result, err := invokeRegisteredTool(context.Background(), registry, test.toolName, json.RawMessage(test.arguments))
			if err != nil || result.Status != tool.ResultInvalid || workspace.calls != 0 {
				t.Fatalf("result=%#v calls=%d err=%v", result, workspace.calls, err)
			}
		})
	}
}

func TestApplyPatchToolConvertsApprovalToBoundedInterrupt(t *testing.T) {
	request := session.ApprovalRequest{
		ID: "approval_test", SessionID: "session_test", TurnID: "turn_test", CreatedAt: time.Now().UTC(),
		Action: session.Action{
			Kind: session.ActionApplyPatch, WorktreeRoot: `C:\trusted\repo`, Fingerprint: "secret-fingerprint", Summary: "Apply a fix",
			Patch: &session.PatchAction{Patch: "private patch body", Files: []string{"main.go"}},
		},
	}
	workspace := &fakeWorkspaceTools{
		applyResult: ApplyPatchResult{ProposedDiff: session.DiffResult{Kind: session.DiffProposed, Text: "proposed patch"}},
		applyErr:    &session.ApprovalRequiredError{Request: request},
	}
	registry, _ := newToolTestRegistry(t, workspace)
	result, err := invokeRegisteredTool(context.Background(), registry, "apply_patch", json.RawMessage(`{"patch":"diff","intent":"fix"}`))
	if err != nil || result.Status != tool.ResultInterrupted || result.Interrupt == nil || result.Interrupt.ID != "approval_test" {
		t.Fatalf("interrupt result=%#v err=%v", result, err)
	}
	payload := string(result.Interrupt.Payload)
	for _, forbidden := range []string{`C:\trusted\repo`, "secret-fingerprint", "private patch body"} {
		if strings.Contains(payload, forbidden) {
			t.Fatalf("approval payload leaked %q: %s", forbidden, payload)
		}
	}
	if !strings.Contains(payload, `"request_id":"approval_test"`) || !strings.Contains(payload, `"files":["main.go"]`) {
		t.Fatalf("approval payload omitted safe fields: %s", payload)
	}
}

func TestToolResultNormalizationAndOutputLimit(t *testing.T) {
	t.Run("invalid workspace input", func(t *testing.T) {
		workspace := &fakeWorkspaceTools{readErr: &session.AppError{Code: session.ErrInvalidInput, UserMessage: "The path is invalid."}}
		registry, _ := newToolTestRegistry(t, workspace)
		result, err := invokeRegisteredTool(context.Background(), registry, "read_file", json.RawMessage(`{"path":"missing.go"}`))
		if err != nil || result.Status != tool.ResultInvalid || result.Content != "The path is invalid." {
			t.Fatalf("result=%#v err=%v", result, err)
		}
	})

	t.Run("unexpected workspace failure", func(t *testing.T) {
		workspace := &fakeWorkspaceTools{statusErr: errors.New("API_TOKEN=must-not-leak")}
		registry, _ := newToolTestRegistry(t, workspace)
		result, err := invokeRegisteredTool(context.Background(), registry, "git_status", json.RawMessage(`{}`))
		if err != nil || result.Status != tool.ResultFailed || strings.Contains(result.Content, "must-not-leak") {
			t.Fatalf("result=%#v err=%v", result, err)
		}
	})

	t.Run("bounded result", func(t *testing.T) {
		workspace := &fakeWorkspaceTools{readResult: ReadFileResult{Path: "large.go", Content: strings.Repeat("界", 200)}}
		registry, _ := newToolTestRegistryWithLimit(t, workspace, 64)
		result, err := invokeRegisteredTool(context.Background(), registry, "read_file", json.RawMessage(`{"path":"large.go"}`))
		if err != nil || result.Status != tool.ResultCompleted || len(result.Content) > 64 || !strings.HasSuffix(result.Content, "...") || result.Data != nil {
			t.Fatalf("result=%#v err=%v", result, err)
		}
	})

	t.Run("cancelled dispatch", func(t *testing.T) {
		workspace := &fakeWorkspaceTools{}
		registry, _ := newToolTestRegistry(t, workspace)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		result, err := invokeRegisteredTool(ctx, registry, "git_status", json.RawMessage(`{}`))
		if !errors.Is(err, context.Canceled) || result.Status != tool.ResultCancelled || workspace.calls != 0 {
			t.Fatalf("result=%#v calls=%d err=%v", result, workspace.calls, err)
		}
	})
}

func TestMutatingToolsReturnStructuredDenials(t *testing.T) {
	workspace := &fakeWorkspaceTools{
		applyResult: ApplyPatchResult{Denied: true, Reason: "Read-only mode.", ProposedDiff: session.DiffResult{Kind: session.DiffProposed}},
		checkResult: RunChecksResult{PlanID: "go-test-all", Outcome: session.CheckDenied, Denied: true, Reason: "Check denied.", Summary: "denied"},
	}
	registry, _ := newToolTestRegistry(t, workspace)
	tests := []struct {
		name      string
		arguments string
	}{
		{name: "apply_patch", arguments: `{"patch":"diff","intent":"fix"}`},
		{name: "run_checks", arguments: `{"plan_id":"go-test-all"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := invokeRegisteredTool(context.Background(), registry, test.name, json.RawMessage(test.arguments))
			if err != nil || result.Status != tool.ResultDenied || result.Data == nil {
				t.Fatalf("result=%#v err=%v", result, err)
			}
		})
	}
}

func TestScriptedInvokerDispatchesRegisteredCallsAndRejectsUnknownName(t *testing.T) {
	workspace := &fakeWorkspaceTools{listResult: ListFilesResult{Files: []FileInfo{{Path: "main.go"}}}}
	registry, _ := newToolTestRegistry(t, workspace)
	invoker := scriptedInvoker{
		registry: registry,
		calls: []scriptedToolCall{
			{Name: "list_files", Arguments: json.RawMessage(`{"pattern":"*.go"}`)},
			{Name: "execute", Arguments: json.RawMessage(`{"command":"whoami"}`)},
		},
	}
	results, err := invoker.Invoke(context.Background())
	if err != nil {
		t.Fatalf("invoke script: %v", err)
	}
	if len(results) != 2 || results[0].Status != tool.ResultCompleted || results[1].Status != tool.ResultInvalid || workspace.calls != 1 {
		t.Fatalf("results=%#v calls=%d", results, workspace.calls)
	}
}

type scriptedToolCall struct {
	Name      string
	Arguments json.RawMessage
}

type scriptedInvoker struct {
	registry *tool.Registry
	calls    []scriptedToolCall
}

func (i scriptedInvoker) Invoke(ctx context.Context) ([]tool.Result, error) {
	results := make([]tool.Result, 0, len(i.calls))
	for _, call := range i.calls {
		result, err := invokeRegisteredTool(ctx, i.registry, call.Name, call.Arguments)
		if err != nil {
			return results, err
		}
		results = append(results, result)
		if result.Status == tool.ResultInterrupted || result.Status == tool.ResultCancelled {
			break
		}
	}
	return results, nil
}

type fakeWorkspaceTools struct {
	calls int

	listRequest ListFilesRequest
	listResult  ListFilesResult
	listErr     error

	searchRequest SearchCodeRequest
	searchResult  SearchCodeResult
	searchErr     error

	readRequest ReadFileRequest
	readResult  ReadFileResult
	readErr     error

	statusRequest GitStatusRequest
	statusResult  GitStatusResult
	statusErr     error

	diffRequest session.DiffRequest
	diffResult  session.DiffResult
	diffErr     error

	applyRequest ApplyPatchRequest
	applyResult  ApplyPatchResult
	applyErr     error

	checkRequest RunChecksRequest
	checkResult  RunChecksResult
	checkErr     error
}

func (f *fakeWorkspaceTools) ListFiles(_ context.Context, request ListFilesRequest) (ListFilesResult, error) {
	f.calls++
	f.listRequest = request
	return f.listResult, f.listErr
}

func (f *fakeWorkspaceTools) SearchCode(_ context.Context, request SearchCodeRequest) (SearchCodeResult, error) {
	f.calls++
	f.searchRequest = request
	return f.searchResult, f.searchErr
}

func (f *fakeWorkspaceTools) ReadFile(_ context.Context, request ReadFileRequest) (ReadFileResult, error) {
	f.calls++
	f.readRequest = request
	return f.readResult, f.readErr
}

func (f *fakeWorkspaceTools) GitStatus(_ context.Context, request GitStatusRequest) (GitStatusResult, error) {
	f.calls++
	f.statusRequest = request
	return f.statusResult, f.statusErr
}

func (f *fakeWorkspaceTools) ReadDiff(_ context.Context, request ReadDiffRequest) (session.DiffResult, error) {
	f.calls++
	f.diffRequest = request
	return f.diffResult, f.diffErr
}

func (f *fakeWorkspaceTools) ApplyPatch(_ context.Context, request ApplyPatchRequest) (ApplyPatchResult, error) {
	f.calls++
	f.applyRequest = request
	return f.applyResult, f.applyErr
}

func (f *fakeWorkspaceTools) RunChecks(_ context.Context, request RunChecksRequest) (RunChecksResult, error) {
	f.calls++
	f.checkRequest = request
	return f.checkResult, f.checkErr
}

func newToolTestRegistry(t *testing.T, workspace WorkspaceTools) (*tool.Registry, *turnToolState) {
	t.Helper()
	return newToolTestRegistryWithLimit(t, workspace, 4096)
}

func newToolTestRegistryWithLimit(t *testing.T, workspace WorkspaceTools, resultLimit int) (*tool.Registry, *turnToolState) {
	t.Helper()
	state := newTurnToolState()
	registry, err := buildToolRegistry(toolTestScope(resultLimit), language.LanguageProfile{
		ID: language.LanguageGo,
		CheckPlans: []language.CheckPlan{{
			ID:          "go-test-all",
			Description: "Run all Go tests.",
			Command: language.CheckCommand{
				ID:             "go-test-all",
				Program:        "go",
				Args:           []string{"test", "./..."},
				Dir:            ".",
				EnvAllowlist:   []string{"GOCACHE"},
				Timeout:        time.Minute,
				MaxOutputBytes: 8192,
			},
		}},
	}, toolsetDependencies{Workspaces: workspace, State: state})
	if err != nil {
		t.Fatalf("build registry: %v", err)
	}
	return registry, state
}

func toolTestScope(resultLimit int) session.TurnScope {
	return session.TurnScope{
		SessionID: "session_test", WorktreeID: "worktree_test", TurnID: "turn_test", WorktreeRoot: `C:\trusted\repo`, PermissionMode: session.PermissionAsk,
		Limits: session.RunLimits{MaxSteps: 10, MaxTurnDuration: time.Minute, CommandTimeout: 30 * time.Second, ToolResultMaxBytes: resultLimit, CommandOutputMaxBytes: 4096},
	}
}

type fakeCodeNavigator struct {
	calls              int
	definitionRequest  DefinitionRequest
	referencesRequest  ReferencesRequest
	symbolsRequest     SymbolsRequest
	diagnosticsRequest DiagnosticsRequest
	definitions        []Location
	references         []Location
	symbols            []Symbol
	diagnostics        []Diagnostic
	err                error
	closedWorktree     session.WorktreeID
}

func (f *fakeCodeNavigator) Definition(_ context.Context, request DefinitionRequest) ([]Location, error) {
	f.calls++
	f.definitionRequest = request
	return f.definitions, f.err
}

func (f *fakeCodeNavigator) References(_ context.Context, request ReferencesRequest) ([]Location, error) {
	f.calls++
	f.referencesRequest = request
	return f.references, f.err
}

func (f *fakeCodeNavigator) Symbols(_ context.Context, request SymbolsRequest) ([]Symbol, error) {
	f.calls++
	f.symbolsRequest = request
	return f.symbols, f.err
}

func (f *fakeCodeNavigator) Diagnostics(_ context.Context, request DiagnosticsRequest) ([]Diagnostic, error) {
	f.calls++
	f.diagnosticsRequest = request
	return f.diagnostics, f.err
}

func (f *fakeCodeNavigator) CloseWorktree(_ context.Context, worktreeID session.WorktreeID) error {
	f.closedWorktree = worktreeID
	return nil
}

func invokeCompletedTool(t *testing.T, registry *tool.Registry, name string, arguments string) tool.Result {
	t.Helper()
	result, err := invokeRegisteredTool(context.Background(), registry, name, json.RawMessage(arguments))
	if err != nil || result.Status != tool.ResultCompleted {
		t.Fatalf("invoke %s: result=%#v err=%v", name, result, err)
	}
	return result
}
