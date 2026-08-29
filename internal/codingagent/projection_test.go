package codingagent

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	agentsession "github.com/eaglc/codepilot/internal/agent/session"
	"github.com/eaglc/codepilot/internal/llm"
)

func TestProjectSnapshotDerivesUsageContextStepAndTimingFromActiveBranch(t *testing.T) {
	started := time.Date(2026, time.August, 24, 10, 0, 0, 0, time.UTC)
	user := agentsession.Entry{ID: "user", Sequence: 2, RunID: "turn", Lane: agentsession.MainLane, Timestamp: started.Add(time.Second), Type: agentsession.EntryMessage,
		Message: &llm.Message{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentText, Text: "work"}}}}
	assistant := agentsession.Entry{ID: "assistant", ParentID: user.ID, Sequence: 8, RunID: "turn", Lane: agentsession.MainLane, Timestamp: started.Add(3 * time.Second), Type: agentsession.EntryMessage,
		Message: &llm.Message{Role: llm.RoleAssistant, Content: []llm.Content{{Type: llm.ContentText, Text: "done"}}}}
	durable := agentsession.Snapshot{
		Metadata: agentsession.Metadata{ID: "agent-session"}, Entries: []agentsession.Entry{user, assistant},
		Lanes: []agentsession.LanePointer{{Lane: agentsession.MainLane, LeafID: assistant.ID}},
		Records: []agentsession.Record{
			{Sequence: 1, RunID: "turn", Lane: agentsession.MainLane, Timestamp: started, Type: agentsession.RecordOperationStarted, Operation: &agentsession.OperationData{Intent: agentsession.OperationRun}},
			{Sequence: 3, RunID: "turn", Lane: agentsession.MainLane, Timestamp: started.Add(time.Second), Type: agentsession.RecordUsage, Usage: &llm.Usage{InputTokens: 80, OutputTokens: 20, TotalTokens: 100, Cost: .02}},
			{Sequence: 4, RunID: "turn", Lane: agentsession.MainLane, Timestamp: started.Add(2 * time.Second), Type: agentsession.RecordStepFinished, Step: &agentsession.StepData{Attempt: 1}},
			{Sequence: 5, RunID: "turn", Lane: agentsession.MainLane, Timestamp: started.Add(3 * time.Second), Type: agentsession.RecordUsage, Usage: &llm.Usage{InputTokens: 40, OutputTokens: 10, CacheReadTokens: 15, ReasoningTokens: 4, TotalTokens: 50, Cost: .01}},
			{Sequence: 6, RunID: "turn", Lane: agentsession.MainLane, Timestamp: started.Add(3 * time.Second), Type: agentsession.RecordStepFinished, Step: &agentsession.StepData{Attempt: 2}},
			{Sequence: 7, RunID: "turn", Lane: agentsession.MainLane, Timestamp: started.Add(4 * time.Second), Type: agentsession.RecordOperationFinished, Operation: &agentsession.OperationData{Outcome: "completed"}},
			{Sequence: 9, RunID: "abandoned", Lane: "other", Timestamp: started.Add(5 * time.Second), Type: agentsession.RecordUsage, Usage: &llm.Usage{TotalTokens: 999, Cost: 9}},
		},
	}
	snapshot, err := ProjectSnapshot(Session{ID: "coding-session", AgentSessionID: "agent-session"}, durable, agentsession.MainLane, RuntimeIdle, 9)
	if err != nil {
		t.Fatal(err)
	}
	metrics := snapshot.Metrics
	if metrics.LatestTurnID != "turn" || metrics.Steps != 2 || metrics.InputTokens != 120 || metrics.OutputTokens != 30 || metrics.TotalTokens != 150 || metrics.CacheReadTokens != 15 || metrics.ReasoningTokens != 4 || metrics.ContextTokens != 40 || metrics.Cost != .03 || metrics.Elapsed != 4*time.Second {
		t.Fatalf("metrics = %#v", metrics)
	}
}

func TestProjectSnapshotAggregatesTimeCostAndFailureByProductPhase(t *testing.T) {
	started := time.Date(2026, time.August, 29, 10, 0, 0, 0, time.UTC)
	user := agentsession.Entry{ID: "user-phase", RunID: "run-direct", Lane: agentsession.MainLane, Timestamp: started, Type: agentsession.EntryMessage,
		Message: &llm.Message{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentText, Text: "complex task"}}}}
	direct := agentsession.Entry{ID: "assistant-direct", ParentID: user.ID, RunID: "run-direct", Lane: agentsession.MainLane, Timestamp: started.Add(time.Second), Type: agentsession.EntryMessage,
		Message: &llm.Message{Role: llm.RoleAssistant, Content: []llm.Content{{Type: llm.ContentText, Text: "suggest Plan"}}}}
	planning := agentsession.Entry{ID: "assistant-plan", ParentID: direct.ID, RunID: "run-plan", Lane: agentsession.MainLane, Timestamp: started.Add(4 * time.Second), Type: agentsession.EntryMessage,
		Message: &llm.Message{Role: llm.RoleAssistant, Content: []llm.Content{{Type: llm.ContentText, Text: "failed Plan"}}}}
	durable := agentsession.Snapshot{
		Metadata: agentsession.Metadata{ID: "agent-phase"}, Entries: []agentsession.Entry{user, direct, planning},
		Lanes: []agentsession.LanePointer{{Lane: agentsession.MainLane, LeafID: planning.ID}},
		Records: []agentsession.Record{
			{RunID: "run-direct", Type: agentsession.RecordUsage, Usage: &llm.Usage{TotalTokens: 100, Cost: .01}},
			{RunID: "run-plan", Type: agentsession.RecordUsage, Usage: &llm.Usage{TotalTokens: 250, Cost: .04}},
		},
	}
	turn := Turn{
		ID: "turn-phase", SessionID: "coding-phase", RequestText: "complex task", Phase: TurnPhasePlanning, Status: TurnFailed, Strategy: ExecutionSingle,
		Runs: []RunBinding{
			{RunID: "run-direct", Phase: TurnPhaseDirect, Profile: CapabilityDirect, Status: RunBindingHandedOff, StartedAt: started, FinishedAt: started.Add(2 * time.Second)},
			{RunID: "run-plan", Phase: TurnPhasePlanning, Profile: CapabilityPlan, Status: RunBindingFailed, StartedAt: started.Add(2 * time.Second), FinishedAt: started.Add(5 * time.Second)},
		},
	}
	snapshot, err := ProjectSnapshotWithTurns(Session{ID: "coding-phase", AgentSessionID: "agent-phase"}, durable, agentsession.MainLane, RuntimeIdle, 1, []Turn{turn})
	if err != nil {
		t.Fatal(err)
	}
	metrics := snapshot.Metrics.ByPhase
	if len(metrics) != 2 || metrics[0].Phase != TurnPhaseDirect || metrics[0].Runs != 1 || metrics[0].FailedRuns != 0 || metrics[0].TotalTokens != 100 || metrics[0].Cost != .01 || metrics[0].Elapsed != 2*time.Second {
		t.Fatalf("Direct phase metrics = %#v", metrics)
	}
	if metrics[1].Phase != TurnPhasePlanning || metrics[1].Runs != 1 || metrics[1].FailedRuns != 1 || metrics[1].TotalTokens != 250 || metrics[1].Cost != .04 || metrics[1].Elapsed != 3*time.Second {
		t.Fatalf("Planning phase metrics = %#v", metrics)
	}
}

func TestProjectSnapshotDoesNotExposeThinkingOrToolArguments(t *testing.T) {
	durable := agentsession.Snapshot{
		Metadata: agentsession.Metadata{ID: "agent-session-1"},
		Lanes:    []agentsession.LanePointer{{Lane: agentsession.MainLane, LeafID: "assistant-1"}},
		Entries: []agentsession.Entry{{
			ID: "assistant-1", RunID: "turn-1", Lane: agentsession.MainLane, Type: agentsession.EntryMessage,
			Message: &llm.Message{Role: llm.RoleAssistant, Content: []llm.Content{
				{Type: llm.ContentThinking, Text: "private reasoning"},
				{Type: llm.ContentToolCall, ToolCall: &llm.ToolCall{ID: "call-1", Name: "read_file", Arguments: []byte(`{"secret":"value"}`)}},
			}},
		}},
	}
	snapshot, err := ProjectSnapshot(Session{ID: "coding-session-1", AgentSessionID: "agent-session-1"}, durable, agentsession.MainLane, RuntimeIdle, 1)
	if err != nil {
		t.Fatalf("project snapshot: %v", err)
	}
	if len(snapshot.Transcript) != 2 || snapshot.Transcript[0].Kind != TranscriptThinking || snapshot.Transcript[0].Text != "" {
		t.Fatalf("transcript = %#v", snapshot.Transcript)
	}
	if snapshot.Transcript[0].SourceEntryID != "assistant-1" || snapshot.Transcript[1].SourceEntryID != "assistant-1" {
		t.Fatalf("source entry ids = %#v", snapshot.Transcript)
	}
	tool := snapshot.Transcript[1].Tool
	if tool == nil || tool.Name != "read_file" || tool.CallID != "call-1" {
		t.Fatalf("tool projection = %#v", tool)
	}
}

func TestProjectSnapshotExposesOnlyTypedInlinePatchDetails(t *testing.T) {
	durable := agentsession.Snapshot{
		Metadata: agentsession.Metadata{ID: "agent-session-1"},
		Lanes:    []agentsession.LanePointer{{Lane: agentsession.MainLane, LeafID: "tool-1"}},
		Entries: []agentsession.Entry{{
			ID: "tool-1", RunID: "turn-1", Lane: agentsession.MainLane, Type: agentsession.EntryMessage,
			Message: &llm.Message{Role: llm.RoleTool, ToolCallID: "call-1", ToolName: "apply_patch",
				Content: []llm.Content{{Type: llm.ContentText, Text: "Applied patch to main.go"}},
				Details: json.RawMessage(`{"kind":"coding_patch_v1","detail":"Applied one file","diff":{"text":"-old\n+new\n","files":["main.go"]},"private":"must-not-cross"}`)},
		}},
	}
	snapshot, err := ProjectSnapshot(Session{ID: "coding-session-1", AgentSessionID: "agent-session-1"}, durable, agentsession.MainLane, RuntimeIdle, 1)
	if err != nil {
		t.Fatalf("project snapshot: %v", err)
	}
	tool := snapshot.Transcript[0].Tool
	if tool == nil || tool.Detail != "Applied one file" || tool.Diff == nil || tool.Diff.Text != "-old\n+new\n" || len(tool.Diff.Files) != 1 {
		t.Fatalf("typed patch projection = %#v", tool)
	}
	if len(tool.Resources) != 1 || tool.Resources[0].Path != "main.go" || tool.Resources[0].AddedLines != 1 || tool.Resources[0].DeletedLines != 1 {
		t.Fatalf("typed patch resources = %#v", tool.Resources)
	}
}

func TestProjectSnapshotExposesReadFileRangeWithoutPrivateDetails(t *testing.T) {
	durable := agentsession.Snapshot{
		Metadata: agentsession.Metadata{ID: "agent-session-1"},
		Lanes:    []agentsession.LanePointer{{Lane: agentsession.MainLane, LeafID: "tool-1"}},
		Entries: []agentsession.Entry{{
			ID: "tool-1", RunID: "turn-1", Lane: agentsession.MainLane, Type: agentsession.EntryMessage,
			Message: &llm.Message{Role: llm.RoleTool, ToolCallID: "call-1", ToolName: "read_file",
				Content: []llm.Content{{Type: llm.ContentText, Text: "package main"}},
				Details: json.RawMessage(`{"path":"internal/ui/model.go","start_line":923,"end_line":981,"bytes":2048,"private":"must-not-cross"}`)},
		}},
	}
	snapshot, err := ProjectSnapshot(Session{ID: "coding-session-1", AgentSessionID: "agent-session-1"}, durable, agentsession.MainLane, RuntimeIdle, 1)
	if err != nil {
		t.Fatalf("project snapshot: %v", err)
	}
	tool := snapshot.Transcript[0].Tool
	if tool == nil || len(tool.Resources) != 1 || tool.Resources[0].Path != "internal/ui/model.go" || tool.Resources[0].StartLine != 923 || tool.Resources[0].EndLine != 981 {
		t.Fatalf("read resource projection = %#v", tool)
	}
	encoded, _ := json.Marshal(snapshot)
	if strings.Contains(string(encoded), "must-not-cross") || strings.Contains(string(encoded), "2048") {
		t.Fatalf("private read details crossed product boundary: %s", encoded)
	}
}

func TestProjectSnapshotExposesTypedProposedPatchWithoutIntegrityPayload(t *testing.T) {
	user := agentsession.Entry{ID: "user-1", RunID: "turn-1", Lane: agentsession.MainLane, Type: agentsession.EntryMessage,
		Message: &llm.Message{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentText, Text: "edit"}}}}
	interrupt := agentsession.Record{ID: "interrupt-1", RunID: "turn-1", Lane: agentsession.MainLane, Type: agentsession.RecordInterruptRequested,
		Interrupt: &agentsession.InterruptData{InterruptID: "approval-1", Kind: "approval", ToolCallID: "call-1",
			Payload: json.RawMessage(`{"kind":"coding_patch_approval_v1","version":1,"summary":"Edit main.go","patch":"--- a/main.go\n+++ b/main.go\n@@ -1 +1,2 @@\n-old\n+new\n+more\n","files":["main.go"],"before_state":{"main.go":"private-hash"},"digest":"private-digest"}`)}}
	durable := agentsession.Snapshot{
		Metadata: agentsession.Metadata{ID: "agent-session-1"}, Entries: []agentsession.Entry{user}, Records: []agentsession.Record{interrupt},
		Lanes: []agentsession.LanePointer{{Lane: agentsession.MainLane, LeafID: user.ID}},
	}
	snapshot, err := ProjectSnapshot(Session{ID: "coding-session-1", AgentSessionID: "agent-session-1"}, durable, agentsession.MainLane, RuntimeAwaitingApproval, 2)
	if err != nil {
		t.Fatalf("project snapshot: %v", err)
	}
	if len(snapshot.PendingInterrupts) != 1 || snapshot.PendingInterrupts[0].Proposed == nil || !snapshot.PendingInterrupts[0].CanGrantSession {
		t.Fatalf("pending proposal = %#v", snapshot.PendingInterrupts)
	}
	proposal := snapshot.PendingInterrupts[0].Proposed
	if proposal.Kind != "patch" || proposal.AddedLines != 2 || proposal.DeletedLines != 1 || len(proposal.Diff.Files) != 1 || !strings.Contains(proposal.Diff.Text, "+more") {
		t.Fatalf("typed proposal = %#v", proposal)
	}
	encoded, _ := json.Marshal(snapshot)
	if strings.Contains(string(encoded), "private-hash") || strings.Contains(string(encoded), "private-digest") {
		t.Fatalf("integrity payload crossed product boundary: %s", encoded)
	}
}

func TestProjectSnapshotExposesTrustedCheckPlanWithoutRawApprovalPayload(t *testing.T) {
	user := agentsession.Entry{ID: "user-1", RunID: "turn-1", Lane: agentsession.MainLane, Type: agentsession.EntryMessage,
		Message: &llm.Message{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentText, Text: "run tests"}}}}
	interrupt := agentsession.Record{ID: "interrupt-1", RunID: "turn-1", Lane: agentsession.MainLane, Type: agentsession.RecordInterruptRequested,
		Interrupt: &agentsession.InterruptData{InterruptID: "approval-1", Kind: "approval", ToolCallID: "call-1",
			Payload: json.RawMessage(`{"kind":"coding_check_approval_v1","version":1,"summary":"Run Go tests","plan_id":"go.test","command":"go test ./...","digest":"private-digest"}`)}}
	durable := agentsession.Snapshot{
		Metadata: agentsession.Metadata{ID: "agent-session-1"}, Entries: []agentsession.Entry{user}, Records: []agentsession.Record{interrupt},
		Lanes: []agentsession.LanePointer{{Lane: agentsession.MainLane, LeafID: user.ID}},
	}
	snapshot, err := ProjectSnapshot(Session{ID: "coding-session-1", AgentSessionID: "agent-session-1"}, durable, agentsession.MainLane, RuntimeAwaitingApproval, 2)
	if err != nil {
		t.Fatalf("project snapshot: %v", err)
	}
	proposal := snapshot.PendingInterrupts[0].Proposed
	if proposal == nil || proposal.Kind != "check" || proposal.PlanID != "go.test" || proposal.Command != "go test ./..." || !snapshot.PendingInterrupts[0].CanGrantSession {
		t.Fatalf("trusted check proposal = %#v", proposal)
	}
	encoded, _ := json.Marshal(snapshot)
	if strings.Contains(string(encoded), "private-digest") {
		t.Fatalf("check approval digest crossed product boundary: %s", encoded)
	}
}

func TestProjectSnapshotExposesSensitiveReadWithoutReusableGrantOrIntegrityPayload(t *testing.T) {
	user := agentsession.Entry{ID: "user-1", RunID: "turn-1", Lane: agentsession.MainLane, Type: agentsession.EntryMessage,
		Message: &llm.Message{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentText, Text: "inspect credentials"}}}}
	interrupt := agentsession.Record{ID: "interrupt-1", RunID: "turn-1", Lane: agentsession.MainLane, Type: agentsession.RecordInterruptRequested,
		Interrupt: &agentsession.InterruptData{InterruptID: "approval-1", Kind: "approval", ToolCallID: "call-1",
			Payload: json.RawMessage(`{"kind":"coding_sensitive_read_approval_v1","version":1,"tool_name":"read_file","path":".env","summary":"Read sensitive path .env with secret values redacted","digest":"private-digest"}`)}}
	durable := agentsession.Snapshot{
		Metadata: agentsession.Metadata{ID: "agent-session-1"}, Entries: []agentsession.Entry{user}, Records: []agentsession.Record{interrupt},
		Lanes: []agentsession.LanePointer{{Lane: agentsession.MainLane, LeafID: user.ID}},
	}
	snapshot, err := ProjectSnapshot(Session{ID: "coding-session-1", AgentSessionID: "agent-session-1"}, durable, agentsession.MainLane, RuntimeAwaitingApproval, 2)
	if err != nil {
		t.Fatalf("project snapshot: %v", err)
	}
	if len(snapshot.PendingInterrupts) != 1 || snapshot.PendingInterrupts[0].Proposed == nil {
		t.Fatalf("sensitive proposal = %#v", snapshot.PendingInterrupts)
	}
	pending := snapshot.PendingInterrupts[0]
	if pending.CanGrantSession || pending.Proposed.Kind != "sensitive_read" || pending.Proposed.Path != ".env" {
		t.Fatalf("sensitive proposal = %#v", pending)
	}
	encoded, _ := json.Marshal(snapshot)
	if strings.Contains(string(encoded), "private-digest") {
		t.Fatalf("sensitive-read digest crossed product boundary: %s", encoded)
	}
}

func TestProjectSnapshotExposesAllowlistedLanguageServerWithoutDigest(t *testing.T) {
	user := agentsession.Entry{ID: "user-1", RunID: "turn-1", Lane: agentsession.MainLane, Type: agentsession.EntryMessage,
		Message: &llm.Message{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentText, Text: "find definition"}}}}
	interrupt := agentsession.Record{ID: "interrupt-1", RunID: "turn-1", Lane: agentsession.MainLane, Type: agentsession.RecordInterruptRequested,
		Interrupt: &agentsession.InterruptData{InterruptID: "approval-1", Kind: "approval", ToolCallID: "call-1",
			Payload: json.RawMessage(`{"kind":"coding_lsp_start_approval_v1","version":1,"grant_tool_name":"language_server","requested_tool":"find_definition","language":"go","program":"gopls","arguments":["serve"],"summary":"Start allowlisted gopls language server","digest":"private-digest"}`)}}
	durable := agentsession.Snapshot{Metadata: agentsession.Metadata{ID: "agent-session-1"}, Entries: []agentsession.Entry{user}, Records: []agentsession.Record{interrupt}, Lanes: []agentsession.LanePointer{{Lane: agentsession.MainLane, LeafID: user.ID}}}
	snapshot, err := ProjectSnapshot(Session{ID: "coding-session-1", AgentSessionID: "agent-session-1"}, durable, agentsession.MainLane, RuntimeAwaitingApproval, 2)
	if err != nil {
		t.Fatal(err)
	}
	pending := snapshot.PendingInterrupts[0]
	if !pending.CanGrantSession || pending.Proposed == nil || pending.Proposed.Kind != "lsp" || pending.Proposed.Language != "go" || pending.Proposed.Command != "gopls serve" {
		t.Fatalf("language-server proposal = %#v", pending)
	}
	encoded, _ := json.Marshal(snapshot)
	if strings.Contains(string(encoded), "private-digest") {
		t.Fatalf("language-server digest crossed product boundary: %s", encoded)
	}
}

func TestProjectSnapshotIncludesDurableTurnFailureInSequence(t *testing.T) {
	user := agentsession.Entry{ID: "user-1", Sequence: 1, RunID: "turn-1", Lane: agentsession.MainLane, Type: agentsession.EntryMessage,
		Message: &llm.Message{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentText, Text: "hello"}}}}
	failure := agentsession.Record{ID: "record-1", Sequence: 2, RunID: "turn-1", Lane: agentsession.MainLane, Type: agentsession.RecordOperationFinished,
		Operation: &agentsession.OperationData{Outcome: "failed", ErrorCode: "model_step", ErrorMessage: `receive Eino stream: dial tcp 127.0.0.1:11434: connectex: actively refused`}}
	durable := agentsession.Snapshot{
		Metadata: agentsession.Metadata{ID: "agent-session-1"}, Entries: []agentsession.Entry{user}, Records: []agentsession.Record{failure},
		Lanes: []agentsession.LanePointer{{Lane: agentsession.MainLane, LeafID: user.ID}},
		Log:   []agentsession.LogItem{{Sequence: 1, Entry: &user}, {Sequence: 2, Record: &failure}},
	}
	snapshot, err := ProjectSnapshot(Session{ID: "coding-session-1", AgentSessionID: "agent-session-1"}, durable, agentsession.MainLane, RuntimeIdle, 2)
	if err != nil {
		t.Fatalf("project snapshot: %v", err)
	}
	if len(snapshot.Transcript) != 2 || snapshot.Transcript[1].Kind != TranscriptFailure || strings.Contains(snapshot.Transcript[1].Text, "Eino") || !strings.Contains(snapshot.Transcript[1].Text, "Ollama") {
		t.Fatalf("failure projection = %#v", snapshot.Transcript)
	}
}

func TestProjectSnapshotSuppressesRecoveryForActiveRuntime(t *testing.T) {
	user := agentsession.Entry{ID: "user-1", Sequence: 2, RunID: "turn-1", Lane: agentsession.MainLane, Type: agentsession.EntryMessage,
		Message: &llm.Message{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentText, Text: "hello"}}}}
	durable := agentsession.Snapshot{
		Metadata: agentsession.Metadata{ID: "agent-session-1"}, Entries: []agentsession.Entry{user},
		Records: []agentsession.Record{{ID: "operation-1", Sequence: 1, RunID: "turn-1", Lane: agentsession.MainLane, Type: agentsession.RecordOperationStarted,
			Operation: &agentsession.OperationData{Intent: agentsession.OperationRun}}},
		Lanes: []agentsession.LanePointer{{Lane: agentsession.MainLane, LeafID: user.ID}},
	}
	product := Session{ID: "coding-session-1", AgentSessionID: "agent-session-1"}
	for _, state := range []RuntimeState{RuntimeRunning, RuntimeCancelling} {
		snapshot, err := ProjectSnapshot(product, durable, agentsession.MainLane, state, 2)
		if err != nil {
			t.Fatalf("project %s snapshot: %v", state, err)
		}
		if len(snapshot.RecoveryActions) != 0 || snapshot.RuntimeState != state {
			t.Fatalf("active %s snapshot exposed recovery: %#v", state, snapshot)
		}
	}

	snapshot, err := ProjectSnapshot(product, durable, agentsession.MainLane, RuntimeIdle, 3)
	if err != nil {
		t.Fatalf("project idle snapshot: %v", err)
	}
	if len(snapshot.RecoveryActions) == 0 || snapshot.RuntimeState != RuntimeInterrupted {
		t.Fatalf("idle unfinished operation was not recoverable: %#v", snapshot)
	}
}
