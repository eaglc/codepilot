package file

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eaglc/codepilot/internal/workflow"
)

func TestWorkflowJournalRepairsInterruptedTailAndPreservesCompletedNodes(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	repository, err := NewRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	value := workflow.Workflow{
		ID: "workflow-restart", OwnerID: "turn-restart", Plan: workflow.PlanReference{ID: "plan-restart", Version: 1, Digest: strings.Repeat("a", 64)},
		Strategy: workflow.StrategySingleAgent, Status: workflow.StatusPending, Budget: workflow.Budget{MaxNodes: 2, MaxRuns: 2, MaxAttempts: 1, MaxAgentSteps: 64},
		Nodes: []workflow.Node{
			{ID: "first", Goal: "Complete the first node.", Role: workflow.RoleImplement, Capability: workflow.CapabilityImplement, AcceptanceCriteria: []string{"First completes."}, FailureAction: workflow.FailureTerminate, MaxAttempts: 1, Status: workflow.NodePending},
			{ID: "second", Goal: "Complete the second node.", DependsOn: []workflow.NodeID{"first"}, Role: workflow.RoleValidate, Capability: workflow.CapabilityValidate, AcceptanceCriteria: []string{"Second completes."}, FailureAction: workflow.FailureBlock, MaxAttempts: 1, Status: workflow.NodePending},
		}, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := repository.CreateWorkflow(context.Background(), value); err != nil {
		t.Fatal(err)
	}
	for _, event := range []workflow.Event{
		{ID: "start", Type: workflow.EventWorkflowStarted, OccurredAt: now.Add(time.Second)},
		{ID: "first-start", Type: workflow.EventNodeStarted, NodeID: "first", OccurredAt: now.Add(2 * time.Second)},
		{ID: "first-complete", Type: workflow.EventNodeCompleted, NodeID: "first", ResultRef: "run:first", OccurredAt: now.Add(3 * time.Second)},
	} {
		value, err = repository.AppendWorkflowEvent(context.Background(), value.ID, value.Revision, event)
		if err != nil {
			t.Fatal(err)
		}
	}
	journal := filepath.Join(root, "coding-workflows", string(value.ID), "events.jsonl")
	file, err := os.OpenFile(journal, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(`{"version":1,"expected_revision":4,"event":`); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := reopened.LoadWorkflow(context.Background(), value.ID)
	if err != nil || loaded.Revision != value.Revision || loaded.Nodes[0].Status != workflow.NodeCompleted || loaded.Nodes[1].Status != workflow.NodePending {
		t.Fatalf("LoadWorkflow after partial tail = %#v, %v", loaded, err)
	}
	loaded, err = reopened.AppendWorkflowEvent(context.Background(), loaded.ID, loaded.Revision, workflow.Event{ID: "second-start", Type: workflow.EventNodeStarted, NodeID: "second", OccurredAt: now.Add(4 * time.Second)})
	if err != nil || loaded.Nodes[0].Status != workflow.NodeCompleted || loaded.Nodes[1].Status != workflow.NodeRunning {
		t.Fatalf("Append after repair = %#v, %v", loaded, err)
	}
}
