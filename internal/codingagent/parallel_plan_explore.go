package codingagent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/eaglc/codepilot/internal/agent"
)

// runParallelPlanExploreChildrenLocked executes one durable, independent
// read-only wave and returns every structured result to the parent planner.
func (s *Service) runParallelPlanExploreChildrenLocked(ctx context.Context, product Session, turn Turn) (TurnResult, error) {
	if s.deps.Children == nil || len(turn.PendingPlanExploreIDs) < 2 || len(turn.PendingPlanExploreIDs) > maxParallelPlanExplorations {
		return TurnResult{}, errors.New("run parallel Plan explorations: repository or durable wave is unavailable")
	}
	limits, err := parallelPlanRunLimits(s.deps.Limits, len(turn.PendingPlanExploreIDs))
	if err != nil {
		return TurnResult{}, err
	}
	executions := make([]parallelNodeExecution, 0, len(turn.PendingPlanExploreIDs))
	for _, childID := range turn.PendingPlanExploreIDs {
		child, loadErr := s.deps.Children.LoadChildAgent(ctx, childID)
		if loadErr != nil {
			return TurnResult{}, fmt.Errorf("run parallel Plan exploration %q: %w", childID, loadErr)
		}
		if child.Kind != ChildAgentPlanExplore || child.ParentSessionID != product.ID || child.ParentTurnID != turn.ID || child.Profile != CapabilityExplore {
			return TurnResult{}, fmt.Errorf("run parallel Plan exploration %q: durable identity is inconsistent", childID)
		}
		child, loadErr = s.ensureChildAgentReady(ctx, product, child)
		if loadErr != nil {
			return TurnResult{}, loadErr
		}
		turn, loadErr = s.bindParallelPlanRun(ctx, turn, child)
		if loadErr != nil {
			return TurnResult{}, loadErr
		}
		if child.Status == ChildAgentReady {
			turn, loadErr = s.markParallelRunStarted(ctx, turn, child.RunID)
			if loadErr != nil {
				return TurnResult{}, loadErr
			}
			child, loadErr = s.transitionChild(ctx, child, ChildAgentRunning, nil, "")
			if loadErr != nil {
				return TurnResult{}, loadErr
			}
		}
		environment, loadErr := s.prepareChildRunEnvironment(ctx, product, turn, child)
		if loadErr != nil {
			return TurnResult{}, loadErr
		}
		message, loadErr := childTaskMessage(child)
		if loadErr != nil {
			return TurnResult{}, loadErr
		}
		executions = append(executions, parallelNodeExecution{child: child, environment: environment, message: message})
	}

	s.setState(product.ID, RuntimeRunning)
	runCtx, finishActive := s.beginActiveTurn(ctx, product.ID)
	var wait sync.WaitGroup
	for index := range executions {
		if executions[index].child.Status == ChildAgentCompleted || executions[index].child.Status == ChildAgentFailed || executions[index].child.Status == ChildAgentCancelled {
			executions[index].result = terminalChildRunResult(executions[index].child)
			executions[index].result, executions[index].err = childWorkflowOutcome(executions[index].child, executions[index].result, nil)
			continue
		}
		wait.Add(1)
		go func(execution *parallelNodeExecution) {
			defer wait.Done()
			execution.result, execution.err = s.runParallelChild(runCtx, product, *execution, limits)
		}(&executions[index])
	}
	wait.Wait()
	finishActive()

	cancelled := false
	last := TurnResult{}
	for index := range executions {
		result, runErr := executions[index].result, executions[index].err
		latest, loadErr := s.deps.Turns.LoadTurn(context.WithoutCancel(ctx), turn.ID)
		if loadErr != nil {
			return last, loadErr
		}
		latest, loadErr = s.finishProductRun(context.WithoutCancel(ctx), latest, result, runErr)
		if loadErr != nil {
			return last, fmt.Errorf("finish parallel Plan child Run %q: %w", result.RunID, loadErr)
		}
		turn = latest
		last = productTurnResult(turn.ID, result)
		if result.Status == agent.RunAborted || errors.Is(runErr, context.Canceled) {
			cancelled = true
		}
	}
	if cancelled {
		turn, err = s.cancelProductTurn(context.WithoutCancel(ctx), turn, "parallel_plan_exploration_cancelled")
		if err != nil {
			return last, err
		}
		s.setState(product.ID, RuntimeIdle)
		last.Status, last.Reason = string(TurnCancelled), "parallel_plan_exploration_cancelled"
		return last, nil
	}
	expected := turn.Revision
	turn.PendingPlanExploreIDs = nil
	turn.UpdatedAt = time.Now().UTC()
	turn.Revision++
	if err := s.deps.Turns.SaveTurn(context.WithoutCancel(ctx), turn, expected); err != nil {
		return last, fmt.Errorf("finish parallel Plan explorations: clear parent wave: %w", err)
	}
	if err := s.touchSession(context.WithoutCancel(ctx), product); err != nil {
		return last, err
	}
	s.setState(product.ID, RuntimeRunning)
	return s.continueTurnLocked(context.WithoutCancel(ctx), product, turn)
}

func (s *Service) bindParallelPlanRun(ctx context.Context, turn Turn, child ChildAgent) (Turn, error) {
	latest, err := s.deps.Turns.LoadTurn(ctx, turn.ID)
	if err != nil {
		return turn, err
	}
	if _, found := latest.Run(child.RunID); found {
		return latest, nil
	}
	expected := latest.Revision
	latest.Runs = append(latest.Runs, RunBinding{
		RunID: child.RunID, ChildAgentID: child.ID, Phase: TurnPhasePlanning,
		Profile: CapabilityExplore, Status: RunBindingPending,
	})
	latest.UpdatedAt = time.Now().UTC()
	latest.Revision++
	if err := s.deps.Turns.SaveTurn(ctx, latest, expected); err != nil {
		return turn, fmt.Errorf("bind parallel Plan child Agent %q: %w", child.ID, err)
	}
	return latest, nil
}

func parallelPlanRunLimits(base agent.RunLimits, count int) (agent.RunLimits, error) {
	if count < 2 || count > maxParallelPlanExplorations {
		return agent.RunLimits{}, errors.New("parallel Plan exploration count is outside its product limit")
	}
	if base.MaxSteps > 0 {
		base.MaxSteps = max(1, base.MaxSteps/count)
	}
	if base.MaxTotalTokens > 0 {
		base.MaxTotalTokens = max(1, base.MaxTotalTokens/count)
	}
	if base.MaxOutputTokens > 0 {
		base.MaxOutputTokens = max(1, base.MaxOutputTokens/count)
	}
	if base.MaxCost > 0 {
		base.MaxCost /= float64(count)
	}
	return base, nil
}
