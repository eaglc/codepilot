package session

import "github.com/eaglc/codepilot/internal/llm"

// Clone returns a defensive deep copy of an entry.
func (entry Entry) Clone() Entry {
	clone := entry
	clone.ActiveTools = append([]string(nil), entry.ActiveTools...)
	if entry.Message != nil {
		message := entry.Message.Clone()
		clone.Message = &message
	}
	if entry.Model != nil {
		model := *entry.Model
		clone.Model = &model
	}
	if entry.Compaction != nil {
		value := *entry.Compaction
		value.Facts = append([]CompactionFact(nil), entry.Compaction.Facts...)
		value.Details = append([]byte(nil), entry.Compaction.Details...)
		if entry.Compaction.Usage != nil {
			usage := *entry.Compaction.Usage
			value.Usage = &usage
		}
		clone.Compaction = &value
	}
	if entry.BranchSummary != nil {
		value := *entry.BranchSummary
		value.Details = append([]byte(nil), entry.BranchSummary.Details...)
		if entry.BranchSummary.Usage != nil {
			usage := *entry.BranchSummary.Usage
			value.Usage = &usage
		}
		clone.BranchSummary = &value
	}
	if entry.CustomMessage != nil {
		value := *entry.CustomMessage
		value.Content = make([]llm.Content, len(entry.CustomMessage.Content))
		for index, content := range entry.CustomMessage.Content {
			value.Content[index] = content.Clone()
		}
		value.Details = append([]byte(nil), entry.CustomMessage.Details...)
		clone.CustomMessage = &value
	}
	return clone
}

// Clone returns a defensive deep copy of a record.
func (record Record) Clone() Record {
	clone := record
	if record.Operation != nil {
		value := *record.Operation
		clone.Operation = &value
	}
	if record.Step != nil {
		value := *record.Step
		clone.Step = &value
	}
	if record.Tool != nil {
		value := *record.Tool
		value.EffectiveArgs = append([]byte(nil), record.Tool.EffectiveArgs...)
		clone.Tool = &value
	}
	if record.Interrupt != nil {
		value := *record.Interrupt
		value.Payload = append([]byte(nil), record.Interrupt.Payload...)
		clone.Interrupt = &value
	}
	if record.Approval != nil {
		value := *record.Approval
		value.Payload = append([]byte(nil), record.Approval.Payload...)
		clone.Approval = &value
	}
	if record.Checkpoint != nil {
		value := *record.Checkpoint
		clone.Checkpoint = &value
	}
	if record.LaneFork != nil {
		value := *record.LaneFork
		clone.LaneFork = &value
	}
	if record.Usage != nil {
		value := *record.Usage
		clone.Usage = &value
	}
	return clone
}
