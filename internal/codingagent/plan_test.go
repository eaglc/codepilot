package codingagent

import (
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

func TestLegacyPlanCompatibilityPreservesOriginalDigest(t *testing.T) {
	legacy := validTestPlan(t)
	legacy.WorkspaceRelevant = false
	legacy.CompletionMode = ""
	legacy.Digest, _ = computeLegacyPlanDigest(legacy)
	originalDigest := legacy.Digest
	upgraded := ApplyPlanCompatibilityDefaults(legacy)
	if upgraded.CompletionMode != PlanCompletionExecute || !upgraded.WorkspaceRelevant || upgraded.Digest != originalDigest {
		t.Fatalf("legacy Plan defaults = %#v", upgraded)
	}
	if err := ValidatePlan(upgraded); err != nil {
		t.Fatalf("validate upgraded legacy Plan: %v", err)
	}
	upgraded.Goal = "tampered"
	if err := ValidatePlan(upgraded); err == nil {
		t.Fatal("legacy digest compatibility accepted tampered content")
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
		WorkspaceRevision: WorkspaceRevision{WorktreeID: "worktree-1", GitHead: "0123456789abcdef0123456789abcdef01234567", StatusDigest: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", RecordedAt: now},
		CreatedAt:         now,
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
