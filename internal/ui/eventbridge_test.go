package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/eaglc/codepilot/internal/codingagent"
)

func TestEventBridgeMergesAdjacentQueuedAssistantDeltas(t *testing.T) {
	bridge, err := NewEventBridge(2)
	if err != nil {
		t.Fatal(err)
	}
	defer bridge.Close()
	ctx := context.Background()
	head := codingagent.Event{ID: "head", Kind: codingagent.EventTurnStarted}
	if err := bridge.PublishCodingEvent(ctx, head); err != nil {
		t.Fatal(err)
	}
	first := codingagent.Event{ID: "delta-1", Sequence: 1, SessionID: "session", TurnID: "turn", Kind: codingagent.EventAssistantOutputDelta, Payload: codingagent.EventPayload{AssistantOutput: &codingagent.AssistantOutputEvent{Delta: "hello "}}}
	second := codingagent.Event{ID: "delta-2", Sequence: 2, SessionID: "session", TurnID: "turn", Kind: codingagent.EventAssistantOutputDelta, Payload: codingagent.EventPayload{AssistantOutput: &codingagent.AssistantOutputEvent{Delta: "world"}}}
	if err := bridge.PublishCodingEvent(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := bridge.PublishCodingEvent(ctx, second); err != nil {
		t.Fatal(err)
	}
	if received := receiveBridgeEvent(t, bridge); received.ID != "head" {
		t.Fatalf("head event = %#v", received)
	}
	merged := receiveBridgeEvent(t, bridge)
	if merged.ID != "delta-2" || merged.Sequence != 2 || merged.Payload.AssistantOutput == nil || merged.Payload.AssistantOutput.Delta != "hello world" {
		t.Fatalf("merged delta = %#v", merged)
	}
}

func TestEventBridgeBackpressureUnblocksWhenConsumerAdvances(t *testing.T) {
	bridge, err := NewEventBridge(1)
	if err != nil {
		t.Fatal(err)
	}
	defer bridge.Close()
	ctx := context.Background()
	if err := bridge.PublishCodingEvent(ctx, codingagent.Event{ID: "one", Kind: codingagent.EventTurnStarted}); err != nil {
		t.Fatal(err)
	}
	waitForBridgeQueueLength(t, bridge, 0)
	if err := bridge.PublishCodingEvent(ctx, codingagent.Event{ID: "two", Kind: codingagent.EventTurnCompleted}); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		result <- bridge.PublishCodingEvent(ctx, codingagent.Event{ID: "three", Kind: codingagent.EventSessionUpdated})
	}()
	select {
	case err := <-result:
		t.Fatalf("publisher bypassed backpressure: %v", err)
	default:
	}
	for _, expected := range []string{"one", "two", "three"} {
		if received := receiveBridgeEvent(t, bridge); received.ID != expected {
			t.Fatalf("event order: got %q, want %q", received.ID, expected)
		}
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("blocked publish: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("publisher stayed blocked after queue space became available")
	}
}

func TestEventBridgeCloseUnblocksPublisherWithoutConsumer(t *testing.T) {
	bridge, err := NewEventBridge(1)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := bridge.PublishCodingEvent(ctx, codingagent.Event{ID: "one", Kind: codingagent.EventTurnStarted}); err != nil {
		t.Fatal(err)
	}
	waitForBridgeQueueLength(t, bridge, 0)
	if err := bridge.PublishCodingEvent(ctx, codingagent.Event{ID: "two", Kind: codingagent.EventTurnCompleted}); err != nil {
		t.Fatal(err)
	}
	published := make(chan error, 1)
	go func() {
		published <- bridge.PublishCodingEvent(ctx, codingagent.Event{ID: "three", Kind: codingagent.EventSessionUpdated})
	}()
	closed := make(chan error, 1)
	go func() { closed <- bridge.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("close bridge: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close blocked behind a publisher")
	}
	select {
	case err := <-published:
		if err == nil || !strings.Contains(err.Error(), "closed") {
			t.Fatalf("blocked publisher error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("publisher was not released by Close")
	}
	if _, ok := <-bridge.Events(); ok {
		t.Fatal("consumer stream remained open after Close")
	}
	if err := bridge.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func receiveBridgeEvent(t *testing.T, bridge *EventBridge) codingagent.Event {
	t.Helper()
	select {
	case event, ok := <-bridge.Events():
		if !ok {
			t.Fatal("event bridge closed early")
		}
		return event
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for bridge event")
		return codingagent.Event{}
	}
}

func waitForBridgeQueueLength(t *testing.T, bridge *EventBridge, length int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		bridge.mu.Lock()
		actual := len(bridge.queue)
		bridge.mu.Unlock()
		if actual == length {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("bridge queue did not reach length %d", length)
}
