package codingagent

import (
	"context"
	"errors"
	"fmt"

	agentsession "github.com/eaglc/codepilot/internal/agent/session"
)

// productRevisionSource keeps event revisions monotonic across the parent
// journal and every independent child journal owned by the Product Session.
type productRevisionSource struct{ service *Service }

func (source productRevisionSource) CurrentRevision(ctx context.Context, sessionID SessionID) (uint64, error) {
	if source.service == nil {
		return 0, errors.New("load product event revision: service is unavailable")
	}
	product, err := source.service.deps.Sessions.LoadSession(ctx, sessionID)
	if err != nil {
		return 0, fmt.Errorf("load product event revision: %w", err)
	}
	durable, err := source.service.deps.AgentSessions.Load(ctx, product.AgentSessionID)
	if err != nil {
		return 0, fmt.Errorf("load product event revision: %w", err)
	}
	revision := lastJournalSequence(durable)
	if source.service.deps.Children == nil || source.service.deps.Turns == nil {
		return revision, nil
	}
	turns, err := source.service.deps.Turns.ListTurns(ctx, sessionID)
	if err != nil {
		return 0, fmt.Errorf("load product event revision: %w", err)
	}
	for _, turn := range turns {
		children, listErr := source.service.deps.Children.ListChildAgents(ctx, turn.ID)
		if listErr != nil {
			return 0, fmt.Errorf("load product event revision: %w", listErr)
		}
		for _, child := range children {
			childDurable, loadErr := source.service.deps.AgentSessions.Load(ctx, child.AgentSessionID)
			if errors.Is(loadErr, agentsession.ErrNotFound) && child.Status == ChildAgentCreating {
				continue
			}
			if loadErr != nil {
				return 0, fmt.Errorf("load product event revision: %w", loadErr)
			}
			revision += lastJournalSequence(childDurable)
		}
	}
	return revision, nil
}

func lastJournalSequence(snapshot agentsession.Snapshot) uint64 {
	if len(snapshot.Log) == 0 {
		return 0
	}
	return snapshot.Log[len(snapshot.Log)-1].Sequence
}
