package codingagent

import (
	"strings"
	"testing"
	"time"
)

func TestValidatePlanRejectsInvalidDependenciesPathsAndDigest(t *testing.T) {
	plan := validTestPlan(t)
	if err := ValidatePlan(plan); err != nil {
		t.Fatalf("valid Plan: %v", err)
	}

	cyclic := plan
	cyclic.Steps = clonePlanSteps(plan.Steps)
	cyclic.Steps[0].DependsOn = []string{"verify"}
	cyclic.Digest, _ = ComputePlanDigest(cyclic)
	if err := ValidatePlan(cyclic); err == nil {
		t.Fatal("ValidatePlan accepted a dependency cycle")
	}

	escaping := plan
	escaping.Steps = clonePlanSteps(plan.Steps)
	escaping.Steps[0].Files = []string{"../outside"}
	escaping.Digest, _ = ComputePlanDigest(escaping)
	if err := ValidatePlan(escaping); err == nil {
		t.Fatal("ValidatePlan accepted an escaping file scope")
	}

	tampered := plan
	tampered.Goal = "different"
	if err := ValidatePlan(tampered); err == nil {
		t.Fatal("ValidatePlan accepted a stale digest")
	}

	nonCanonicalHead := plan
	nonCanonicalHead.WorkspaceRevision.GitHead = strings.Repeat("a", 41)
	nonCanonicalHead.Digest, _ = ComputePlanDigest(nonCanonicalHead)
	if err := ValidatePlan(nonCanonicalHead); err == nil {
		t.Fatal("ValidatePlan accepted a non-canonical Git object id")
	}
}

func TestValidatePlanSupportsWorkspaceIndependentDeliverable(t *testing.T) {
	plan := validTestPlan(t)
	plan.WorkspaceRelevant = false
	plan.CompletionMode = PlanCompletionDeliverable
	plan.WorkspaceRevision = WorkspaceRevision{}
	plan.Steps = clonePlanSteps(plan.Steps)
	for index := range plan.Steps {
		plan.Steps[index].Files = nil
	}
	plan.Digest, _ = ComputePlanDigest(plan)
	if err := ValidatePlan(plan); err != nil {
		t.Fatalf("valid general deliverable Plan: %v", err)
	}
	plan.CompletionMode = PlanCompletionExecute
	plan.Digest, _ = ComputePlanDigest(plan)
	if err := ValidatePlan(plan); err == nil {
		t.Fatal("workspace-independent Plan was allowed to start execution")
	}
}

func validTestPlan(t *testing.T) Plan {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Millisecond)
	value := Plan{
		ID: "plan-1", TurnID: "turn-1", Version: 1, Goal: "Deliver explicit Plan mode.",
		Scope:    PlanScope{Included: []string{"internal/codingagent"}, Excluded: []string{"docs"}},
		Findings: []string{"Product Turns already support multiple Runs."}, Risks: []string{"Approval must remain separate from write permission."},
		Steps: []PlanStep{
			{ID: "model", Goal: "Add the Plan model.", Files: []string{"internal/codingagent/plan.go"}, Validation: []string{"Run unit tests."}},
			{ID: "verify", Goal: "Verify the Plan flow.", DependsOn: []string{"model"}, Validation: []string{"Run integration tests."}},
		},
		AcceptanceCriteria: []string{"Planning exposes no write tools."}, RecommendedStrategy: ExecutionSingle,
		WorkspaceRelevant: true, CompletionMode: PlanCompletionExecute,
		WorkspaceRevision: WorkspaceRevision{
			Version: workspaceRevisionVersion, WorktreeID: "worktree-1", IdentityDigest: strings.Repeat("a", 64),
			GitHead: "0123456789abcdef0123456789abcdef01234567", StatusDigest: strings.Repeat("b", 64), DiffDigest: strings.Repeat("c", 64),
			RelevantPaths: []WorkspacePathRevision{{Path: "internal/codingagent/plan.go", Kind: "file", Digest: strings.Repeat("d", 64), Files: 1}}, RecordedAt: now,
		},
		CreatedAt: now,
	}
	value.Digest, _ = ComputePlanDigest(value)
	return value
}

func clonePlanSteps(values []PlanStep) []PlanStep {
	cloned := append([]PlanStep(nil), values...)
	for index := range cloned {
		cloned[index].DependsOn = append([]string(nil), cloned[index].DependsOn...)
		cloned[index].Files = append([]string(nil), cloned[index].Files...)
		cloned[index].Validation = append([]string(nil), cloned[index].Validation...)
	}
	return cloned
}
