package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/eaglc/codepilot/internal/agent"
	"github.com/eaglc/codepilot/internal/approval"
	"github.com/eaglc/codepilot/internal/contextmanager"
	"github.com/eaglc/codepilot/internal/language"
	"github.com/eaglc/codepilot/internal/session"
	"github.com/eaglc/codepilot/internal/sessionstore"
	"github.com/eaglc/codepilot/internal/workspace"
)

func TestPythonBugfixEndToEndIsRecordedAsVerified(t *testing.T) {
	root := newBrokenPythonWorktree(t)
	authorizer := approval.NewService()
	t.Cleanup(func() { _ = authorizer.Close() })
	executor := &pytestContractExecutor{}
	workspaces, err := workspace.NewService(workspace.Dependencies{
		Authorizer: authorizer,
		Executor:   executor,
		Limits:     workspace.DefaultLimits(),
	})
	if err != nil {
		t.Fatal(err)
	}
	languages, err := language.NewRegistry(language.NewGoStrategy(), language.NewPythonStrategy())
	if err != nil {
		t.Fatal(err)
	}
	modelValue := &pythonBugfixModel{}
	checkpoints := agent.NewMemoryCheckpointStore()
	t.Cleanup(func() { _ = checkpoints.Close() })
	invokers, err := agent.NewEinoInvokerFactory(agent.EinoInvokerDependencies{
		Models:      bugfixModelFactory{value: modelValue},
		Checkpoints: checkpoints,
	})
	if err != nil {
		t.Fatal(err)
	}
	contexts, err := contextmanager.NewManager(contextmanager.NopStrategy{})
	if err != nil {
		t.Fatal(err)
	}
	agents, err := agent.NewFactory(agent.Dependencies{
		Workspaces: workspaces,
		Languages:  languages,
		Authorizer: authorizer,
		Invokers:   invokers,
		Contexts:   contexts,
	})
	if err != nil {
		t.Fatal(err)
	}
	store := sessionstore.NewMemoryStore()
	events := newApprovingEventSink()
	service, err := session.NewService(session.Dependencies{
		CodingAgents:      agents,
		SessionStore:      store,
		WorkspaceRegistry: store,
		WorkspaceReader:   workspaces,
		ModelCatalog:      bugfixModelCatalog{},
		Authorizer:        authorizer,
		Events:            events,
		Limits: session.RunLimits{
			MaxSteps:              20,
			MaxTurnDuration:       30 * time.Second,
			CommandTimeout:        30 * time.Second,
			ToolResultMaxBytes:    1 << 20,
			CommandOutputMaxBytes: 1 << 20,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	events.service = service
	t.Cleanup(func() { _ = service.Close() })

	snapshot, err := service.Activate(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.SwitchModel(context.Background(), session.ModelSelection{ProviderProfileID: "test-provider", ModelID: "test-model"}); err != nil {
		t.Fatal(err)
	}
	turnID, err := service.StartTurn(context.Background(), "Fix answer() so the existing pytest test passes.")
	if err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-events.completed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("timed out waiting for the Python bugfix turn")
	}
	waitForIdle(t, service)
	stored, err := store.LoadSession(context.Background(), snapshot.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Turns) != 1 || stored.Turns[0].ID != turnID || stored.Turns[0].Status != session.TurnVerified {
		t.Fatalf("turns = %#v, check command = %#v", stored.Turns, executor.snapshot())
	}
	if stored.Turns[0].CheckSummary.Outcome != session.CheckPassed || len(stored.Patches) != 1 {
		t.Fatalf("check = %#v, patches = %#v", stored.Turns[0].CheckSummary, stored.Patches)
	}
	content, err := os.ReadFile(filepath.Join(root, "answer.py"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "return 42") {
		t.Fatalf("answer.py = %q", content)
	}
	specs := executor.snapshot()
	if len(specs) != 1 || specs[0].Program != "python" || !reflect.DeepEqual(specs[0].Args, []string{"-m", "pytest", "-q"}) {
		t.Fatalf("pytest command = %#v", specs)
	}
	if events.count(session.EventApprovalRequested) != 2 || events.count(session.EventPatchApplied) != 1 {
		t.Fatalf("events = %#v", events.snapshot())
	}
	if _, exists, err := checkpoints.Get(context.Background(), string(turnID)); err != nil || exists {
		t.Fatalf("terminal checkpoint exists = %v, error = %v", exists, err)
	}
}

type pythonBugfixModel struct{}

func (m *pythonBugfixModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

func (m *pythonBugfixModel) Generate(ctx context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	toolResults := 0
	for _, message := range input {
		if message != nil && message.Role == schema.Tool {
			toolResults++
		}
	}
	call := func(name string, arguments any) (*schema.Message, error) {
		encoded, err := json.Marshal(arguments)
		if err != nil {
			return nil, err
		}
		return schema.AssistantMessage("", []schema.ToolCall{{
			ID:   fmt.Sprintf("python-call-%d", toolResults+1),
			Type: "function",
			Function: schema.FunctionCall{
				Name:      name,
				Arguments: string(encoded),
			},
		}}), nil
	}
	switch toolResults {
	case 0:
		return call("search_code", map[string]any{"query": "return 41", "regex": false, "glob": "*.py", "limit": 20})
	case 1:
		return call("read_file", map[string]any{"path": "answer.py", "start_line": 1, "line_count": 100})
	case 2:
		return call("apply_patch", map[string]any{
			"patch":  pythonAnswerPatch(),
			"intent": "Correct the answer returned by answer().",
		})
	case 3:
		return call("run_checks", map[string]any{"plan_id": "pytest-all"})
	default:
		return schema.AssistantMessage("Updated answer() to return 42 and confirmed the existing pytest suite passes.", nil), nil
	}
}

func (m *pythonBugfixModel) Stream(ctx context.Context, input []*schema.Message, options ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	message, err := m.Generate(ctx, input, options...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

// pytestContractExecutor verifies the exact safe command and derives a
// deterministic pytest-like result from the patched fixture. It keeps the Go
// test suite independent of a machine-wide Python installation.
type pytestContractExecutor struct {
	mu    sync.Mutex
	specs []workspace.CommandSpec
}

func (e *pytestContractExecutor) Run(ctx context.Context, spec workspace.CommandSpec) (workspace.CommandResult, error) {
	if err := ctx.Err(); err != nil {
		return workspace.CommandResult{}, err
	}
	e.mu.Lock()
	e.specs = append(e.specs, cloneCommandSpec(spec))
	e.mu.Unlock()
	if spec.Program != "python" || !reflect.DeepEqual(spec.Args, []string{"-m", "pytest", "-q"}) {
		return workspace.CommandResult{ExitCode: 2, Stderr: "unexpected pytest command"}, nil
	}
	content, err := os.ReadFile(filepath.Join(spec.Dir, "answer.py"))
	if err != nil {
		return workspace.CommandResult{}, err
	}
	if !strings.Contains(string(content), "return 42") {
		return workspace.CommandResult{ExitCode: 1, Stdout: "1 failed"}, nil
	}
	return workspace.CommandResult{ExitCode: 0, Stdout: "1 passed"}, nil
}

func (e *pytestContractExecutor) Start(context.Context, workspace.ProcessSpec) (workspace.CommandProcess, error) {
	return nil, errors.New("unexpected process start")
}

func (e *pytestContractExecutor) snapshot() []workspace.CommandSpec {
	e.mu.Lock()
	defer e.mu.Unlock()
	values := make([]workspace.CommandSpec, len(e.specs))
	for index, spec := range e.specs {
		values[index] = cloneCommandSpec(spec)
	}
	return values
}

func cloneCommandSpec(spec workspace.CommandSpec) workspace.CommandSpec {
	spec.Args = append([]string(nil), spec.Args...)
	spec.EnvAllowlist = append([]string(nil), spec.EnvAllowlist...)
	return spec
}

func newBrokenPythonWorktree(t *testing.T) string {
	t.Helper()
	root := filepath.Clean(t.TempDir())
	files := map[string]string{
		"pyproject.toml": "[project]\nname = \"answer-fixture\"\nversion = \"0.0.0\"\n\n[tool.pytest.ini_options]\naddopts = \"-q\"\n",
		"answer.py":      "def answer():\n    return 41\n",
		"test_answer.py": "from answer import answer\n\n\ndef test_answer():\n    assert answer() == 42\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	gitCommand(t, root, "init")
	gitCommand(t, root, "config", "user.email", "codepilot@example.test")
	gitCommand(t, root, "config", "user.name", "CodePilot Test")
	gitCommand(t, root, "add", ".")
	gitCommand(t, root, "commit", "-m", "initial")
	return root
}

func pythonAnswerPatch() string {
	return "diff --git a/answer.py b/answer.py\n--- a/answer.py\n+++ b/answer.py\n@@ -1,2 +1,2 @@\n def answer():\n-    return 41\n+    return 42\n"
}
