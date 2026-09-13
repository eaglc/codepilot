package codingagent

import (
	"context"
	"fmt"

	"github.com/eaglc/codepilot/internal/workflow"
)

func (s *Service) publishWorkflowTransition(ctx context.Context, value workflow.Workflow, transition workflow.Event) error {
	turn, err := s.deps.Turns.LoadTurn(ctx, TurnID(value.OwnerID))
	if err != nil {
		return fmt.Errorf("publish Coding workflow event: load Product Turn: %w", err)
	}
	kind := EventKind("")
	switch transition.Type {
	case workflow.EventWorkflowStarted:
		kind = EventWorkflowStarted
	case workflow.EventNodeStarted:
		kind = EventWorkflowNodeStarted
	case workflow.EventNodeCompleted:
		kind = EventWorkflowNodeCompleted
	case workflow.EventNodeFailed:
		kind = EventWorkflowNodeFailed
	case workflow.EventNodeRetryScheduled:
		kind = EventWorkflowNodeRetrying
	case workflow.EventNodeFallbackScheduled:
		kind = EventWorkflowNodeRetrying
	case workflow.EventNodeBlocked, workflow.EventWorkflowBlocked:
		kind = EventWorkflowBlocked
	case workflow.EventWorkflowReplanRequested:
		kind = EventWorkflowReplanRequested
	case workflow.EventWorkflowCompleted:
		kind = EventWorkflowCompleted
	case workflow.EventWorkflowCancelled:
		kind = EventWorkflowCancelled
	case workflow.EventWorkflowFailed:
		kind = EventWorkflowFailed
	default:
		return nil
	}
	id, err := newID("event")
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.eventSeq++
	sequence := s.eventSeq
	s.mu.Unlock()
	payload := &WorkflowEvent{WorkflowID: string(value.ID), Status: string(value.Status), NodeID: NodeID(transition.NodeID), Summary: boundedUTF8(redactSensitiveText(transition.Summary), 2048)}
	if node, found := workflowNode(value, transition.NodeID); found {
		payload.NodeStatus = string(node.Status)
		payload.Role = string(node.Role)
		payload.Executor = string(workflow.NodeExecutor(node))
		payload.Attempts = node.Attempts
		if workflow.NodeExecutor(node) == workflow.ExecutorChild {
			payload.ChildAgentID = WorkflowChildAgentID(string(value.ID), NodeID(node.ID), node.Attempts)
		}
	}
	return s.deps.Events.PublishCodingEvent(ctx, Event{
		ID: id, Sequence: sequence, SessionID: turn.SessionID, TurnID: turn.ID, NodeID: NodeID(transition.NodeID),
		Timestamp: transition.OccurredAt, Kind: kind, Payload: EventPayload{Workflow: payload},
	})
}
