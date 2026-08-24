package session

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/eaglc/codepilot/internal/llm"
)

func TestMemoryRepositorySharesSequenceAndAssignsParents(t *testing.T) {
	repository := NewMemoryRepository()
	ctx := context.Background()
	if err := repository.Create(ctx, Metadata{ID: "session-1"}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	first, err := repository.AppendEntry(ctx, "session-1", MainLane, Entry{ID: "entry-1", Type: EntryMessage, Message: &llm.Message{
		Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentText, Text: "hello"}},
	}})
	if err != nil {
		t.Fatalf("append first entry: %v", err)
	}
	record, err := repository.AppendRecord(ctx, "session-1", MainLane, Record{ID: "record-1", Type: RecordOperationStarted, RunID: "run-1", Operation: &OperationData{Intent: OperationRun}})
	if err != nil {
		t.Fatalf("append record: %v", err)
	}
	second, err := repository.AppendEntry(ctx, "session-1", MainLane, Entry{ID: "entry-2", Type: EntryMessage, Message: &llm.Message{
		Role: llm.RoleAssistant, Content: []llm.Content{{Type: llm.ContentText, Text: "done"}},
	}})
	if err != nil {
		t.Fatalf("append second entry: %v", err)
	}
	if first.Sequence != 1 || record.Sequence != 2 || second.Sequence != 3 {
		t.Fatalf("sequences = %d, %d, %d", first.Sequence, record.Sequence, second.Sequence)
	}
	if second.ParentID != first.ID {
		t.Fatalf("second parent = %q, want %q", second.ParentID, first.ID)
	}
}

func TestAnalyzeRecoveryFindsUnfinishedTool(t *testing.T) {
	snapshot := Snapshot{Records: []Record{
		{Type: RecordOperationStarted, RunID: "run-1"},
		{Type: RecordToolStarted, RunID: "run-1", Tool: &ToolData{ToolCallID: "call-1", ToolName: "write", ResultEntryID: "result-1", ReplayPolicy: "never", EffectiveArgs: json.RawMessage(`{}`)}},
	}}
	state := AnalyzeRecovery(snapshot)
	if len(state.PendingRuns) != 1 || len(state.PendingTools) != 1 {
		t.Fatalf("recovery state = %#v", state)
	}
	if state.PendingTools[0].ToolCallID != "call-1" || state.PendingTools[0].ReplayPolicy != "never" {
		t.Fatalf("pending tool = %#v", state.PendingTools[0])
	}
}

func TestBuildRecoveryPlanAppliesReplayPolicyAndPreservesPrivateRecoveryMaterial(t *testing.T) {
	tests := []struct {
		name      string
		policy    string
		key       string
		result    bool
		wantKind  RecoveryActionKind
		automatic bool
	}{
		{name: "safe", policy: "safe", wantKind: RecoveryRetryTool, automatic: true},
		{name: "idempotent with original key", policy: "idempotent", key: "original-key", wantKind: RecoveryRetryTool, automatic: true},
		{name: "idempotent without original key", policy: "idempotent", wantKind: RecoveryDecideTool},
		{name: "never", policy: "never", wantKind: RecoveryDecideTool},
		{name: "durable result wins", policy: "never", result: true, wantKind: RecoveryReconcileTool, automatic: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := Snapshot{Records: []Record{
				{Type: RecordOperationStarted, RunID: "run-1", Lane: MainLane},
				{Type: RecordToolStarted, RunID: "run-1", Lane: MainLane, Sequence: 2, Tool: &ToolData{
					AssistantEntryID: "assistant-1", ToolCallID: "call-1", ToolName: "workspace_tool",
					EffectiveArgs: json.RawMessage(`{"path":"main.go"}`), IdempotencyKey: test.key,
					ResultEntryID: "result-1", ReplayPolicy: test.policy,
				}},
			}}
			if test.result {
				snapshot.Entries = append(snapshot.Entries, Entry{ID: "result-1", RunID: "run-1", Type: EntryMessage, Message: &llm.Message{
					Role: llm.RoleTool, ToolCallID: "call-1", ToolName: "workspace_tool", Content: []llm.Content{{Type: llm.ContentText, Text: "done"}},
				}})
			}
			plan := BuildRecoveryPlan(snapshot)
			if len(plan.Actions) != 1 || plan.Actions[0].Kind != test.wantKind || plan.Actions[0].Automatic != test.automatic {
				t.Fatalf("plan = %#v", plan)
			}
			if plan.Actions[0].Tool == nil || string(plan.Actions[0].Tool.EffectiveArgs) != `{"path":"main.go"}` || plan.Actions[0].Tool.IdempotencyKey != test.key {
				t.Fatalf("private Tool recovery material = %#v", plan.Actions[0].Tool)
			}
		})
	}
}

func TestBuildRecoveryPlanPrioritizesExternalInterruptAndIgnoresTerminalRun(t *testing.T) {
	snapshot := Snapshot{Records: []Record{
		{Type: RecordOperationStarted, RunID: "run-pending", Lane: MainLane},
		{Type: RecordToolStarted, RunID: "run-pending", Lane: MainLane, Tool: &ToolData{
			AssistantEntryID: "assistant", ToolCallID: "call", ToolName: "apply_patch", EffectiveArgs: json.RawMessage(`{}`), ResultEntryID: "result", ReplayPolicy: "never",
		}},
		{Type: RecordInterruptRequested, RunID: "run-pending", Lane: MainLane, Interrupt: &InterruptData{InterruptID: "approval", Kind: "approval", ToolCallID: "call"}},
		{Type: RecordOperationStarted, RunID: "run-terminal", Lane: MainLane},
		{Type: RecordToolStarted, RunID: "run-terminal", Lane: MainLane, Tool: &ToolData{
			AssistantEntryID: "assistant-old", ToolCallID: "call-old", ToolName: "old", EffectiveArgs: json.RawMessage(`{}`), ResultEntryID: "result-old", ReplayPolicy: "never",
		}},
		{Type: RecordOperationFinished, RunID: "run-terminal", Lane: MainLane, Operation: &OperationData{Outcome: "failed"}},
	}}
	plan := BuildRecoveryPlan(snapshot)
	if len(plan.Actions) != 1 || plan.Actions[0].Kind != RecoveryResolveInterrupt || plan.Actions[0].Interrupt == nil || plan.Actions[0].Interrupt.InterruptID != "approval" {
		t.Fatalf("plan = %#v", plan)
	}
	state := AnalyzeRecovery(snapshot)
	if len(state.PendingTools) != 1 || state.PendingTools[0].RunID != "run-pending" {
		t.Fatalf("terminal Tool leaked into recovery state: %#v", state)
	}
}

func TestBuildRecoveryPlanRequiresDecisionWhenOperationStartedBeforeUserEntry(t *testing.T) {
	withoutContext := BuildRecoveryPlan(Snapshot{Records: []Record{{Type: RecordOperationStarted, RunID: "run-empty", Lane: MainLane}}})
	if len(withoutContext.Actions) != 1 || withoutContext.Actions[0].Kind != RecoveryDecideRun {
		t.Fatalf("empty run plan = %#v", withoutContext)
	}
	withContext := BuildRecoveryPlan(Snapshot{
		Entries: []Entry{{ID: "user", RunID: "run-context", Type: EntryMessage, Message: &llm.Message{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentText, Text: "continue"}}}}},
		Records: []Record{{Type: RecordOperationStarted, RunID: "run-context", Lane: MainLane}},
	})
	if len(withContext.Actions) != 1 || withContext.Actions[0].Kind != RecoveryContinueRun || withContext.Actions[0].Automatic {
		t.Fatalf("context run plan = %#v", withContext)
	}
}

func TestRecoveryPlanClassifiesEveryDurableTurnWriteGap(t *testing.T) {
	user := Entry{ID: "user", Sequence: 2, RunID: "run", Type: EntryMessage, Message: &llm.Message{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentText, Text: "work"}}}}
	assistant := Entry{ID: "assistant", Sequence: 4, RunID: "run", Type: EntryMessage, Message: &llm.Message{
		Role: llm.RoleAssistant, StopReason: llm.StopReasonToolUse,
		Content: []llm.Content{{Type: llm.ContentToolCall, ToolCall: &llm.ToolCall{ID: "call", Name: "read_file", Arguments: json.RawMessage(`{"path":"main.go"}`)}}},
	}}
	result := Entry{ID: "result", Sequence: 7, RunID: "run", Type: EntryMessage, Message: &llm.Message{
		Role: llm.RoleTool, ToolCallID: "call", ToolName: "read_file", Content: []llm.Content{{Type: llm.ContentText, Text: "done"}},
	}}
	operation := Record{ID: "operation", Sequence: 1, Lane: MainLane, Type: RecordOperationStarted, RunID: "run", Operation: &OperationData{Intent: OperationRun}}
	stepStarted := Record{ID: "step-started", Sequence: 3, Lane: MainLane, Type: RecordStepStarted, RunID: "run", Step: &StepData{Attempt: 1}}
	stepFinished := Record{ID: "step-finished", Sequence: 5, Lane: MainLane, Type: RecordStepFinished, RunID: "run", Step: &StepData{Attempt: 1, AssistantEntryID: "assistant", StopReason: string(llm.StopReasonToolUse)}}
	toolStarted := Record{ID: "tool-started", Sequence: 6, Lane: MainLane, Type: RecordToolStarted, RunID: "run", Tool: &ToolData{
		AssistantEntryID: "assistant", ToolCallID: "call", ToolName: "read_file", EffectiveArgs: json.RawMessage(`{"path":"main.go"}`),
		IdempotencyKey: "run:call", ResultEntryID: "result", ReplayPolicy: "safe",
	}}
	toolFinished := Record{ID: "tool-finished", Sequence: 8, Lane: MainLane, Type: RecordToolFinished, RunID: "run", Tool: &ToolData{
		AssistantEntryID: "assistant", ToolCallID: "call", ToolName: "read_file", IdempotencyKey: "run:call",
		ResultEntryID: "result", ReplayPolicy: "safe", Status: "completed",
	}}
	interrupt := Record{ID: "interrupt", Sequence: 7, Lane: MainLane, Type: RecordInterruptRequested, RunID: "run", Interrupt: &InterruptData{InterruptID: "approval", Kind: "approval", ToolCallID: "call"}}
	terminal := Record{ID: "terminal", Sequence: 9, Lane: MainLane, Type: RecordOperationFinished, RunID: "run", Operation: &OperationData{Outcome: "completed"}}
	tests := []struct {
		name    string
		entries []Entry
		records []Record
		kind    RecoveryActionKind
		none    bool
	}{
		{name: "after operation started", records: []Record{operation}, kind: RecoveryDecideRun},
		{name: "after user entry", entries: []Entry{user}, records: []Record{operation}, kind: RecoveryContinueRun},
		{name: "after step started", entries: []Entry{user}, records: []Record{operation, stepStarted}, kind: RecoveryContinueRun},
		{name: "after assistant entry", entries: []Entry{user, assistant}, records: []Record{operation, stepStarted}, kind: RecoveryContinueRun},
		{name: "after step finished before ToolStarted", entries: []Entry{user, assistant}, records: []Record{operation, stepStarted, stepFinished}, kind: RecoveryContinueRun},
		{name: "after ToolStarted", entries: []Entry{user, assistant}, records: []Record{operation, stepStarted, stepFinished, toolStarted}, kind: RecoveryRetryTool},
		{name: "after interrupt requested", entries: []Entry{user, assistant}, records: []Record{operation, stepStarted, stepFinished, toolStarted, interrupt}, kind: RecoveryResolveInterrupt},
		{name: "after Tool result entry before ToolFinished", entries: []Entry{user, assistant, result}, records: []Record{operation, stepStarted, stepFinished, toolStarted}, kind: RecoveryReconcileTool},
		{name: "after ToolFinished before next step", entries: []Entry{user, assistant, result}, records: []Record{operation, stepStarted, stepFinished, toolStarted, toolFinished}, kind: RecoveryContinueRun},
		{name: "after operation finished", entries: []Entry{user, assistant, result}, records: []Record{operation, stepStarted, stepFinished, toolStarted, toolFinished, terminal}, none: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := BuildRecoveryPlan(Snapshot{Entries: test.entries, Records: test.records})
			if test.none {
				if len(plan.Actions) != 0 {
					t.Fatalf("terminal plan = %#v", plan)
				}
				return
			}
			if len(plan.Actions) != 1 || plan.Actions[0].Kind != test.kind {
				t.Fatalf("plan = %#v, want kind %q", plan, test.kind)
			}
		})
	}
}
