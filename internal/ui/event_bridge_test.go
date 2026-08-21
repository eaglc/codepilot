package ui

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/eaglc/codepilot/internal/session"
)

func TestEventBridge_PreservesOrderAndPayloadOwnership(t *testing.T) {
	bridge := NewEventBridge(2)
	t.Cleanup(func() { _ = bridge.Close() })
	first := session.Event{
		ID:   "first",
		Kind: session.EventApprovalRequested,
		Payload: session.EventPayload{Approval: &session.ApprovalEventPayload{Request: &session.ApprovalRequest{
			ID: "approval",
			Action: session.Action{Patch: &session.PatchAction{
				Files: []string{"main.go"},
			}},
		}}},
	}
	second := session.Event{ID: "second", Kind: session.EventTurnCompleted}
	if err := bridge.Publish(context.Background(), first); err != nil {
		t.Fatalf("publish first event: %v", err)
	}
	if err := bridge.Publish(context.Background(), second); err != nil {
		t.Fatalf("publish second event: %v", err)
	}
	first.Payload.Approval.Request.Action.Patch.Files[0] = "mutated.go"

	gotFirst, ok := bridge.WaitForEvent()().(eventBridgeMsg)
	if !ok || gotFirst.event.ID != "first" {
		t.Fatalf("first event = %#v", gotFirst)
	}
	if gotFirst.event.Payload.Approval.Request.Action.Patch.Files[0] != "main.go" {
		t.Fatalf("queued event payload was mutated: %#v", gotFirst.event.Payload)
	}
	gotSecond, ok := bridge.WaitForEvent()().(eventBridgeMsg)
	if !ok || gotSecond.event.ID != "second" {
		t.Fatalf("second event = %#v", gotSecond)
	}
}

func TestEventBridge_FullQueueRespectsCancellation(t *testing.T) {
	bridge := NewEventBridge(1)
	t.Cleanup(func() { _ = bridge.Close() })
	if err := bridge.Publish(context.Background(), session.Event{ID: "queued"}); err != nil {
		t.Fatalf("fill event queue: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := bridge.Publish(ctx, session.Event{ID: "blocked"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("publish error = %v, want context canceled", err)
	}
}

func TestEventBridge_CloseReleasesWaiterAndRejectsPublish(t *testing.T) {
	bridge := NewEventBridge(1)
	result := make(chan any, 1)
	go func() {
		result <- bridge.WaitForEvent()()
	}()

	if err := bridge.Close(); err != nil {
		t.Fatalf("close bridge: %v", err)
	}
	select {
	case message := <-result:
		if _, ok := message.(eventBridgeClosedMsg); !ok {
			t.Fatalf("wait result = %#v, want closed message", message)
		}
	case <-time.After(time.Second):
		t.Fatal("event waiter did not exit after close")
	}
	if err := bridge.Publish(context.Background(), session.Event{}); !errors.Is(err, ErrEventBridgeClosed) {
		t.Fatalf("publish after close error = %v, want closed", err)
	}
}
