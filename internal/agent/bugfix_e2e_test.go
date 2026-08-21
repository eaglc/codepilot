package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	"github.com/eaglc/codepilot/internal/provider"
	"github.com/eaglc/codepilot/internal/session"
	"github.com/eaglc/codepilot/internal/sessionstore"
	"github.com/eaglc/codepilot/internal/workspace"
)

func TestGoBugfixEndToEndIsRecordedAsVerified(t *testing.T) {
	root := newBrokenGoWorktree(t)
	authorizer := approval.NewService()
	t.Cleanup(func() { _ = authorizer.Close() })
	workspaces, err := workspace.NewService(workspace.Dependencies{
		Authorizer: authorizer,
		Executor:   workspace.NewLocalCommandExecutor(),
		Limits:     workspace.DefaultLimits(),
	})
	if err != nil {
		t.Fatal(err)
	}
	languages, err := language.NewRegistry(language.NewGoStrategy())
	if err != nil {
		t.Fatal(err)
	}
	modelValue := &bugfixModel{}
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
	turnID, err := service.StartTurn(context.Background(), "Fix Answer so the Go test passes.")
	if err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-events.completed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("timed out waiting for the Go bugfix turn")
	}
	waitForIdle(t, service)
	stored, err := store.LoadSession(context.Background(), snapshot.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Turns) != 1 || stored.Turns[0].ID != turnID || stored.Turns[0].Status != session.TurnVerified {
		content, _ := os.ReadFile(filepath.Join(root, "answer.go"))
		command := exec.Command("go", "test", "./...")
		command.Dir = root
		output, commandErr := command.CombinedOutput()
		t.Fatalf("turns = %#v\nanswer.go:\n%s\nrun_checks tool result:\n%s\ndirect go test: %v\n%s", stored.Turns, content, modelValue.lastToolResult(), commandErr, output)
	}
	if stored.Turns[0].CheckSummary.Outcome != session.CheckPassed || len(stored.Patches) != 1 {
		t.Fatalf("check = %#v, patches = %#v", stored.Turns[0].CheckSummary, stored.Patches)
	}
	content, err := os.ReadFile(filepath.Join(root, "answer.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "return 42") {
		t.Fatalf("answer.go = %q", content)
	}
	if events.count(session.EventApprovalRequested) != 2 || events.count(session.EventPatchApplied) != 1 {
		t.Fatalf("events = %#v", events.snapshot())
	}
	if _, exists, err := checkpoints.Get(context.Background(), string(turnID)); err != nil || exists {
		t.Fatalf("terminal checkpoint exists = %v, error = %v", exists, err)
	}
}

type bugfixModelFactory struct {
	value model.ToolCallingChatModel
}

func (f bugfixModelFactory) NewChatModel(ctx context.Context, ref provider.ModelRef) (model.ToolCallingChatModel, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if ref.Provider != "test-provider" || ref.Model != "test-model" {
		return nil, fmt.Errorf("unexpected model ref: %#v", ref)
	}
	return f.value, nil
}

type bugfixModel struct {
	mu             sync.Mutex
	lastToolOutput string
}

func (m *bugfixModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

func (m *bugfixModel) Generate(ctx context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	toolResults := 0
	for _, message := range input {
		if message != nil && message.Role == schema.Tool {
			toolResults++
			m.mu.Lock()
			m.lastToolOutput = message.Content
			m.mu.Unlock()
		}
	}
	call := func(name string, arguments any) (*schema.Message, error) {
		encoded, err := json.Marshal(arguments)
		if err != nil {
			return nil, err
		}
		return schema.AssistantMessage("", []schema.ToolCall{{
			ID:   fmt.Sprintf("call-%d", toolResults+1),
			Type: "function",
			Function: schema.FunctionCall{
				Name:      name,
				Arguments: string(encoded),
			},
		}}), nil
	}
	switch toolResults {
	case 0:
		return call("search_code", map[string]any{"query": "return 41", "regex": false, "glob": "*.go", "limit": 20})
	case 1:
		return call("read_file", map[string]any{"path": "answer.go", "start_line": 1, "line_count": 100})
	case 2:
		return call("apply_patch", map[string]any{
			"patch":  answerPatch(),
			"intent": "Correct the off-by-one answer returned by Answer.",
		})
	case 3:
		return call("run_checks", map[string]any{"plan_id": "go-test-all"})
	default:
		return schema.AssistantMessage("Updated Answer to return 42 and confirmed all Go tests pass.", nil), nil
	}
}

func (m *bugfixModel) Stream(ctx context.Context, input []*schema.Message, options ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	message, err := m.Generate(ctx, input, options...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

func (m *bugfixModel) lastToolResult() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastToolOutput
}

type bugfixModelCatalog struct{}

func (bugfixModelCatalog) ListProviderProfiles(context.Context) ([]session.ProviderProfile, error) {
	return []session.ProviderProfile{{ID: "test-provider", ModelID: "test-model"}}, nil
}

func (bugfixModelCatalog) ConfigureProvider(context.Context, session.ConfigureProviderRequest) (session.ProviderProfile, error) {
	return session.ProviderProfile{}, errors.New("not used")
}

func (bugfixModelCatalog) ListModels(context.Context, session.ProviderProfileID) ([]session.ModelOption, error) {
	return []session.ModelOption{{ID: "test-model"}}, nil
}

func (bugfixModelCatalog) ValidateSelection(_ context.Context, selection session.ModelSelection) (session.ModelValidation, error) {
	return session.ModelValidation{Valid: selection.ProviderProfileID == "test-provider" && selection.ModelID == "test-model", UserMessage: "invalid test model"}, nil
}

// approvingEventSink simulates the UI approval action after Session has moved
// the turn into awaiting-approval state.
type approvingEventSink struct {
	mu        sync.Mutex
	service   *session.Service
	events    []session.Event
	completed chan error
	doneOnce  sync.Once
}

func newApprovingEventSink() *approvingEventSink {
	return &approvingEventSink{completed: make(chan error, 1)}
}

func (s *approvingEventSink) Publish(ctx context.Context, event session.Event) error {
	s.mu.Lock()
	s.events = append(s.events, event)
	service := s.service
	s.mu.Unlock()
	if event.Kind == session.EventApprovalRequested && event.Payload.Approval != nil && event.Payload.Approval.Request != nil {
		request := event.Payload.Approval.Request
		if service == nil {
			return errors.New("approval sink has no session service")
		}
		return service.ResolveApproval(ctx, session.ApprovalResolution{
			RequestID: request.ID,
			SessionID: request.SessionID,
			TurnID:    request.TurnID,
			Decision:  session.ApprovalDecision{Kind: session.ApprovalAllowOnce},
		})
	}
	if event.Kind == session.EventTurnCompleted || event.Kind == session.EventTurnFailed || event.Kind == session.EventTurnCancelled {
		s.doneOnce.Do(func() { s.completed <- nil })
	}
	return nil
}

func (s *approvingEventSink) snapshot() []session.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]session.Event(nil), s.events...)
}

func (s *approvingEventSink) count(kind session.EventKind) int {
	count := 0
	for _, event := range s.snapshot() {
		if event.Kind == kind {
			count++
		}
	}
	return count
}

func newBrokenGoWorktree(t *testing.T) string {
	t.Helper()
	root := filepath.Clean(t.TempDir())
	files := map[string]string{
		"go.mod":         "module example.test/answer\n\ngo 1.26\n",
		"answer.go":      "package answer\n\nfunc Answer() int {\n\treturn 41\n}\n",
		"answer_test.go": "package answer\n\nimport \"testing\"\n\nfunc TestAnswer(t *testing.T) {\n\tif Answer() != 42 {\n\t\tt.Fatalf(\"Answer() = %d, want 42\", Answer())\n\t}\n}\n",
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

func gitCommand(t *testing.T, root string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", arguments, err, output)
	}
}

func answerPatch() string {
	return "diff --git a/answer.go b/answer.go\n--- a/answer.go\n+++ b/answer.go\n@@ -1,5 +1,5 @@\n package answer\n \n func Answer() int {\n-\treturn 41\n+\treturn 42\n }\n"
}

func waitForIdle(t *testing.T, service *session.Service) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		value, err := service.CurrentSession(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if value.RuntimeState == session.RuntimeIdle {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("session did not become idle")
}
