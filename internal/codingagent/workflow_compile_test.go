package codingagent

import (
	"strings"
	"testing"
	"time"

	"github.com/eaglc/codepilot/internal/codingagent/roleprofile"
	"github.com/eaglc/codepilot/internal/workflow"
)

func TestCompilePlanWorkflowBindsExactPlanAndAddsFinalValidation(t *testing.T) {
	plan := workflowCompilerPlan(t)
	now := plan.CreatedAt.Add(time.Second)
	value, err := CompilePlanWorkflow(plan, now)
	if err != nil {
		t.Fatalf("CompilePlanWorkflow: %v", err)
	}
	if value.OwnerID != string(plan.TurnID) || value.Plan.ID != string(plan.ID) || value.Plan.Version != plan.Version || value.Plan.Digest != plan.Digest || value.Strategy != workflow.StrategySingleAgent {
		t.Fatalf("Workflow Plan binding = %#v", value)
	}
	if len(value.Nodes) != 4 || value.Nodes[0].ID != "inspect" || value.Nodes[1].DependsOn[0] != "inspect" || value.Nodes[3].Role != workflow.RoleValidate {
		t.Fatalf("compiled nodes = %#v", value.Nodes)
	}
	if value.Nodes[0].Capability != workflow.CapabilityReview || len(value.Nodes[0].Scope.WritePaths) != 0 || value.Nodes[1].Capability != workflow.CapabilityImplement || len(value.Nodes[1].Scope.WritePaths) != 1 || value.Nodes[0].PolicyVersion != 1 || value.Nodes[1].PolicyVersion != 1 {
		t.Fatalf("compiled capability scopes = %#v", value.Nodes)
	}
}

func TestCompilePlanWorkflowPinsCurrentRolePolicyVersion(t *testing.T) {
	plan := workflowCompilerPlan(t)
	defaults, err := roleprofile.NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	definitions := defaults.Definitions()
	implementV2 := roleprofile.Implement()
	implementV2.PolicyVersion = 2
	implementV2.Prompt.Instructions = "Version two implementation policy."
	roles, err := roleprofile.NewRegistry(append(definitions, implementV2)...)
	if err != nil {
		t.Fatal(err)
	}
	value, err := CompilePlanWorkflowWithRegistry(plan, plan.CreatedAt.Add(time.Second), roles)
	if err != nil {
		t.Fatal(err)
	}
	if value.Nodes[1].Role != workflow.RoleImplement || value.Nodes[1].PolicyVersion != 2 || value.Nodes[0].PolicyVersion != 1 {
		t.Fatalf("compiled role policy versions = %#v", value.Nodes)
	}
}

func TestCompilePlanWorkflowBuildsTrustedP7IntegrationGraph(t *testing.T) {
	plan := workflowCompilerPlan(t)
	plan.RecommendedStrategy = ExecutionWorkflowMultiParallelIsolatedWrite
	plan.WorkspaceRevision.GitHead = "0123456789abcdef0123456789abcdef01234567"
	plan.Digest, _ = ComputePlanDigest(plan)
	value, err := CompilePlanWorkflow(plan, plan.CreatedAt.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if value.Strategy != workflow.StrategyMultiAgentParallelIsolatedWrite || len(value.Nodes) != 6 || value.Budget.MaxConcurrency != 2 {
		t.Fatalf("P7 Workflow shape = %#v", value)
	}
	implement := value.Nodes[1]
	integrate := value.Nodes[3]
	validateStep := value.Nodes[2]
	finalValidate, finalReview := value.Nodes[4], value.Nodes[5]
	if !implement.Isolated || workflow.NodeExecutor(implement) != workflow.ExecutorChild || len(implement.Scope.WritePaths) != 1 {
		t.Fatalf("P7 Implement node = %#v", implement)
	}
	if integrate.Role != workflow.RoleIntegrate || workflow.NodeExecutor(integrate) != workflow.ExecutorMain || len(integrate.IntegrationSources) != 1 || integrate.IntegrationSources[0] != implement.ID || len(integrate.DependsOn) != 1 || integrate.DependsOn[0] != implement.ID {
		t.Fatalf("P7 Integrate node = %#v", integrate)
	}
	if len(validateStep.DependsOn) != 2 || validateStep.DependsOn[0] != implement.ID || validateStep.DependsOn[1] != integrate.ID {
		t.Fatalf("dependent P7 node bypassed integration = %#v", validateStep)
	}
	if finalValidate.Role != workflow.RoleValidate || workflow.NodeExecutor(finalValidate) != workflow.ExecutorChild || len(finalValidate.Scope.WritePaths) != 0 || finalReview.Role != workflow.RoleReview || workflow.NodeExecutor(finalReview) != workflow.ExecutorChild || len(finalReview.Scope.WritePaths) != 0 {
		t.Fatalf("P7 final validation/review = %#v / %#v", finalValidate, finalReview)
	}
}

func TestCompilePlanWorkflowRejectsDirectAndDeliverablePlans(t *testing.T) {
	plan := workflowCompilerPlan(t)
	plan.RecommendedStrategy = ExecutionSingle
	plan.Digest, _ = ComputePlanDigest(plan)
	if _, err := CompilePlanWorkflow(plan, plan.CreatedAt.Add(time.Second)); err == nil {
		t.Fatal("CompilePlanWorkflow accepted direct Plan")
	}
	plan.RecommendedStrategy = ExecutionWorkflowSingle
	plan.CompletionMode = PlanCompletionDeliverable
	plan.WorkspaceRelevant = false
	plan.WorkspaceRevision = WorkspaceRevision{}
	for index := range plan.Steps {
		plan.Steps[index].Files = nil
	}
	plan.Digest, _ = ComputePlanDigest(plan)
	if _, err := CompilePlanWorkflow(plan, plan.CreatedAt.Add(time.Second)); err == nil {
		t.Fatal("CompilePlanWorkflow accepted deliverable Plan")
	}
}

func workflowCompilerPlan(t *testing.T) Plan {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Millisecond)
	plan := Plan{
		ID: "plan-workflow", TurnID: "turn-workflow", Version: 2, Goal: "Deliver a durable Workflow.",
		Scope: PlanScope{Included: []string{"internal workflow execution"}}, Findings: []string{"Product Turns are durable."}, Risks: []string{"Recovery must be idempotent."},
		Steps: []PlanStep{
			{ID: "inspect", Goal: "Inspect the current state.", Files: []string{"internal/codingagent"}, Validation: []string{"Relevant state is understood."}, Role: workflow.RoleReview, FailureAction: workflow.FailureReplan, MaxAttempts: 1},
			{ID: "implement", Goal: "Implement the Workflow.", DependsOn: []string{"inspect"}, Files: []string{"internal/workflow"}, Validation: []string{"Workflow compiles."}, Role: workflow.RoleImplement, FailureAction: workflow.FailureRetry, MaxAttempts: 2},
			{ID: "test", Goal: "Run validation.", DependsOn: []string{"implement"}, Files: []string{"internal"}, Validation: []string{"Tests pass."}, Role: workflow.RoleValidate, FailureAction: workflow.FailureBlock, MaxAttempts: 1},
		},
		AcceptanceCriteria: []string{"All tests pass."}, RecommendedStrategy: ExecutionWorkflowSingle, WorkspaceRelevant: true, CompletionMode: PlanCompletionExecute,
		WorkspaceRevision: WorkspaceRevision{Version: 2, WorktreeID: "worktree", IdentityDigest: strings.Repeat("a", 64), StatusDigest: strings.Repeat("b", 64), DiffDigest: strings.Repeat("c", 64), RelevantPaths: []WorkspacePathRevision{
			{Path: "internal", Kind: "directory", Digest: strings.Repeat("d", 64), Files: 3},
			{Path: "internal/codingagent", Kind: "directory", Digest: strings.Repeat("e", 64), Files: 1},
			{Path: "internal/workflow", Kind: "directory", Digest: strings.Repeat("f", 64), Files: 2},
		}, RecordedAt: now}, CreatedAt: now,
	}
	plan.Digest, _ = ComputePlanDigest(plan)
	if err := ValidatePlan(plan); err != nil {
		t.Fatalf("test Plan invalid: %v", err)
	}
	return plan
}
