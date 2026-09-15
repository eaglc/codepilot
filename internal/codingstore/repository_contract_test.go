package codingstore_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eaglc/codepilot/internal/codingagent"
	filecodingstore "github.com/eaglc/codepilot/internal/codingstore/file"
	memorycodingstore "github.com/eaglc/codepilot/internal/codingstore/memory"
	"github.com/eaglc/codepilot/internal/workflow"
)

type repository interface {
	codingagent.WorkspaceRepository
	codingagent.SessionRepository
	codingagent.TurnRepository
	codingagent.PlanRepository
	workflow.Repository
	codingagent.ChildAgentRepository
}

func TestRepositoriesEnforceTheSamePersistenceContract(t *testing.T) {
	tests := []struct {
		name string
		new  func(*testing.T) repository
	}{
		{name: "memory", new: func(*testing.T) repository { return memorycodingstore.NewRepository() }},
		{name: "file", new: func(t *testing.T) repository {
			value, err := filecodingstore.NewRepository(filepath.Join(t.TempDir(), "state"))
			if err != nil {
				t.Fatalf("new file repository: %v", err)
			}
			return value
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			testRepositoryContract(t, test.new(t))
		})
	}
}

func testRepositoryContract(t *testing.T, repository repository) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	root := filepath.Join(t.TempDir(), "worktree")
	gitDir := filepath.Join(root, ".git")
	workspace := codingagent.Workspace{
		ID: "workspace", DisplayName: "repository", GitCommonDir: gitDir,
		RepositoryFingerprint: "git-anchor-v1:sha1:0123456789abcdef0123456789abcdef01234567",
		Trusted:               true, CreatedAt: now, UpdatedAt: now,
	}
	invalidWorkspace := workspace
	invalidWorkspace.DisplayName = ""
	if err := repository.SaveWorkspace(ctx, invalidWorkspace); err == nil {
		t.Fatal("SaveWorkspace accepted incomplete workspace")
	}
	if err := repository.SaveWorkspace(ctx, workspace); err != nil {
		t.Fatalf("SaveWorkspace: %v", err)
	}
	changedWorkspace := workspace
	changedWorkspace.CreatedAt = now.Add(time.Second)
	if err := repository.SaveWorkspace(ctx, changedWorkspace); err == nil {
		t.Fatal("SaveWorkspace accepted immutable creation-time change")
	}

	worktree := codingagent.Worktree{ID: "worktree", WorkspaceID: workspace.ID, Root: root, GitDir: gitDir, CreatedAt: now, LastUsedAt: now}
	invalidWorktree := worktree
	invalidWorktree.Root = "relative"
	if err := repository.SaveWorktree(ctx, invalidWorktree); err == nil {
		t.Fatal("SaveWorktree accepted a relative root")
	}
	orphanWorktree := worktree
	orphanWorktree.ID = "orphan"
	orphanWorktree.WorkspaceID = "missing"
	if err := repository.SaveWorktree(ctx, orphanWorktree); err == nil {
		t.Fatal("SaveWorktree accepted a missing workspace")
	}
	if err := repository.SaveWorktree(ctx, worktree); err != nil {
		t.Fatalf("SaveWorktree: %v", err)
	}
	changedWorktree := worktree
	changedWorktree.Root = filepath.Join(t.TempDir(), "changed")
	changedWorktree.GitDir = filepath.Join(changedWorktree.Root, ".git")
	if err := repository.SaveWorktree(ctx, changedWorktree); err == nil {
		t.Fatal("SaveWorktree accepted immutable path change")
	}

	session := codingagent.Session{
		ID: "session", AgentSessionID: "agent", WorkspaceID: workspace.ID, WorktreeID: worktree.ID,
		ProviderProfileID: "provider", ModelID: "model", PermissionMode: codingagent.PermissionAsk,
		CreatedAt: now, UpdatedAt: now,
	}
	invalidSession := session
	invalidSession.ModelID = ""
	if err := repository.CreateSession(ctx, invalidSession); err == nil {
		t.Fatal("CreateSession accepted an incomplete model binding")
	}
	wrongBinding := session
	wrongBinding.ID = "wrong-binding"
	wrongBinding.WorkspaceID = "another"
	if err := repository.CreateSession(ctx, wrongBinding); err == nil {
		t.Fatal("CreateSession accepted a mismatched worktree binding")
	}
	if err := repository.CreateSession(ctx, session); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	turn := codingagent.Turn{
		ID: "turn", SessionID: session.ID, RequestText: "request", Phase: codingagent.TurnPhaseDirect,
		Status: codingagent.TurnPending, Strategy: codingagent.ExecutionSingle, Revision: 1,
		Runs:      []codingagent.RunBinding{{RunID: "run", UserEntryID: "entry", Phase: codingagent.TurnPhaseDirect, Profile: codingagent.CapabilityDirect, Status: codingagent.RunBindingPending}},
		CreatedAt: now, UpdatedAt: now,
	}
	if err := repository.CreateTurn(ctx, turn); err != nil {
		t.Fatalf("CreateTurn: %v", err)
	}
	plan := codingagent.Plan{
		ID: "plan", TurnID: turn.ID, Version: 1, Goal: "Implement the approved scope.",
		Scope:    codingagent.PlanScope{Included: []string{"internal/codingagent"}},
		Findings: []string{"Product Turn exists."}, Risks: []string{"Keep write approval separate."},
		Steps:              []codingagent.PlanStep{{ID: "implement", Goal: "Implement the change.", Files: []string{"internal/codingagent/plan.go"}, Validation: []string{"Run tests."}}},
		AcceptanceCriteria: []string{"Tests pass."}, RecommendedStrategy: codingagent.ExecutionSingle,
		StrategyRecommendation: &codingagent.StrategyRecommendation{
			Version: codingagent.StrategyRecommendationVersion, PolicyVersion: 1, EvaluationSet: codingagent.StrategyEvaluationSetVersion,
			ProposedStrategy: codingagent.ExecutionSingle, SelectedStrategy: codingagent.ExecutionSingle,
			ReasonCodes: []codingagent.StrategyReasonCode{codingagent.StrategyReasonDirectDefault}, Summary: "Direct execution has the lowest overhead.",
			EstimatedAgents: 1, EstimatedConcurrency: 1, IndependentStreams: 1, AutoEligible: true,
		},
		WorkspaceRelevant: true, CompletionMode: codingagent.PlanCompletionExecute,
		WorkspaceRevision: codingagent.WorkspaceRevision{
			Version: 2, WorktreeID: worktree.ID, IdentityDigest: strings.Repeat("a", 64), StatusDigest: strings.Repeat("b", 64), DiffDigest: strings.Repeat("c", 64),
			RelevantPaths: []codingagent.WorkspacePathRevision{{Path: "internal/codingagent/plan.go", Kind: "file", Digest: strings.Repeat("d", 64), Files: 1}}, RecordedAt: now,
		}, CreatedAt: now,
	}
	plan.Digest, _ = codingagent.ComputePlanDigest(plan)
	if err := repository.CreatePlanVersion(ctx, plan); err != nil {
		t.Fatalf("CreatePlanVersion: %v", err)
	}
	loadedPlan, err := repository.LoadPlan(ctx, plan.ID, plan.Version)
	if err != nil || loadedPlan.Digest != plan.Digest || loadedPlan.StrategyRecommendation == nil || loadedPlan.StrategyRecommendation.SelectedStrategy != codingagent.ExecutionSingle {
		t.Fatalf("LoadPlan = %#v, %v", loadedPlan, err)
	}
	loadedPlan.StrategyRecommendation.ReasonCodes[0] = codingagent.StrategyReasonWorkflowDisabled
	reloadedPlan, reloadErr := repository.LoadPlan(ctx, plan.ID, plan.Version)
	if reloadErr != nil || reloadedPlan.StrategyRecommendation.ReasonCodes[0] != codingagent.StrategyReasonDirectDefault {
		t.Fatalf("Plan recommendation was not deeply cloned: %#v, %v", reloadedPlan.StrategyRecommendation, reloadErr)
	}
	secondPlan := plan
	secondPlan.Version = 2
	secondPlan.Goal = "Implement the revised approved scope."
	secondPlan.CreatedAt = now.Add(time.Second)
	secondPlan.Digest, _ = codingagent.ComputePlanDigest(secondPlan)
	if err := repository.CreatePlanVersion(ctx, secondPlan); err != nil {
		t.Fatalf("CreatePlanVersion second: %v", err)
	}
	versions, err := repository.ListPlanVersions(ctx, plan.ID)
	if err != nil || len(versions) != 2 || versions[1].Version != 2 {
		t.Fatalf("ListPlanVersions = %#v, %v", versions, err)
	}
	workflowValue := workflow.Workflow{
		ID: "workflow", OwnerID: string(turn.ID), Plan: workflow.PlanReference{ID: string(plan.ID), Version: plan.Version, Digest: plan.Digest},
		Strategy: workflow.StrategySingleAgent, Status: workflow.StatusPending, Budget: workflow.Budget{MaxNodes: 1, MaxRuns: 2, MaxAttempts: 2, MaxAgentSteps: 64},
		Nodes:    []workflow.Node{{ID: "implement", Goal: "Implement the approved node.", Role: workflow.RoleImplement, Capability: workflow.CapabilityImplement, Scope: workflow.Scope{ReadPaths: []string{"internal"}, WritePaths: []string{"internal"}}, AcceptanceCriteria: []string{"Node completes."}, FailureAction: workflow.FailureRetry, MaxAttempts: 2, Status: workflow.NodePending}},
		Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := repository.CreateWorkflow(ctx, workflowValue); err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	startEvent := workflow.Event{ID: "workflow-start", Type: workflow.EventWorkflowStarted, OccurredAt: now.Add(time.Second)}
	workflowValue, err = repository.AppendWorkflowEvent(ctx, workflowValue.ID, 1, startEvent)
	if err != nil || workflowValue.Status != workflow.StatusRunning || workflowValue.Revision != 2 {
		t.Fatalf("AppendWorkflowEvent start = %#v, %v", workflowValue, err)
	}
	idempotent, err := repository.AppendWorkflowEvent(ctx, workflowValue.ID, 1, startEvent)
	if err != nil || idempotent.Revision != workflowValue.Revision {
		t.Fatalf("AppendWorkflowEvent idempotent = %#v, %v", idempotent, err)
	}
	if _, err := repository.AppendWorkflowEvent(ctx, workflowValue.ID, 1, workflow.Event{ID: "node-start-stale", Type: workflow.EventNodeStarted, NodeID: "implement", OccurredAt: now.Add(2 * time.Second)}); err == nil {
		t.Fatal("AppendWorkflowEvent accepted stale revision")
	}
	workflowValue, err = repository.AppendWorkflowEvent(ctx, workflowValue.ID, 2, workflow.Event{ID: "node-start", Type: workflow.EventNodeStarted, NodeID: "implement", OccurredAt: now.Add(2 * time.Second)})
	if err != nil || workflowValue.Nodes[0].Status != workflow.NodeRunning {
		t.Fatalf("AppendWorkflowEvent node start = %#v, %v", workflowValue, err)
	}
	loadedWorkflow, err := repository.LoadWorkflow(ctx, workflowValue.ID)
	if err != nil || loadedWorkflow.Revision != 3 || loadedWorkflow.Nodes[0].Attempts != 1 {
		t.Fatalf("LoadWorkflow = %#v, %v", loadedWorkflow, err)
	}
	workflows, err := repository.ListWorkflows(ctx, string(turn.ID))
	if err != nil || len(workflows) != 1 || workflows[0].ID != workflowValue.ID {
		t.Fatalf("ListWorkflows = %#v, %v", workflows, err)
	}
	childID := codingagent.WorkflowChildAgentID(string(workflowValue.ID), "implement", 1)
	child := codingagent.ChildAgent{
		ID: childID, Kind: codingagent.ChildAgentWorkflowNode, ParentSessionID: session.ID, ParentTurnID: turn.ID,
		WorkflowID: string(workflowValue.ID), NodeID: "implement", PlanID: plan.ID, PlanVersion: plan.Version, PlanDigest: plan.Digest,
		Role: workflow.RoleImplement, Profile: codingagent.CapabilityImplement,
		Task:           codingagent.AgentTask{Goal: "Implement the bounded node.", ReadPaths: []string{"internal"}, WritePaths: []string{"internal"}, AcceptanceCriteria: []string{"Node completes."}},
		AgentSessionID: codingagent.ChildAgentSessionID(childID), RunID: codingagent.ChildAgentRunID(childID), Attempt: 1,
		Status: codingagent.ChildAgentCreating, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := repository.CreateChildAgent(ctx, child); err != nil {
		t.Fatalf("CreateChildAgent: %v", err)
	}
	if err := repository.CreateChildAgent(ctx, child); err != nil {
		t.Fatalf("CreateChildAgent idempotent: %v", err)
	}
	conflictingChild := child
	conflictingChild.Task.Goal = "Different task."
	if err := repository.CreateChildAgent(ctx, conflictingChild); err == nil {
		t.Fatal("CreateChildAgent accepted conflicting deterministic identity")
	}
	child.Status = codingagent.ChildAgentReady
	child.UpdatedAt = now.Add(time.Second)
	child.Revision++
	if err := repository.SaveChildAgent(ctx, child, 1); err != nil {
		t.Fatalf("SaveChildAgent ready: %v", err)
	}
	if err := repository.SaveChildAgent(ctx, child, 1); err == nil {
		t.Fatal("SaveChildAgent accepted stale revision")
	}
	child.Status = codingagent.ChildAgentRunning
	child.StartedAt = now.Add(2 * time.Second)
	child.UpdatedAt = child.StartedAt
	child.Revision++
	if err := repository.SaveChildAgent(ctx, child, 2); err != nil {
		t.Fatalf("SaveChildAgent running: %v", err)
	}
	child.Status = codingagent.ChildAgentCompleted
	child.Result = &codingagent.AgentTaskResult{Status: codingagent.AgentTaskSucceeded, Conclusion: "Implemented.", Evidence: []codingagent.AgentTaskEvidence{{SourceID: "test", Summary: "Validation passed."}}, SubmittedAt: now.Add(3 * time.Second)}
	child.CompletedAt = now.Add(3 * time.Second)
	child.UpdatedAt = child.CompletedAt
	child.Revision++
	if err := repository.SaveChildAgent(ctx, child, 3); err != nil {
		t.Fatalf("SaveChildAgent completed: %v", err)
	}
	loadedChild, err := repository.LoadChildAgent(ctx, child.ID)
	if err != nil || loadedChild.Status != codingagent.ChildAgentCompleted || loadedChild.Result == nil || loadedChild.Result.Conclusion != "Implemented." {
		t.Fatalf("LoadChildAgent = %#v, %v", loadedChild, err)
	}
	children, err := repository.ListChildAgents(ctx, turn.ID)
	if err != nil || len(children) != 1 || children[0].ID != child.ID {
		t.Fatalf("ListChildAgents = %#v, %v", children, err)
	}
	secondSession := session
	secondSession.ID = "session-2"
	secondSession.AgentSessionID = "agent-2"
	if err := repository.CreateSession(ctx, secondSession); err != nil {
		t.Fatalf("CreateSession second: %v", err)
	}
	duplicateTurn := turn
	duplicateTurn.SessionID = secondSession.ID
	if err := repository.CreateTurn(ctx, duplicateTurn); err == nil {
		t.Fatal("CreateTurn accepted a globally duplicated TurnID")
	}
	loadedTurn, err := repository.LoadTurn(ctx, turn.ID)
	if err != nil || loadedTurn.ID != turn.ID {
		t.Fatalf("LoadTurn = %#v, %v", loadedTurn, err)
	}
	turn.Runs[0].Status = codingagent.RunBindingRunning
	turn.Runs[0].StartedAt = now
	turn.Status = codingagent.TurnRunning
	turn.Revision = 2
	if err := repository.SaveTurn(ctx, turn, 1); err != nil {
		t.Fatalf("SaveTurn: %v", err)
	}
	if err := repository.SaveTurn(ctx, turn, 1); err == nil {
		t.Fatal("SaveTurn accepted a stale revision")
	}
	suggestion := codingagent.PlanEntrySuggestion{
		ReasonCode:  codingagent.PlanEntryCrossModuleChange,
		Summary:     "The change crosses durable product boundaries.",
		Digest:      "",
		SuggestedAt: now.Add(time.Second),
	}
	suggestion.Digest = planEntrySuggestionDigest(suggestion.ReasonCode, suggestion.Summary)
	turn.Phase = codingagent.TurnPhaseAwaitingPlanEntryApproval
	turn.PlanEntrySuggestion = &suggestion
	turn.UpdatedAt = suggestion.SuggestedAt
	turn.Revision++
	if err := repository.SaveTurn(ctx, turn, 2); err != nil {
		t.Fatalf("SaveTurn Plan entry suggestion: %v", err)
	}
	turn.Phase = codingagent.TurnPhaseDirect
	turn.DeclinedPlanReasons = []codingagent.PlanEntryReasonCode{suggestion.ReasonCode}
	turn.UpdatedAt = turn.UpdatedAt.Add(time.Second)
	turn.Revision++
	if err := repository.SaveTurn(ctx, turn, 3); err != nil {
		t.Fatalf("SaveTurn Plan entry decline: %v", err)
	}
	loadedTurn, err = repository.LoadTurn(ctx, turn.ID)
	if err != nil || loadedTurn.PlanEntrySuggestion == nil || loadedTurn.PlanEntrySuggestion.Digest != suggestion.Digest || len(loadedTurn.DeclinedPlanReasons) != 1 {
		t.Fatalf("LoadTurn Plan entry history = %#v, %v", loadedTurn, err)
	}
	changedSession := session
	changedSession.WorktreeID = "another"
	if err := repository.SaveSession(ctx, changedSession); err == nil {
		t.Fatal("SaveSession accepted immutable binding change")
	}

	intent := codingagent.SessionCreationIntent{
		ID: "not-deterministic", Session: session, Status: codingagent.SessionCreationPending,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := repository.BeginSessionCreation(ctx, intent); err == nil {
		t.Fatal("BeginSessionCreation accepted a non-deterministic identity")
	}
}

func planEntrySuggestionDigest(reason codingagent.PlanEntryReasonCode, summary string) string {
	digest := sha256.Sum256([]byte(string(reason) + "\x00" + summary))
	return hex.EncodeToString(digest[:])
}
