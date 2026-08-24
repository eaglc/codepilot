package codingagent

import (
	"context"
	"errors"
	"fmt"
	"strings"

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
	environment, err := s.prepareRecovery(ctx, request.SessionID)
	if err != nil {
		return TurnResult{}, err
	}
	s.setState(request.SessionID, RuntimeRunning)
	runCtx, finishActive := s.beginActiveTurn(ctx, request.SessionID)
	defer finishActive()
	result, recoverErr := s.deps.Agent.Recover(runCtx, agent.RecoverRequest{
		SessionID: environment.product.AgentSessionID, Lane: sessionLane(environment.product),
		RunID: agentsession.RunID(request.TurnID), ActionID: request.ActionID, Decision: decision, ContinueRun: true,
		SystemPrompt:     environment.systemPrompt,
		Model:            llm.ModelRef{Provider: environment.product.ProviderProfileID, Model: environment.product.ModelID},
		UntrustedContext: environment.untrustedContext, Tools: environment.tools, Limits: s.deps.Limits,
	}, environment.events)
	s.refreshRecoveryState(ctx, environment.product)
	touchErr := s.touchSession(context.WithoutCancel(ctx), environment.product)
	productResult := productTurnResult(result)
	if recoverErr != nil {
		return productResult, fmt.Errorf("recover Coding Agent turn: %w", recoverErr)
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
	environment, err := s.prepareRecovery(ctx, sessionID)
	if err != nil {
		return 0, err
	}
	completed := 0
	for completed < 32 {
		durable, err := s.deps.AgentSessions.Load(ctx, environment.product.AgentSessionID)
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
				if err := s.touchSession(ctx, environment.product); err != nil {
					return completed, fmt.Errorf("coordinate Coding Agent recovery: update product session: %w", err)
				}
			}
			return completed, nil
		}
		s.setState(sessionID, RuntimeRunning)
		result, err := s.deps.Agent.Recover(ctx, agent.RecoverRequest{
			SessionID: environment.product.AgentSessionID, Lane: action.Lane, RunID: action.RunID, ActionID: action.ID,
			Automatic: true, ContinueRun: false, SystemPrompt: environment.systemPrompt,
			Model:            llm.ModelRef{Provider: environment.product.ProviderProfileID, Model: environment.product.ModelID},
			UntrustedContext: environment.untrustedContext, Tools: environment.tools, Limits: s.deps.Limits,
		}, environment.events)
		if err != nil {
			s.setState(sessionID, RuntimeInterrupted)
			return completed, fmt.Errorf("coordinate Coding Agent recovery action %q: %w", action.ID, err)
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

type recoveryEnvironment struct {
	product          Session
	tools            *tool.Registry
	systemPrompt     string
	untrustedContext []llm.Message
	events           *AgentEventAdapter
}

func (s *Service) prepareRecovery(ctx context.Context, sessionID SessionID) (recoveryEnvironment, error) {
	product, err := s.deps.Sessions.LoadSession(ctx, sessionID)
	if err != nil {
		return recoveryEnvironment{}, fmt.Errorf("prepare Coding Agent recovery: load session: %w", err)
	}
	worktree, err := s.deps.Worktrees.LoadWorktree(ctx, product.WorktreeID)
	if err != nil {
		return recoveryEnvironment{}, fmt.Errorf("prepare Coding Agent recovery: load worktree: %w", err)
	}
	tools, err := s.deps.Tools.CreateTools(ctx, ToolScope{
		SessionID: product.ID, WorkspaceID: product.WorkspaceID, WorktreeID: product.WorktreeID,
		WorktreeRoot: worktree.Root, PermissionMode: product.PermissionMode, PermissionGrants: clonePermissionGrants(product.PermissionGrants), SensitivePaths: append([]string(nil), product.SensitivePaths...),
	})
	if err != nil {
		return recoveryEnvironment{}, fmt.Errorf("prepare Coding Agent recovery: create tools: %w", err)
	}
	definitions := tools.Definitions()
	names := make([]string, len(definitions))
	for index, definition := range definitions {
		names[index] = definition.Name
	}
	promptScope := PromptScope{
		WorkspaceID: product.WorkspaceID, WorktreeID: product.WorktreeID, WorktreeRoot: worktree.Root,
		ToolNames: names, SensitivePaths: append([]string(nil), product.SensitivePaths...),
	}
	systemPrompt, untrustedContext, err := buildPromptContext(ctx, s.deps.Prompts, promptScope)
	if err != nil {
		return recoveryEnvironment{}, fmt.Errorf("prepare Coding Agent recovery: build prompt: %w", err)
	}
	revisions := durableRevisionSource{repository: s.deps.AgentSessions, agentSessionID: product.AgentSessionID}
	events, err := NewAgentEventAdapter(product.ID, s.deps.Events, revisions)
	if err != nil {
		return recoveryEnvironment{}, err
	}
	return recoveryEnvironment{product: product, tools: tools, systemPrompt: systemPrompt, untrustedContext: untrustedContext, events: events}, nil
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

func productTurnResult(result agent.RunResult) TurnResult {
	response := ""
	if result.FinalMessage != nil {
		response = visibleText(*result.FinalMessage)
	}
	product := TurnResult{TurnID: TurnID(result.RunID), Status: string(result.Status), Response: response, Steps: result.Steps, Reason: result.Reason}
	if result.Interrupt != nil {
		product.InterruptID = result.Interrupt.ID
		product.InterruptKind = result.Interrupt.Kind
	}
	return product
}
