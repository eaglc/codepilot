package codingagent

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/eaglc/codepilot/internal/agent"
	agentsession "github.com/eaglc/codepilot/internal/agent/session"
	"github.com/eaglc/codepilot/internal/llm"
	"github.com/eaglc/codepilot/internal/workflow"
)

func (s *Service) beginWorkflowLocked(ctx context.Context, product Session, turn Turn, expectedTurnRevision uint64, drift WorkspaceDrift, publishDrift bool) (TurnResult, error) {
	if s.deps.Workflows == nil {
		return TurnResult{}, errors.New("start Coding workflow: repository is unavailable")
	}
	plan, err := s.deps.Plans.LoadPlan(ctx, turn.PlanID, turn.PlanVersion)
	if err != nil || plan.Digest != turn.PlanDigest {
		return TurnResult{}, errors.New("start Coding workflow: exact approved Plan is unavailable")
	}
	compiled, err := CompilePlanWorkflowWithRegistry(plan, time.Now().UTC(), s.deps.Roles)
	if err != nil {
		return TurnResult{}, err
	}
	if compiled.Strategy == workflow.StrategyMultiAgentParallelReadOnly || compiled.Strategy == workflow.StrategyMultiAgentParallelIsolatedWrite {
		compiled.Budget.MaxConcurrency = min(compiled.Budget.MaxConcurrency, s.deps.MaxParallelAgents)
		if s.deps.Limits.MaxTotalTokens > 0 {
			compiled.Budget.MaxTotalTokens = s.deps.Limits.MaxTotalTokens
		}
		if s.deps.Limits.MaxCost > 0 {
			compiled.Budget.MaxCost = s.deps.Limits.MaxCost
		}
		if s.deps.Limits.MaxDuration > 0 {
			compiled.Budget.MaxDurationSeconds = int64((s.deps.Limits.MaxDuration + time.Second - 1) / time.Second)
		}
		if err := workflow.Validate(compiled); err != nil {
			return TurnResult{}, fmt.Errorf("start Coding workflow: configured parallel limits: %w", err)
		}
	}
	durable, err := s.deps.Workflows.LoadWorkflow(ctx, compiled.ID)
	if errors.Is(err, workflow.ErrNotFound) {
		if !s.features.Workflows {
			return TurnResult{}, errors.New("start Coding workflow: feature is disabled for new Workflows")
		}
		if createErr := s.deps.Workflows.CreateWorkflow(ctx, compiled); createErr != nil {
			return TurnResult{}, fmt.Errorf("start Coding workflow: persist metadata: %w", createErr)
		}
		durable = compiled
	} else if err != nil {
		return TurnResult{}, fmt.Errorf("start Coding workflow: load metadata: %w", err)
	} else if durable.OwnerID != compiled.OwnerID || durable.Plan != compiled.Plan || durable.Strategy != compiled.Strategy {
		return TurnResult{}, errors.New("start Coding workflow: durable identity conflicts with approved Plan")
	}
	turn.WorkflowID = string(durable.ID)
	if publishDrift {
		kind := EventPlanDriftDetected
		if err := s.publishPlanLifecycleEvent(context.WithoutCancel(ctx), product, turn, kind, &drift, turn.PlanReplan); err != nil {
			return TurnResult{}, fmt.Errorf("start Coding workflow: publish Plan workspace drift: %w", err)
		}
	}
	return s.continueWorkflowWithRevisionLocked(ctx, product, turn, durable, expectedTurnRevision, TurnResult{})
}

func (s *Service) continueWorkflowLocked(ctx context.Context, product Session, turn Turn, last TurnResult) (TurnResult, error) {
	if turn.WorkflowID == "" || s.deps.Workflows == nil {
		return last, errors.New("continue Coding workflow: durable Workflow is unavailable")
	}
	durable, err := s.deps.Workflows.LoadWorkflow(ctx, workflow.ID(turn.WorkflowID))
	if err != nil {
		return last, fmt.Errorf("continue Coding workflow: load state: %w", err)
	}
	return s.continueWorkflowWithRevisionLocked(ctx, product, turn, durable, turn.Revision, last)
}

func (s *Service) continueWorkflowWithRevisionLocked(ctx context.Context, product Session, turn Turn, durable workflow.Workflow, expectedTurnRevision uint64, last TurnResult) (TurnResult, error) {
	for transitions := 0; transitions < workflow.MaxNodes*4; transitions++ {
		if (durable.Strategy == workflow.StrategyMultiAgentParallelReadOnly || durable.Strategy == workflow.StrategyMultiAgentParallelIsolatedWrite) && durable.Status == workflow.StatusRunning {
			result, handled, parallelErr := s.continueParallelWorkflowLocked(ctx, product, turn, durable, last)
			if handled || parallelErr != nil {
				return result, parallelErr
			}
		}
		action, err := workflow.NextAction(durable)
		if err != nil {
			return last, fmt.Errorf("continue Coding workflow: schedule: %w", err)
		}
		switch action.Kind {
		case workflow.ActionStartWorkflow:
			durable, err = s.appendWorkflowTransition(ctx, durable, workflow.Event{Type: workflow.EventWorkflowStarted})
		case workflow.ActionRetryNode:
			durable, err = s.appendWorkflowTransition(ctx, durable, workflow.Event{Type: workflow.EventNodeRetryScheduled, NodeID: action.NodeID, Summary: action.Reason})
		case workflow.ActionFallbackNode:
			durable, err = s.appendWorkflowTransition(ctx, durable, workflow.Event{Type: workflow.EventNodeFallbackScheduled, NodeID: action.NodeID, Summary: action.Reason})
		case workflow.ActionBlockNode:
			durable, err = s.appendWorkflowTransition(ctx, durable, workflow.Event{Type: workflow.EventNodeBlocked, NodeID: action.NodeID, Summary: action.Reason})
		case workflow.ActionBlockWorkflow:
			durable, err = s.appendWorkflowTransition(ctx, durable, workflow.Event{Type: workflow.EventWorkflowBlocked, Summary: action.Reason})
			if err == nil {
				s.setState(product.ID, RuntimeInterrupted)
				last.Status, last.Reason = string(workflow.StatusBlocked), action.Reason
			}
			return last, err
		case workflow.ActionRequestReplan:
			durable, err = s.appendWorkflowTransition(ctx, durable, workflow.Event{Type: workflow.EventWorkflowReplanRequested, NodeID: action.NodeID, Summary: action.Reason})
			if err == nil {
				s.setState(product.ID, RuntimeInterrupted)
				last.Status, last.Reason = string(workflow.StatusNeedsReplan), action.Reason
			}
			return last, err
		case workflow.ActionFailWorkflow:
			durable, err = s.appendWorkflowTransition(ctx, durable, workflow.Event{Type: workflow.EventWorkflowFailed, NodeID: action.NodeID, Summary: action.Reason})
			if err != nil {
				return last, err
			}
			turn, err = s.finishWorkflowTurn(ctx, turn, TurnFailed, action.Reason)
			s.setState(product.ID, RuntimeIdle)
			last.Status, last.Reason = string(workflow.StatusFailed), action.Reason
			return last, err
		case workflow.ActionComplete:
			durable, err = s.appendWorkflowTransition(ctx, durable, workflow.Event{Type: workflow.EventWorkflowCompleted})
			if err != nil {
				return last, err
			}
			turn, err = s.finishWorkflowTurn(ctx, turn, TurnCompleted, "workflow_completed")
			s.setState(product.ID, RuntimeIdle)
			last.Status, last.Reason = string(workflow.StatusCompleted), "workflow_completed"
			return last, err
		case workflow.ActionStartNode:
			durable, err = s.appendWorkflowTransition(ctx, durable, workflow.Event{Type: workflow.EventNodeStarted, NodeID: action.NodeID})
			if err != nil {
				return last, err
			}
			return s.runWorkflowNodeLocked(ctx, product, turn, durable, action.NodeID, expectedTurnRevision)
		case workflow.ActionWait:
			if action.NodeID == "" {
				return last, nil
			}
			if binding, active := turn.ActiveRun(); active && binding.NodeID == NodeID(action.NodeID) {
				return last, nil
			}
			return s.runWorkflowNodeLocked(ctx, product, turn, durable, action.NodeID, expectedTurnRevision)
		case workflow.ActionNone:
			return last, nil
		default:
			return last, fmt.Errorf("continue Coding workflow: unsupported scheduler action %q", action.Kind)
		}
		if err != nil {
			return last, err
		}
	}
	return last, errors.New("continue Coding workflow: transition limit exceeded")
}

func (s *Service) runWorkflowNodeLocked(ctx context.Context, product Session, turn Turn, durable workflow.Workflow, nodeID workflow.NodeID, expectedTurnRevision uint64) (TurnResult, error) {
	node, found := workflowNode(durable, nodeID)
	if !found || node.Status != workflow.NodeRunning {
		return TurnResult{}, fmt.Errorf("run Coding workflow node %q: durable node is not running", nodeID)
	}
	if workflow.NodeExecutor(node) == workflow.ExecutorChild {
		return s.runWorkflowChildNodeLocked(ctx, product, turn, durable, node, expectedTurnRevision)
	}
	profile, err := s.nodeCapabilityProfile(node)
	if err != nil {
		return TurnResult{}, fmt.Errorf("run Coding workflow node %q: %w", nodeID, err)
	}
	continuation, ok := s.deps.Agent.(ContinuationRunner)
	if !ok {
		return TurnResult{}, errors.New("run Coding workflow node: Agent runner does not support continuation")
	}
	runIDValue, err := newID("run")
	if err != nil {
		return TurnResult{}, err
	}
	now := time.Now().UTC()
	turn.Phase = TurnPhaseExecuting
	turn.Status = TurnRunning
	turn.WorkflowID = string(durable.ID)
	turn.Runs = append(turn.Runs, RunBinding{RunID: agentsession.RunID(runIDValue), NodeID: NodeID(nodeID), Phase: TurnPhaseExecuting, Profile: profile, Status: RunBindingPending})
	turn.UpdatedAt = now
	turn.Revision = expectedTurnRevision + 1
	if err := s.deps.Turns.SaveTurn(ctx, turn, expectedTurnRevision); err != nil {
		return TurnResult{}, fmt.Errorf("run Coding workflow node %q: bind Run: %w", nodeID, err)
	}
	environment, err := s.prepareRunEnvironment(ctx, product, turn, RunID(runIDValue), NodeID(nodeID), profile)
	if err != nil {
		return TurnResult{}, fmt.Errorf("run Coding workflow node %q: %w", nodeID, err)
	}
	turn, err = s.markRunStarted(ctx, turn, agentsession.RunID(runIDValue), now)
	if err != nil {
		return TurnResult{}, fmt.Errorf("run Coding workflow node %q: %w", nodeID, err)
	}
	s.setState(product.ID, RuntimeRunning)
	runCtx, finishActive := s.beginActiveTurn(ctx, product.ID)
	defer finishActive()
	limits, err := workflowNodeRunLimits(s.deps.Limits, durable.Budget)
	if err != nil {
		return TurnResult{}, fmt.Errorf("run Coding workflow node %q: %w", nodeID, err)
	}
	result, runErr := continuation.Continue(runCtx, agent.ContinueRequest{
		SessionID: product.AgentSessionID, Lane: sessionLane(product), RunID: agentsession.RunID(runIDValue),
		SystemPrompt: environment.systemPrompt, Model: llm.ModelRef{Provider: product.ProviderProfileID, Model: product.ModelID},
		UntrustedContext: environment.untrustedContext, Tools: environment.tools, ToolCallPreviewer: environment.toolCallPreviewer, Limits: limits,
	}, environment.events)
	if result.RunID == "" {
		result.RunID = agentsession.RunID(runIDValue)
	}
	turn, refreshErr := s.refreshProductTurn(context.WithoutCancel(ctx), turn)
	if refreshErr == nil {
		turn, refreshErr = s.finishProductRun(context.WithoutCancel(ctx), turn, result, runErr)
	}
	productResult := productTurnResult(turn.ID, result)
	if refreshErr != nil {
		return productResult, refreshErr
	}
	if touchErr := s.touchSession(context.WithoutCancel(ctx), product); touchErr != nil {
		return productResult, touchErr
	}
	if result.Status == agent.RunInterrupted {
		s.setState(product.ID, RuntimeAwaitingApproval)
		return productResult, runErr
	}
	return s.advanceWorkflowAfterRunLocked(context.WithoutCancel(ctx), product, turn, result, runErr, productResult)
}

func (s *Service) advanceWorkflowAfterRunLocked(ctx context.Context, product Session, turn Turn, result agent.RunResult, runErr error, productResult TurnResult) (TurnResult, error) {
	if len(turn.Runs) == 0 || turn.Runs[len(turn.Runs)-1].NodeID == "" {
		return productResult, errors.New("advance Coding workflow: terminal Run has no node binding")
	}
	durable, err := s.deps.Workflows.LoadWorkflow(ctx, workflow.ID(turn.WorkflowID))
	if err != nil {
		return productResult, err
	}
	binding := turn.Runs[len(turn.Runs)-1]
	nodeID := workflow.NodeID(binding.NodeID)
	resultRef := "run:" + string(result.RunID)
	if binding.ChildAgentID != "" {
		resultRef = "child:" + string(binding.ChildAgentID)
	}
	node, found := workflowNode(durable, nodeID)
	if !found {
		return productResult, fmt.Errorf("advance Coding workflow: node %q is unavailable", nodeID)
	}
	if workflowTerminal(durable.Status) {
		return s.reconcileWorkflowTerminalTurn(ctx, product, turn, durable, productResult)
	}
	if node.Status != workflow.NodeRunning {
		outcomeRecorded := node.Status == workflow.NodeCompleted && result.Status == agent.RunCompleted && node.ResultRef == resultRef ||
			(node.Status == workflow.NodeFailed || node.Status == workflow.NodeBlocked) && (runErr != nil || result.Status == agent.RunFailed || result.Status == agent.RunLimitReached) ||
			node.Status == workflow.NodeCancelled && (result.Status == agent.RunAborted || errors.Is(runErr, context.Canceled))
		if !outcomeRecorded {
			return productResult, fmt.Errorf("advance Coding workflow: node %q status %q conflicts with Run status %q", nodeID, node.Status, result.Status)
		}
		return s.continueWorkflowLocked(ctx, product, turn, productResult)
	}
	switch {
	case result.Status == agent.RunAborted || errors.Is(runErr, context.Canceled):
		durable, err = s.appendWorkflowTransition(ctx, durable, workflow.Event{Type: workflow.EventWorkflowCancelled, NodeID: nodeID, Summary: "Workflow cancelled while a node was active.", AgentSteps: result.Steps})
		productResult.Status, productResult.Reason = string(workflow.StatusCancelled), "workflow_cancelled"
		s.setState(product.ID, RuntimeIdle)
		return productResult, err
	case runErr != nil || result.Status == agent.RunFailed || result.Status == agent.RunLimitReached:
		reason := result.Reason
		if reason == "" && runErr != nil {
			reason = runErr.Error()
		}
		if reason == "" {
			reason = "Agent Run failed"
		}
		durable, err = s.appendWorkflowTransition(ctx, durable, workflow.Event{Type: workflow.EventNodeFailed, NodeID: nodeID, Summary: boundedUTF8(redactSensitiveText(reason), 1024), AgentSteps: result.Steps})
	case result.Status == agent.RunCompleted:
		durable, err = s.appendWorkflowTransition(ctx, durable, workflow.Event{Type: workflow.EventNodeCompleted, NodeID: nodeID, ResultRef: resultRef, AgentSteps: result.Steps})
	case result.Status == agent.RunHandedOff:
		return productResult, nil
	default:
		return productResult, fmt.Errorf("advance Coding workflow: unsupported Run status %q", result.Status)
	}
	if err != nil {
		return productResult, err
	}
	return s.continueWorkflowLocked(ctx, product, turn, productResult)
}

func workflowNodeRunLimits(base agent.RunLimits, budget workflow.Budget) (agent.RunLimits, error) {
	remaining := budget.MaxAgentSteps - budget.UsedAgentSteps
	if remaining <= 0 {
		return agent.RunLimits{}, errors.New("parent Workflow Agent step budget is exhausted")
	}
	if base.MaxSteps <= 0 || base.MaxSteps > remaining {
		base.MaxSteps = remaining
	}
	return base, nil
}

func parallelWorkflowNodeRunLimits(base agent.RunLimits, budget workflow.Budget, count int) (agent.RunLimits, error) {
	if count <= 0 {
		return agent.RunLimits{}, errors.New("parallel Workflow requires at least one scheduled node")
	}
	remaining := budget.MaxAgentSteps - budget.UsedAgentSteps
	share := remaining / count
	if share <= 0 {
		return agent.RunLimits{}, errors.New("parent Workflow Agent step budget cannot cover the parallel runnable set")
	}
	if base.MaxSteps <= 0 || base.MaxSteps > share {
		base.MaxSteps = share
	}
	remainingTokens := budget.MaxTotalTokens - budget.UsedTotalTokens
	tokenShare := remainingTokens / count
	if tokenShare <= 0 {
		return agent.RunLimits{}, errors.New("parent Workflow token budget cannot cover the parallel runnable set")
	}
	if base.MaxTotalTokens <= 0 || base.MaxTotalTokens > tokenShare {
		base.MaxTotalTokens = tokenShare
	}
	if base.MaxOutputTokens <= 0 || base.MaxOutputTokens > tokenShare {
		base.MaxOutputTokens = tokenShare
	}
	remainingCost := budget.MaxCost - budget.UsedCost
	costShare := remainingCost / float64(count)
	if costShare <= 0 {
		return agent.RunLimits{}, errors.New("parent Workflow cost budget cannot cover the parallel runnable set")
	}
	if base.MaxCost <= 0 || base.MaxCost > costShare {
		base.MaxCost = costShare
	}
	return base, nil
}

func (s *Service) workflowRunLimitsForTurn(ctx context.Context, turn Turn) (agent.RunLimits, error) {
	if !isWorkflowStrategy(turn.Strategy) || turn.WorkflowID == "" {
		return s.deps.Limits, nil
	}
	durable, err := s.deps.Workflows.LoadWorkflow(ctx, workflow.ID(turn.WorkflowID))
	if err != nil {
		return agent.RunLimits{}, fmt.Errorf("load parent Workflow budget: %w", err)
	}
	return workflowNodeRunLimits(s.deps.Limits, durable.Budget)
}

func workflowTerminal(status workflow.Status) bool {
	return status == workflow.StatusCompleted || status == workflow.StatusCancelled || status == workflow.StatusFailed
}

func (s *Service) reconcileWorkflowTerminalTurn(ctx context.Context, product Session, turn Turn, durable workflow.Workflow, result TurnResult) (TurnResult, error) {
	var status TurnStatus
	switch durable.Status {
	case workflow.StatusCompleted:
		status, result.Status, result.Reason = TurnCompleted, string(workflow.StatusCompleted), "workflow_completed"
	case workflow.StatusCancelled:
		status, result.Status, result.Reason = TurnCancelled, string(workflow.StatusCancelled), "workflow_cancelled"
	case workflow.StatusFailed:
		status, result.Status, result.Reason = TurnFailed, string(workflow.StatusFailed), "workflow_failed"
	default:
		return result, fmt.Errorf("reconcile Coding workflow: status %q is not terminal", durable.Status)
	}
	if turn.Status != TurnCompleted && turn.Status != TurnCancelled && turn.Status != TurnFailed {
		if _, err := s.finishWorkflowTurn(ctx, turn, status, result.Reason); err != nil {
			return result, err
		}
	}
	s.setState(product.ID, RuntimeIdle)
	return result, nil
}

func (s *Service) appendWorkflowTransition(ctx context.Context, value workflow.Workflow, event workflow.Event) (workflow.Workflow, error) {
	if event.ID == "" {
		event.ID = fmt.Sprintf("event_%d_%s_%s", value.Revision+1, event.Type, event.NodeID)
	}
	if event.OccurredAt.IsZero() {
		event.OccurredAt = time.Now().UTC()
		if event.OccurredAt.Before(value.UpdatedAt) {
			event.OccurredAt = value.UpdatedAt
		}
	}
	next, err := s.deps.Workflows.AppendWorkflowEvent(ctx, value.ID, value.Revision, event)
	if err != nil {
		return value, fmt.Errorf("append Coding workflow %q transition %q: %w", value.ID, event.Type, err)
	}
	if err := s.publishWorkflowTransition(ctx, next, event); err != nil {
		return next, err
	}
	return next, nil
}

func (s *Service) finishWorkflowTurn(ctx context.Context, turn Turn, status TurnStatus, reason string) (Turn, error) {
	if status != TurnCompleted && status != TurnCancelled && status != TurnFailed {
		return turn, fmt.Errorf("finish Coding workflow: unsupported Turn status %q", status)
	}
	now := time.Now().UTC()
	expected := turn.Revision
	turn.Status = status
	turn.UpdatedAt = now
	turn.CompletedAt = now
	if len(turn.Runs) != 0 && reason != "" {
		turn.Runs[len(turn.Runs)-1].Reason = reason
	}
	turn.Revision++
	if err := s.deps.Turns.SaveTurn(ctx, turn, expected); err != nil {
		return turn, err
	}
	return turn, nil
}

func workflowNode(value workflow.Workflow, id workflow.NodeID) (workflow.Node, bool) {
	for _, node := range value.Nodes {
		if node.ID == id {
			return node, true
		}
	}
	return workflow.Node{}, false
}
