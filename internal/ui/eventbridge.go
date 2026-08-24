// Package ui contains presentation adapters that depend only on Coding Agent product contracts.
package ui

import (
	"context"
	"errors"
	"sync"

	"github.com/eaglc/codepilot/internal/codingagent"
)

const maxMergedAssistantDeltaBytes = 64 << 10

// EventBridge is a bounded product-event queue for presentation runtimes. The
// queue owns channel delivery so publishers never send while holding its lock.
type EventBridge struct {
	mu       sync.Mutex
	events   chan codingagent.Event
	wake     chan struct{}
	space    chan struct{}
	closing  chan struct{}
	stopped  chan struct{}
	queue    []codingagent.Event
	capacity int
	closed   bool
}

// NewEventBridge creates a bounded Coding Agent event bridge.
func NewEventBridge(capacity int) (*EventBridge, error) {
	if capacity < 1 {
		return nil, errors.New("create UI event bridge: capacity must be positive")
	}
	bridge := &EventBridge{
		events: make(chan codingagent.Event), wake: make(chan struct{}, 1), space: make(chan struct{}, 1),
		closing: make(chan struct{}), stopped: make(chan struct{}), capacity: capacity,
	}
	go bridge.deliver()
	return bridge, nil
}

// PublishCodingEvent implements codingagent.EventSink without accepting lower-level events.
func (b *EventBridge) PublishCodingEvent(ctx context.Context, event codingagent.Event) error {
	if b == nil {
		return errors.New("publish UI event: bridge is nil")
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		b.mu.Lock()
		if b.closed {
			b.mu.Unlock()
			return errors.New("publish UI event: bridge is closed")
		}
		if b.mergeAssistantDelta(event) {
			b.mu.Unlock()
			b.signal(b.wake)
			return nil
		}
		if len(b.queue) < b.capacity {
			b.queue = append(b.queue, event)
			b.mu.Unlock()
			b.signal(b.wake)
			return nil
		}
		b.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-b.closing:
			return errors.New("publish UI event: bridge is closed")
		case <-b.space:
		}
	}
}

// Events returns the read-only product event stream consumed by the TUI runtime.
func (b *EventBridge) Events() <-chan codingagent.Event {
	return b.events
}

// Close rejects new events, unblocks publishers, and closes the consumer stream exactly once.
func (b *EventBridge) Close() error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	if !b.closed {
		b.closed = true
		close(b.closing)
	}
	b.mu.Unlock()
	<-b.stopped
	return nil
}

func (b *EventBridge) deliver() {
	defer close(b.stopped)
	defer close(b.events)
	for {
		select {
		case <-b.closing:
			return
		case <-b.wake:
		}
		for {
			b.mu.Lock()
			if len(b.queue) == 0 {
				b.mu.Unlock()
				break
			}
			event := b.queue[0]
			b.queue[0] = codingagent.Event{}
			b.queue = b.queue[1:]
			b.mu.Unlock()
			b.signal(b.space)
			select {
			case b.events <- event:
			case <-b.closing:
				return
			}
		}
	}
}

// mergeAssistantDelta must be called with b.mu held.
func (b *EventBridge) mergeAssistantDelta(event codingagent.Event) bool {
	if event.Kind != codingagent.EventAssistantOutputDelta || event.Payload.AssistantOutput == nil || len(b.queue) == 0 {
		return false
	}
	last := &b.queue[len(b.queue)-1]
	if last.Kind != codingagent.EventAssistantOutputDelta || last.Payload.AssistantOutput == nil || last.SessionID != event.SessionID || last.TurnID != event.TurnID {
		return false
	}
	if len(last.Payload.AssistantOutput.Delta)+len(event.Payload.AssistantOutput.Delta) > maxMergedAssistantDeltaBytes {
		return false
	}
	last.Payload.AssistantOutput.Delta += event.Payload.AssistantOutput.Delta
	last.ID = event.ID
	last.Sequence = event.Sequence
	last.SnapshotRevision = event.SnapshotRevision
	last.Timestamp = event.Timestamp
	return true
}

func (*EventBridge) signal(channel chan struct{}) {
	select {
	case channel <- struct{}{}:
	default:
	}
}
