package workflow

import (
	"strings"
	"testing"
	"time"
)

func TestValidateRejectsUntrustedDAGInputs(t *testing.T) {
	base := testWorkflow()
	tests := []struct {
		name   string
		mutate func(*Workflow)
	}{
		{name: "missing dependency", mutate: func(value *Workflow) { value.Nodes[1].DependsOn = []NodeID{"missing"} }},
		{name: "cycle", mutate: func(value *Workflow) { value.Nodes[0].DependsOn = []NodeID{"validate"} }},
		{name: "escaping scope", mutate: func(value *Workflow) { value.Nodes[0].Scope.WritePaths = []string{"../outside"} }},
		{name: "unknown role", mutate: func(value *Workflow) { value.Nodes[0].Role = Role("operator") }},
		{name: "read-only write scope", mutate: func(value *Workflow) { value.Nodes[1].Scope.WritePaths = []string{"internal"} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := Clone(base)
			test.mutate(&value)
			if err := Validate(value); err == nil {
				t.Fatal("Validate accepted an unsafe Workflow")
			}
		})
	}
}

func TestSerialSchedulerRunsThreeDependencyOrderedNodes(t *testing.T) {
	value := testWorkflow()
	now := value.CreatedAt
	assertAction(t, value, ActionStartWorkflow, "")
	value = apply(t, value, Event{ID: "start", Type: EventWorkflowStarted, OccurredAt: now.Add(time.Second)})
	for index, expected := range []NodeID{"implement", "validate", "review"} {
		action, err := NextAction(value)
		if err != nil || action.Kind != ActionStartNode || action.NodeID != expected {
			t.Fatalf("NextAction node %d = %#v, %v", index, action, err)
		}
		value = apply(t, value, Event{ID: "node-start-" + string(expected), Type: EventNodeStarted, NodeID: expected, OccurredAt: now.Add(time.Duration(index*2+2) * time.Second)})
		assertAction(t, value, ActionWait, expected)
		value = apply(t, value, Event{ID: "node-complete-" + string(expected), Type: EventNodeCompleted, NodeID: expected, ResultRef: "result:" + string(expected), OccurredAt: now.Add(time.Duration(index*2+3) * time.Second)})
	}
	assertAction(t, value, ActionComplete, "")
	value = apply(t, value, Event{ID: "complete", Type: EventWorkflowCompleted, OccurredAt: now.Add(9 * time.Second)})
	if value.Status != StatusCompleted || value.Revision != 9 {
		t.Fatalf("completed Workflow = %#v", value)
	}
}

func TestSerialSchedulerRetriesThenFailsAtBound(t *testing.T) {
	value := testWorkflow()
	value.Nodes = value.Nodes[:1]
	value.Nodes[0].MaxAttempts = 2
	value.Budget.MaxNodes = 1
	value.Budget.MaxRuns = 2
	now := value.CreatedAt
	value = apply(t, value, Event{ID: "start", Type: EventWorkflowStarted, OccurredAt: now.Add(time.Second)})
	value = apply(t, value, Event{ID: "attempt-1", Type: EventNodeStarted, NodeID: "implement", OccurredAt: now.Add(2 * time.Second)})
	value = apply(t, value, Event{ID: "failure-1", Type: EventNodeFailed, NodeID: "implement", Summary: "first failure", OccurredAt: now.Add(3 * time.Second)})
	assertAction(t, value, ActionRetryNode, "implement")
	value = apply(t, value, Event{ID: "retry", Type: EventNodeRetryScheduled, NodeID: "implement", OccurredAt: now.Add(4 * time.Second)})
	value = apply(t, value, Event{ID: "attempt-2", Type: EventNodeStarted, NodeID: "implement", OccurredAt: now.Add(5 * time.Second)})
	value = apply(t, value, Event{ID: "failure-2", Type: EventNodeFailed, NodeID: "implement", Summary: "second failure", OccurredAt: now.Add(6 * time.Second)})
	assertAction(t, value, ActionFailWorkflow, "implement")
}

func TestCancellationMarksActiveAndUnstartedNodesCancelled(t *testing.T) {
	value := testWorkflow()
	now := value.CreatedAt
	value = apply(t, value, Event{ID: "start", Type: EventWorkflowStarted, OccurredAt: now.Add(time.Second)})
	value = apply(t, value, Event{ID: "node-start", Type: EventNodeStarted, NodeID: "implement", OccurredAt: now.Add(2 * time.Second)})
	value = apply(t, value, Event{ID: "cancel", Type: EventWorkflowCancelled, Summary: "user cancelled", OccurredAt: now.Add(3 * time.Second)})
	if value.Status != StatusCancelled {
		t.Fatalf("Status = %q", value.Status)
	}
	for _, node := range value.Nodes {
		if node.Status != NodeCancelled {
			t.Fatalf("node %q status = %q", node.ID, node.Status)
		}
	}
}

func TestParentStepBudgetStopsLaterNodes(t *testing.T) {
	value := testWorkflow()
	value.Budget.MaxAgentSteps = 2
	now := value.CreatedAt
	value = apply(t, value, Event{ID: "start-budgeted", Type: EventWorkflowStarted, OccurredAt: now.Add(time.Second)})
	value = apply(t, value, Event{ID: "start-implement-budgeted", Type: EventNodeStarted, NodeID: "implement", OccurredAt: now.Add(2 * time.Second)})
	value = apply(t, value, Event{ID: "complete-implement-budgeted", Type: EventNodeCompleted, NodeID: "implement", ResultRef: "run:implement", AgentSteps: 2, OccurredAt: now.Add(3 * time.Second)})
	if value.Budget.UsedAgentSteps != 2 {
		t.Fatalf("UsedAgentSteps = %d", value.Budget.UsedAgentSteps)
	}
	action, err := NextAction(value)
	if err != nil || action.Kind != ActionFailWorkflow || action.Reason != "workflow exhausted its Agent step budget" {
		t.Fatalf("budget action = %#v, %v", action, err)
	}
	if _, err := ApplyEvent(value, Event{ID: "over-budget", Type: EventNodeStarted, NodeID: "validate", OccurredAt: now.Add(4 * time.Second)}); err == nil {
		t.Fatal("exhausted parent budget started another node")
	}
}

func TestTerminalRunCannotOverconsumeParentStepBudget(t *testing.T) {
	value := testWorkflow()
	value.Budget.MaxAgentSteps = 1
	now := value.CreatedAt
	value = apply(t, value, Event{ID: "start-over-budget", Type: EventWorkflowStarted, OccurredAt: now.Add(time.Second)})
	value = apply(t, value, Event{ID: "node-over-budget", Type: EventNodeStarted, NodeID: "implement", OccurredAt: now.Add(2 * time.Second)})
	if _, err := ApplyEvent(value, Event{ID: "complete-over-budget", Type: EventNodeCompleted, NodeID: "implement", ResultRef: "run:implement", AgentSteps: 2, OccurredAt: now.Add(3 * time.Second)}); err == nil {
		t.Fatal("terminal Run exceeded its parent Workflow step budget")
	}
}

func TestParallelReadOnlySchedulerReturnsIndependentRunnableSet(t *testing.T) {
	value := parallelReadOnlyWorkflow()
	now := value.CreatedAt
	value = apply(t, value, Event{ID: "parallel-start", Type: EventWorkflowStarted, OccurredAt: now.Add(time.Second)})
	runnable, err := RunnableNodes(value)
	if err != nil || len(runnable) != 2 || runnable[0] != "explore-api" || runnable[1] != "explore-ui" {
		t.Fatalf("RunnableNodes = %#v, %v", runnable, err)
	}
	value = apply(t, value, Event{ID: "start-api", Type: EventNodeStarted, NodeID: "explore-api", OccurredAt: now.Add(2 * time.Second)})
	value = apply(t, value, Event{ID: "start-ui", Type: EventNodeStarted, NodeID: "explore-ui", OccurredAt: now.Add(3 * time.Second)})
	if runnable, err = RunnableNodes(value); err != nil || len(runnable) != 0 {
		t.Fatalf("RunnableNodes at capacity = %#v, %v", runnable, err)
	}
	value = apply(t, value, Event{ID: "complete-ui", Type: EventNodeCompleted, NodeID: "explore-ui", ResultRef: "child:ui", OccurredAt: now.Add(4 * time.Second)})
	if runnable, err = RunnableNodes(value); err != nil || len(runnable) != 0 {
		t.Fatalf("dependent node ran before every dependency: %#v, %v", runnable, err)
	}
	value = apply(t, value, Event{ID: "complete-api", Type: EventNodeCompleted, NodeID: "explore-api", ResultRef: "child:api", OccurredAt: now.Add(5 * time.Second)})
	if runnable, err = RunnableNodes(value); err != nil || len(runnable) != 1 || runnable[0] != "review" {
		t.Fatalf("dependent runnable set = %#v, %v", runnable, err)
	}
}

func TestParallelReadOnlyWorkflowRejectsWritesAndQuotaOverflow(t *testing.T) {
	base := parallelReadOnlyWorkflow()
	tests := []struct {
		name   string
		mutate func(*Workflow)
	}{
		{name: "write capability", mutate: func(value *Workflow) {
			value.Nodes[0].Role, value.Nodes[0].Capability = RoleImplement, CapabilityImplement
			value.Nodes[0].Scope.WritePaths = []string{"internal"}
		}},
		{name: "main executor", mutate: func(value *Workflow) { value.Nodes[0].Executor = ExecutorMain }},
		{name: "one concurrency slot", mutate: func(value *Workflow) { value.Budget.MaxConcurrency = 1 }},
		{name: "too few Agents", mutate: func(value *Workflow) { value.Budget.MaxAgents = 2 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := Clone(base)
			test.mutate(&value)
			if err := Validate(value); err == nil {
				t.Fatal("Validate accepted unsafe parallel Workflow")
			}
		})
	}
}

func TestParallelReadOnlyFailureIsolatedUntilDependencyPropagation(t *testing.T) {
	value := parallelReadOnlyWorkflow()
	now := value.CreatedAt
	value = apply(t, value, Event{ID: "start-isolation", Type: EventWorkflowStarted, OccurredAt: now.Add(time.Second)})
	value = apply(t, value, Event{ID: "start-api-isolation", Type: EventNodeStarted, NodeID: "explore-api", OccurredAt: now.Add(2 * time.Second)})
	value = apply(t, value, Event{ID: "start-ui-isolation", Type: EventNodeStarted, NodeID: "explore-ui", OccurredAt: now.Add(3 * time.Second)})
	value = apply(t, value, Event{ID: "fail-api-isolation", Type: EventNodeFailed, NodeID: "explore-api", Summary: "API inspection failed", AgentSteps: 2, AgentTokens: 200, AgentCost: 0.2, OccurredAt: now.Add(4 * time.Second)})
	value = apply(t, value, Event{ID: "complete-ui-isolation", Type: EventNodeCompleted, NodeID: "explore-ui", ResultRef: "child:ui", AgentSteps: 3, AgentTokens: 300, AgentCost: 0.3, OccurredAt: now.Add(5 * time.Second)})
	if value.Nodes[1].Status != NodeCompleted || value.Nodes[1].ResultRef != "child:ui" {
		t.Fatalf("unrelated node result was lost after peer failure: %#v", value.Nodes[1])
	}
	if value.Budget.UsedAgentSteps != 5 || value.Budget.UsedTotalTokens != 500 || value.Budget.UsedCost != 0.5 {
		t.Fatalf("parallel usage accounting = %#v", value.Budget)
	}
	assertAction(t, value, ActionBlockNode, "explore-api")
	value = apply(t, value, Event{ID: "block-api-isolation", Type: EventNodeBlocked, NodeID: "explore-api", Summary: "API inspection failed", OccurredAt: now.Add(6 * time.Second)})
	assertAction(t, value, ActionBlockNode, "review")
	value = apply(t, value, Event{ID: "block-review-isolation", Type: EventNodeBlocked, NodeID: "review", Summary: "dependency explore-api did not complete", OccurredAt: now.Add(7 * time.Second)})
	assertAction(t, value, ActionBlockWorkflow, "")
}

func TestParallelIsolatedWriteSchedulerUsesConflictMatrix(t *testing.T) {
	value := parallelIsolatedWriteWorkflow()
	now := value.CreatedAt
	value = apply(t, value, Event{ID: "p7-start", Type: EventWorkflowStarted, OccurredAt: now.Add(time.Second)})
	runnable, err := RunnableNodes(value)
	if err != nil || len(runnable) != 2 || runnable[0] != "implement-api" || runnable[1] != "implement-ui" {
		t.Fatalf("independent isolated writes = %#v, %v", runnable, err)
	}
	value.Nodes[1].Scope.WritePaths = []string{"internal/api/client"}
	runnable, err = RunnableNodes(value)
	if err != nil || len(runnable) != 1 || runnable[0] != "implement-api" {
		t.Fatalf("overlapping isolated writes were parallel: %#v, %v", runnable, err)
	}
	value.Nodes[0].Scope.Unknown = true
	runnable, err = RunnableNodes(value)
	if err != nil || len(runnable) != 1 || runnable[0] != "implement-api" {
		t.Fatalf("unknown write scope was not serialized: %#v, %v", runnable, err)
	}
}

func TestParallelIsolatedWriteIntegrateNodeRunsAlone(t *testing.T) {
	value := parallelIsolatedWriteWorkflow()
	now := value.CreatedAt
	value = apply(t, value, Event{ID: "p7-integrate-start", Type: EventWorkflowStarted, OccurredAt: now.Add(time.Second)})
	value = apply(t, value, Event{ID: "p7-api-start", Type: EventNodeStarted, NodeID: "implement-api", OccurredAt: now.Add(2 * time.Second)})
	value = apply(t, value, Event{ID: "p7-ui-start", Type: EventNodeStarted, NodeID: "implement-ui", OccurredAt: now.Add(3 * time.Second)})
	value = apply(t, value, Event{ID: "p7-api-done", Type: EventNodeCompleted, NodeID: "implement-api", ResultRef: "child:api", OccurredAt: now.Add(4 * time.Second)})
	value = apply(t, value, Event{ID: "p7-ui-done", Type: EventNodeCompleted, NodeID: "implement-ui", ResultRef: "child:ui", OccurredAt: now.Add(5 * time.Second)})
	runnable, err := RunnableNodes(value)
	if err != nil || len(runnable) != 1 || runnable[0] != "integrate-api" {
		t.Fatalf("first deterministic Integrate node = %#v, %v", runnable, err)
	}
}

func testWorkflow() Workflow {
	now := time.Now().UTC().Truncate(time.Millisecond)
	return Workflow{
		ID: "workflow-1", OwnerID: "turn-1", Plan: PlanReference{ID: "plan-1", Version: 3, Digest: strings.Repeat("a", 64)},
		Strategy: StrategySingleAgent, Status: StatusPending, Budget: Budget{MaxNodes: 3, MaxRuns: 6, MaxAttempts: 2, MaxAgentSteps: 120},
		Nodes: []Node{
			{ID: "implement", Goal: "Implement the bounded change.", Role: RoleImplement, Capability: CapabilityImplement, Scope: Scope{ReadPaths: []string{"internal"}, WritePaths: []string{"internal"}}, AcceptanceCriteria: []string{"Implementation is complete."}, FailureAction: FailureRetry, MaxAttempts: 2, Status: NodePending},
			{ID: "validate", Goal: "Validate the implementation.", DependsOn: []NodeID{"implement"}, Role: RoleValidate, Capability: CapabilityValidate, Scope: Scope{ReadPaths: []string{"internal"}}, AcceptanceCriteria: []string{"Tests pass."}, FailureAction: FailureBlock, MaxAttempts: 1, Status: NodePending},
			{ID: "review", Goal: "Review the final result.", DependsOn: []NodeID{"validate"}, Role: RoleReview, Capability: CapabilityReview, Scope: Scope{ReadPaths: []string{"internal"}}, AcceptanceCriteria: []string{"Approved scope is satisfied."}, FailureAction: FailureReplan, MaxAttempts: 1, Status: NodePending},
		},
		Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
}

func parallelReadOnlyWorkflow() Workflow {
	now := time.Now().UTC().Truncate(time.Millisecond)
	return Workflow{
		ID: "parallel-workflow", OwnerID: "turn-1", Plan: PlanReference{ID: "plan-1", Version: 3, Digest: strings.Repeat("a", 64)},
		Strategy: StrategyMultiAgentParallelReadOnly, Status: StatusPending,
		Budget: Budget{MaxNodes: 3, MaxRuns: 3, MaxAttempts: 1, MaxAgentSteps: 96, MaxAgents: 3, MaxConcurrency: 2, MaxTotalTokens: 3000, MaxCost: 3, MaxDurationSeconds: 60},
		Nodes: []Node{
			{ID: "explore-api", Goal: "Explore API.", Role: RoleExplore, Capability: CapabilityExplore, Executor: ExecutorChild, Delegated: true, Scope: Scope{ReadPaths: []string{"internal/api"}}, AcceptanceCriteria: []string{"API evidence collected."}, FailureAction: FailureBlock, MaxAttempts: 1, Status: NodePending},
			{ID: "explore-ui", Goal: "Explore UI.", Role: RoleExplore, Capability: CapabilityExplore, Executor: ExecutorChild, Delegated: true, Scope: Scope{ReadPaths: []string{"internal/ui"}}, AcceptanceCriteria: []string{"UI evidence collected."}, FailureAction: FailureBlock, MaxAttempts: 1, Status: NodePending},
			{ID: "review", Goal: "Review evidence.", DependsOn: []NodeID{"explore-api", "explore-ui"}, Role: RoleReview, Capability: CapabilityReview, Executor: ExecutorChild, Delegated: true, Scope: Scope{ReadPaths: []string{"internal"}}, AcceptanceCriteria: []string{"Evidence reviewed."}, FailureAction: FailureBlock, MaxAttempts: 1, Status: NodePending},
		},
		Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
}

func parallelIsolatedWriteWorkflow() Workflow {
	now := time.Now().UTC().Truncate(time.Millisecond)
	return Workflow{
		ID: "isolated-write-workflow", OwnerID: "turn-1", Plan: PlanReference{ID: "plan-1", Version: 1, Digest: strings.Repeat("a", 64)},
		Strategy: StrategyMultiAgentParallelIsolatedWrite, Status: StatusPending,
		Budget: Budget{MaxNodes: 4, MaxRuns: 4, MaxAttempts: 1, MaxAgentSteps: 128, MaxAgents: 4, MaxConcurrency: 2, MaxTotalTokens: 4000, MaxCost: 4, MaxDurationSeconds: 60},
		Nodes: []Node{
			{ID: "implement-api", Goal: "Implement API.", Role: RoleImplement, Capability: CapabilityImplement, Executor: ExecutorChild, Delegated: true, Isolated: true, Scope: Scope{ReadPaths: []string{"internal/api"}, WritePaths: []string{"internal/api"}}, AcceptanceCriteria: []string{"API complete."}, FailureAction: FailureBlock, MaxAttempts: 1, Status: NodePending},
			{ID: "implement-ui", Goal: "Implement UI.", Role: RoleImplement, Capability: CapabilityImplement, Executor: ExecutorChild, Delegated: true, Isolated: true, Scope: Scope{ReadPaths: []string{"internal/ui"}, WritePaths: []string{"internal/ui"}}, AcceptanceCriteria: []string{"UI complete."}, FailureAction: FailureBlock, MaxAttempts: 1, Status: NodePending},
			{ID: "integrate-api", Goal: "Integrate API.", DependsOn: []NodeID{"implement-api"}, Role: RoleIntegrate, Capability: CapabilityIntegrate, Executor: ExecutorMain, IntegrationSources: []NodeID{"implement-api"}, Scope: Scope{ReadPaths: []string{"internal/api"}, WritePaths: []string{"internal/api"}}, AcceptanceCriteria: []string{"API integrated."}, FailureAction: FailureReplan, MaxAttempts: 1, Status: NodePending},
			{ID: "integrate-ui", Goal: "Integrate UI.", DependsOn: []NodeID{"implement-ui"}, Role: RoleIntegrate, Capability: CapabilityIntegrate, Executor: ExecutorMain, IntegrationSources: []NodeID{"implement-ui"}, Scope: Scope{ReadPaths: []string{"internal/ui"}, WritePaths: []string{"internal/ui"}}, AcceptanceCriteria: []string{"UI integrated."}, FailureAction: FailureReplan, MaxAttempts: 1, Status: NodePending},
		}, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
}

func apply(t *testing.T, value Workflow, event Event) Workflow {
	t.Helper()
	next, err := ApplyEvent(value, event)
	if err != nil {
		t.Fatalf("ApplyEvent(%s): %v", event.Type, err)
	}
	return next
}

func assertAction(t *testing.T, value Workflow, kind ActionKind, node NodeID) {
	t.Helper()
	action, err := NextAction(value)
	if err != nil || action.Kind != kind || action.NodeID != node {
		t.Fatalf("NextAction = %#v, %v; want %s/%s", action, err, kind, node)
	}
}
