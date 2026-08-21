package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/eaglc/codepilot/internal/session"
	"github.com/eaglc/codepilot/internal/tool"
)

const assistantEventWindow = 25 * time.Millisecond

// codingEventAdapter translates provider-neutral invocation events into stable
// session events. Assistant fragments are coalesced synchronously so no
// background publisher can outlive its turn.
type codingEventAdapter struct {
	scope         session.TurnScope
	sink          session.EventSink
	assistantText strings.Builder
	windowStarted time.Time
}

func newCodingEventAdapter(scope session.TurnScope, sink session.EventSink) (*codingEventAdapter, error) {
	if isNilDependency(sink) {
		return nil, errors.New("create coding event adapter: event sink is required")
	}
	return &codingEventAdapter{scope: scope, sink: sink}, nil
}

func (a *codingEventAdapter) PublishInvocationEvent(ctx context.Context, event InvocationEvent) error {
	if a == nil || isNilDependency(a.sink) {
		return errors.New("publish coding event: adapter is unavailable")
	}
	switch event.Kind {
	case InvocationEventAssistantText:
		if event.Text == "" {
			return nil
		}
		if a.windowStarted.IsZero() {
			a.windowStarted = time.Now()
		}
		a.assistantText.WriteString(event.Text)
		if time.Since(a.windowStarted) >= assistantEventWindow || a.assistantText.Len() >= maxToolEventSummaryBytes {
			return a.Flush(ctx)
		}
		return nil
	case InvocationEventToolStarted, InvocationEventToolFinished:
		if err := a.Flush(ctx); err != nil {
			return err
		}
		if event.Tool == nil || strings.TrimSpace(event.Tool.Name) == "" {
			return errors.New("publish coding event: tool metadata is missing")
		}
		kind := session.EventToolStarted
		if event.Kind == InvocationEventToolFinished {
			kind = session.EventToolCompleted
			if event.Tool.Status == tool.ResultFailed {
				kind = session.EventToolFailed
			}
		}
		return a.publish(ctx, session.Event{
			Kind: kind,
			Payload: session.EventPayload{Tool: &session.ToolEventPayload{
				Name: event.Tool.Name, Status: string(event.Tool.Status), Summary: event.Tool.Summary,
			}},
		})
	case InvocationEventInterrupted:
		if err := a.Flush(ctx); err != nil {
			return err
		}
		_, err := decodeApprovalRequest(a.scope, event.Interrupt)
		return err
	default:
		return fmt.Errorf("publish coding event: unsupported invocation event %q", event.Kind)
	}
}

// Flush publishes any pending assistant text before a lifecycle boundary.
func (a *codingEventAdapter) Flush(ctx context.Context) error {
	if a == nil || a.assistantText.Len() == 0 {
		return nil
	}
	value := a.assistantText.String()
	a.assistantText.Reset()
	a.windowStarted = time.Time{}
	return a.publish(ctx, session.Event{
		Kind:    session.EventAssistantDelta,
		Payload: session.EventPayload{Text: &session.TextEventPayload{Text: value}},
	})
}

func (a *codingEventAdapter) publish(ctx context.Context, event session.Event) error {
	event.SessionID = a.scope.SessionID
	event.TurnID = a.scope.TurnID
	return a.sink.Publish(ctx, event)
}

func decodeApprovalRequest(scope session.TurnScope, interrupt *InvocationInterrupt) (session.ApprovalRequest, error) {
	if interrupt == nil || strings.TrimSpace(interrupt.ID) == "" || interrupt.Kind != "approval" || len(interrupt.Payload) == 0 {
		return session.ApprovalRequest{}, errors.New("decode approval interrupt: metadata is invalid")
	}
	var payload approvalInterruptPayload
	if err := json.Unmarshal(interrupt.Payload, &payload); err != nil {
		return session.ApprovalRequest{}, errors.New("decode approval interrupt: payload is invalid")
	}
	if strings.TrimSpace(payload.RequestID) == "" || session.SessionID(payload.SessionID) != scope.SessionID || session.TurnID(payload.TurnID) != scope.TurnID || payload.CreatedAt.IsZero() {
		return session.ApprovalRequest{}, errors.New("decode approval interrupt: request does not match the active turn")
	}
	action := session.Action{
		ID:        "approval:" + payload.RequestID,
		SessionID: scope.SessionID,
		TurnID:    scope.TurnID,
		Kind:      payload.Kind,
		Summary:   payload.Summary,
	}
	switch payload.Kind {
	case session.ActionApplyPatch:
		if len(payload.Files) == 0 || payload.Command != nil {
			return session.ApprovalRequest{}, errors.New("decode approval interrupt: patch context is invalid")
		}
		action.Patch = &session.PatchAction{Files: append([]string(nil), payload.Files...)}
	case session.ActionRunCheck, session.ActionStartLanguageServer:
		if payload.Command == nil || strings.TrimSpace(payload.Command.Program) == "" || payload.Command.TimeoutMS <= 0 || len(payload.Files) != 0 {
			return session.ApprovalRequest{}, errors.New("decode approval interrupt: command context is invalid")
		}
		action.Command = &session.CommandAction{
			Program: payload.Command.Program,
			Args:    append([]string(nil), payload.Command.Args...),
			Timeout: time.Duration(payload.Command.TimeoutMS) * time.Millisecond,
		}
	default:
		return session.ApprovalRequest{}, errors.New("decode approval interrupt: action kind is unsupported")
	}
	return session.ApprovalRequest{
		ID:        session.ApprovalRequestID(payload.RequestID),
		SessionID: scope.SessionID,
		TurnID:    scope.TurnID,
		Action:    action,
		CreatedAt: payload.CreatedAt,
	}, nil
}
