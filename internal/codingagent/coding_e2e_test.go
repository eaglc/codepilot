package codingagent_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/eaglc/codepilot/internal/agent"
	agentsession "github.com/eaglc/codepilot/internal/agent/session"
	"github.com/eaglc/codepilot/internal/codingagent"
	codingprompt "github.com/eaglc/codepilot/internal/codingagent/prompt"
	codingtools "github.com/eaglc/codepilot/internal/codingagent/tools"
	workspaceinfra "github.com/eaglc/codepilot/internal/codingagent/workspace"
	codingfile "github.com/eaglc/codepilot/internal/codingstore/file"
	"github.com/eaglc/codepilot/internal/contextmanager"
	"github.com/eaglc/codepilot/internal/llm"
	sessionfile "github.com/eaglc/codepilot/internal/sessionstore/file"
)

func TestCodingAgentRepairsRealGoAndPythonRepositoriesEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("Git is required for Coding E2E")
	}
	python := findPythonE2E(t)
	tests := []codingE2ECase{
		{
			name: "go", files: map[string]string{
				"go.mod":       "module example.invalid/calc\n\ngo 1.23\n",
				"calc.go":      "package calc\n\nfunc Add(a, b int) int {\n\treturn a - b\n}\n",
				"calc_test.go": "package calc\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif got := Add(2, 3); got != 5 {\n\t\tt.Fatalf(\"Add(2, 3) = %d, want 5\", got)\n\t}\n}\n",
			},
			patch:       "diff --git a/calc.go b/calc.go\n--- a/calc.go\n+++ b/calc.go\n@@ -1,5 +1,5 @@\n package calc\n \n func Add(a, b int) int {\n-\treturn a - b\n+\treturn a + b\n }\n",
			changedFile: "calc.go", changedText: "return a + b", check: commandSpec{name: "go", args: []string{"test", "./..."}},
		},
		{
			name: "python", files: map[string]string{
				"math_utils.py":      "def multiply(a, b):\n    return a + b\n",
				"test_math_utils.py": "import unittest\n\nfrom math_utils import multiply\n\n\nclass MathUtilsTest(unittest.TestCase):\n    def test_multiply(self):\n        self.assertEqual(multiply(3, 4), 12)\n\n\nif __name__ == \"__main__\":\n    unittest.main()\n",
			},
			patch:       "diff --git a/math_utils.py b/math_utils.py\n--- a/math_utils.py\n+++ b/math_utils.py\n@@ -1,2 +1,2 @@\n def multiply(a, b):\n-    return a + b\n+    return a * b\n",
			changedFile: "math_utils.py", changedText: "return a * b", check: python,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			if test.check.name == "" {
				t.Skip("Python is unavailable")
			}
			runCodingE2E(t, test)
		})
	}
}

type codingE2ECase struct {
	name, patch, changedFile, changedText string
	files                                 map[string]string
	check                                 commandSpec
}

type commandSpec struct {
	name string
	args []string
}

func runCodingE2E(t *testing.T, test codingE2ECase) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	root := createBrokenGitRepository(t, test.files)
	if output, err := runE2ECommand(ctx, root, test.check); err == nil {
		t.Fatalf("broken %s fixture unexpectedly passed before Agent repair: %s", test.name, output)
	}
	stateDir := filepath.Join(t.TempDir(), "state")
	products, err := codingfile.NewRepository(stateDir)
	if err != nil {
		t.Fatalf("create product store: %v", err)
	}
	agentSessions, err := sessionfile.NewRepository(stateDir)
	if err != nil {
		t.Fatalf("create Agent store: %v", err)
	}
	resolved, err := workspaceinfra.ResolveWorktree(ctx, root)
	if err != nil {
		t.Fatalf("resolve worktree: %v", err)
	}
	now := time.Now().UTC()
	workspace := codingagent.Workspace{
		ID: codingagent.WorkspaceID("workspace-" + test.name), DisplayName: test.name + " e2e", GitCommonDir: resolved.GitCommonDir,
		RepositoryFingerprint: resolved.RepositoryFingerprint, Trusted: true, CreatedAt: now, UpdatedAt: now,
	}
	worktree := codingagent.Worktree{
		ID: codingagent.WorktreeID("worktree-" + test.name), WorkspaceID: workspace.ID, Root: resolved.Root, GitDir: resolved.GitDir, CreatedAt: now, LastUsedAt: now,
	}
	if err := products.SaveWorkspace(ctx, workspace); err != nil {
		t.Fatalf("save workspace: %v", err)
	}
	if err := products.SaveWorktree(ctx, worktree); err != nil {
		t.Fatalf("save worktree: %v", err)
	}
	security, err := codingagent.NewSecurityPolicy(nil)
	if err != nil {
		t.Fatalf("create security policy: %v", err)
	}
	model := &patchSequenceModel{profile: "scripted", model: "scripted", patch: test.patch}
	contexts, err := contextmanager.NewManager()
	if err != nil {
		t.Fatalf("create context manager: %v", err)
	}
	runtimeAgent, err := agent.NewRuntime(agent.Dependencies{
		Models: patchModelFactory{model: model}, Contexts: contexts, Sessions: agentSessions, DataPolicy: security,
	})
	if err != nil {
		t.Fatalf("create Agent runtime: %v", err)
	}
	service, err := codingagent.NewService(codingagent.Dependencies{
		Sessions: products, AgentSessions: agentSessions, Worktrees: products, Agent: runtimeAgent,
		Tools:   codingtools.NewFactory(codingtools.Options{Artifacts: products, Security: security}),
		Prompts: codingprompt.NewBuilder(), Events: &e2eEventSink{},
		Limits: agent.RunLimits{MaxSteps: 6, MaxDuration: time.Minute, MaxModelAttempts: 1, MaxToolCalls: 8, MaxRepeatedToolCalls: 3, MaxOutputBytes: 1 << 20, MaxNoProgressSteps: 3},
	})
	if err != nil {
		t.Fatalf("create Coding service: %v", err)
	}
	session, err := service.CreateSession(ctx, codingagent.Session{
		ID: codingagent.SessionID("session-" + test.name), AgentSessionID: agentsession.ID("agent-" + test.name),
		WorkspaceID: workspace.ID, WorktreeID: worktree.ID, ProviderProfileID: "scripted", ModelID: "scripted", PermissionMode: codingagent.PermissionAutoEdit,
	})
	if err != nil {
		t.Fatalf("create Coding session: %v", err)
	}
	result, err := service.StartTurn(ctx, codingagent.TurnRequest{SessionID: session.ID, Text: "Repair the failing " + test.name + " test."})
	if err != nil {
		t.Fatalf("run Coding Agent: %v", err)
	}
	if result.Status != string(agent.RunCompleted) || result.Steps != 2 || !strings.Contains(result.Response, "repaired") {
		t.Fatalf("unexpected Coding result: %#v", result)
	}
	content, err := os.ReadFile(filepath.Join(root, test.changedFile))
	if err != nil || !strings.Contains(string(content), test.changedText) {
		t.Fatalf("Agent did not apply expected %s change: content=%q err=%v", test.name, content, err)
	}
	if output, err := runE2ECommand(ctx, root, test.check); err != nil {
		t.Fatalf("repaired %s fixture failed: %v\n%s", test.name, err, output)
	}
	if len(model.requests) != 2 || !requestHasToolResult(model.requests[1], "apply_patch") {
		t.Fatalf("model did not receive durable Tool Result on step two: requests=%#v", model.requests)
	}
	snapshot, err := service.Snapshot(ctx, session.ID)
	if err != nil {
		t.Fatalf("project product snapshot: %v", err)
	}
	if !snapshotHasAppliedDiff(snapshot, test.changedFile) {
		t.Fatalf("product snapshot does not contain applied inline diff: %#v", snapshot.Transcript)
	}
	reopenedAgents, err := sessionfile.OpenRepository(stateDir)
	if err != nil {
		t.Fatalf("reopen Agent store: %v", err)
	}
	durable, err := reopenedAgents.Load(ctx, session.AgentSessionID)
	if err != nil {
		t.Fatalf("reload Agent session: %v", err)
	}
	if plan := agentsession.BuildRecoveryPlan(durable); len(plan.Actions) != 0 {
		t.Fatalf("completed E2E left recovery work: %#v", plan)
	}
	if !durableHasToolLifecycle(durable, "apply_patch") {
		t.Fatalf("reloaded journal lacks complete apply_patch lifecycle: %#v", durable.Records)
	}
}

type patchModelFactory struct{ model *patchSequenceModel }

func (factory patchModelFactory) CreateModel(context.Context, llm.ModelRef) (llm.ChatModel, error) {
	return factory.model, nil
}

type patchSequenceModel struct {
	profile, model, patch string
	requests              []llm.ChatRequest
}

func (model *patchSequenceModel) Complete(context.Context, llm.ChatRequest) (llm.Message, error) {
	return llm.Message{}, errors.New("Coding E2E expects streaming Agent calls")
}

func (model *patchSequenceModel) Stream(_ context.Context, request llm.ChatRequest) (llm.Stream, error) {
	model.requests = append(model.requests, request)
	var response llm.Message
	switch len(model.requests) {
	case 1:
		arguments, _ := json.Marshal(map[string]string{"patch": model.patch, "intent": "Repair the failing test"})
		response = llm.Message{
			Role: llm.RoleAssistant, Provider: model.profile, Model: model.model, StopReason: llm.StopReasonToolUse, Timestamp: time.Now().UTC(),
			Content: []llm.Content{{Type: llm.ContentToolCall, ToolCall: &llm.ToolCall{ID: "apply-e2e", Name: "apply_patch", Arguments: arguments}}},
		}
	case 2:
		response = llm.Message{
			Role: llm.RoleAssistant, Provider: model.profile, Model: model.model, StopReason: llm.StopReasonStop, Timestamp: time.Now().UTC(),
			Content: []llm.Content{{Type: llm.ContentText, Text: "The failing implementation was repaired."}},
		}
	default:
		return nil, fmt.Errorf("unexpected E2E model step %d", len(model.requests))
	}
	return &patchModelStream{events: []llm.StreamEvent{{Kind: llm.StreamResponseFinished, Message: &response}}}, nil
}

type patchModelStream struct{ events []llm.StreamEvent }

func (stream *patchModelStream) Recv() (llm.StreamEvent, error) {
	if len(stream.events) == 0 {
		return llm.StreamEvent{}, io.EOF
	}
	event := stream.events[0]
	stream.events = stream.events[1:]
	return event, nil
}

func (*patchModelStream) Close() error { return nil }

type e2eEventSink struct{}

func (*e2eEventSink) PublishCodingEvent(context.Context, codingagent.Event) error { return nil }

func createBrokenGitRepository(t *testing.T, files map[string]string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "repository")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, command := range [][]string{
		{"init", "--quiet"}, {"config", "user.name", "CodePilot E2E"}, {"config", "user.email", "codepilot-e2e@example.invalid"},
		{"add", "."}, {"commit", "--quiet", "-m", "broken fixture"},
	} {
		process := exec.Command("git", append([]string{"-C", root}, command...)...)
		if output, err := process.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", command, err, output)
		}
	}
	return filepath.Clean(root)
}

func runE2ECommand(ctx context.Context, root string, command commandSpec) (string, error) {
	process := exec.CommandContext(ctx, command.name, command.args...)
	process.Dir = root
	output, err := process.CombinedOutput()
	return string(output), err
}

func findPythonE2E(t *testing.T) commandSpec {
	t.Helper()
	candidates := []commandSpec{{name: "python", args: []string{"-m", "unittest", "-q"}}, {name: "python3", args: []string{"-m", "unittest", "-q"}}}
	if runtime.GOOS == "windows" {
		candidates = append(candidates, commandSpec{name: "py", args: []string{"-3", "-m", "unittest", "-q"}})
	}
	for _, candidate := range candidates {
		if _, err := exec.LookPath(candidate.name); err == nil {
			return candidate
		}
	}
	if liveEnabledForE2E(os.Getenv("CODEPILOT_REQUIRE_PYTHON_E2E")) {
		t.Fatal("CODEPILOT_REQUIRE_PYTHON_E2E requires a python, python3, or py executable")
	}
	return commandSpec{}
}

func liveEnabledForE2E(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func requestHasToolResult(request llm.ChatRequest, toolName string) bool {
	for _, message := range request.Messages {
		if message.Role == llm.RoleTool && message.ToolName == toolName {
			return true
		}
	}
	return false
}

func snapshotHasAppliedDiff(snapshot codingagent.Snapshot, file string) bool {
	for _, item := range snapshot.Transcript {
		if item.Tool == nil || item.Tool.Name != "apply_patch" || item.Tool.Diff == nil {
			continue
		}
		for _, changed := range item.Tool.Diff.Files {
			if changed == file {
				return true
			}
		}
	}
	return false
}

func durableHasToolLifecycle(snapshot agentsession.Snapshot, name string) bool {
	started, finished := false, false
	for _, record := range snapshot.Records {
		if record.Tool == nil || record.Tool.ToolName != name {
			continue
		}
		started = started || record.Type == agentsession.RecordToolStarted
		finished = finished || record.Type == agentsession.RecordToolFinished
	}
	return started && finished
}
