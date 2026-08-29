package codingagent

import (
	"context"
	"time"
)

func (s *Service) publishPlanEntryEvent(ctx context.Context, product Session, turn Turn, suggestion PlanEntrySuggestion, kind EventKind, decision string) error {
	id, err := newID("event")
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.eventSeq++
	sequence := s.eventSeq
	s.mu.Unlock()
	return s.deps.Events.PublishCodingEvent(ctx, Event{
		ID: id, Sequence: sequence, SessionID: product.ID, TurnID: turn.ID,
		Timestamp: time.Now().UTC(), Kind: kind,
		Payload: EventPayload{PlanEntry: &PlanEntryEvent{
			ReasonCode: suggestion.ReasonCode,
			Summary:    boundedUTF8(redactSensitiveText(suggestion.Summary), maxPlanEntrySummaryBytes),
			Digest:     suggestion.Digest, Decision: decision,
		}},
	})
}
