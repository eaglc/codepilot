package codingagent_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eaglc/codepilot/internal/agent"
	agentsession "github.com/eaglc/codepilot/internal/agent/session"
	"github.com/eaglc/codepilot/internal/codingagent"
	codingtools "github.com/eaglc/codepilot/internal/codingagent/tools"
	codingfile "github.com/eaglc/codepilot/internal/codingstore/file"
	"github.com/eaglc/codepilot/internal/contextmanager"
	"github.com/eaglc/codepilot/internal/llm"
	"github.com/eaglc/codepilot/internal/tool"
	"github.com/eaglc/codepilot/internal/workflow"
)

func TestP7ParallelIsolatedWritesIntegrateExactArtifactsAndFinishCombinedReview(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("base-a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "b.txt"), []byte("base-b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, arguments := range [][]string{{"init", "--quiet"}, {"config", "user.name", "CodePilot Test"}, {"config", "user.email", "test@example.invalid"}, {"add", "a.txt", "b.txt"}, {"commit", "--quiet", "-m", "baseline"}} {
		command := exec.CommandContext(ctx, "git", append([]string{"-C", root}, arguments...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", arguments, err, output)
		}
	}
	stateRoot := t.TempDir()
	products, err := codingfile.NewRepository(filepath.Join(stateRoot, "product"))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := codingagent.NewGitManagedWorktreeManager(filepath.Join(stateRoot, "managed-worktrees"), products)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	workspace := codingagent.Workspace{ID: "workspace-p7", DisplayName: "p7", GitCommonDir: filepath.Join(root, ".git"), Trusted: true, CreatedAt: now, UpdatedAt: now}
	worktree := codingagent.Worktree{ID: "worktree-p7", WorkspaceID: workspace.ID, Root: root, GitDir: workspace.GitCommonDir, CreatedAt: now, LastUsedAt: now}
	if err := products.SaveWorkspace(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	if err := products.SaveWorktree(ctx, worktree); err != nil {
		t.Fatal(err)
	}
	planArguments, _ := json.Marshal(codingagent.PlanSubmission{
		Goal:     "Update two independent files in isolated worktrees and integrate their exact artifacts.",
		Scope:    codingagent.PlanScope{Included: []string{"a.txt", "b.txt"}},
		Findings: []string{"The files are independent."}, Risks: []string{"Only reviewed artifacts may enter the active worktree."},
		Steps: []codingagent.PlanStep{
			{ID: "implement-a", Goal: "Implement A.", Files: []string{"a.txt"}, Validation: []string{"A is updated."}, Role: workflow.RoleImplement, FailureAction: workflow.FailureBlock, MaxAttempts: 1},
			{ID: "implement-b", Goal: "Implement B.", Files: []string{"b.txt"}, Validation: []string{"B is updated."}, Role: workflow.RoleImplement, FailureAction: workflow.FailureBlock, MaxAttempts: 1},
		},
		AcceptanceCriteria: []string{"Both exact changes are integrated and reviewed."}, RecommendedStrategy: codingagent.ExecutionWorkflowMultiParallelIsolatedWrite,
		WorkspaceRelevant: true, CompletionMode: codingagent.PlanCompletionExecute,
	})
	model := &p7Model{plan: planArguments, implementReady: make(chan struct{})}
	agentSessions := agentsession.NewMemoryRepository()
	contexts, _ := contextmanager.NewManager()
	runtime, err := agent.NewRuntime(agent.Dependencies{Models: p7ModelFactory{model: model}, Contexts: contexts, Sessions: agentSessions})
	if err != nil {
		t.Fatal(err)
	}
	tools := &p7RecordingToolFactory{inner: codingtools.NewFactory(codingtools.Options{Artifacts: products}), activeRoot: filepath.Clean(root)}
	features := codingagent.DefaultFeatureFlags()
	features.ParallelWriteSubagents = true
	service, err := codingagent.NewService(codingagent.Dependencies{
		Sessions: products, Turns: products, Plans: products, Workflows: products, Children: products, ManagedWorktrees: manager,
		AgentSessions: agentSessions, Worktrees: products, Agent: runtime, Tools: tools, Prompts: staticPrompt{}, Events: &parallelProductEvents{}, Features: &features,
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := service.CreateSession(ctx, codingagent.Session{ID: "coding-p7", AgentSessionID: "agent-p7", WorkspaceID: workspace.ID, WorktreeID: worktree.ID, ProviderProfileID: "profile-1", ModelID: "model-1", PermissionMode: codingagent.PermissionAsk})
	if err != nil {
		t.Fatal(err)
	}
	planned, err := service.StartTurn(ctx, codingagent.TurnRequest{SessionID: session.ID, Text: "Plan isolated parallel implementation", Mode: codingagent.TurnModePlan})
	if err != nil || planned.InterruptKind != "plan_approval" {
		t.Fatalf("planned = %#v, %v", planned, err)
	}
	firstApproval, err := service.ResumeTurn(ctx, codingagent.ResumeTurnRequest{SessionID: session.ID, TurnID: planned.TurnID, InterruptID: planned.InterruptID, Decision: codingagent.ResolutionApproved, GrantScope: codingagent.PermissionGrantOnce, Strategy: codingagent.ExecutionWorkflowMultiParallelIsolatedWrite})
	if err != nil || firstApproval.InterruptKind != "approval" {
		t.Fatalf("first integration approval = %#v, %v", firstApproval, err)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "a.txt")); string(got) != "base-a\n" {
		t.Fatalf("active a.txt changed before integration: %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "b.txt")); string(got) != "base-b\n" {
		t.Fatalf("active b.txt changed before integration: %q", got)
	}
	secondApproval, err := service.ResumeTurn(ctx, codingagent.ResumeTurnRequest{SessionID: session.ID, TurnID: planned.TurnID, InterruptID: firstApproval.InterruptID, Decision: codingagent.ResolutionApproved, GrantScope: codingagent.PermissionGrantOnce})
	if err != nil || secondApproval.InterruptKind != "approval" {
		t.Fatalf("second integration approval = %#v, %v", secondApproval, err)
	}
	completed, err := service.ResumeTurn(ctx, codingagent.ResumeTurnRequest{SessionID: session.ID, TurnID: planned.TurnID, InterruptID: secondApproval.InterruptID, Decision: codingagent.ResolutionApproved, GrantScope: codingagent.PermissionGrantOnce})
	if err != nil || completed.Status != string(workflow.StatusCompleted) {
		t.Fatalf("completed P7 Workflow = %#v, %v", completed, err)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "a.txt")); string(got) != "done-a\n" {
		t.Fatalf("integrated a.txt = %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "b.txt")); string(got) != "done-b\n" {
		t.Fatalf("integrated b.txt = %q", got)
	}
	model.mu.Lock()
	maximumActive, elapsed := model.maximumActive, model.implementFinished.Sub(model.implementStarted)
	model.mu.Unlock()
	if maximumActive < 2 || elapsed <= 0 || elapsed >= 450*time.Millisecond {
		t.Fatalf("isolated implementation did not overlap: active=%d elapsed=%s", maximumActive, elapsed)
	}
	tools.mu.Lock()
	isolatedRoots := append([]string(nil), tools.isolatedRoots...)
	trustedIntegrations := len(tools.integrationNodes)
	tools.mu.Unlock()
	if len(isolatedRoots) != 2 || isolatedRoots[0] == isolatedRoots[1] || isolatedRoots[0] == filepath.Clean(root) || isolatedRoots[1] == filepath.Clean(root) || trustedIntegrations != 2 {
		t.Fatalf("tool scopes did not preserve isolation and serial integration: roots=%v integrations=%d", isolatedRoots, trustedIntegrations)
	}
	turn, err := products.LoadTurn(ctx, planned.TurnID)
	if err != nil || turn.Status != codingagent.TurnCompleted || turn.Strategy != codingagent.ExecutionWorkflowMultiParallelIsolatedWrite {
		t.Fatalf("P7 Turn = %#v, %v", turn, err)
	}
	durable, err := products.LoadWorkflow(ctx, workflow.ID(turn.WorkflowID))
	if err != nil || durable.Status != workflow.StatusCompleted || durable.Strategy != workflow.StrategyMultiAgentParallelIsolatedWrite {
		t.Fatalf("P7 Workflow = %#v, %v", durable, err)
	}
	children, err := products.ListChildAgents(ctx, turn.ID)
	if err != nil || len(children) != 4 {
		t.Fatalf("P7 children = %#v, %v", children, err)
	}
	implementCount, finalReadOnlyCount := 0, 0
	for _, child := range children {
		if child.Role == workflow.RoleImplement {
			implementCount++
			if child.ManagedWorktree == nil || child.ManagedWorktree.Status != codingagent.ManagedWorktreeCleaned || child.ManagedWorktree.ChangeSet == nil || child.ManagedWorktree.ChangeSet.IntegratedAt.IsZero() {
				t.Fatalf("implemented child did not reach durable cleaned state: %#v", child)
			}
			artifact, loadErr := products.LoadArtifact(ctx, child.ManagedWorktree.ChangeSet.Patch)
			if loadErr != nil || artifact.MediaType != "text/x-diff" || len(artifact.Data) == 0 {
				t.Fatalf("preserved change artifact = %#v, %v", artifact, loadErr)
			}
			if _, statErr := os.Stat(child.ManagedWorktree.Root); !os.IsNotExist(statErr) {
				t.Fatalf("managed worktree was not cleaned: %q, %v", child.ManagedWorktree.Root, statErr)
			}
		} else if child.Role == workflow.RoleValidate || child.Role == workflow.RoleReview {
			finalReadOnlyCount++
			if len(child.Task.WritePaths) != 0 || child.ManagedWorktree != nil {
				t.Fatalf("final validation/review escaped read-only policy: %#v", child)
			}
		}
	}
	if implementCount != 2 || finalReadOnlyCount != 2 {
		t.Fatalf("P7 child roles: implement=%d final-read-only=%d", implementCount, finalReadOnlyCount)
	}
	snapshot, err := service.Snapshot(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ActivePlan == nil || snapshot.ActivePlan.StrategyRecommendation.Version != codingagent.StrategyRecommendationVersion || snapshot.ActivePlan.StrategyRecommendation.IndependentStreams != 2 || snapshot.ActivePlan.StrategyRecommendation.EstimatedAgents != 4 || snapshot.ActivePlan.StrategyRecommendation.EstimatedConcurrency != 2 || snapshot.Metrics.Strategy.Recommendations != 1 || snapshot.Metrics.Strategy.MultiAgentRecommendations != 1 || snapshot.Metrics.Strategy.ApprovedSelections != 1 || snapshot.Metrics.Strategy.SelectedMultiAgent != 1 {
		t.Fatalf("P8 recommendation/selection projection = plan %#v metrics %#v", snapshot.ActivePlan, snapshot.Metrics.Strategy)
	}
	if len(snapshot.Metrics.Strategy.Outcomes) != 1 || snapshot.Metrics.Strategy.Outcomes[0].Strategy != codingagent.ExecutionWorkflowMultiParallelIsolatedWrite || snapshot.Metrics.Strategy.Outcomes[0].CompletedTurns != 1 || snapshot.Metrics.Strategy.Outcomes[0].Runs < 6 {
		t.Fatalf("P8 strategy outcomes = %#v", snapshot.Metrics.Strategy.Outcomes)
	}
	visibleArtifacts := 0
	for _, child := range snapshot.ChildAgents {
		if child.ManagedStatus == codingagent.ManagedWorktreeCleaned && child.ChangeSetID != "" && child.PatchArtifact != "" && len(child.ChangeFiles) == 1 {
			visibleArtifacts++
		}
	}
	if visibleArtifacts != 2 {
		t.Fatalf("snapshot did not expose both durable change artifacts: %#v", snapshot.ChildAgents)
	}
}

type p7ModelFactory struct{ model *p7Model }

func (f p7ModelFactory) CreateModel(context.Context, llm.ModelRef) (llm.ChatModel, error) {
	return f.model, nil
}

type p7Model struct {
	mu                sync.Mutex
	plan              json.RawMessage
	parentCalls       int
	implementCount    int
	active            int
	maximumActive     int
	implementReady    chan struct{}
	implementStarted  time.Time
	implementFinished time.Time
}

func (*p7Model) Complete(context.Context, llm.ChatRequest) (llm.Message, error) {
	return llm.Message{}, nil
}

func (m *p7Model) Stream(ctx context.Context, request llm.ChatRequest) (llm.Stream, error) {
	goal := delegatedGoal(request)
	if goal != "" {
		if strings.HasPrefix(goal, "Implement ") && !requestHasToolResult(request, "edit_file") {
			m.mu.Lock()
			if m.implementCount == 0 {
				m.implementStarted = time.Now()
			}
			m.implementCount++
			m.active++
			if m.active > m.maximumActive {
				m.maximumActive = m.active
			}
			if m.implementCount == 2 {
				close(m.implementReady)
			}
			m.mu.Unlock()
			select {
			case <-m.implementReady:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			select {
			case <-time.After(200 * time.Millisecond):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			m.mu.Lock()
			m.active--
			if m.active == 0 {
				m.implementFinished = time.Now()
			}
			m.mu.Unlock()
			path, oldText, newText := "a.txt", "base-a\n", "done-a\n"
			if strings.Contains(goal, "B") {
				path, oldText, newText = "b.txt", "base-b\n", "done-b\n"
			}
			arguments, _ := json.Marshal(map[string]string{"path": path, "old_text": oldText, "new_text": newText, "intent": goal})
			response := p5ToolCall("p7-edit-"+path, "edit_file", arguments)
			return streamMessage(response), nil
		}
		response := p5ChildResult("p7-result-"+strings.ReplaceAll(strings.ToLower(goal), " ", "-"), "Completed: "+goal, nil, []string{"Scoped result validated."})
		return streamMessage(response), nil
	}
	if hasToolDefinition(request, "integrate_change_set") {
		changeID := assignedP7ChangeSet(request)
		callID := "p7-integrate-" + changeID
		if changeID == "" || requestHasToolResultID(request, callID) {
			response := finalAssistant()
			return streamMessage(response), nil
		}
		arguments, _ := json.Marshal(map[string]string{"change_set_id": changeID})
		response := p5ToolCall(callID, "integrate_change_set", arguments)
		return streamMessage(response), nil
	}
	m.mu.Lock()
	call := m.parentCalls
	m.parentCalls++
	m.mu.Unlock()
	response := p5ToolCall("p7-context", "request_workspace_context", json.RawMessage(`{"reason":"The isolated changes depend on the committed workspace baseline."}`))
	if call != 0 {
		response = p5ToolCall("p7-plan", "exit_plan_mode", m.plan)
	}
	return streamMessage(response), nil
}

func streamMessage(message llm.Message) llm.Stream {
	return &finalStream{events: []llm.StreamEvent{{Kind: llm.StreamResponseFinished, Message: &message}}}
}

func hasToolDefinition(request llm.ChatRequest, name string) bool {
	for _, definition := range request.Tools {
		if definition.Name == name {
			return true
		}
	}
	return false
}

func requestHasToolResultID(request llm.ChatRequest, id string) bool {
	for _, message := range request.Messages {
		if message.Role == llm.RoleTool && message.ToolCallID == id {
			return true
		}
	}
	return false
}

func assignedP7ChangeSet(request llm.ChatRequest) string {
	source := ""
	for messageIndex := len(request.Messages) - 1; messageIndex >= 0 && source == ""; messageIndex-- {
		for contentIndex := len(request.Messages[messageIndex].Content) - 1; contentIndex >= 0; contentIndex-- {
			text := request.Messages[messageIndex].Content[contentIndex].Text
			if !strings.Contains(text, "Execute only this durable Workflow node") || !strings.Contains(text, "\"integration_sources\"") {
				continue
			}
			var node struct {
				Sources []string `json:"integration_sources"`
			}
			if start := strings.Index(text, "{"); start >= 0 && json.Unmarshal([]byte(text[start:]), &node) == nil && len(node.Sources) == 1 {
				source = node.Sources[0]
				break
			}
		}
	}
	if source == "" {
		return ""
	}
	for messageIndex := len(request.Messages) - 1; messageIndex >= 0; messageIndex-- {
		for contentIndex := len(request.Messages[messageIndex].Content) - 1; contentIndex >= 0; contentIndex-- {
			text := request.Messages[messageIndex].Content[contentIndex].Text
			if !strings.Contains(text, "product-validated structured summaries") {
				continue
			}
			var summaries []struct {
				NodeID      string `json:"node_id"`
				ChangeSetID string `json:"change_set_id"`
			}
			if start := strings.Index(text, "["); start >= 0 && json.Unmarshal([]byte(text[start:]), &summaries) == nil {
				for _, summary := range summaries {
					if summary.NodeID == source {
						return summary.ChangeSetID
					}
				}
			}
		}
	}
	return ""
}

type p7RecordingToolFactory struct {
	inner            codingagent.ToolFactory
	activeRoot       string
	mu               sync.Mutex
	isolatedRoots    []string
	integrationNodes map[codingagent.NodeID]bool
}

func (f *p7RecordingToolFactory) CreateTools(ctx context.Context, scope codingagent.ToolScope) (*tool.Registry, error) {
	f.mu.Lock()
	if scope.Profile == codingagent.CapabilityImplement {
		f.isolatedRoots = append(f.isolatedRoots, filepath.Clean(scope.WorktreeRoot))
	}
	if scope.TrustedIntegration {
		if f.integrationNodes == nil {
			f.integrationNodes = make(map[codingagent.NodeID]bool)
		}
		f.integrationNodes[scope.NodeID] = true
	}
	f.mu.Unlock()
	return f.inner.CreateTools(ctx, scope)
}
