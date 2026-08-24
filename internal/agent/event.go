// Package agent implements a provider-neutral model/tool runtime.
package agent

import (
	"context"
	"encoding/json"
	"time"

	agentsession "github.com/eaglc/codepilot/internal/agent/session"
)

// EventKind identifies a normalized Agent runtime transition.
type EventKind string

const (
	EventRunStarted               EventKind = "run_started"
	EventStepStarted              EventKind = "step_started"
	EventStepFinished             EventKind = "step_finished"
	EventAssistantTextDelta       EventKind = "assistant_text_delta"
	EventAssistantThinkingChanged EventKind = "assistant_thinking_changed"
	EventToolStarted              EventKind = "tool_started"
	EventToolProgress             EventKind = "tool_progress"
	EventToolFinished             EventKind = "tool_finished"
	EventCompactionStarted        EventKind = "compaction_started"
	EventCompactionFinished       EventKind = "compaction_finished"
	EventRetryScheduled           EventKind = "retry_scheduled"
	EventRunInterrupted           EventKind = "run_interrupted"
	EventRunResumed               EventKind = "run_resumed"
	EventRunFinished              EventKind = "run_finished"
	EventRunFailed                EventKind = "run_failed"
)

// AssistantEvent contains provider-neutral incremental assistant state.
type AssistantEvent struct {
	Text           string
	ThinkingActive bool
}

// StepEvent contains one model-step boundary without provider response objects.
type StepEvent struct {
	Number           int
	AssistantEntryID string
	StopReason       string
}

// ToolEvent contains bounded activity metadata and no executable or business object.
type ToolEvent struct {
	CallID  string
	Name    string
	Status  string
	Summary string
	Details json.RawMessage
}

// CompactionEvent identifies a durable summary without exposing summary prompts to observers.
type CompactionEvent struct {
	SourceDigest string
	FromEntryID  string
	ToEntryID    string
}

// RetryEvent describes a scheduled retry using normalized timing and reason data.
type RetryEvent struct {
	Attempt int
	Delay   time.Duration
	Reason  string
}

// InterruptEvent identifies resumable external input.
type InterruptEvent struct {
	ID       string
	Kind     string
	Decision string
}

// TerminalEvent describes an Agent run terminal state.
type TerminalEvent struct {
	Status string
	Reason string
	Steps  int
}

// Event is the Agent-only event envelope. Product adapters must copy allowed fields into their own event types.
type Event struct {
	ID         string
	Sequence   uint64
	SessionID  agentsession.ID
	RunID      agentsession.RunID
	Timestamp  time.Time
	Kind       EventKind
	Assistant  *AssistantEvent
	Step       *StepEvent
	Tool       *ToolEvent
	Compaction *CompactionEvent
	Retry      *RetryEvent
	Interrupt  *InterruptEvent
	Terminal   *TerminalEvent
}

// EventSink receives ordered runtime events and must return only after accepting or rejecting an event.
type EventSink interface {
	PublishAgentEvent(ctx context.Context, event Event) error
}

// NopEventSink discards Agent events.
type NopEventSink struct{}

// PublishAgentEvent implements EventSink.
func (NopEventSink) PublishAgentEvent(context.Context, Event) error { return nil }
