package codingagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/eaglc/codepilot/internal/agent"
	agentsession "github.com/eaglc/codepilot/internal/agent/session"
	"github.com/eaglc/codepilot/internal/llm"
	"github.com/eaglc/codepilot/internal/tool"
)

// RecoverTurn applies one explicit product decision and, unless that decision
// abandons the turn, continues the same durable Agent run.
func (s *Service) RecoverTurn(ctx context.Context, request RecoverTurnRequest) (TurnResult, error) {
	if request.SessionID == "" || request.TurnID == "" || strings.TrimSpace(request.ActionID) == "" {
		return TurnResult{}, errors.New("recover Coding Agent turn: session, turn, and action ids are required")
	}
	decision, err := agentRecoveryDecision(request.Decision)
	if err != nil {
		return TurnResult{}, err
	}
	operation := s.operationLock(request.SessionID)
	operation.Lock()
	defer operation.Unlock()
	turn := Turn{}
	legacy := !s.features.ProductTurns
	if !legacy {
		turn, err = s.deps.Turns.LoadTurn(ctx, request.TurnID)
		legacy = errors.Is(err, ErrTurnNotFound)
	}
	if err != nil && !legacy {
		return TurnResult{}, fmt.Errorf("recover Coding Agent turn: load Product Turn: %w", err)
	}
	if legacy {
		turn = Turn{ID: request.TurnID, SessionID: request.SessionID}
	}
	if !legacy && turn.SessionID != request.SessionID {
		return TurnResult{}, errors.New("recover Coding Agent turn: Product Turn belongs to another session")
	}
	binding, found := turn.ActiveRun()
	if legacy {
		binding, found = RunBinding{RunID: agentsession.RunID(request.TurnID)}, true
	}
	if !found {
		return TurnResult{}, errors.New("recover Coding Agent turn: Product Turn has no active Run")
	}
	environment, err := s.prepareRecovery(ctx, request.SessionID, turn.ID, binding.RunID)
	if err != nil {
		return TurnResult{}, err
	}
	s.setState(request.SessionID, RuntimeRunning)
	runCtx, finishActive := s.beginActiveTurn(ctx, request.SessionID)
	defer finishActive()
	limits, err := s.workflowRunLimitsForTurn(ctx, turn)
	if err != nil {
		return TurnResult{}, fmt.Errorf("recover Coding Agent turn: %w", err)
	}
	result, recoverErr := s.deps.Agent.Recover(runCtx, agent.RecoverRequest{
		SessionID: environment.agentSessionID, Lane: environment.lane,
		RunID: binding.RunID, ActionID: request.ActionID, Decision: decision, ContinueRun: true,
		SystemPrompt:     environment.systemPrompt,
		Model:            llm.ModelRef{Provider: environment.product.ProviderProfileID, Model: environment.product.ModelID},
		UntrustedContext: environment.untrustedContext, Tools: environment.tools, Limits: limits,
	}, environment.events)
	if result.RunID == "" {
		result.RunID = binding.RunID
	}
	var finishErr error
	if legacy {
		s.refreshRecoveryState(ctx, environment.product)
	} else {
		turn, finishErr = s.refreshProductTurn(context.WithoutCancel(ctx), turn)
		if finishErr == nil && environment.child != nil {
			turn, _, result, recoverErr, finishErr = s.finishChildProductRun(context.WithoutCancel(ctx), turn, *environment.child, result, recoverErr)
		} else if finishErr == nil {
			turn, finishErr = s.finishProductRun(context.WithoutCancel(ctx), turn, result, recoverErr)
		}
		s.setState(environment.product.ID, runtimeStateForTurn(turn))
	}
	touchErr := s.touchSession(context.WithoutCancel(ctx), environment.product)
	productResult := productTurnResult(turn.ID, result)
	if !legacy && environment.child != nil && environment.child.Kind == ChildAgentPlanExplore && result.Status != agent.RunInterrupted && finishErr == nil {
		if touchErr != nil {
			return productResult, fmt.Errorf("recover Coding Agent turn: update product session: %w", touchErr)
		}
		child, loadErr := s.deps.Children.LoadChildAgent(ctx, environment.child.ID)
		if loadErr != nil {
			return productResult, loadErr
		}
		return s.finishPlanExploreAndContinue(context.WithoutCancel(ctx), environment.product, turn, child)
	}
	if !legacy && isWorkflowStrategy(turn.Strategy) && binding.NodeID != "" && result.Status != agent.RunInterrupted {
		if touchErr != nil {
			return productResult, fmt.Errorf("recover Coding Agent turn: update product session: %w", touchErr)
		}
		return s.advanceWorkflowAfterRunLocked(context.WithoutCancel(ctx), environment.product, turn, result, recoverErr, productResult)
	}
	if recoverErr != nil {
		return productResult, fmt.Errorf("recover Coding Agent turn: %w", recoverErr)
	}
	if finishErr != nil {
		return productResult, fmt.Errorf("recover Coding Agent turn: persist Product Turn: %w", finishErr)
	}
	if touchErr != nil {
		return productResult, fmt.Errorf("recover Coding Agent turn: update product session: %w", touchErr)
	}
	if !legacy {
		if err := s.publishPendingPlanReplanEvent(context.WithoutCancel(ctx), environment.product, turn, result); err != nil {
			return productResult, fmt.Errorf("recover Coding Agent turn: publish Plan replan request: %w", err)
		}
	}
	return productResult, nil
}

// RecoverAutomatically is the startup RecoveryCoordinator. It repeatedly
// rebuilds the plan and applies only actions explicitly marked automatic by the
// Agent layer. It stops before continuing model conversation or making a human
// decision.
func (s *Service) RecoverAutomatically(ctx context.Context, sessionID SessionID) (int, error) {
	if sessionID == "" {
		return 0, errors.New("coordinate Coding Agent recovery: session id is required")
	}
	operation := s.operationLock(sessionID)
	operation.Lock()
	defer operation.Unlock()
	product, err := s.deps.Sessions.LoadSession(ctx, sessionID)
	if err != nil {
		return 0, fmt.Errorf("coordinate Coding Agent recovery: load session: %w", err)
	}
	completed := 0
	if s.features.ProductTurns {
		completed, err = s.reconcileProductTurns(ctx, product)
		if err != nil {
			return completed, err
		}
	}
	for completed < 32 {
		targetSessionID, targetLane := product.AgentSessionID, sessionLane(product)
		targetTurn, targetFound := Turn{}, false
		if s.features.ProductTurns && s.deps.Children != nil {
			child, childTurn, found, childErr := s.activeChildForSession(ctx, product.ID)
			if childErr != nil {
				return completed, fmt.Errorf("coordinate Coding Agent recovery: locate active child Agent: %w", childErr)
			}
			if found {
				targetSessionID, targetLane, targetTurn, targetFound = child.AgentSessionID, agentsession.MainLane, childTurn, true
			}
		}
		durable, err := s.deps.AgentSessions.Load(ctx, targetSessionID)
		if err != nil {
			return completed, fmt.Errorf("coordinate Coding Agent recovery: load Agent session: %w", err)
		}
		plan := agentsession.BuildRecoveryPlan(durable)
		var action *agentsession.RecoveryAction
		for index := range plan.Actions {
			if plan.Actions[index].Automatic {
				action = &plan.Actions[index]
				break
			}
		}
		if action == nil {
			s.setState(sessionID, recoveryState(plan, agentsession.AnalyzeRecovery(durable)))
			if completed != 0 {
				if err := s.touchSession(ctx, product); err != nil {
					return completed, fmt.Errorf("coordinate Coding Agent recovery: update product session: %w", err)
				}
			}
			return completed, nil
		}
		turn, found := targetTurn, targetFound
		if s.features.ProductTurns && !found {
			turn, found, err = s.turnForRun(ctx, sessionID, action.RunID)
			if err != nil {
				return completed, fmt.Errorf("coordinate Coding Agent recovery: resolve Product Turn: %w", err)
			}
		}
		turnID := TurnID(action.RunID)
		if found {
			turnID = turn.ID
		}
		environment, err := s.prepareRecovery(ctx, sessionID, turnID, action.RunID)
		if err != nil {
			return completed, err
		}
		recoveryLimits := s.deps.Limits
		if found {
			recoveryLimits, err = s.workflowRunLimitsForTurn(ctx, turn)
			if err != nil {
				return completed, fmt.Errorf("coordinate Coding Agent recovery action %q: %w", action.ID, err)
			}
		}
		s.setState(sessionID, RuntimeRunning)
		result, err := s.deps.Agent.Recover(ctx, agent.RecoverRequest{
			SessionID: environment.agentSessionID, Lane: targetLane, RunID: action.RunID, ActionID: action.ID,
			Automatic: true, ContinueRun: false, SystemPrompt: environment.systemPrompt,
			Model:            llm.ModelRef{Provider: environment.product.ProviderProfileID, Model: environment.product.ModelID},
			UntrustedContext: environment.untrustedContext, Tools: environment.tools, Limits: recoveryLimits,
		}, environment.events)
		if result.RunID == "" {
			result.RunID = action.RunID
		}
		if err != nil {
			s.setState(sessionID, RuntimeInterrupted)
			return completed, fmt.Errorf("coordinate Coding Agent recovery action %q: %w", action.ID, err)
		}
		if found {
			turn, finishErr := s.refreshProductTurn(context.WithoutCancel(ctx), turn)
			if finishErr == nil && environment.child != nil {
				turn, _, result, err, finishErr = s.finishChildProductRun(context.WithoutCancel(ctx), turn, *environment.child, result, nil)
			} else if finishErr == nil {
				turn, finishErr = s.finishProductRun(context.WithoutCancel(ctx), turn, result, nil)
			}
			if finishErr != nil {
				return completed, fmt.Errorf("coordinate Coding Agent recovery action %q: persist Product Turn: %w", action.ID, finishErr)
			}
			if err := s.publishPendingPlanReplanEvent(context.WithoutCancel(ctx), product, turn, result); err != nil {
				return completed, fmt.Errorf("coordinate Coding Agent recovery action %q: publish Plan replan request: %w", action.ID, err)
			}
			if isWorkflowStrategy(turn.Strategy) && workflowRunBinding(turn, result.RunID) != nil && result.Status != agent.RunInterrupted {
				if _, advanceErr := s.advanceWorkflowAfterRunLocked(context.WithoutCancel(ctx), product, turn, result, err, productTurnResult(turn.ID, result)); advanceErr != nil {
					return completed, fmt.Errorf("coordinate Coding Agent recovery action %q: advance Workflow: %w", action.ID, advanceErr)
				}
				if refreshed, loadErr := s.deps.Turns.LoadTurn(ctx, turn.ID); loadErr == nil {
					turn = refreshed
				}
			}
			if environment.child != nil && environment.child.Kind == ChildAgentPlanExplore && result.Status != agent.RunInterrupted {
				child, loadErr := s.deps.Children.LoadChildAgent(ctx, environment.child.ID)
				if loadErr != nil {
					return completed, loadErr
				}
				if _, continueErr := s.finishPlanExploreAndContinue(context.WithoutCancel(ctx), product, turn, child); continueErr != nil {
					return completed, fmt.Errorf("coordinate Coding Agent recovery action %q: continue after Plan exploration: %w", action.ID, continueErr)
				}
				if refreshed, loadErr := s.deps.Turns.LoadTurn(ctx, turn.ID); loadErr == nil {
					turn = refreshed
				}
			}
			s.setState(sessionID, runtimeStateForTurn(turn))
		}
		completed++
		if result.Interrupt != nil {
			s.setState(sessionID, RuntimeAwaitingApproval)
			return completed, nil
		}
	}
	s.setState(sessionID, RuntimeInterrupted)
	return completed, errors.New("coordinate Coding Agent recovery: action limit exceeded")
}

// reconcileProductTurns closes durable gaps around the Product Turn boundary.
// A pending binding with no Agent operation is safe to replay with its original
// RunID/UserEntryID; a finished Agent operation is projected without rerunning
// any model or Tool side effect.
func (s *Service) reconcileProductTurns(ctx context.Context, product Session) (int, error) {
	turns, err := s.deps.Turns.ListTurns(ctx, product.ID)
	if err != nil {
		return 0, fmt.Errorf("coordinate Coding Agent recovery: list Product Turns: %w", err)
	}
	durable, err := s.deps.AgentSessions.Load(ctx, product.AgentSessionID)
	if err != nil {
		return 0, fmt.Errorf("coordinate Coding Agent recovery: load Agent session: %w", err)
	}
	globalRecovery := agentsession.AnalyzeRecovery(durable)
	if len(globalRecovery.PendingRuns) != 0 || len(globalRecovery.PendingInterrupts) != 0 || len(globalRecovery.PendingTools) != 0 {
		return 0, nil
	}
	completed := 0
	for _, turn := range turns {
		if len(turn.PendingPlanExploreIDs) != 0 && turn.Status == TurnRunning && turn.Phase == TurnPhasePlanning {
			if _, continueErr := s.runParallelPlanExploreChildrenLocked(ctx, product, turn); continueErr != nil {
				return completed, fmt.Errorf("coordinate Coding Agent recovery: continue parallel Plan explorations: %w", continueErr)
			}
			completed++
			continue
		}
		binding, active := turn.ActiveRun()
		if !active {
			if turn.PendingPlanExploreID != "" && len(turn.Runs) != 0 {
				latest := turn.Runs[len(turn.Runs)-1]
				if latest.ChildAgentID == turn.PendingPlanExploreID && latest.Profile == CapabilityExplore && (latest.Status == RunBindingCompleted || latest.Status == RunBindingFailed || latest.Status == RunBindingCancelled) {
					child, loadErr := s.deps.Children.LoadChildAgent(ctx, latest.ChildAgentID)
					if loadErr != nil {
						return completed, loadErr
					}
					if _, continueErr := s.finishPlanExploreAndContinue(ctx, product, turn, child); continueErr != nil {
						return completed, fmt.Errorf("coordinate Coding Agent recovery: continue after Plan exploration: %w", continueErr)
					}
					completed++
					continue
				}
			}
			if isWorkflowStrategy(turn.Strategy) && turn.Phase == TurnPhaseExecuting && len(turn.Runs) != 0 && turn.Runs[len(turn.Runs)-1].NodeID != "" {
				latest := turn.Runs[len(turn.Runs)-1]
				if latest.Status == RunBindingCompleted || latest.Status == RunBindingFailed || latest.Status == RunBindingCancelled {
					status := agent.RunCompleted
					var runErr error
					if latest.Status == RunBindingFailed {
						status, runErr = agent.RunFailed, errors.New(latest.Reason)
					} else if latest.Status == RunBindingCancelled {
						status = agent.RunAborted
					}
					result := agent.RunResult{RunID: latest.RunID, Status: status, Steps: latest.Steps, Reason: latest.Reason, TerminalOutput: append(json.RawMessage(nil), latest.TerminalOutput...)}
					if latest.ChildAgentID != "" {
						child, loadErr := s.deps.Children.LoadChildAgent(ctx, latest.ChildAgentID)
						if loadErr != nil {
							return completed, fmt.Errorf("coordinate Coding Agent recovery: load child Agent %q: %w", latest.ChildAgentID, loadErr)
						}
						result, runErr = childWorkflowOutcome(child, result, runErr)
					}
					if _, advanceErr := s.advanceWorkflowAfterRunLocked(ctx, product, turn, result, runErr, productTurnResult(turn.ID, result)); advanceErr != nil {
						return completed, fmt.Errorf("coordinate Coding Agent recovery: advance Workflow Turn %q: %w", turn.ID, advanceErr)
					}
					completed++
					continue
				}
			}
			if len(turn.Runs) != 0 && turn.Status == TurnRunning && turn.Runs[len(turn.Runs)-1].Status == RunBindingHandedOff {
				if _, continueErr := s.continueTurnLocked(ctx, product, turn); continueErr != nil {
					return completed, fmt.Errorf("coordinate Coding Agent recovery: continue handed-off Product Turn %q: %w", turn.ID, continueErr)
				}
				completed++
			}
			continue
		}
		runDurable := durable
		var runChild *ChildAgent
		if binding.ChildAgentID != "" {
			if s.deps.Children == nil {
				return completed, errors.New("coordinate Coding Agent recovery: child Agent repository is unavailable")
			}
			child, loadErr := s.deps.Children.LoadChildAgent(ctx, binding.ChildAgentID)
			if loadErr != nil {
				return completed, fmt.Errorf("coordinate Coding Agent recovery: load child Agent %q: %w", binding.ChildAgentID, loadErr)
			}
			if child.ParentSessionID != product.ID || child.ParentTurnID != turn.ID || child.RunID != binding.RunID {
				return completed, errors.New("coordinate Coding Agent recovery: child Agent identity conflicts with parent Run")
			}
			runChild = &child
			runDurable, loadErr = s.deps.AgentSessions.Load(ctx, child.AgentSessionID)
			if errors.Is(loadErr, agentsession.ErrNotFound) && child.Status == ChildAgentCreating {
				ready, readyErr := s.ensureChildAgentReady(ctx, product, child)
				if readyErr != nil {
					return completed, readyErr
				}
				runChild = &ready
				runDurable, loadErr = s.deps.AgentSessions.Load(ctx, ready.AgentSessionID)
			}
			if loadErr != nil {
				return completed, fmt.Errorf("coordinate Coding Agent recovery: load child Agent session: %w", loadErr)
			}
			childRecovery := agentsession.AnalyzeRecovery(runDurable)
			if len(childRecovery.PendingRuns) != 0 || len(childRecovery.PendingInterrupts) != 0 || len(childRecovery.PendingTools) != 0 {
				continue
			}
		}
		started, finished, outcome := runTerminalFacts(runDurable, binding.RunID)
		if finished {
			status := agent.RunCompleted
			if outcome == string(agent.RunAborted) {
				status = agent.RunAborted
			} else if outcome == string(agent.RunFailed) {
				status = agent.RunFailed
			} else if outcome == string(agent.RunHandedOff) {
				status = agent.RunHandedOff
			}
			status, statusErr := s.normalizeRecoveredControlHandoff(ctx, turn, status, runDurable)
			if statusErr != nil {
				return completed, fmt.Errorf("coordinate Coding Agent recovery: inspect Plan completion for Turn %q: %w", turn.ID, statusErr)
			}
			steps := durableRunSteps(runDurable, binding.RunID)
			result := agent.RunResult{RunID: binding.RunID, Status: status, Steps: steps, Reason: "recovered_terminal", TerminalOutput: durableRunTerminalOutput(runDurable, binding.RunID)}
			var updated Turn
			var runErr error
			var saveErr error
			if runChild != nil {
				updated, _, result, runErr, saveErr = s.finishChildProductRun(ctx, turn, *runChild, result, nil)
			} else {
				updated, saveErr = s.finishProductRun(ctx, turn, result, nil)
			}
			if saveErr != nil {
				return completed, fmt.Errorf("coordinate Coding Agent recovery: reconcile Product Turn %q: %w", turn.ID, saveErr)
			}
			s.setState(product.ID, runtimeStateForTurn(updated))
			completed++
			if isWorkflowStrategy(updated.Strategy) && binding.NodeID != "" && status != agent.RunInterrupted {
				if _, advanceErr := s.advanceWorkflowAfterRunLocked(ctx, product, updated, result, runErr, productTurnResult(updated.ID, result)); advanceErr != nil {
					return completed, fmt.Errorf("coordinate Coding Agent recovery: advance Workflow Turn %q: %w", turn.ID, advanceErr)
				}
				continue
			}
			if runChild != nil && runChild.Kind == ChildAgentPlanExplore && status != agent.RunInterrupted {
				child, loadErr := s.deps.Children.LoadChildAgent(ctx, runChild.ID)
				if loadErr != nil {
					return completed, loadErr
				}
				if _, continueErr := s.finishPlanExploreAndContinue(ctx, product, updated, child); continueErr != nil {
					return completed, fmt.Errorf("coordinate Coding Agent recovery: continue after Plan exploration: %w", continueErr)
				}
				continue
			}
			if status == agent.RunHandedOff {
				if _, continueErr := s.continueTurnLocked(ctx, product, updated); continueErr != nil {
					return completed, fmt.Errorf("coordinate Coding Agent recovery: continue recovered Product Turn %q: %w", turn.ID, continueErr)
				}
				completed++
			}
			continue
		}
		if started {
			continue
		}
		if binding.Status != RunBindingPending && binding.Status != RunBindingRunning {
			continue
		}
		var environment runEnvironment
		var envErr error
		if runChild != nil {
			environment, envErr = s.prepareChildRunEnvironment(ctx, product, turn, *runChild)
		} else {
			environment, envErr = s.prepareRunEnvironment(ctx, product, turn, RunID(binding.RunID), binding.NodeID, binding.Profile)
		}
		if envErr != nil {
			return completed, fmt.Errorf("coordinate Coding Agent recovery: prepare Product Turn %q: %w", turn.ID, envErr)
		}
		if binding.Status == RunBindingPending {
			turn, envErr = s.markRunStarted(ctx, turn, binding.RunID, time.Now().UTC())
			if envErr != nil {
				return completed, fmt.Errorf("coordinate Coding Agent recovery: bind Product Turn %q: %w", turn.ID, envErr)
			}
		}
		var result agent.RunResult
		var runErr error
		limits, limitErr := s.workflowRunLimitsForTurn(ctx, turn)
		if limitErr != nil {
			return completed, fmt.Errorf("coordinate Coding Agent recovery: Product Turn %q: %w", turn.ID, limitErr)
		}
		if runChild != nil {
			if runChild.Status == ChildAgentCreating {
				ready, readyErr := s.ensureChildAgentReady(ctx, product, *runChild)
				if readyErr != nil {
					return completed, readyErr
				}
				runChild = &ready
			}
			if runChild.Status == ChildAgentReady {
				running, runningErr := s.transitionChild(ctx, *runChild, ChildAgentRunning, nil, "")
				if runningErr != nil {
					return completed, runningErr
				}
				runChild = &running
			}
			message, messageErr := childTaskMessage(*runChild)
			if messageErr != nil {
				return completed, messageErr
			}
			result, runErr = s.deps.Agent.Run(ctx, agent.RunRequest{
				SessionID: runChild.AgentSessionID, Lane: agentsession.MainLane, RunID: binding.RunID, UserEntryID: agentsession.EntryID("entry_" + string(runChild.ID)),
				SystemPrompt: environment.systemPrompt, Model: llm.ModelRef{Provider: product.ProviderProfileID, Model: product.ModelID}, UserMessage: message,
				UntrustedContext: environment.untrustedContext, Tools: environment.tools, ToolCallPreviewer: environment.toolCallPreviewer, Limits: limits,
			}, environment.events)
		} else if binding.UserEntryID == "" {
			continuation, ok := s.deps.Agent.(ContinuationRunner)
			if !ok {
				return completed, fmt.Errorf("coordinate Coding Agent recovery: Product Turn %q continuation is unsupported", turn.ID)
			}
			result, runErr = continuation.Continue(ctx, agent.ContinueRequest{
				SessionID: product.AgentSessionID, Lane: sessionLane(product), RunID: binding.RunID,
				SystemPrompt: environment.systemPrompt, Model: llm.ModelRef{Provider: product.ProviderProfileID, Model: product.ModelID},
				UntrustedContext: environment.untrustedContext, Tools: environment.tools, ToolCallPreviewer: environment.toolCallPreviewer, Limits: limits,
			}, environment.events)
		} else {
			result, runErr = s.deps.Agent.Run(ctx, agent.RunRequest{
				SessionID: product.AgentSessionID, Lane: sessionLane(product), RunID: binding.RunID, UserEntryID: binding.UserEntryID,
				SystemPrompt: environment.systemPrompt, Model: llm.ModelRef{Provider: product.ProviderProfileID, Model: product.ModelID},
				UserMessage:      llm.Message{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentText, Text: turn.RequestText}}, Timestamp: turn.CreatedAt},
				UntrustedContext: environment.untrustedContext, Tools: environment.tools, ToolCallPreviewer: environment.toolCallPreviewer, Limits: limits,
			}, environment.events)
		}
		if result.RunID == "" {
			result.RunID = binding.RunID
		}
		result.Status, err = s.normalizeRecoveredControlHandoff(ctx, turn, result.Status, runDurable)
		if err != nil {
			return completed, fmt.Errorf("coordinate Coding Agent recovery: inspect Plan completion for Turn %q: %w", turn.ID, err)
		}
		refreshed, saveErr := s.refreshProductTurn(context.WithoutCancel(ctx), turn)
		if saveErr != nil {
			return completed, fmt.Errorf("coordinate Coding Agent recovery: reload Product Turn %q: %w", turn.ID, saveErr)
		}
		turn = refreshed
		var updated Turn
		if runChild != nil {
			updated, _, result, runErr, saveErr = s.finishChildProductRun(context.WithoutCancel(ctx), turn, *runChild, result, runErr)
		} else {
			updated, saveErr = s.finishProductRun(context.WithoutCancel(ctx), turn, result, runErr)
		}
		if saveErr != nil {
			return completed, fmt.Errorf("coordinate Coding Agent recovery: finish Product Turn %q: %w", turn.ID, saveErr)
		}
		s.setState(product.ID, runtimeStateForTurn(updated))
		if err := s.publishPendingPlanReplanEvent(context.WithoutCancel(ctx), product, updated, result); err != nil {
			return completed, fmt.Errorf("coordinate Coding Agent recovery: publish Plan replan request for Turn %q: %w", turn.ID, err)
		}
		if isWorkflowStrategy(updated.Strategy) && binding.NodeID != "" && result.Status != agent.RunInterrupted {
			if _, advanceErr := s.advanceWorkflowAfterRunLocked(context.WithoutCancel(ctx), product, updated, result, runErr, productTurnResult(updated.ID, result)); advanceErr != nil {
				return completed, fmt.Errorf("coordinate Coding Agent recovery: advance Workflow Turn %q: %w", turn.ID, advanceErr)
			}
		}
		if runChild != nil && runChild.Kind == ChildAgentPlanExplore && result.Status != agent.RunInterrupted {
			child, loadErr := s.deps.Children.LoadChildAgent(ctx, runChild.ID)
			if loadErr != nil {
				return completed, loadErr
			}
			if _, continueErr := s.finishPlanExploreAndContinue(context.WithoutCancel(ctx), product, updated, child); continueErr != nil {
				return completed, fmt.Errorf("coordinate Coding Agent recovery: continue after Plan exploration: %w", continueErr)
			}
		}
		completed++
		if runErr != nil || result.Status == agent.RunInterrupted {
			return completed, nil
		}
	}
	return completed, nil
}

func workflowRunBinding(turn Turn, runID agentsession.RunID) *RunBinding {
	for index := range turn.Runs {
		if turn.Runs[index].RunID == runID && turn.Runs[index].NodeID != "" {
			return &turn.Runs[index]
		}
	}
	return nil
}

func durableRunSteps(snapshot agentsession.Snapshot, runID agentsession.RunID) int {
	steps := 0
	for _, record := range snapshot.Records {
		if record.RunID == runID && record.Type == agentsession.RecordStepFinished && record.Step != nil && record.Step.Attempt > steps {
			steps = record.Step.Attempt
		}
	}
	return steps
}

func durableRunTerminalOutput(snapshot agentsession.Snapshot, runID agentsession.RunID) json.RawMessage {
	for index := len(snapshot.Records) - 1; index >= 0; index-- {
		record := snapshot.Records[index]
		if record.RunID == runID && record.Type == agentsession.RecordOperationFinished && record.Operation != nil {
			return append(json.RawMessage(nil), record.Operation.TerminalOutput...)
		}
	}
	return nil
}

func (s *Service) normalizeRecoveredControlHandoff(ctx context.Context, turn Turn, status agent.RunStatus, durable agentsession.Snapshot) (agent.RunStatus, error) {
	if status != agent.RunHandedOff || len(turn.Runs) == 0 {
		return status, nil
	}
	runID := turn.Runs[len(turn.Runs)-1].RunID
	switch turn.Phase {
	case TurnPhaseAwaitingPlanEntryApproval:
		decision := recoveredControlDecision(durable, runID, planEntryApprovalKind)
		if decision == "cancelled" {
			return agent.RunAborted, nil
		}
		if decision != "" && decision != "approved" {
			return status, fmt.Errorf("recovered Plan entry handoff has unexpected decision %q", decision)
		}
		return status, nil
	case TurnPhaseAwaitingPlanApproval:
		decision := recoveredControlDecision(durable, runID, "plan_approval")
		if decision == "cancelled" {
			return agent.RunAborted, nil
		}
		if decision != "" && decision != "approved" {
			return status, fmt.Errorf("recovered Plan approval handoff has unexpected decision %q", decision)
		}
	case TurnPhaseNeedsReplan:
		decision := recoveredControlDecision(durable, runID, planReplanApprovalKind)
		if decision == "cancelled" {
			return agent.RunAborted, nil
		}
		if decision != "" && decision != "replan" {
			return status, fmt.Errorf("recovered Plan replan handoff has unexpected decision %q", decision)
		}
		return status, nil
	default:
		return status, nil
	}
	if turn.PlanID == "" || s.deps.Plans == nil {
		return status, errors.New("approved Plan revision is unavailable")
	}
	plan, err := s.deps.Plans.LoadPlan(ctx, turn.PlanID, turn.PlanVersion)
	if err != nil || plan.Digest != turn.PlanDigest {
		return status, errors.New("approved Plan revision is unavailable or changed")
	}
	if plan.CompletionMode == PlanCompletionDeliverable {
		return agent.RunCompleted, nil
	}
	return status, nil
}

func recoveredControlDecision(durable agentsession.Snapshot, runID agentsession.RunID, kind string) string {
	for index := len(durable.Records) - 1; index >= 0; index-- {
		record := durable.Records[index]
		if record.RunID != runID || record.Type != agentsession.RecordInterruptResolved || record.Interrupt == nil || record.Interrupt.Kind != kind || len(record.Interrupt.Payload) == 0 {
			continue
		}
		var payload struct {
			Decision string `json:"decision"`
		}
		if json.Unmarshal(record.Interrupt.Payload, &payload) == nil {
			return payload.Decision
		}
		return ""
	}
	return ""
}

func runTerminalFacts(snapshot agentsession.Snapshot, runID agentsession.RunID) (started, finished bool, outcome string) {
	for _, record := range snapshot.Records {
		if record.RunID != runID {
			continue
		}
		switch record.Type {
		case agentsession.RecordOperationStarted:
			started = true
		case agentsession.RecordOperationFinished:
			finished = true
			if record.Operation != nil {
				outcome = record.Operation.Outcome
			}
		}
	}
	return started, finished, outcome
}

type recoveryEnvironment struct {
	product          Session
	agentSessionID   agentsession.ID
	lane             agentsession.Lane
	child            *ChildAgent
	tools            *tool.Registry
	systemPrompt     string
	untrustedContext []llm.Message
	events           *AgentEventAdapter
}

func (s *Service) prepareRecovery(ctx context.Context, sessionID SessionID, turnID TurnID, runID agentsession.RunID) (recoveryEnvironment, error) {
	product, err := s.deps.Sessions.LoadSession(ctx, sessionID)
	if err != nil {
		return recoveryEnvironment{}, fmt.Errorf("prepare Coding Agent recovery: load session: %w", err)
	}
	turn := Turn{ID: turnID, Phase: TurnPhaseDirect}
	profile := CapabilityDirect
	if s.features.ProductTurns {
		if durableTurn, loadErr := s.deps.Turns.LoadTurn(ctx, turnID); loadErr == nil {
			turn = durableTurn
			if binding, found := durableTurn.Run(runID); found {
				profile = binding.Profile
			}
		} else if !errors.Is(loadErr, ErrTurnNotFound) {
			return recoveryEnvironment{}, fmt.Errorf("prepare Coding Agent recovery: load Product Turn: %w", loadErr)
		}
	}
	nodeID := NodeID("")
	var child *ChildAgent
	agentSessionID, lane := product.AgentSessionID, sessionLane(product)
	if binding, found := turn.Run(runID); found {
		nodeID = binding.NodeID
		if binding.ChildAgentID != "" {
			if s.deps.Children == nil {
				return recoveryEnvironment{}, errors.New("prepare Coding Agent recovery: child Agent repository is unavailable")
			}
			value, loadErr := s.deps.Children.LoadChildAgent(ctx, binding.ChildAgentID)
			if loadErr != nil || value.ParentSessionID != product.ID || value.ParentTurnID != turn.ID || value.RunID != runID {
				return recoveryEnvironment{}, errors.New("prepare Coding Agent recovery: child Agent binding is unavailable or inconsistent")
			}
			child, agentSessionID, lane = &value, value.AgentSessionID, agentsession.MainLane
		}
	}
	var environment runEnvironment
	if child != nil {
		environment, err = s.prepareChildRunEnvironment(ctx, product, turn, *child)
	} else {
		environment, err = s.prepareRunEnvironment(ctx, product, turn, RunID(runID), nodeID, profile)
	}
	if err != nil {
		return recoveryEnvironment{}, err
	}
	return recoveryEnvironment{product: product, agentSessionID: agentSessionID, lane: lane, child: child, tools: environment.tools, systemPrompt: environment.systemPrompt, untrustedContext: environment.untrustedContext, events: environment.events}, nil
}

func (s *Service) refreshRecoveryState(ctx context.Context, product Session) {
	durable, err := s.deps.AgentSessions.Load(ctx, product.AgentSessionID)
	if err != nil {
		s.setState(product.ID, RuntimeInterrupted)
		return
	}
	s.setState(product.ID, recoveryState(agentsession.BuildRecoveryPlan(durable), agentsession.AnalyzeRecovery(durable)))
}

func recoveryState(plan agentsession.RecoveryPlan, state agentsession.RecoveryState) RuntimeState {
	if len(state.PendingInterrupts) != 0 {
		return RuntimeAwaitingApproval
	}
	if len(plan.Actions) != 0 {
		return RuntimeInterrupted
	}
	return RuntimeIdle
}

func agentRecoveryDecision(value RecoveryDecision) (agentsession.RecoveryDecision, error) {
	switch value {
	case RecoveryRetry:
		return agentsession.RecoveryRetry, nil
	case RecoveryConfirmExecuted:
		return agentsession.RecoveryConfirmExecuted, nil
	case RecoveryMarkFailed:
		return agentsession.RecoveryMarkFailed, nil
	case RecoveryAbandonTurn:
		return agentsession.RecoveryAbandonRun, nil
	default:
		return "", fmt.Errorf("recover Coding Agent turn: unsupported decision %q", value)
	}
}

func productTurnResult(turnID TurnID, result agent.RunResult) TurnResult {
	response := ""
	if result.FinalMessage != nil {
		response = visibleText(*result.FinalMessage)
	}
	product := TurnResult{TurnID: turnID, RunID: RunID(result.RunID), Status: string(result.Status), Response: response, Steps: result.Steps, Reason: result.Reason}
	if result.Interrupt != nil {
		product.InterruptID = result.Interrupt.ID
		product.InterruptKind = result.Interrupt.Kind
	}
	return product
}

func (s *Service) turnForRun(ctx context.Context, sessionID SessionID, runID agentsession.RunID) (Turn, bool, error) {
	turns, err := s.deps.Turns.ListTurns(ctx, sessionID)
	if err != nil {
		return Turn{}, false, err
	}
	for _, turn := range turns {
		if _, found := turn.Run(runID); found {
			return turn, true, nil
		}
	}
	return Turn{}, false, nil
}
