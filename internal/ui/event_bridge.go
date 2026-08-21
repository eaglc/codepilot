package ui

import (
	"context"
	"errors"
	"sync"

	tea "charm.land/bubbletea/v2"
	"github.com/eaglc/codepilot/internal/session"
)

// ErrEventBridgeClosed indicates that an event was published after shutdown.
var ErrEventBridgeClosed = errors.New("UI event bridge is closed")

var _ session.EventSink = (*EventBridge)(nil)

// EventBridge transfers ordered session events into the Bubble Tea update loop.
type EventBridge struct {
	events chan session.Event
	done   chan struct{}
	once   sync.Once
}

// NewEventBridge creates a bounded bridge. Non-positive capacities use one slot.
func NewEventBridge(capacity int) *EventBridge {
	if capacity < 1 {
		capacity = 1
	}
	return &EventBridge{
		events: make(chan session.Event, capacity),
		done:   make(chan struct{}),
	}
}

// Publish queues an event without mutating the caller's payload. A full queue
// applies backpressure so approval, diff, and terminal events cannot be lost.
func (b *EventBridge) Publish(ctx context.Context, event session.Event) error {
	if b == nil {
		return ErrEventBridgeClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}

	// The first checks make calls after cancellation or Close deterministic. The
	// final select releases publishers that were already waiting for capacity.
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	select {
	case <-b.done:
		return ErrEventBridgeClosed
	default:
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-b.done:
		return ErrEventBridgeClosed
	case b.events <- cloneEvent(event):
		return nil
	}
}

// WaitForEvent returns a command that delivers exactly one event to Update.
func (b *EventBridge) WaitForEvent() tea.Cmd {
	return func() tea.Msg {
		if b == nil {
			return eventBridgeClosedMsg{}
		}
		select {
		case <-b.done:
			return eventBridgeClosedMsg{}
		default:
		}
		select {
		case <-b.done:
			return eventBridgeClosedMsg{}
		case event := <-b.events:
			return eventBridgeMsg{event: event}
		}
	}
}

// Close idempotently releases blocked publishers and event-wait commands.
func (b *EventBridge) Close() error {
	if b == nil {
		return nil
	}
	b.once.Do(func() {
		// events remains open because a publisher may already be selecting on it.
		close(b.done)
	})
	return nil
}

type eventBridgeMsg struct {
	event session.Event
}

type eventBridgeClosedMsg struct{}

func cloneEvent(value session.Event) session.Event {
	payload := value.Payload
	if payload.Text != nil {
		copyValue := *payload.Text
		payload.Text = &copyValue
	}
	if payload.Tool != nil {
		copyValue := *payload.Tool
		payload.Tool = &copyValue
	}
	if payload.Approval != nil {
		copyValue := *payload.Approval
		if copyValue.Request != nil {
			request := *copyValue.Request
			request.Action = cloneAction(request.Action)
			copyValue.Request = &request
		}
		if copyValue.Decision != nil {
			decision := *copyValue.Decision
			copyValue.Decision = &decision
		}
		payload.Approval = &copyValue
	}
	if payload.Patch != nil {
		copyValue := *payload.Patch
		copyValue.Record.Files = append([]session.PatchedFile(nil), copyValue.Record.Files...)
		payload.Patch = &copyValue
	}
	if payload.Diff != nil {
		copyValue := *payload.Diff
		if copyValue.Result != nil {
			result := *copyValue.Result
			result.Files = append([]session.DiffFile(nil), result.Files...)
			copyValue.Result = &result
		}
		payload.Diff = &copyValue
	}
	if payload.Turn != nil {
		copyValue := *payload.Turn
		payload.Turn = &copyValue
	}
	if payload.Error != nil {
		copyValue := *payload.Error
		payload.Error = &copyValue
	}
	value.Payload = payload
	return value
}

func cloneAction(value session.Action) session.Action {
	if value.Patch != nil {
		copyValue := *value.Patch
		copyValue.Files = append([]string(nil), value.Patch.Files...)
		value.Patch = &copyValue
	}
	if value.Command != nil {
		copyValue := *value.Command
		copyValue.Args = append([]string(nil), value.Command.Args...)
		value.Command = &copyValue
	}
	return value
}
