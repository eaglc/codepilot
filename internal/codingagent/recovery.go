package codingagent

import (
	"context"
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
	result, recoverErr := s.deps.Agent.Recover(runCtx, agent.RecoverRequest{
		SessionID: environment.product.AgentSessionID, Lane: sessionLane(environment.product),
		RunID: binding.RunID, ActionID: request.ActionID, Decision: decision, ContinueRun: true,
		SystemPrompt:     environment.systemPrompt,
		Model:            llm.ModelRef{Provider: environment.product.ProviderProfileID, Model: environment.product.ModelID},
		UntrustedContext: environment.untrustedContext, Tools: environment.tools, Limits: s.deps.Limits,
	}, environment.events)
	if result.RunID == "" {
		result.RunID = binding.RunID
	}
	var finishErr error
	if legacy {
		s.refreshRecoveryState(ctx, environment.product)
	} else {
		turn, finishErr = s.refreshProductTurn(context.WithoutCancel(ctx), turn)
		if finishErr == nil {
			turn, finishErr = s.finishProductRun(context.WithoutCancel(ctx), turn, result, recoverErr)
		}
		s.setState(environment.product.ID, runtimeStateForTurn(turn))
	}
	touchErr := s.touchSession(context.WithoutCancel(ctx), environment.product)
	productResult := productTurnResult(turn.ID, result)
	if recoverErr != nil {
		return productResult, fmt.Errorf("recover Coding Agent turn: %w", recoverErr)
	}
	if finishErr != nil {
		return productResult, fmt.Errorf("recover Coding Agent turn: persist Product Turn: %w", finishErr)
	}
	if touchErr != nil {
		return productResult, fmt.Errorf("recover Coding Agent turn: update product session: %w", touchErr)
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
		durable, err := s.deps.AgentSessions.Load(ctx, product.AgentSessionID)
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
		turn, found := Turn{}, false
		if s.features.ProductTurns {
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
		s.setState(sessionID, RuntimeRunning)
		result, err := s.deps.Agent.Recover(ctx, agent.RecoverRequest{
			SessionID: environment.product.AgentSessionID, Lane: action.Lane, RunID: action.RunID, ActionID: action.ID,
			Automatic: true, ContinueRun: false, SystemPrompt: environment.systemPrompt,
			Model:            llm.ModelRef{Provider: environment.product.ProviderProfileID, Model: environment.product.ModelID},
			UntrustedContext: environment.untrustedContext, Tools: environment.tools, Limits: s.deps.Limits,
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
			if finishErr == nil {
				turn, finishErr = s.finishProductRun(context.WithoutCancel(ctx), turn, result, nil)
			}
			if finishErr != nil {
				return completed, fmt.Errorf("coordinate Coding Agent recovery action %q: persist Product Turn: %w", action.ID, finishErr)
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
		binding, active := turn.ActiveRun()
		if !active {
			if len(turn.Runs) != 0 && turn.Status == TurnRunning && turn.Runs[len(turn.Runs)-1].Status == RunBindingHandedOff {
				if _, continueErr := s.continueTurnLocked(ctx, product, turn); continueErr != nil {
					return completed, fmt.Errorf("coordinate Coding Agent recovery: continue handed-off Product Turn %q: %w", turn.ID, continueErr)
				}
				completed++
			}
			continue
		}
		started, finished, outcome := runTerminalFacts(durable, binding.RunID)
		if finished {
			status := agent.RunCompleted
			if outcome == string(agent.RunAborted) {
				status = agent.RunAborted
			} else if outcome == string(agent.RunFailed) {
				status = agent.RunFailed
			} else if outcome == string(agent.RunHandedOff) {
				status = agent.RunHandedOff
			}
			status, statusErr := s.normalizeRecoveredPlanHandoff(ctx, turn, status)
			if statusErr != nil {
				return completed, fmt.Errorf("coordinate Coding Agent recovery: inspect Plan completion for Turn %q: %w", turn.ID, statusErr)
			}
			updated, saveErr := s.finishProductRun(ctx, turn, agent.RunResult{RunID: binding.RunID, Status: status, Reason: "recovered_terminal"}, nil)
			if saveErr != nil {
				return completed, fmt.Errorf("coordinate Coding Agent recovery: reconcile Product Turn %q: %w", turn.ID, saveErr)
			}
			s.setState(product.ID, runtimeStateForTurn(updated))
			completed++
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
		environment, envErr := s.prepareRunEnvironment(ctx, product, turn, RunID(binding.RunID), "", binding.Profile)
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
		if binding.UserEntryID == "" {
			continuation, ok := s.deps.Agent.(ContinuationRunner)
			if !ok {
				return completed, fmt.Errorf("coordinate Coding Agent recovery: Product Turn %q continuation is unsupported", turn.ID)
			}
			result, runErr = continuation.Continue(ctx, agent.ContinueRequest{
				SessionID: product.AgentSessionID, Lane: sessionLane(product), RunID: binding.RunID,
				SystemPrompt: environment.systemPrompt, Model: llm.ModelRef{Provider: product.ProviderProfileID, Model: product.ModelID},
				UntrustedContext: environment.untrustedContext, Tools: environment.tools, Limits: s.deps.Limits,
			}, environment.events)
		} else {
			result, runErr = s.deps.Agent.Run(ctx, agent.RunRequest{
				SessionID: product.AgentSessionID, Lane: sessionLane(product), RunID: binding.RunID, UserEntryID: binding.UserEntryID,
				SystemPrompt: environment.systemPrompt, Model: llm.ModelRef{Provider: product.ProviderProfileID, Model: product.ModelID},
				UserMessage:      llm.Message{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentText, Text: turn.RequestText}}, Timestamp: turn.CreatedAt},
				UntrustedContext: environment.untrustedContext, Tools: environment.tools, Limits: s.deps.Limits,
			}, environment.events)
		}
		if result.RunID == "" {
			result.RunID = binding.RunID
		}
		result.Status, err = s.normalizeRecoveredPlanHandoff(ctx, turn, result.Status)
		if err != nil {
			return completed, fmt.Errorf("coordinate Coding Agent recovery: inspect Plan completion for Turn %q: %w", turn.ID, err)
		}
		refreshed, saveErr := s.refreshProductTurn(context.WithoutCancel(ctx), turn)
		if saveErr != nil {
			return completed, fmt.Errorf("coordinate Coding Agent recovery: reload Product Turn %q: %w", turn.ID, saveErr)
		}
		turn = refreshed
		updated, saveErr := s.finishProductRun(context.WithoutCancel(ctx), turn, result, runErr)
		if saveErr != nil {
			return completed, fmt.Errorf("coordinate Coding Agent recovery: finish Product Turn %q: %w", turn.ID, saveErr)
		}
		s.setState(product.ID, runtimeStateForTurn(updated))
		completed++
		if runErr != nil || result.Status == agent.RunInterrupted {
			return completed, nil
		}
	}
	return completed, nil
}

func (s *Service) normalizeRecoveredPlanHandoff(ctx context.Context, turn Turn, status agent.RunStatus) (agent.RunStatus, error) {
	if status != agent.RunHandedOff || turn.Phase != TurnPhaseAwaitingPlanApproval || turn.PlanID == "" || s.deps.Plans == nil {
		return status, nil
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
	environment, err := s.prepareRunEnvironment(ctx, product, turn, RunID(runID), "", profile)
	if err != nil {
		return recoveryEnvironment{}, err
	}
	return recoveryEnvironment{product: product, tools: environment.tools, systemPrompt: environment.systemPrompt, untrustedContext: environment.untrustedContext, events: environment.events}, nil
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
