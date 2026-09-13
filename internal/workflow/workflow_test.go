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
