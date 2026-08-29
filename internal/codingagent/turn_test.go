package codingagent

import (
	"strings"
	"testing"
	"time"

	agentsession "github.com/eaglc/codepilot/internal/agent/session"
)

func validTestTurn() Turn {
	now := time.Unix(100, 0).UTC()
	return Turn{
		ID: "turn-1", SessionID: "session-1", RequestText: "make the change",
		Phase: TurnPhaseDirect, Status: TurnPending, Strategy: ExecutionSingle, Revision: 1,
		Runs:      []RunBinding{{RunID: "run-1", UserEntryID: "entry-1", Phase: TurnPhaseDirect, Profile: CapabilityDirect, Status: RunBindingPending}},
		CreatedAt: now, UpdatedAt: now,
	}
}

func TestValidateTurnRejectsDuplicateRunAndEntryBindings(t *testing.T) {
	turn := validTestTurn()
	turn.Runs = append(turn.Runs, turn.Runs[0])
	if err := ValidateTurn(turn); err == nil || !strings.Contains(err.Error(), "duplicated") {
		t.Fatalf("duplicate run validation error = %v", err)
	}
	turn = validTestTurn()
	turn.Runs[0].RunID = "run-2"
	turn.Runs = append(turn.Runs, RunBinding{RunID: "run-1", UserEntryID: "entry-1", Phase: TurnPhaseDirect, Profile: CapabilityDirect, Status: RunBindingPending})
	if err := ValidateTurn(turn); err == nil || !strings.Contains(err.Error(), "user entry") {
		t.Fatalf("duplicate entry validation error = %v", err)
	}
}

func TestTurnActiveRunOnlyReturnsNonTerminalBinding(t *testing.T) {
	turn := validTestTurn()
	turn.Runs[0].Status = RunBindingCompleted
	turn.Runs[0].StartedAt = turn.CreatedAt
	turn.Runs[0].FinishedAt = turn.CreatedAt.Add(time.Second)
	turn.Runs = append(turn.Runs, RunBinding{RunID: agentsession.RunID("run-2"), Phase: TurnPhaseDirect, Profile: CapabilityDirect, Status: RunBindingRunning, StartedAt: turn.CreatedAt})
	active, ok := turn.ActiveRun()
	if !ok || active.RunID != "run-2" {
		t.Fatalf("active run = %#v, %v", active, ok)
	}
}

func TestValidateTurnRejectsInconsistentLifecycle(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Turn)
	}{
		{name: "missing run", mutate: func(turn *Turn) { turn.Runs = nil }},
		{name: "terminal mismatch", mutate: func(turn *Turn) {
			turn.Status = TurnCompleted
			turn.CompletedAt = turn.CreatedAt.Add(time.Second)
		}},
		{name: "second user entry", mutate: func(turn *Turn) {
			turn.Runs[0].Status = RunBindingHandedOff
			turn.Runs[0].StartedAt = turn.CreatedAt
			turn.Runs[0].FinishedAt = turn.CreatedAt.Add(time.Second)
			turn.Status = TurnRunning
			turn.Runs = append(turn.Runs, RunBinding{RunID: "run-2", UserEntryID: "entry-2", Phase: TurnPhaseDirect, Profile: CapabilityDirect, Status: RunBindingPending})
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			turn := validTestTurn()
			test.mutate(&turn)
			if err := ValidateTurn(turn); err == nil {
				t.Fatalf("invalid lifecycle was accepted: %#v", turn)
			}
		})
	}
}

func TestValidateTurnTransitionAllowsHandoffContinuationAndRejectsRollback(t *testing.T) {
	previous := validTestTurn()
	previous.Status = TurnRunning
	previous.Runs[0].Status = RunBindingHandedOff
	previous.Runs[0].StartedAt = previous.CreatedAt
	previous.Runs[0].FinishedAt = previous.CreatedAt.Add(time.Second)
	previous.UpdatedAt = previous.Runs[0].FinishedAt
	previous.Revision = 3
	next := previous
	next.Runs = append([]RunBinding(nil), previous.Runs...)
	next.Runs = append(next.Runs, RunBinding{RunID: "run-2", Phase: TurnPhaseDirect, Profile: CapabilityDirect, Status: RunBindingPending})
	next.UpdatedAt = previous.UpdatedAt.Add(time.Second)
	next.Revision++
	if err := ValidateTurnTransition(previous, next); err != nil {
		t.Fatalf("handoff continuation rejected: %v", err)
	}

	completed := next
	completed.Runs = append([]RunBinding(nil), next.Runs...)
	completed.Runs[1].Status = RunBindingCompleted
	completed.Runs[1].StartedAt = next.UpdatedAt
	completed.Runs[1].FinishedAt = next.UpdatedAt.Add(time.Second)
	completed.Status = TurnCompleted
	completed.UpdatedAt = completed.Runs[1].FinishedAt
	completed.CompletedAt = completed.UpdatedAt
	completed.Revision++
	if err := ValidateTurnTransition(next, completed); err == nil {
		t.Fatal("pending Run skipped directly to completed")
	}

	rollback := completed
	rollback.Runs = append([]RunBinding(nil), completed.Runs...)
	rollback.Runs[1].Status = RunBindingRunning
	rollback.Runs[1].FinishedAt = time.Time{}
	rollback.Status = TurnRunning
	rollback.CompletedAt = time.Time{}
	rollback.UpdatedAt = completed.UpdatedAt.Add(time.Second)
	rollback.Revision++
	if err := ValidateTurnTransition(completed, rollback); err == nil {
		t.Fatal("terminal Turn rollback was accepted")
	}
}

func TestValidateTurnTransitionKeepsPlanEntryConsentSeparateFromPlanning(t *testing.T) {
	turn := validTestTurn()
	turn.Status = TurnRunning
	turn.Runs[0].Status = RunBindingRunning
	turn.Runs[0].StartedAt = turn.CreatedAt
	suggestion := PlanEntrySuggestion{
		ReasonCode:  PlanEntryCrossModuleChange,
		Summary:     "The change crosses the coordinator and UI boundaries.",
		SuggestedAt: turn.CreatedAt.Add(time.Second),
	}
	suggestion.Digest = computePlanEntryDigest(suggestion.ReasonCode, suggestion.Summary)
	waiting := turn
	waiting.Phase = TurnPhaseAwaitingPlanEntryApproval
	waiting.PlanEntrySuggestion = &suggestion
	waiting.UpdatedAt = suggestion.SuggestedAt
	waiting.Revision++
	if err := ValidateTurnTransition(turn, waiting); err != nil {
		t.Fatalf("Direct-to-entry-approval transition rejected: %v", err)
	}

	declined := waiting
	declined.Phase = TurnPhaseDirect
	declined.DeclinedPlanReasons = []PlanEntryReasonCode{suggestion.ReasonCode}
	declined.UpdatedAt = waiting.UpdatedAt.Add(time.Second)
	declined.Revision++
	if err := ValidateTurnTransition(waiting, declined); err != nil {
		t.Fatalf("declined Plan entry transition rejected: %v", err)
	}

	handedOff := waiting
	handedOff.Runs = append([]RunBinding(nil), waiting.Runs...)
	handedOff.Runs[0].Status = RunBindingHandedOff
	handedOff.Runs[0].FinishedAt = waiting.UpdatedAt.Add(time.Second)
	handedOff.UpdatedAt = handedOff.Runs[0].FinishedAt
	handedOff.Revision++
	if err := ValidateTurnTransition(waiting, handedOff); err != nil {
		t.Fatalf("approved Plan entry handoff rejected: %v", err)
	}
	planning := handedOff
	planning.Phase = TurnPhasePlanning
	planning.Runs = append([]RunBinding(nil), handedOff.Runs...)
	planning.Runs = append(planning.Runs, RunBinding{RunID: "run-plan", Phase: TurnPhasePlanning, Profile: CapabilityPlan, Status: RunBindingPending})
	planning.UpdatedAt = handedOff.UpdatedAt.Add(time.Second)
	planning.Revision++
	if err := ValidateTurnTransition(handedOff, planning); err != nil {
		t.Fatalf("approved entry-to-Planning transition rejected: %v", err)
	}
}
