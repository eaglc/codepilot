package codingagent

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/eaglc/codepilot/internal/codingagent/roleprofile"
	"github.com/eaglc/codepilot/internal/workflow"
)

// CompilePlanWorkflow converts one exact approved Plan revision into a bounded,
// provider-neutral serial DAG. Model ordering hints never bypass Plan or
// Workflow validation.
func CompilePlanWorkflow(plan Plan, now time.Time) (workflow.Workflow, error) {
	roles, err := roleprofile.NewDefaultRegistry()
	if err != nil {
		return workflow.Workflow{}, fmt.Errorf("compile Coding workflow: load role profiles: %w", err)
	}
	return CompilePlanWorkflowWithRegistry(plan, now, roles)
}

// CompilePlanWorkflowWithRegistry compiles against the exact immutable role
// policies wired into the product service.
func CompilePlanWorkflowWithRegistry(plan Plan, now time.Time, roles *roleprofile.Registry) (workflow.Workflow, error) {
	if err := ValidatePlan(plan); err != nil {
		return workflow.Workflow{}, fmt.Errorf("compile Coding workflow: invalid Plan: %w", err)
	}
	if plan.CompletionMode != PlanCompletionExecute || !isWorkflowStrategy(plan.RecommendedStrategy) {
		return workflow.Workflow{}, errors.New("compile Coding workflow: Plan does not select Workflow execution")
	}
	if now.IsZero() {
		return workflow.Workflow{}, errors.New("compile Coding workflow: creation time is required")
	}
	nodes := make([]workflow.Node, 0, len(plan.Steps)+1)
	maximumAttempts, totalRuns := 1, 0
	for _, step := range plan.Steps {
		role := step.Role
		if role == "" {
			role = workflow.RoleImplement
		}
		definition, err := roles.ResolveRole(role)
		if err != nil {
			return workflow.Workflow{}, fmt.Errorf("compile Coding workflow: Plan step %q: %w", step.ID, err)
		}
		capability, failure, attempts, err := compileWorkflowPolicy(step, definition)
		if err != nil {
			return workflow.Workflow{}, err
		}
		readPaths := append([]string(nil), step.Files...)
		writePaths := []string(nil)
		if definition.Result.AllowChanges {
			writePaths = append([]string(nil), step.Files...)
		}
		dependencies := make([]workflow.NodeID, len(step.DependsOn))
		for index, dependency := range step.DependsOn {
			dependencies[index] = workflow.NodeID(dependency)
		}
		executor := workflow.ExecutorMain
		if plan.RecommendedStrategy == ExecutionWorkflowMultiSerial || plan.RecommendedStrategy == ExecutionWorkflowMultiParallelReadOnly {
			executor = workflow.ExecutorChild
		}
		nodes = append(nodes, workflow.Node{
			ID: workflow.NodeID(step.ID), Goal: step.Goal, DependsOn: dependencies,
			Role: role, Capability: capability, Executor: executor, Delegated: executor == workflow.ExecutorChild, PolicyVersion: definition.PolicyVersion, Scope: workflow.Scope{ReadPaths: readPaths, WritePaths: writePaths},
			AcceptanceCriteria: append([]string(nil), step.Validation...), FailureAction: failure, MaxAttempts: attempts, Status: workflow.NodePending,
		})
		maximumAttempts = max(maximumAttempts, attempts)
		totalRuns += attempts
	}
	finalID := uniqueFinalNodeID(nodes, "workflow-final-validate")
	leaves := workflowLeaves(nodes)
	allPaths := planRelevantPaths(planSubmissionFromPlan(plan))
	validateDefinition, err := roles.ResolveRole(workflow.RoleValidate)
	if err != nil {
		return workflow.Workflow{}, fmt.Errorf("compile Coding workflow: final validation role: %w", err)
	}
	nodes = append(nodes, workflow.Node{
		ID: finalID, Goal: "Validate the combined result against the approved Plan acceptance criteria.", DependsOn: leaves,
		Role: validateDefinition.Role, Capability: validateDefinition.Workflow.Capability, Executor: workflowExecutorForStrategy(plan.RecommendedStrategy), Delegated: plan.RecommendedStrategy == ExecutionWorkflowMultiParallelReadOnly, PolicyVersion: validateDefinition.PolicyVersion, Scope: workflow.Scope{ReadPaths: allPaths},
		AcceptanceCriteria: append([]string(nil), plan.AcceptanceCriteria...), FailureAction: validateDefinition.Workflow.DefaultFailure, MaxAttempts: validateDefinition.Workflow.DefaultAttempts, Status: workflow.NodePending,
	})
	totalRuns += validateDefinition.Workflow.DefaultAttempts
	maximumAttempts = max(maximumAttempts, validateDefinition.Workflow.DefaultAttempts)
	if plan.RecommendedStrategy == ExecutionWorkflowMultiSerial || plan.RecommendedStrategy == ExecutionWorkflowMultiParallelReadOnly {
		reviewDefinition, resolveErr := roles.ResolveRole(workflow.RoleReview)
		if resolveErr != nil {
			return workflow.Workflow{}, fmt.Errorf("compile Coding workflow: final review role: %w", resolveErr)
		}
		reviewID := uniqueFinalNodeID(nodes, "workflow-final-review")
		nodes = append(nodes, workflow.Node{
			ID: reviewID, Goal: "Review and summarize the complete multi-Agent result for the user.", DependsOn: []workflow.NodeID{finalID},
			Role: reviewDefinition.Role, Capability: reviewDefinition.Workflow.Capability, Executor: workflowExecutorForStrategy(plan.RecommendedStrategy), Delegated: plan.RecommendedStrategy == ExecutionWorkflowMultiParallelReadOnly, PolicyVersion: reviewDefinition.PolicyVersion, Scope: workflow.Scope{ReadPaths: allPaths},
			AcceptanceCriteria: append([]string(nil), plan.AcceptanceCriteria...), FailureAction: reviewDefinition.Workflow.DefaultFailure, MaxAttempts: reviewDefinition.Workflow.DefaultAttempts, Status: workflow.NodePending,
		})
		totalRuns += reviewDefinition.Workflow.DefaultAttempts
		maximumAttempts = max(maximumAttempts, reviewDefinition.Workflow.DefaultAttempts)
	}
	digest := sha256.Sum256([]byte(string(plan.ID) + "\x00" + fmt.Sprint(plan.Version) + "\x00" + plan.Digest))
	value := workflow.Workflow{
		ID: workflow.ID("workflow_" + hex.EncodeToString(digest[:16])), OwnerID: string(plan.TurnID),
		Plan: workflow.PlanReference{ID: string(plan.ID), Version: plan.Version, Digest: plan.Digest}, Strategy: workflowStrategyForExecution(plan.RecommendedStrategy),
		Status: workflow.StatusPending, Budget: workflow.Budget{MaxNodes: len(nodes), MaxRuns: totalRuns, MaxAttempts: maximumAttempts, MaxAgentSteps: totalRuns * 32},
		Nodes: nodes, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	if plan.RecommendedStrategy == ExecutionWorkflowMultiParallelReadOnly {
		value.Budget.MaxAgents = len(nodes)
		value.Budget.MaxConcurrency = min(2, len(nodes))
		value.Budget.MaxTotalTokens = 2_000_000
		value.Budget.MaxCost = 50
		value.Budget.MaxDurationSeconds = int64((30 * time.Minute) / time.Second)
	}
	if err := workflow.Validate(value); err != nil {
		return workflow.Workflow{}, fmt.Errorf("compile Coding workflow: %w", err)
	}
	return value, nil
}

func compileWorkflowPolicy(step PlanStep, definition roleprofile.Definition) (workflow.Capability, workflow.FailureAction, int, error) {
	capability := definition.Workflow.Capability
	failure, attempts := step.FailureAction, step.MaxAttempts
	if failure == "" {
		failure = definition.Workflow.DefaultFailure
	}
	if attempts == 0 {
		attempts = definition.Workflow.DefaultAttempts
	}
	if failure == workflow.FailureFallbackMain && attempts < 2 {
		return "", "", 0, fmt.Errorf("compile Coding workflow: Plan step %q fallback_main requires max_attempts of at least 2", step.ID)
	}
	return capability, failure, attempts, nil
}

func workflowLeaves(nodes []workflow.Node) []workflow.NodeID {
	referenced := make(map[workflow.NodeID]bool, len(nodes))
	for _, node := range nodes {
		for _, dependency := range node.DependsOn {
			referenced[dependency] = true
		}
	}
	leaves := make([]workflow.NodeID, 0)
	for _, node := range nodes {
		if !referenced[node.ID] {
			leaves = append(leaves, node.ID)
		}
	}
	sort.Slice(leaves, func(left, right int) bool { return leaves[left] < leaves[right] })
	return leaves
}

func uniqueFinalNodeID(nodes []workflow.Node, name string) workflow.NodeID {
	used := make(map[workflow.NodeID]bool, len(nodes))
	for _, node := range nodes {
		used[node.ID] = true
	}
	base := workflow.NodeID(name)
	if !used[base] {
		return base
	}
	for index := 2; ; index++ {
		candidate := workflow.NodeID(fmt.Sprintf("%s-%d", name, index))
		if !used[candidate] {
			return candidate
		}
	}
}

func workflowStrategyForExecution(value ExecutionStrategy) workflow.Strategy {
	if value == ExecutionWorkflowMultiSerial {
		return workflow.StrategyMultiAgentSerial
	}
	if value == ExecutionWorkflowMultiParallelReadOnly {
		return workflow.StrategyMultiAgentParallelReadOnly
	}
	return workflow.StrategySingleAgent
}

func workflowExecutorForStrategy(value ExecutionStrategy) workflow.Executor {
	if value == ExecutionWorkflowMultiParallelReadOnly {
		return workflow.ExecutorChild
	}
	return workflow.ExecutorMain
}
