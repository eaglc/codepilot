package session

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Validate checks a provisioned entry before repository-assigned fields are added.
func (e Entry) Validate() error {
	if e.ID == "" {
		return fmt.Errorf("validate session entry: id is required")
	}
	if e.Sequence != 0 || !e.Timestamp.IsZero() || e.ParentID != "" || e.Lane != "" {
		return fmt.Errorf("validate session entry %q: storage-assigned fields must be empty", e.ID)
	}
	switch e.Type {
	case EntryMessage:
		if e.Message == nil {
			return fmt.Errorf("validate message entry %q: message is missing", e.ID)
		}
		if err := e.Message.Validate(); err != nil {
			return fmt.Errorf("validate message entry %q: %w", e.ID, err)
		}
	case EntryModelChange:
		if e.Model == nil {
			return fmt.Errorf("validate model-change entry %q: model is missing", e.ID)
		}
		if err := e.Model.Validate(); err != nil {
			return err
		}
	case EntryThinkingLevelChange:
		if e.ThinkingLevel == "" {
			return fmt.Errorf("validate thinking-level entry %q: level is required", e.ID)
		}
	case EntryActiveToolsChange:
		if e.ActiveTools == nil {
			return fmt.Errorf("validate active-tools entry %q: tool names are missing", e.ID)
		}
	case EntryCompaction:
		if e.Compaction == nil || e.Compaction.Summary == "" || e.Compaction.CoversFromEntryID == "" || e.Compaction.CoversToEntryID == "" || e.Compaction.SourceDigest == "" || e.Compaction.Strategy == "" || e.Compaction.StrategyVersion == "" {
			return fmt.Errorf("validate compaction entry %q: compaction metadata is incomplete", e.ID)
		}
		if len(e.Compaction.Details) != 0 && !json.Valid(e.Compaction.Details) {
			return fmt.Errorf("validate compaction entry %q: details are not valid JSON", e.ID)
		}
	case EntryBranchSummary:
		if e.BranchSummary == nil || e.BranchSummary.FromEntryID == "" || e.BranchSummary.Summary == "" {
			return fmt.Errorf("validate branch-summary entry %q: summary metadata is incomplete", e.ID)
		}
	case EntryCustomMessage:
		if e.CustomMessage == nil || e.CustomMessage.Type == "" || len(e.CustomMessage.Content) == 0 {
			return fmt.Errorf("validate custom-message entry %q: message metadata is incomplete", e.ID)
		}
	default:
		return fmt.Errorf("validate session entry %q: unsupported type %q", e.ID, e.Type)
	}
	return nil
}

// Validate checks a provisioned record before repository-assigned fields are added.
func (r Record) Validate() error {
	if r.ID == "" {
		return fmt.Errorf("validate session record: id is required")
	}
	if r.Sequence != 0 || !r.Timestamp.IsZero() || r.Lane != "" {
		return fmt.Errorf("validate session record %q: storage-assigned fields must be empty", r.ID)
	}
	if r.RunID == "" && r.Type != RecordUsage && r.Type != RecordLaneForked {
		return fmt.Errorf("validate session record %q: run id is required", r.ID)
	}
	switch r.Type {
	case RecordOperationStarted:
		if r.Operation == nil || r.Operation.Intent == "" {
			return fmt.Errorf("validate operation-started record %q: intent is required", r.ID)
		}
	case RecordAbortRequested:
	case RecordOperationFinished:
		if r.Operation == nil || r.Operation.Outcome == "" {
			return fmt.Errorf("validate operation-finished record %q: outcome is required", r.ID)
		}
	case RecordStepStarted, RecordStepFinished:
		if r.Step == nil || r.Step.Attempt < 1 {
			return fmt.Errorf("validate step record %q: positive attempt is required", r.ID)
		}
	case RecordToolStarted:
		if r.Tool == nil || r.Tool.AssistantEntryID == "" || r.Tool.ToolCallID == "" || r.Tool.ToolName == "" || r.Tool.ResultEntryID == "" || r.Tool.ReplayPolicy == "" || !validJSONObject(r.Tool.EffectiveArgs) {
			return fmt.Errorf("validate tool-started record %q: tool metadata is incomplete", r.ID)
		}
	case RecordToolFinished:
		if r.Tool == nil || r.Tool.ToolCallID == "" || r.Tool.ToolName == "" || r.Tool.ResultEntryID == "" || r.Tool.Status == "" {
			return fmt.Errorf("validate tool-finished record %q: tool metadata is incomplete", r.ID)
		}
	case RecordInterruptRequested:
		if r.Interrupt == nil || r.Interrupt.InterruptID == "" || r.Interrupt.Kind == "" || r.Interrupt.ToolCallID == "" {
			return fmt.Errorf("validate interrupt-requested record %q: interrupt metadata is incomplete", r.ID)
		}
		if len(r.Interrupt.Payload) != 0 && !json.Valid(r.Interrupt.Payload) {
			return fmt.Errorf("validate interrupt-requested record %q: payload is not valid JSON", r.ID)
		}
	case RecordInterruptResolved:
		if r.Interrupt == nil || r.Interrupt.InterruptID == "" || r.Interrupt.Kind == "" || r.Interrupt.ToolCallID == "" || r.Interrupt.Decision == "" {
			return fmt.Errorf("validate interrupt-resolved record %q: interrupt metadata is incomplete", r.ID)
		}
		if len(r.Interrupt.Payload) != 0 && !json.Valid(r.Interrupt.Payload) {
			return fmt.Errorf("validate interrupt-resolved record %q: payload is not valid JSON", r.ID)
		}
	case RecordApprovalRequested, RecordApprovalResolved:
		if r.Approval == nil || r.Approval.RequestID == "" || r.Approval.Kind == "" {
			return fmt.Errorf("validate approval record %q: approval metadata is incomplete", r.ID)
		}
	case RecordCheckpointSaved:
		if r.Checkpoint == nil || r.Checkpoint.CheckpointID == "" || r.Checkpoint.Digest == "" {
			return fmt.Errorf("validate checkpoint record %q: checkpoint metadata is incomplete", r.ID)
		}
	case RecordUsage:
		if r.Usage == nil {
			return fmt.Errorf("validate usage record %q: usage is missing", r.ID)
		}
	case RecordLaneForked:
		if r.LaneFork == nil || r.LaneFork.Lane == "" {
			return fmt.Errorf("validate lane-forked record %q: lane is required", r.ID)
		}
	default:
		return fmt.Errorf("validate session record %q: unsupported type %q", r.ID, r.Type)
	}
	return nil
}

func validJSONObject(value json.RawMessage) bool {
	trimmed := bytes.TrimSpace(value)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return false
	}
	var decoded map[string]json.RawMessage
	return json.Unmarshal(trimmed, &decoded) == nil
}
