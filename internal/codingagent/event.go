// Package codingagent composes generic Agent capabilities into a Coding Agent product.
package codingagent

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/eaglc/codepilot/internal/agent"
)

// SessionID uniquely identifies one Coding Agent product session.
type SessionID string

// TurnID identifies one complete user-triggered Coding Agent turn.
type TurnID string

// RunID identifies one generic execution bound to a Product Turn.
type RunID string

// NodeID identifies an optional Workflow node without exposing Workflow internals.
type NodeID string

// EventKind identifies the stable event protocol exposed to UI, CLI, and future RPC adapters.
type EventKind string

const (
	EventSessionActivated          EventKind = "session_activated"
	EventSessionUpdated            EventKind = "session_updated"
	EventTurnStarted               EventKind = "turn_started"
	EventTurnProgressChanged       EventKind = "turn_progress_changed"
	EventAssistantOutputDelta      EventKind = "assistant_output_delta"
	EventAssistantOutputFinished   EventKind = "assistant_output_finished"
	EventAssistantStatusChanged    EventKind = "assistant_status_changed"
	EventToolActivityStarted       EventKind = "tool_activity_started"
	EventToolActivityUpdated       EventKind = "tool_activity_updated"
	EventToolActivityFinished      EventKind = "tool_activity_finished"
	EventApprovalRequested         EventKind = "approval_requested"
	EventApprovalResolved          EventKind = "approval_resolved"
	EventPatchApplied              EventKind = "patch_applied"
	EventWorkspaceChanged          EventKind = "workspace_changed"
	EventDiffChanged               EventKind = "diff_changed"
	EventContextCompactionStarted  EventKind = "context_compaction_started"
	EventContextCompactionFinished EventKind = "context_compaction_finished"
	EventTurnRetryScheduled        EventKind = "turn_retry_scheduled"
	EventTurnInterrupted           EventKind = "turn_interrupted"
	EventTurnResumed               EventKind = "turn_resumed"
	EventTurnCompleted             EventKind = "turn_completed"
	EventTurnCancelled             EventKind = "turn_cancelled"
	EventTurnFailed                EventKind = "turn_failed"
	EventPersistenceWarning        EventKind = "persistence_warning"
)

// AssistantOutputEvent contains only normalized display text.
type AssistantOutputEvent struct {
	Delta string
}

// AssistantStatusEvent exposes state, never raw model thinking content.
type AssistantStatusEvent struct {
	Thinking bool
}

// ToolActivityEvent is a bounded, secret-free tool activity projection.
type ToolActivityEvent struct {
	CallID    string
	Name      string
	Status    string
	Summary   string
	Detail    string
	Diff      *InlineDiff
	Resources []ToolResource
}

// CompactionEvent identifies a durable context compaction boundary.
type CompactionEvent struct {
	SourceDigest string
	FromEntryID  string
	ToEntryID    string
}

// TurnEvent contains stable turn progress or terminal facts.
type TurnEvent struct {
	Status       string
	Reason       string
	Steps        int
	RetryAttempt int
	RetryAfter   time.Duration
}

// ApprovalEvent contains a product approval identity and safe description.
type ApprovalEvent struct {
	RequestID string
	Kind      string
	Summary   string
	Decision  string
}

// WorkspaceEvent contains product workspace state without lower-level runtime objects.
type WorkspaceEvent struct {
	WorkspaceID string
	WorktreeID  string
	Changed     bool
}

// SessionEvent contains mutable product selection facts without exposing
// Provider implementation or storage DTOs.
type SessionEvent struct {
	SessionID         SessionID
	Title             string
	ProviderProfileID string
	ModelID           string
	PermissionMode    PermissionMode
	ActiveLane        string
	Archived          bool
}

// ErrorEvent contains a safe product-facing error.
type ErrorEvent struct {
	Code        string
	UserMessage string
	Retryable   bool
}

// EventPayload is the typed union carried by one product event.
type EventPayload struct {
	AssistantOutput *AssistantOutputEvent
	AssistantStatus *AssistantStatusEvent
	Tool            *ToolActivityEvent
	Compaction      *CompactionEvent
	Turn            *TurnEvent
	Approval        *ApprovalEvent
	Workspace       *WorkspaceEvent
	Session         *SessionEvent
	Error           *ErrorEvent
}

// Event is the only runtime event envelope exposed by Coding Agent to presentation layers.
type Event struct {
	ID               string
	Sequence         uint64
	SnapshotRevision uint64
	SessionID        SessionID
	TurnID           TurnID
	RunID            RunID
	NodeID           NodeID
	Timestamp        time.Time
	Kind             EventKind
	Payload          EventPayload
}

// EventSink receives ordered Coding Agent product events.
type EventSink interface {
	PublishCodingEvent(ctx context.Context, event Event) error
}

// RevisionSource returns the current authoritative product snapshot revision.
type RevisionSource interface {
	CurrentRevision(ctx context.Context, sessionID SessionID) (uint64, error)
}

// AgentEventAdapter maps generic Agent events to the product protocol using explicit field copies.
type AgentEventAdapter struct {
	mu        sync.Mutex
	sessionID SessionID
	turnID    TurnID
	runID     RunID
	nodeID    NodeID
	sink      EventSink
	revisions RevisionSource
	sequence  uint64
}

// NewAgentEventAdapter creates an isolated Agent-to-product event boundary.
func NewAgentEventAdapter(sessionID SessionID, turnID TurnID, runID RunID, nodeID NodeID, sink EventSink, revisions RevisionSource) (*AgentEventAdapter, error) {
	if sessionID == "" || turnID == "" || runID == "" || sink == nil {
		return nil, fmt.Errorf("create coding event adapter: session, turn, run, and sink are required")
	}
	return &AgentEventAdapter{sessionID: sessionID, turnID: turnID, runID: runID, nodeID: nodeID, sink: sink, revisions: revisions}, nil
}

// PublishAgentEvent implements agent.EventSink without forwarding Agent or LLM event objects.
func (a *AgentEventAdapter) PublishAgentEvent(ctx context.Context, source agent.Event) error {
	if source.RunID != "" && RunID(source.RunID) != a.runID {
		return fmt.Errorf("publish coding event: Agent run %q does not match bound run %q", source.RunID, a.runID)
	}
	event, publish := a.translate(source)
	if !publish {
		return nil
	}
	a.mu.Lock()
	a.sequence++
	event.Sequence = a.sequence
	a.mu.Unlock()
	event.ID = "product:" + source.ID
	event.SessionID = a.sessionID
	event.TurnID = a.turnID
	event.RunID = a.runID
	event.NodeID = a.nodeID
	event.Timestamp = source.Timestamp
	if a.revisions != nil {
		revision, err := a.revisions.CurrentRevision(ctx, a.sessionID)
		if err != nil {
			return fmt.Errorf("publish coding event: load snapshot revision: %w", err)
		}
		event.SnapshotRevision = revision
	}
	return a.sink.PublishCodingEvent(ctx, event)
}

func (a *AgentEventAdapter) translate(source agent.Event) (Event, bool) {
	switch source.Kind {
	case agent.EventRunStarted:
		return Event{Kind: EventTurnStarted, Payload: EventPayload{Turn: &TurnEvent{Status: "running"}}}, true
	case agent.EventStepStarted:
		steps := 0
		if source.Step != nil {
			steps = source.Step.Number
		}
		return Event{Kind: EventTurnProgressChanged, Payload: EventPayload{Turn: &TurnEvent{Status: "running", Steps: steps}}}, true
	case agent.EventStepFinished:
		steps := 0
		if source.Step != nil {
			steps = source.Step.Number
		}
		return Event{Kind: EventAssistantOutputFinished, Payload: EventPayload{Turn: &TurnEvent{Status: "running", Steps: steps}}}, true
	case agent.EventAssistantTextDelta:
		if source.Assistant == nil || source.Assistant.Text == "" {
			return Event{}, false
		}
		return Event{Kind: EventAssistantOutputDelta, Payload: EventPayload{AssistantOutput: &AssistantOutputEvent{Delta: boundedUTF8(redactSensitiveText(source.Assistant.Text), 16<<10)}}}, true
	case agent.EventAssistantThinkingChanged:
		if source.Assistant == nil {
			return Event{}, false
		}
		return Event{Kind: EventAssistantStatusChanged, Payload: EventPayload{AssistantStatus: &AssistantStatusEvent{Thinking: source.Assistant.ThinkingActive}}}, true
	case agent.EventToolStarted, agent.EventToolProgress, agent.EventToolFinished:
		if source.Tool == nil {
			return Event{}, false
		}
		kind := EventToolActivityStarted
		if source.Kind == agent.EventToolProgress {
			kind = EventToolActivityUpdated
		} else if source.Kind == agent.EventToolFinished {
			kind = EventToolActivityFinished
		}
		toolEvent := &ToolActivityEvent{
			CallID: source.Tool.CallID, Name: source.Tool.Name, Status: source.Tool.Status,
			Summary: toolSummary(source.Tool.Summary),
		}
		projectLiveToolDetails(toolEvent, source.Tool.Details)
		return Event{Kind: kind, Payload: EventPayload{Tool: toolEvent}}, true
	case agent.EventCompactionStarted, agent.EventCompactionFinished:
		if source.Compaction == nil {
			return Event{}, false
		}
		kind := EventContextCompactionStarted
		if source.Kind == agent.EventCompactionFinished {
			kind = EventContextCompactionFinished
		}
		return Event{Kind: kind, Payload: EventPayload{Compaction: &CompactionEvent{SourceDigest: source.Compaction.SourceDigest, FromEntryID: source.Compaction.FromEntryID, ToEntryID: source.Compaction.ToEntryID}}}, true
	case agent.EventRetryScheduled:
		if source.Retry == nil {
			return Event{}, false
		}
		return Event{Kind: EventTurnRetryScheduled, Payload: EventPayload{Turn: &TurnEvent{Status: "retrying", Reason: boundedUTF8(source.Retry.Reason, 1024), RetryAttempt: source.Retry.Attempt, RetryAfter: source.Retry.Delay}}}, true
	case agent.EventRunInterrupted:
		reason := "external_input"
		if source.Interrupt != nil && source.Interrupt.Kind != "" {
			reason = source.Interrupt.Kind
		}
		if source.Interrupt != nil && source.Interrupt.Kind == "approval" {
			return Event{Kind: EventApprovalRequested, Payload: EventPayload{
				Approval: &ApprovalEvent{RequestID: source.Interrupt.ID, Kind: source.Interrupt.Kind},
				Turn:     &TurnEvent{Status: "interrupted", Reason: reason},
			}}, true
		}
		return Event{Kind: EventTurnInterrupted, Payload: EventPayload{Turn: &TurnEvent{Status: "interrupted", Reason: reason}}}, true
	case agent.EventRunResumed:
		if source.Interrupt != nil && source.Interrupt.Kind == "approval" {
			return Event{Kind: EventApprovalResolved, Payload: EventPayload{
				Approval: &ApprovalEvent{RequestID: source.Interrupt.ID, Kind: source.Interrupt.Kind, Decision: source.Interrupt.Decision},
				Turn:     &TurnEvent{Status: "running"},
			}}, true
		}
		return Event{Kind: EventTurnResumed, Payload: EventPayload{Turn: &TurnEvent{Status: "running"}}}, true
	case agent.EventRunFinished:
		return terminalProductEvent(source, false)
	case agent.EventRunFailed:
		return terminalProductEvent(source, true)
	default:
		return Event{}, false
	}
}

func terminalProductEvent(source agent.Event, failed bool) (Event, bool) {
	terminal := source.Terminal
	if terminal == nil {
		return Event{}, false
	}
	kind := EventTurnCompleted
	if terminal.Status == "aborted" {
		kind = EventTurnCancelled
	} else if failed || terminal.Status == "failed" {
		kind = EventTurnFailed
	}
	return Event{Kind: kind, Payload: EventPayload{Turn: &TurnEvent{Status: terminal.Status, Reason: boundedUTF8(terminal.Reason, 1024), Steps: terminal.Steps}}}, true
}

func projectLiveToolDetails(target *ToolActivityEvent, raw json.RawMessage) {
	projected := &TranscriptTool{Name: target.Name}
	projectToolDetails(projected, raw)
	target.Detail = projected.Detail
	target.Diff = projected.Diff
	target.Resources = projected.Resources
}

func boundedUTF8(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	if limit <= 3 {
		return "..."[:limit]
	}
	marker := "...[truncated]"
	if limit <= len(marker) {
		return "..."[:min(limit, 3)]
	}
	value = value[:limit-len(marker)]
	for !utf8.ValidString(value) && len(value) > 0 {
		value = value[:len(value)-1]
	}
	return value + marker
}
