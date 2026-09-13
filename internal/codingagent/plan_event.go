package codingagent

import (
	"context"
	"time"

	"github.com/eaglc/codepilot/internal/agent"
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

func (s *Service) publishPlanLifecycleEvent(ctx context.Context, product Session, turn Turn, kind EventKind, drift *WorkspaceDrift, replan *PlanReplanRequest) error {
	id, err := newID("event")
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.eventSeq++
	sequence := s.eventSeq
	s.mu.Unlock()
	event := &PlanEvent{PlanID: turn.PlanID, Version: turn.PlanVersion, Digest: turn.PlanDigest}
	if drift != nil {
		event.Drift = drift.Severity
		event.Reason = boundedUTF8(drift.Reason, 256)
		event.Summary = boundedUTF8(redactSensitiveText(drift.Summary), 2048)
		event.Paths = append([]string(nil), drift.Paths...)
	}
	if replan != nil {
		event.ReplanReason = replan.ReasonCode
		event.Reason = string(replan.ReasonCode)
		event.Summary = boundedUTF8(redactSensitiveText(replan.Summary), maxPlanReplanSummaryBytes)
		event.Decision = replan.Decision
	}
	return s.deps.Events.PublishCodingEvent(ctx, Event{
		ID: id, Sequence: sequence, SessionID: product.ID, TurnID: turn.ID,
		Timestamp: time.Now().UTC(), Kind: kind, Payload: EventPayload{Plan: event},
	})
}

func (s *Service) publishPendingPlanReplanEvent(ctx context.Context, product Session, turn Turn, result agent.RunResult) error {
	if result.Status != agent.RunInterrupted || turn.Phase != TurnPhaseNeedsReplan || turn.PlanReplan == nil || turn.PlanReplan.Decision != "" {
		return nil
	}
	return s.publishPlanLifecycleEvent(ctx, product, turn, EventPlanReplanRequested, turn.WorkspaceDrift, turn.PlanReplan)
}
