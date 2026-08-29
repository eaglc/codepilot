package codingagent

import (
	"context"
	"time"
)

func (s *Service) publishPlanEvent(ctx context.Context, product Session, turn Turn, kind EventKind, decision string) error {
	id, err := newID("event")
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.eventSeq++
	sequence := s.eventSeq
	s.mu.Unlock()
	event := &PlanEvent{PlanID: turn.PlanID, Version: turn.PlanVersion, Digest: turn.PlanDigest, Decision: decision}
	if turn.PlanID != "" && s.deps.Plans != nil {
		if plan, loadErr := s.deps.Plans.LoadPlan(ctx, turn.PlanID, turn.PlanVersion); loadErr == nil && plan.Digest == turn.PlanDigest {
			event.Goal = boundedUTF8(redactSensitiveText(plan.Goal), maxPlanTextBytes)
		}
	}
	return s.deps.Events.PublishCodingEvent(ctx, Event{
		ID: id, Sequence: sequence, SessionID: product.ID, TurnID: turn.ID,
		Timestamp: time.Now().UTC(), Kind: kind, Payload: EventPayload{Plan: event},
	})
}
