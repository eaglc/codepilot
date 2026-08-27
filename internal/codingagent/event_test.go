package codingagent

import (
	"context"
	"testing"
	"time"

	"github.com/eaglc/codepilot/internal/agent"
	agentsession "github.com/eaglc/codepilot/internal/agent/session"
)

type productEventCollector struct{ events []Event }

func (c *productEventCollector) PublishCodingEvent(_ context.Context, event Event) error {
	c.events = append(c.events, event)
	return nil
}

func TestAgentEventAdapterWhitelistsAssistantAndToolFields(t *testing.T) {
	collector := &productEventCollector{}
	adapter, err := NewAgentEventAdapter("coding-session-1", "turn-1", "run-1", "", collector, nil)
	if err != nil {
		t.Fatalf("create adapter: %v", err)
	}
	timestamp := time.Now().UTC()
	inputs := []agent.Event{
		{ID: "a1", SessionID: "agent-session-1", RunID: agentsession.RunID("run-1"), Timestamp: timestamp, Kind: agent.EventAssistantTextDelta, Assistant: &agent.AssistantEvent{Text: "hello"}},
		{ID: "a2", SessionID: "agent-session-1", RunID: agentsession.RunID("run-1"), Timestamp: timestamp, Kind: agent.EventAssistantThinkingChanged, Assistant: &agent.AssistantEvent{Text: "private reasoning", ThinkingActive: true}},
		{ID: "a3", SessionID: "agent-session-1", RunID: agentsession.RunID("run-1"), Timestamp: timestamp, Kind: agent.EventToolStarted, Tool: &agent.ToolEvent{CallID: "call-1", Name: "read_file", Status: "running", Summary: "reading"}},
	}
	for _, input := range inputs {
		if err := adapter.PublishAgentEvent(context.Background(), input); err != nil {
			t.Fatalf("publish event: %v", err)
		}
	}
	if len(collector.events) != 3 {
		t.Fatalf("product events = %#v", collector.events)
	}
	if collector.events[0].Payload.AssistantOutput.Delta != "hello" {
		t.Fatalf("assistant output = %#v", collector.events[0])
	}
	if status := collector.events[1].Payload.AssistantStatus; status == nil || !status.Thinking {
		t.Fatalf("assistant status = %#v", collector.events[1])
	}
	if collector.events[1].Payload.AssistantOutput != nil {
		t.Fatal("thinking text crossed the product boundary")
	}
	if tool := collector.events[2].Payload.Tool; tool == nil || tool.CallID != "call-1" || tool.Name != "read_file" {
		t.Fatalf("tool activity = %#v", collector.events[2])
	}
	if collector.events[0].TurnID != "turn-1" || collector.events[0].RunID != "run-1" {
		t.Fatalf("explicit event binding = %#v", collector.events[0])
	}
}

func TestAgentEventAdapterRejectsMismatchedRun(t *testing.T) {
	collector := &productEventCollector{}
	adapter, err := NewAgentEventAdapter("coding-session-1", "turn-1", "run-1", "node-1", collector, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = adapter.PublishAgentEvent(context.Background(), agent.Event{ID: "event", RunID: "run-2", Kind: agent.EventRunStarted, Timestamp: time.Now().UTC()})
	if err == nil {
		t.Fatal("mismatched run was accepted")
	}
}

func TestAgentEventAdapterMapsApprovalInterruptsToProductEvents(t *testing.T) {
	collector := &productEventCollector{}
	adapter, err := NewAgentEventAdapter("coding-session-1", "turn-1", "run-1", "", collector, nil)
	if err != nil {
		t.Fatalf("create adapter: %v", err)
	}
	inputs := []agent.Event{
		{ID: "a1", RunID: "run-1", Timestamp: time.Now().UTC(), Kind: agent.EventRunInterrupted, Interrupt: &agent.InterruptEvent{ID: "approval-1", Kind: "approval"}},
		{ID: "a2", RunID: "run-1", Timestamp: time.Now().UTC(), Kind: agent.EventRunResumed, Interrupt: &agent.InterruptEvent{ID: "approval-1", Kind: "approval", Decision: "completed"}},
	}
	for _, input := range inputs {
		if err := adapter.PublishAgentEvent(context.Background(), input); err != nil {
			t.Fatalf("publish event: %v", err)
		}
	}
	if len(collector.events) != 2 || collector.events[0].Kind != EventApprovalRequested || collector.events[1].Kind != EventApprovalResolved {
		t.Fatalf("approval events = %#v", collector.events)
	}
	if approval := collector.events[1].Payload.Approval; approval == nil || approval.RequestID != "approval-1" || approval.Decision != "completed" {
		t.Fatalf("approval resolution = %#v", collector.events[1])
	}
}

func TestAgentEventAdapterMapsRetryWithoutProviderDetails(t *testing.T) {
	collector := &productEventCollector{}
	adapter, _ := NewAgentEventAdapter("coding-session-1", "turn-1", "run-1", "", collector, nil)
	input := agent.Event{ID: "retry", RunID: "run-1", Timestamp: time.Now().UTC(), Kind: agent.EventRetryScheduled, Retry: &agent.RetryEvent{Attempt: 2, Delay: 500 * time.Millisecond, Reason: "rate_limited"}}
	if err := adapter.PublishAgentEvent(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if len(collector.events) != 1 || collector.events[0].Kind != EventTurnRetryScheduled {
		t.Fatalf("retry events = %#v", collector.events)
	}
	turn := collector.events[0].Payload.Turn
	if turn == nil || turn.RetryAttempt != 2 || turn.RetryAfter != 500*time.Millisecond || turn.Reason != "rate_limited" {
		t.Fatalf("retry projection = %#v", collector.events[0])
	}
}
