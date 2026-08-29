package codingagent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/eaglc/codepilot/internal/agent"
	agentsession "github.com/eaglc/codepilot/internal/agent/session"
)

func TestRecoveredControlHandoffPreservesCancellationDecision(t *testing.T) {
	tests := []struct {
		name  string
		phase TurnPhase
		kind  string
	}{
		{name: "Plan entry", phase: TurnPhaseAwaitingPlanEntryApproval, kind: planEntryApprovalKind},
		{name: "Plan approval", phase: TurnPhaseAwaitingPlanApproval, kind: "plan_approval"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			turn := Turn{Phase: test.phase, Runs: []RunBinding{{RunID: "run-control"}}}
			durable := agentsession.Snapshot{Records: []agentsession.Record{{
				Type: agentsession.RecordInterruptResolved, RunID: "run-control",
				Interrupt: &agentsession.InterruptData{Kind: test.kind, Payload: json.RawMessage(`{"decision":"cancelled"}`)},
			}}}
			status, err := (&Service{}).normalizeRecoveredControlHandoff(context.Background(), turn, agent.RunHandedOff, durable)
			if err != nil || status != agent.RunAborted {
				t.Fatalf("recovered cancellation status = %q, %v", status, err)
			}
		})
	}
}

func TestRecoveredPlanEntryApprovalContinuesOnlyApprovedDecision(t *testing.T) {
	turn := Turn{Phase: TurnPhaseAwaitingPlanEntryApproval, Runs: []RunBinding{{RunID: "run-entry"}}}
	durable := agentsession.Snapshot{Records: []agentsession.Record{{
		Type: agentsession.RecordInterruptResolved, RunID: "run-entry",
		Interrupt: &agentsession.InterruptData{Kind: planEntryApprovalKind, Payload: json.RawMessage(`{"decision":"approved"}`)},
	}}}
	status, err := (&Service{}).normalizeRecoveredControlHandoff(context.Background(), turn, agent.RunHandedOff, durable)
	if err != nil || status != agent.RunHandedOff {
		t.Fatalf("recovered approval status = %q, %v", status, err)
	}
	durable.Records[0].Interrupt.Payload = json.RawMessage(`{"decision":"declined"}`)
	if _, err := (&Service{}).normalizeRecoveredControlHandoff(context.Background(), turn, agent.RunHandedOff, durable); err == nil {
		t.Fatal("recovered declined decision was treated as an approved handoff")
	}
}
