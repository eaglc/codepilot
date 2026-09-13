package codingagent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/eaglc/codepilot/internal/agent"
	agentsession "github.com/eaglc/codepilot/internal/agent/session"
	"github.com/eaglc/codepilot/internal/codingagent/roleprofile"
	"github.com/eaglc/codepilot/internal/llm"
	"github.com/eaglc/codepilot/internal/tool"
	"github.com/eaglc/codepilot/internal/workflow"
)

// AgentRunner is the generic runtime boundary consumed by Coding Agent.
type AgentRunner interface {
	Run(ctx context.Context, request agent.RunRequest, events agent.EventSink) (agent.RunResult, error)
	Resume(ctx context.Context, request agent.ResumeRequest, events agent.EventSink) (agent.RunResult, error)
	Recover(ctx context.Context, request agent.RecoverRequest, events agent.EventSink) (agent.RunResult, error)
}

// ContinuationRunner is implemented by runtimes that can start another Run
// without appending a synthetic user message. It remains optional so existing
// AgentRunner adapters continue to compile while the P0 seam rolls out.
type ContinuationRunner interface {
	Continue(ctx context.Context, request agent.ContinueRequest, events agent.EventSink) (agent.RunResult, error)
}

// FeatureFlags controls independently reversible product capabilities.
type FeatureFlags struct {
	ProductTurns      bool
	PlanMode          bool
	PlanSuggestions   bool
	Workflows         bool
	Subagents         bool
	ParallelSubagents bool
}

// DefaultFeatureFlags returns the current stable product defaults.
func DefaultFeatureFlags() FeatureFlags {
	return FeatureFlags{ProductTurns: true, PlanMode: true, PlanSuggestions: true, Workflows: true, Subagents: true, ParallelSubagents: true}
}

// ToolScope contains immutable trusted Coding facts captured before model-controlled execution.
type ToolScope struct {
	Profile          CapabilityProfile
	PolicyVersion    uint32
	TurnID           TurnID
	RunID            RunID
	NodeID           NodeID
	SessionID        SessionID
	WorkspaceID      WorkspaceID
	WorktreeID       WorktreeID
	WorktreeRoot     string
	PermissionMode   PermissionMode
	PermissionGrants []PermissionGrant
	SensitivePaths   []string
	ReadScope        []string
	WriteScope       []string
}

// ToolFactory creates the exact Coding tool set available to one turn.
type ToolFactory interface {
	CreateTools(ctx context.Context, scope ToolScope) (*tool.Registry, error)
}

// PromptScope contains trusted Coding facts used to build a system prompt.
type PromptScope struct {
	Profile        CapabilityProfile
	PolicyVersion  uint32
	TurnID         TurnID
	RunID          RunID
	NodeID         NodeID
	WorkspaceID    WorkspaceID
	WorktreeID     WorktreeID
	WorktreeRoot   string
	ToolNames      []string
	SensitivePaths []string
	ReadScope      []string
	WriteScope     []string
}

// PromptBuilder creates Coding policy text without entering generic Agent packages.
type PromptBuilder interface {
	BuildSystemPrompt(ctx context.Context, scope PromptScope) (string, error)
}

// UntrustedContextBuilder optionally supplies repository-derived context as
// lower-priority user-role data. It must never place repository content in the
// trusted system prompt.
type UntrustedContextBuilder interface {
	BuildUntrustedContext(ctx context.Context, scope PromptScope) ([]llm.Message, error)
}

// Dependencies contains concrete capabilities required by Service.
type Dependencies struct {
	Sessions      SessionRepository
	Turns         TurnRepository
	Plans         PlanRepository
	Workflows     workflow.Repository
	Children      ChildAgentRepository
	AgentSessions agentsession.Repository
	Worktrees     WorktreeReader
	Workspaces    WorkspaceController
	Agent         AgentRunner
	Tools         ToolFactory
	Prompts       PromptBuilder
	Events        EventSink
	Providers     ProviderManager
	Limits        agent.RunLimits
	// MaxParallelAgents bounds child Agent executions across all sessions owned
	// by this Service. Zero selects the product default.
	MaxParallelAgents int
	Features          *FeatureFlags
	Roles             *roleprofile.Registry
}

// Service owns Coding session lifecycle while delegating model/tool loops to generic Agent.
type Service struct {
	deps          Dependencies
	mu            sync.RWMutex
	states        map[SessionID]RuntimeState
	operations    map[SessionID]*sync.Mutex
	activeTurns   map[SessionID]activeTurn
	activeSeq     uint64
	active        SessionID
	eventSeq      uint64
	features      FeatureFlags
	parallelSlots chan struct{}
}

// NewService validates and creates a Coding Agent product service.
func NewService(deps Dependencies) (*Service, error) {
	features := DefaultFeatureFlags()
	if deps.Features != nil {
		features = *deps.Features
	}
	if deps.Turns == nil {
		if repository, ok := deps.Sessions.(TurnRepository); ok {
			deps.Turns = repository
		}
	}
	if deps.Plans == nil {
		if repository, ok := deps.Sessions.(PlanRepository); ok {
			deps.Plans = repository
		}
	}
	if deps.Workflows == nil {
		if repository, ok := deps.Sessions.(workflow.Repository); ok {
			deps.Workflows = repository
		}
	}
	if deps.Children == nil {
		if repository, ok := deps.Sessions.(ChildAgentRepository); ok {
			deps.Children = repository
		}
	}
	if deps.Roles == nil {
		var err error
		deps.Roles, err = roleprofile.NewDefaultRegistry()
		if err != nil {
			return nil, fmt.Errorf("create Coding Agent service: role profiles: %w", err)
		}
	}
	for _, role := range []workflow.Role{workflow.RoleExplore, workflow.RoleImplement, workflow.RoleValidate, workflow.RoleReview, workflow.RoleIntegrate} {
		if _, err := deps.Roles.ResolveRole(role); err != nil {
			return nil, fmt.Errorf("create Coding Agent service: required role profile: %w", err)
		}
	}
	if !features.ProductTurns {
		features.PlanMode = false
		features.PlanSuggestions = false
		features.Workflows = false
		features.Subagents = false
		features.ParallelSubagents = false
	}
	if !features.PlanMode {
		features.PlanSuggestions = false
	}
	if deps.Children == nil {
		features.Subagents = false
		features.ParallelSubagents = false
	}
	if !features.Subagents {
		features.ParallelSubagents = false
	}
	if deps.MaxParallelAgents == 0 {
		deps.MaxParallelAgents = 8
	}
	if deps.MaxParallelAgents < 1 || deps.MaxParallelAgents > workflow.MaxNodes {
		return nil, errors.New("create Coding Agent service: global parallel Agent limit must be between 1 and 64")
	}
	if features.ParallelSubagents && deps.MaxParallelAgents < 2 {
		return nil, errors.New("create Coding Agent service: parallel subagents require a global Agent limit of at least 2")
	}
	if deps.Sessions == nil || (features.ProductTurns && deps.Turns == nil) || (features.PlanMode && deps.Plans == nil) || (features.Workflows && deps.Workflows == nil) || deps.AgentSessions == nil || deps.Worktrees == nil || deps.Agent == nil || deps.Tools == nil || deps.Prompts == nil || deps.Events == nil {
		return nil, errors.New("create Coding Agent service: dependencies are incomplete")
	}
	return &Service{deps: deps, features: features, states: make(map[SessionID]RuntimeState), operations: make(map[SessionID]*sync.Mutex), activeTurns: make(map[SessionID]activeTurn), parallelSlots: make(chan struct{}, deps.MaxParallelAgents)}, nil
}

func (s *Service) acquireParallelAgent(ctx context.Context) (func(), error) {
	select {
	case s.parallelSlots <- struct{}{}:
		return func() { <-s.parallelSlots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type activeTurn struct {
	sequence uint64
	cancel   context.CancelFunc
}

// CreateSession establishes product and generic Agent session identities behind
// a durable intent that an explicit consistency repair can safely reconcile.
func (s *Service) CreateSession(ctx context.Context, value Session) (Session, error) {
	if err := ctx.Err(); err != nil {
		return Session{}, err
	}
	if value.ID == "" {
		id, err := newID("coding")
		if err != nil {
			return Session{}, err
		}
		value.ID = SessionID(id)
	}
	if value.AgentSessionID == "" {
		id, err := newID("agent")
		if err != nil {
			return Session{}, err
		}
		value.AgentSessionID = agentsession.ID(id)
	}
	if value.WorkspaceID == "" || value.WorktreeID == "" || value.ProviderProfileID == "" || value.ModelID == "" {
		return Session{}, errors.New("create Coding Agent session: workspace, worktree, provider profile, and model are required")
	}
	if value.PermissionMode == "" {
		value.PermissionMode = PermissionAsk
	}
	sensitivePaths, err := NormalizeSensitivePaths(value.SensitivePaths)
	if err != nil {
		return Session{}, fmt.Errorf("create Coding Agent session: %w", err)
	}
	value.SensitivePaths = sensitivePaths
	if value.ActiveLane == "" {
		value.ActiveLane = agentsession.MainLane
	}
	if _, err := s.deps.Worktrees.LoadWorktree(ctx, value.WorktreeID); err != nil {
		return Session{}, fmt.Errorf("create Coding Agent session: load worktree: %w", err)
	}
	now := time.Now().UTC()
	if value.CreatedAt.IsZero() {
		value.CreatedAt = now
	}
	if value.UpdatedAt.IsZero() {
		value.UpdatedAt = value.CreatedAt
	}
	intent := SessionCreationIntent{
		ID: CreationIntentID(value.ID), Session: value, Status: SessionCreationPending,
		CreatedAt: value.CreatedAt, UpdatedAt: value.CreatedAt,
	}
	if err := s.deps.Sessions.BeginSessionCreation(ctx, intent); err != nil {
		return Session{}, fmt.Errorf("create Coding Agent session: persist creation intent: %w", err)
	}
	if err := reconcileSessionCreation(ctx, s.deps.Sessions, s.deps.AgentSessions, intent); err != nil {
		return Session{}, fmt.Errorf("create Coding Agent session: reconcile intent %q: %w", intent.ID, err)
	}
	s.setState(value.ID, RuntimeIdle)
	return value, nil
}

// TurnMode selects the explicit product entry mode for a new request.
type TurnMode string

const (
	TurnModeDirect TurnMode = "direct"
	TurnModePlan   TurnMode = "plan"
)

// TurnRequest starts one complete user-triggered Coding Agent turn.
type TurnRequest struct {
	SessionID SessionID
	Text      string
	Mode      TurnMode
}

// TurnResult contains product-level terminal facts without lower-layer messages or events.
type TurnResult struct {
	TurnID        TurnID
	RunID         RunID
	Status        string
	Response      string
	Steps         int
	Reason        string
	InterruptID   string
	InterruptKind string
}

// StartTurn builds trusted Coding policy and invokes the generic Agent runtime.
func (s *Service) StartTurn(ctx context.Context, request TurnRequest) (TurnResult, error) {
	if request.SessionID == "" || strings.TrimSpace(request.Text) == "" {
		return TurnResult{}, errors.New("start Coding Agent turn: session id and text are required")
	}
	operation := s.operationLock(request.SessionID)
	operation.Lock()
	defer operation.Unlock()
	product, err := s.deps.Sessions.LoadSession(ctx, request.SessionID)
	if err != nil {
		return TurnResult{}, fmt.Errorf("start Coding Agent turn: load session: %w", err)
	}
	durable, err := s.deps.AgentSessions.Load(ctx, product.AgentSessionID)
	if err != nil {
		return TurnResult{}, fmt.Errorf("start Coding Agent turn: load Agent session: %w", err)
	}
	if recovery := agentsession.AnalyzeRecovery(durable); len(recovery.PendingRuns) != 0 || len(recovery.PendingInterrupts) != 0 || len(recovery.PendingTools) != 0 {
		return TurnResult{}, errors.New("start Coding Agent turn: the session has unfinished work that must be resumed first")
	}
	if request.Mode == "" {
		request.Mode = TurnModeDirect
	}
	if request.Mode != TurnModeDirect && request.Mode != TurnModePlan {
		return TurnResult{}, fmt.Errorf("start Coding Agent turn: unsupported mode %q", request.Mode)
	}
	if !s.features.ProductTurns {
		if request.Mode == TurnModePlan {
			return TurnResult{}, errors.New("start Coding Agent turn: Plan mode is disabled")
		}
		return s.startLegacyTurn(ctx, product, strings.TrimSpace(request.Text))
	}
	if request.Mode == TurnModePlan && !s.features.PlanMode {
		return TurnResult{}, errors.New("start Coding Agent turn: Plan mode is disabled")
	}
	turnIDValue, err := newID("turn")
	if err != nil {
		return TurnResult{}, err
	}
	runIDValue, err := newID("run")
	if err != nil {
		return TurnResult{}, err
	}
	entryIDValue, err := newID("entry")
	if err != nil {
		return TurnResult{}, err
	}
	now := time.Now().UTC()
	phase, profile, entrySource := TurnPhaseDirect, CapabilityDirect, TurnEntryDirect
	if request.Mode == TurnModePlan {
		phase, profile, entrySource = TurnPhasePlanning, CapabilityPlan, TurnEntryUserPlan
	}
	turn := Turn{
		ID: TurnID(turnIDValue), SessionID: product.ID, RequestText: strings.TrimSpace(request.Text),
		EntrySource: entrySource, Phase: phase, Status: TurnPending, Strategy: ExecutionSingle, Revision: 1,
		Runs:      []RunBinding{{RunID: agentsession.RunID(runIDValue), UserEntryID: agentsession.EntryID(entryIDValue), Phase: phase, Profile: profile, Status: RunBindingPending}},
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.deps.Turns.CreateTurn(ctx, turn); err != nil {
		return TurnResult{}, fmt.Errorf("start Coding Agent turn: persist Product Turn: %w", err)
	}
	if request.Mode == TurnModePlan {
		if err := s.publishPlanEvent(ctx, product, turn, EventPlanStarted, ""); err != nil {
			return TurnResult{}, fmt.Errorf("start Coding Agent turn: publish Plan start: %w", err)
		}
	}
	environment, err := s.prepareRunEnvironment(ctx, product, turn, RunID(runIDValue), "", profile)
	if err != nil {
		return TurnResult{}, fmt.Errorf("start Coding Agent turn: %w", err)
	}
	turn, err = s.markRunStarted(ctx, turn, agentsession.RunID(runIDValue), now)
	if err != nil {
		return TurnResult{}, fmt.Errorf("start Coding Agent turn: %w", err)
	}
	s.setState(product.ID, RuntimeRunning)
	runCtx, finishActive := s.beginActiveTurn(ctx, product.ID)
	defer finishActive()
	result, runErr := s.deps.Agent.Run(runCtx, agent.RunRequest{
		SessionID: product.AgentSessionID, Lane: sessionLane(product), RunID: agentsession.RunID(runIDValue), UserEntryID: agentsession.EntryID(entryIDValue), SystemPrompt: environment.systemPrompt,
		Model:            llm.ModelRef{Provider: product.ProviderProfileID, Model: product.ModelID},
		UserMessage:      llm.Message{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentText, Text: turn.RequestText}}, Timestamp: now},
		UntrustedContext: environment.untrustedContext, Tools: environment.tools, ToolCallPreviewer: environment.toolCallPreviewer, Limits: s.deps.Limits,
	}, environment.events)
	if result.RunID == "" {
		result.RunID = agentsession.RunID(runIDValue)
	}
	turn, err = s.refreshProductTurn(context.WithoutCancel(ctx), turn)
	if err != nil {
		return productTurnResult(turn.ID, result), fmt.Errorf("start Coding Agent turn: %w", err)
	}
	turn, finishErr := s.finishProductRun(context.WithoutCancel(ctx), turn, result, runErr)
	s.setState(product.ID, runtimeStateForTurn(turn))
	touchErr := s.touchSession(context.WithoutCancel(ctx), product)
	productResult := productTurnResult(turn.ID, result)
	if runErr != nil {
		return productResult, fmt.Errorf("start Coding Agent turn: %w", runErr)
	}
	if finishErr != nil {
		return productResult, fmt.Errorf("start Coding Agent turn: persist terminal Product Turn: %w", finishErr)
	}
	if request.Mode == TurnModePlan && turn.Phase == TurnPhaseAwaitingPlanApproval && turn.PlanVersion != 0 {
		if err := s.publishPlanEvent(context.WithoutCancel(ctx), product, turn, EventPlanCreated, ""); err != nil {
			return productResult, fmt.Errorf("start Coding Agent turn: publish Plan creation: %w", err)
		}
	}
	if request.Mode == TurnModeDirect && turn.Phase == TurnPhaseAwaitingPlanEntryApproval && turn.PlanEntrySuggestion != nil {
		if err := s.publishPlanEntryEvent(context.WithoutCancel(ctx), product, turn, *turn.PlanEntrySuggestion, EventPlanEntrySuggested, ""); err != nil {
			return productResult, fmt.Errorf("start Coding Agent turn: publish Plan entry suggestion: %w", err)
		}
	}
	if result.Status == agent.RunHandedOff && turn.Status == TurnRunning && turn.Phase == TurnPhasePlanning && turn.Runs[len(turn.Runs)-1].Profile == CapabilityPlan {
		if touchErr != nil {
			return productResult, fmt.Errorf("start Coding Agent turn: update product session: %w", touchErr)
		}
		return s.continueTurnLocked(ctx, product, turn)
	}
	if touchErr != nil {
		return productResult, fmt.Errorf("start Coding Agent turn: update product session: %w", touchErr)
	}
	return productResult, nil
}

// ResolutionDecision identifies a product-level response to a durable interrupt.
type ResolutionDecision string

const (
	// ResolutionApproved supplies a successful tool result and continues the turn.
	ResolutionApproved ResolutionDecision = "approved"
	// ResolutionDenied supplies a denied tool result and lets the model adapt.
	ResolutionDenied ResolutionDecision = "denied"
	// ResolutionCancelled supplies a cancelled tool result and lets the model adapt.
	ResolutionCancelled ResolutionDecision = "cancelled"
)

// ResumeTurnRequest resolves one pending product interrupt without exposing tool runtime types.
type ResumeTurnRequest struct {
	SessionID   SessionID
	TurnID      TurnID
	InterruptID string
	Decision    ResolutionDecision
	GrantScope  PermissionGrantScope
	Message     string
	Details     json.RawMessage
	Strategy    ExecutionStrategy
}

// RecoverTurnRequest applies one action from the current product RecoveryPlan.
type RecoverTurnRequest struct {
	SessionID SessionID
	TurnID    TurnID
	ActionID  string
	Decision  RecoveryDecision
}

// ResumeTurn continues the same durable turn after product-level external input.
func (s *Service) ResumeTurn(ctx context.Context, request ResumeTurnRequest) (TurnResult, error) {
	if request.SessionID == "" || request.TurnID == "" || strings.TrimSpace(request.InterruptID) == "" {
		return TurnResult{}, errors.New("resume Coding Agent turn: session, turn, and interrupt ids are required")
	}
	if len(request.Details) != 0 && !json.Valid(request.Details) {
		return TurnResult{}, errors.New("resume Coding Agent turn: details must be valid JSON")
	}
	operation := s.operationLock(request.SessionID)
	operation.Lock()
	defer operation.Unlock()
	product, err := s.deps.Sessions.LoadSession(ctx, request.SessionID)
	if err != nil {
		return TurnResult{}, fmt.Errorf("resume Coding Agent turn: load session: %w", err)
	}
	turn := Turn{ID: request.TurnID, SessionID: request.SessionID}
	binding := RunBinding{RunID: agentsession.RunID(request.TurnID), Status: RunBindingInterrupted}
	if s.features.ProductTurns {
		turn, err = s.deps.Turns.LoadTurn(ctx, request.TurnID)
		if err != nil {
			return TurnResult{}, fmt.Errorf("resume Coding Agent turn: load Product Turn: %w", err)
		}
		if turn.SessionID != request.SessionID {
			return TurnResult{}, errors.New("resume Coding Agent turn: Product Turn belongs to another session")
		}
		var found bool
		binding, found = turn.ActiveRun()
		if !found || binding.Status != RunBindingInterrupted {
			return TurnResult{}, errors.New("resume Coding Agent turn: Product Turn has no interrupted Run")
		}
	}
	if request.GrantScope == "" {
		request.GrantScope = PermissionGrantOnce
	}
	agentSessionID := product.AgentSessionID
	agentLane := sessionLane(product)
	var resumedChild *ChildAgent
	if binding.ChildAgentID != "" {
		if s.deps.Children == nil {
			return TurnResult{}, errors.New("resume Coding Agent turn: child Agent repository is unavailable")
		}
		child, childErr := s.deps.Children.LoadChildAgent(ctx, binding.ChildAgentID)
		if childErr != nil {
			return TurnResult{}, fmt.Errorf("resume Coding Agent turn: load child Agent: %w", childErr)
		}
		if child.ParentSessionID != product.ID || child.ParentTurnID != turn.ID || child.RunID != binding.RunID || child.Status != ChildAgentAwaitingApproval {
			return TurnResult{}, errors.New("resume Coding Agent turn: interrupted Run is not the active child Agent approval boundary")
		}
		agentSessionID, agentLane, resumedChild = child.AgentSessionID, agentsession.MainLane, &child
	}
	durableInterrupts, loadInterruptErr := s.deps.AgentSessions.Load(ctx, agentSessionID)
	if loadInterruptErr != nil {
		return TurnResult{}, fmt.Errorf("resume Coding Agent turn: load pending interrupt: %w", loadInterruptErr)
	}
	pendingKind := ""
	for _, pending := range agentsession.AnalyzeRecovery(durableInterrupts).PendingInterrupts {
		if pending.RunID == binding.RunID && pending.InterruptID == request.InterruptID {
			pendingKind = pending.Kind
			break
		}
	}
	if pendingKind == "" {
		return TurnResult{}, errors.New("resume Coding Agent turn: interrupt is not the current durable decision boundary")
	}
	planProfile := binding.Profile == CapabilityPlan || binding.Profile == CapabilityPlanWorkspace
	planApproval := s.features.ProductTurns && pendingKind == "plan_approval" && (turn.Phase == TurnPhaseAwaitingPlanApproval || turn.Phase == TurnPhasePlanning) && planProfile
	planEntryApproval := s.features.ProductTurns && pendingKind == planEntryApprovalKind && (turn.Phase == TurnPhaseAwaitingPlanEntryApproval || turn.Phase == TurnPhaseDirect) && binding.Profile == CapabilityDirect
	planReplanApproval := s.features.ProductTurns && pendingKind == planReplanApprovalKind && (turn.Phase == TurnPhaseNeedsReplan || turn.Phase == TurnPhaseExecuting) && binding.Profile == CapabilityDirect && binding.Phase == TurnPhaseExecuting
	clarification := s.features.ProductTurns && pendingKind == clarificationInterruptKind && turn.Phase == TurnPhasePlanning && planProfile
	if pendingKind == planEntryApprovalKind && !planEntryApproval {
		return TurnResult{}, errors.New("resume Coding Agent turn: Plan entry suggestion is not attached to the active Direct Run")
	}
	if planEntryApproval && turn.PlanEntrySuggestion == nil {
		return TurnResult{}, errors.New("resume Coding Agent turn: durable Plan entry suggestion is unavailable")
	}
	if pendingKind == planReplanApprovalKind && !planReplanApproval {
		return TurnResult{}, errors.New("resume Coding Agent turn: Plan replan request is not attached to the active execution Run")
	}
	if planReplanApproval && turn.PlanReplan == nil {
		return TurnResult{}, errors.New("resume Coding Agent turn: durable Plan replan request is unavailable")
	}
	if planApproval && turn.Phase == TurnPhasePlanning && request.Decision != ResolutionDenied {
		return TurnResult{}, errors.New("resume Coding Agent turn: only the already-recorded Plan revision request can continue")
	}
	if planEntryApproval && turn.Phase == TurnPhaseDirect && request.Decision != ResolutionDenied {
		return TurnResult{}, errors.New("resume Coding Agent turn: only the already-recorded Direct continuation can proceed")
	}
	if planEntryApproval && turn.Phase == TurnPhaseDirect {
		declined := false
		for _, reason := range turn.DeclinedPlanReasons {
			declined = declined || reason == turn.PlanEntrySuggestion.ReasonCode
		}
		if !declined {
			return TurnResult{}, errors.New("resume Coding Agent turn: Direct continuation is missing its durable decline record")
		}
	}
	if planReplanApproval && turn.Phase == TurnPhaseExecuting && request.Decision != ResolutionDenied {
		return TurnResult{}, errors.New("resume Coding Agent turn: only the already-recorded execution continuation can proceed")
	}
	if pendingKind == clarificationInterruptKind && !clarification {
		return TurnResult{}, errors.New("resume Coding Agent turn: clarification is not attached to the active Planning Run")
	}
	if clarification {
		if request.GrantScope == PermissionGrantSession {
			return TurnResult{}, errors.New("resume Coding Agent turn: clarification cannot create a permission grant")
		}
		if request.Decision != ResolutionApproved || len(request.Details) == 0 {
			return TurnResult{}, errors.New("resume Coding Agent turn: clarification requires one selected or free-form answer")
		}
	}
	if (planApproval || planEntryApproval || planReplanApproval) && request.GrantScope == PermissionGrantSession {
		return TurnResult{}, errors.New("resume Coding Agent turn: Plan decisions cannot create a permission grant")
	}
	reviewedPlan := Plan{}
	if planApproval {
		if s.deps.Plans == nil {
			return TurnResult{}, errors.New("resume Coding Agent turn: Plan repository is unavailable")
		}
		reviewedPlan, err = s.deps.Plans.LoadPlan(ctx, turn.PlanID, turn.PlanVersion)
		if err != nil || reviewedPlan.Digest != turn.PlanDigest {
			return TurnResult{}, errors.New("resume Coding Agent turn: current Plan revision is unavailable or changed")
		}
	}
	forcedPlanRevisionByDrift := false
	forcedPlanRevisionFrom := uint64(0)
	var resolvedPlanReplan *PlanReplanRequest
	if planApproval && request.Decision == ResolutionApproved && turn.Phase == TurnPhaseAwaitingPlanApproval {
		drift, driftErr := s.assessPlanWorkspace(ctx, product, turn, WorkspaceDriftAtApproval)
		if driftErr != nil {
			return TurnResult{}, fmt.Errorf("resume Coding Agent turn: verify Plan workspace before approval: %w", driftErr)
		}
		if drift.Severity != WorkspaceDriftNone {
			turn, driftErr = s.recordWorkspaceDrift(ctx, turn, drift)
			if driftErr != nil {
				return TurnResult{}, driftErr
			}
			kind := EventPlanDriftDetected
			if drift.Severity == WorkspaceDriftMaterial {
				kind = EventPlanReplanRequested
				forcedPlanRevisionByDrift = true
				forcedPlanRevisionFrom = turn.PlanVersion
				request.Decision = ResolutionDenied
				request.Message = "Material workspace drift invalidated this Plan baseline. Remain read-only, inspect the changed facts, and submit a complete new Plan version."
			}
			if err := s.publishPlanLifecycleEvent(context.WithoutCancel(ctx), product, turn, kind, &drift, nil); err != nil {
				return TurnResult{}, fmt.Errorf("resume Coding Agent turn: publish Plan workspace drift: %w", err)
			}
		}
	}
	if planApproval && request.Decision == ResolutionApproved && turn.Phase == TurnPhaseAwaitingPlanApproval {
		selected := request.Strategy
		if selected == "" {
			selected = reviewedPlan.RecommendedStrategy
		}
		if reviewedPlan.CompletionMode == PlanCompletionDeliverable {
			selected = ExecutionSingle
		}
		if !validExecutionStrategy(selected) || selected != ExecutionSingle && selected != reviewedPlan.RecommendedStrategy {
			return TurnResult{}, errors.New("resume Coding Agent turn: selected execution strategy was not offered by the reviewed Plan")
		}
		if isWorkflowStrategy(selected) && (!s.features.Workflows || s.deps.Workflows == nil) {
			return TurnResult{}, errors.New("resume Coding Agent turn: Workflow execution is disabled")
		}
		if (selected == ExecutionWorkflowMultiSerial || selected == ExecutionWorkflowMultiParallelReadOnly) && (!s.features.Subagents || s.deps.Children == nil) {
			return TurnResult{}, errors.New("resume Coding Agent turn: multi-Agent execution is disabled")
		}
		if selected == ExecutionWorkflowMultiParallelReadOnly && !s.features.ParallelSubagents {
			return TurnResult{}, errors.New("resume Coding Agent turn: parallel read-only execution is disabled")
		}
		if turn.Strategy != selected {
			expected := turn.Revision
			turn.Strategy = selected
			if selected == ExecutionSingle {
				turn.WorkflowID = ""
			}
			turn.UpdatedAt = time.Now().UTC()
			turn.Revision++
			if err := s.deps.Turns.SaveTurn(ctx, turn, expected); err != nil {
				return TurnResult{}, fmt.Errorf("resume Coding Agent turn: select execution strategy: %w", err)
			}
		}
	}
	entrySuggestion := PlanEntrySuggestion{}
	if planEntryApproval && turn.PlanEntrySuggestion != nil {
		entrySuggestion = *turn.PlanEntrySuggestion
	}
	if planEntryApproval && request.Decision == ResolutionDenied && turn.Phase == TurnPhaseAwaitingPlanEntryApproval {
		expected := turn.Revision
		turn.Phase = TurnPhaseDirect
		turn.DeclinedPlanReasons = append(turn.DeclinedPlanReasons, entrySuggestion.ReasonCode)
		turn.UpdatedAt = time.Now().UTC()
		turn.Revision++
		if err := s.deps.Turns.SaveTurn(ctx, turn, expected); err != nil {
			return TurnResult{}, fmt.Errorf("resume Coding Agent turn: decline Plan entry: %w", err)
		}
	}
	if planApproval && request.Decision == ResolutionDenied && turn.Phase == TurnPhaseAwaitingPlanApproval {
		expected := turn.Revision
		turn.Phase = TurnPhasePlanning
		turn.UpdatedAt = time.Now().UTC()
		turn.Revision++
		if err := s.deps.Turns.SaveTurn(ctx, turn, expected); err != nil {
			return TurnResult{}, fmt.Errorf("resume Coding Agent turn: begin Plan revision: %w", err)
		}
	}
	if planReplanApproval && turn.Phase == TurnPhaseNeedsReplan {
		now := time.Now().UTC()
		decision := ""
		switch request.Decision {
		case ResolutionApproved:
			decision = "replan"
		case ResolutionDenied:
			decision = "continue"
			turn.Phase = TurnPhaseExecuting
		case ResolutionCancelled:
			decision = "cancelled"
		default:
			return TurnResult{}, fmt.Errorf("resume Coding Agent turn: unsupported replan decision %q", request.Decision)
		}
		if turn.PlanReplan.Decision == "" {
			expected := turn.Revision
			if err := resolvePlanReplan(&turn, decision, now); err != nil {
				return TurnResult{}, err
			}
			turn.UpdatedAt = now
			turn.Revision++
			if err := s.deps.Turns.SaveTurn(ctx, turn, expected); err != nil {
				return TurnResult{}, fmt.Errorf("resume Coding Agent turn: persist replan decision: %w", err)
			}
		} else if turn.PlanReplan.Decision != decision {
			return TurnResult{}, errors.New("resume Coding Agent turn: replan request was already resolved differently")
		}
		resolved := *turn.PlanReplan
		resolvedPlanReplan = &resolved
	}
	if request.GrantScope != PermissionGrantOnce && request.GrantScope != PermissionGrantSession {
		return TurnResult{}, fmt.Errorf("resume Coding Agent turn: unsupported grant scope %q", request.GrantScope)
	}
	if request.GrantScope == PermissionGrantSession {
		if request.Decision != ResolutionApproved {
			return TurnResult{}, errors.New("resume Coding Agent turn: a session grant requires an approved decision")
		}
		grant, grantErr := deriveSessionGrant(product, durableInterrupts, request, binding.RunID, time.Now().UTC())
		if grantErr != nil {
			return TurnResult{}, fmt.Errorf("resume Coding Agent turn: %w", grantErr)
		}
		appended, appendErr := appendPermissionGrant(&product, grant)
		if appendErr != nil {
			return TurnResult{}, fmt.Errorf("resume Coding Agent turn: %w", appendErr)
		}
		if appended {
			product.UpdatedAt = grant.CreatedAt
			if saveErr := s.deps.Sessions.SaveSession(ctx, product); saveErr != nil {
				return TurnResult{}, fmt.Errorf("resume Coding Agent turn: save session grant: %w", saveErr)
			}
		}
	}
	var environment runEnvironment
	if resumedChild != nil {
		environment, err = s.prepareChildRunEnvironment(ctx, product, turn, *resumedChild)
	} else {
		environment, err = s.prepareRunEnvironment(ctx, product, turn, RunID(binding.RunID), binding.NodeID, binding.Profile)
	}
	if err != nil {
		return TurnResult{}, fmt.Errorf("resume Coding Agent turn: %w", err)
	}
	resolution, err := productResolution(request)
	if err != nil {
		return TurnResult{}, err
	}
	if s.features.ProductTurns {
		turn, err = s.markRunResumed(ctx, turn, binding.RunID, time.Now().UTC())
		if err != nil {
			return TurnResult{}, fmt.Errorf("resume Coding Agent turn: %w", err)
		}
	}
	if resumedChild != nil {
		child, childErr := s.transitionChild(ctx, *resumedChild, ChildAgentRunning, nil, "")
		if childErr != nil {
			return TurnResult{}, fmt.Errorf("resume Coding Agent turn: %w", childErr)
		}
		resumedChild = &child
	}
	s.setState(product.ID, RuntimeRunning)
	runCtx, finishActive := s.beginActiveTurn(ctx, product.ID)
	defer finishActive()
	resumeLimits, limitErr := s.workflowRunLimitsForTurn(ctx, turn)
	if limitErr != nil {
		return TurnResult{}, fmt.Errorf("resume Coding Agent turn: %w", limitErr)
	}
	result, resumeErr := s.deps.Agent.Resume(runCtx, agent.ResumeRequest{
		SessionID: agentSessionID, Lane: agentLane, RunID: binding.RunID, InterruptID: request.InterruptID,
		Resolution: resolution, SystemPrompt: environment.systemPrompt,
		Model: llm.ModelRef{Provider: product.ProviderProfileID, Model: product.ModelID}, UntrustedContext: environment.untrustedContext, Tools: environment.tools, ToolCallPreviewer: environment.toolCallPreviewer, Limits: resumeLimits,
	}, environment.events)
	if result.RunID == "" {
		result.RunID = binding.RunID
	}
	planCompletion := PlanCompletionExecute
	if planApproval {
		planCompletion = reviewedPlan.CompletionMode
	}
	if planApproval && request.Decision == ResolutionCancelled && result.Status == agent.RunHandedOff {
		result.Status = agent.RunAborted
		result.Reason = "plan_cancelled"
	}
	if planEntryApproval && request.Decision == ResolutionCancelled && result.Status == agent.RunHandedOff {
		result.Status = agent.RunAborted
		result.Reason = "plan_entry_cancelled"
	}
	if planReplanApproval && request.Decision == ResolutionCancelled && result.Status == agent.RunHandedOff {
		result.Status = agent.RunAborted
		result.Reason = "plan_replan_cancelled"
	}
	if planApproval && request.Decision == ResolutionApproved && planCompletion == PlanCompletionDeliverable && result.Status == agent.RunHandedOff {
		result.Status = agent.RunCompleted
		result.Reason = "plan_delivered"
	}
	var finishErr error
	if s.features.ProductTurns {
		turn, finishErr = s.refreshProductTurn(context.WithoutCancel(ctx), turn)
		if finishErr == nil && resumeErr == nil && forcedPlanRevisionByDrift && (result.Status != agent.RunInterrupted || turn.Phase != TurnPhaseAwaitingPlanApproval || turn.PlanVersion <= forcedPlanRevisionFrom) {
			result.Status = agent.RunFailed
			result.Reason = "required_plan_revision_missing"
			resumeErr = errors.New("material workspace drift did not produce a new Plan approval boundary")
		}
		if finishErr == nil && resumedChild != nil {
			var child ChildAgent
			turn, child, result, resumeErr, finishErr = s.finishChildProductRun(context.WithoutCancel(ctx), turn, *resumedChild, result, resumeErr)
			resumedChild = &child
		} else if finishErr == nil {
			turn, finishErr = s.finishProductRun(context.WithoutCancel(ctx), turn, result, resumeErr)
		}
		s.setState(product.ID, runtimeStateForTurn(turn))
	} else {
		s.setState(product.ID, runtimeStateForResult(result, resumeErr))
	}
	touchErr := s.touchSession(context.WithoutCancel(ctx), product)
	productResult := productTurnResult(turn.ID, result)
	workflowNodeRun := isWorkflowStrategy(turn.Strategy) && binding.NodeID != "" && !planReplanApproval
	if finishErr != nil {
		return productResult, fmt.Errorf("resume Coding Agent turn: persist Product Turn: %w", finishErr)
	}
	if resumedChild != nil && resumedChild.Kind == ChildAgentPlanExplore && result.Status != agent.RunInterrupted {
		if touchErr != nil {
			return productResult, fmt.Errorf("resume Coding Agent turn: update product session: %w", touchErr)
		}
		return s.finishPlanExploreAndContinue(context.WithoutCancel(ctx), product, turn, *resumedChild)
	}
	if resumeErr != nil && !workflowNodeRun {
		return productResult, fmt.Errorf("resume Coding Agent turn: %w", resumeErr)
	}
	if planApproval {
		kind, decision := EventKind(""), string(request.Decision)
		switch request.Decision {
		case ResolutionDenied:
			if result.Status == agent.RunInterrupted && turn.PlanVersion > 1 {
				kind = EventPlanRevised
			}
		case ResolutionApproved:
			kind = EventPlanApproved
		case ResolutionCancelled:
			kind = EventPlanCancelled
		}
		if kind != "" {
			if err := s.publishPlanEvent(context.WithoutCancel(ctx), product, turn, kind, decision); err != nil {
				return productResult, fmt.Errorf("resume Coding Agent turn: publish Plan decision: %w", err)
			}
		}
	}
	if planEntryApproval {
		kind := EventPlanEntryDeclined
		switch request.Decision {
		case ResolutionApproved:
			kind = EventPlanEntryApproved
		case ResolutionCancelled:
			kind = EventPlanEntryCancelled
		}
		if err := s.publishPlanEntryEvent(context.WithoutCancel(ctx), product, turn, entrySuggestion, kind, string(request.Decision)); err != nil {
			return productResult, fmt.Errorf("resume Coding Agent turn: publish Plan entry decision: %w", err)
		}
		if request.Decision == ResolutionDenied && turn.Phase == TurnPhaseAwaitingPlanEntryApproval && turn.PlanEntrySuggestion != nil && turn.PlanEntrySuggestion.Digest != entrySuggestion.Digest {
			if err := s.publishPlanEntryEvent(context.WithoutCancel(ctx), product, turn, *turn.PlanEntrySuggestion, EventPlanEntrySuggested, ""); err != nil {
				return productResult, fmt.Errorf("resume Coding Agent turn: publish renewed Plan entry suggestion: %w", err)
			}
		}
	}
	if planReplanApproval {
		if err := s.publishPlanLifecycleEvent(context.WithoutCancel(ctx), product, turn, EventPlanReplanResolved, turn.WorkspaceDrift, resolvedPlanReplan); err != nil {
			return productResult, fmt.Errorf("resume Coding Agent turn: publish Plan replan decision: %w", err)
		}
		if request.Decision == ResolutionApproved && isWorkflowStrategy(turn.Strategy) && turn.WorkflowID != "" {
			durableWorkflow, loadErr := s.deps.Workflows.LoadWorkflow(context.WithoutCancel(ctx), workflow.ID(turn.WorkflowID))
			if loadErr != nil {
				return productResult, fmt.Errorf("resume Coding Agent turn: load Workflow for replan: %w", loadErr)
			}
			if durableWorkflow.Status == workflow.StatusRunning || durableWorkflow.Status == workflow.StatusBlocked {
				if _, appendErr := s.appendWorkflowTransition(context.WithoutCancel(ctx), durableWorkflow, workflow.Event{Type: workflow.EventWorkflowReplanRequested, Summary: resolvedPlanReplan.Summary}); appendErr != nil {
					return productResult, fmt.Errorf("resume Coding Agent turn: pause Workflow for replan: %w", appendErr)
				}
			}
		}
	}
	if err := s.publishPendingPlanReplanEvent(context.WithoutCancel(ctx), product, turn, result); err != nil {
		return productResult, fmt.Errorf("resume Coding Agent turn: publish renewed Plan replan request: %w", err)
	}
	if workflowNodeRun && result.Status != agent.RunInterrupted {
		if touchErr != nil {
			return productResult, fmt.Errorf("resume Coding Agent turn: update product session: %w", touchErr)
		}
		return s.advanceWorkflowAfterRunLocked(context.WithoutCancel(ctx), product, turn, result, resumeErr, productResult)
	}
	if planEntryApproval && request.Decision == ResolutionApproved {
		if result.Status != agent.RunHandedOff || turn.Status != TurnRunning {
			return productResult, errors.New("resume Coding Agent turn: approved Plan entry did not reach a durable control handoff")
		}
		if err := s.publishPlanEvent(context.WithoutCancel(ctx), product, turn, EventPlanStarted, "agent_suggestion"); err != nil {
			return productResult, fmt.Errorf("resume Coding Agent turn: publish Plan start: %w", err)
		}
		return s.continueTurnLocked(ctx, product, turn)
	}
	if planApproval && request.Decision == ResolutionApproved {
		if planCompletion == PlanCompletionDeliverable {
			if result.Status != agent.RunCompleted || turn.Status != TurnCompleted {
				return productResult, errors.New("resume Coding Agent turn: accepted deliverable Plan did not complete its Product Turn")
			}
			if touchErr != nil {
				return productResult, fmt.Errorf("resume Coding Agent turn: update product session: %w", touchErr)
			}
			return productResult, nil
		}
		if result.Status != agent.RunHandedOff || turn.Status != TurnRunning {
			return productResult, errors.New("resume Coding Agent turn: approved Plan did not reach a durable control handoff")
		}
		return s.continueTurnLocked(ctx, product, turn)
	}
	if planReplanApproval && request.Decision == ResolutionApproved {
		if result.Status != agent.RunHandedOff || turn.Status != TurnRunning || turn.Phase != TurnPhaseNeedsReplan {
			return productResult, errors.New("resume Coding Agent turn: approved replan did not reach a durable control handoff")
		}
		return s.continueTurnLocked(ctx, product, turn)
	}
	if touchErr != nil {
		return productResult, fmt.Errorf("resume Coding Agent turn: update product session: %w", touchErr)
	}
	return productResult, nil
}

func productResolution(request ResumeTurnRequest) (tool.Result, error) {
	status := tool.ResultCompleted
	defaultMessage := "The requested action was approved."
	switch request.Decision {
	case ResolutionApproved:
	case ResolutionDenied:
		status = tool.ResultDenied
		defaultMessage = "The requested action was denied by the user."
	case ResolutionCancelled:
		status = tool.ResultCancelled
		defaultMessage = "The requested action was cancelled by the user."
	default:
		return tool.Result{}, fmt.Errorf("resume Coding Agent turn: unsupported decision %q", request.Decision)
	}
	message := strings.TrimSpace(request.Message)
	if message == "" {
		message = defaultMessage
	}
	return tool.Result{Status: status, Content: []llm.Content{{Type: llm.ContentText, Text: message}}, Details: append(json.RawMessage(nil), request.Details...)}, nil
}

// Snapshot returns the authoritative product projection for one session.
func (s *Service) Snapshot(ctx context.Context, id SessionID) (Snapshot, error) {
	product, err := s.deps.Sessions.LoadSession(ctx, id)
	if err != nil {
		return Snapshot{}, fmt.Errorf("load Coding Agent snapshot: %w", err)
	}
	durable, err := s.deps.AgentSessions.Load(ctx, product.AgentSessionID)
	if err != nil {
		return Snapshot{}, fmt.Errorf("load Coding Agent snapshot: load Agent session: %w", err)
	}
	revision := uint64(0)
	if len(durable.Log) != 0 {
		revision = durable.Log[len(durable.Log)-1].Sequence
	}
	if !s.features.ProductTurns {
		return ProjectSnapshot(product, durable, sessionLane(product), s.state(id), revision)
	}
	turns, err := s.deps.Turns.ListTurns(ctx, id)
	if err != nil {
		return Snapshot{}, fmt.Errorf("load Coding Agent snapshot: list Product Turns: %w", err)
	}
	snapshot, err := ProjectSnapshotWithTurns(product, durable, sessionLane(product), s.state(id), revision, turns)
	if err != nil {
		return Snapshot{}, err
	}
	if s.deps.Plans != nil {
		if err := s.projectPlanSnapshot(ctx, &snapshot, turns); err != nil {
			return Snapshot{}, err
		}
	}
	if s.deps.Workflows != nil {
		if err := s.projectWorkflowSnapshot(ctx, &snapshot, turns); err != nil {
			return Snapshot{}, err
		}
	}
	if s.deps.Children != nil {
		if err := s.projectChildAgentSnapshot(ctx, &snapshot, turns); err != nil {
			return Snapshot{}, err
		}
	}
	return snapshot, nil
}

func (s *Service) projectChildAgentSnapshot(ctx context.Context, snapshot *Snapshot, turns []Turn) error {
	if snapshot == nil || s.deps.Children == nil {
		return nil
	}
	for _, turn := range turns {
		children, err := s.deps.Children.ListChildAgents(ctx, turn.ID)
		if err != nil {
			return fmt.Errorf("load Coding Agent snapshot: list child Agents: %w", err)
		}
		for _, child := range children {
			value := ChildAgentSnapshot{
				ID: child.ID, Kind: child.Kind, TurnID: child.ParentTurnID, WorkflowID: child.WorkflowID, NodeID: child.NodeID,
				Role: string(child.Role), Profile: child.Profile, PolicyVersion: child.PolicyVersion, Status: child.Status, Attempt: child.Attempt,
				Goal: boundedUTF8(redactSensitiveText(child.Task.Goal), maxPlanTextBytes), Failure: boundedUTF8(redactSensitiveText(child.Failure), 2048),
				StartedAt: child.StartedAt, CompletedAt: child.CompletedAt,
			}
			if child.Result != nil {
				value.Conclusion = boundedUTF8(redactSensitiveText(child.Result.Conclusion), maxPlanTextBytes)
				value.Evidence = cloneTaskEvidence(child.Result.Evidence)
				for index := range value.Evidence {
					value.Evidence[index].Summary = boundedUTF8(redactSensitiveText(value.Evidence[index].Summary), maxPlanTextBytes)
				}
				value.Validation = append([]string(nil), child.Result.Validation...)
				value.Artifacts = append([]string(nil), child.Result.ArtifactRefs...)
				value.Unresolved = append([]string(nil), child.Result.Unresolved...)
			}
			snapshot.ChildAgents = append(snapshot.ChildAgents, value)
			durable, loadErr := s.deps.AgentSessions.Load(ctx, child.AgentSessionID)
			if errors.Is(loadErr, agentsession.ErrNotFound) && child.Status == ChildAgentCreating {
				continue
			}
			if loadErr != nil {
				return fmt.Errorf("load Coding Agent snapshot: load child Agent session: %w", loadErr)
			}
			if len(durable.Log) != 0 {
				snapshot.Revision += durable.Log[len(durable.Log)-1].Sequence
			}
			entries, branchErr := agentsession.BranchEntries(durable, agentsession.MainLane)
			if branchErr != nil {
				return fmt.Errorf("load Coding Agent snapshot: load child Agent branch: %w", branchErr)
			}
			metrics := projectSessionMetrics(entries, durable.Records)
			snapshot.Metrics.InputTokens += metrics.InputTokens
			snapshot.Metrics.OutputTokens += metrics.OutputTokens
			snapshot.Metrics.CacheReadTokens += metrics.CacheReadTokens
			snapshot.Metrics.CacheWriteTokens += metrics.CacheWriteTokens
			snapshot.Metrics.ReasoningTokens += metrics.ReasoningTokens
			snapshot.Metrics.TotalTokens += metrics.TotalTokens
			snapshot.Metrics.Cost += metrics.Cost
			if child.Kind == ChildAgentWorkflowNode {
				snapshot.Metrics.Workflow.TotalTokens += metrics.TotalTokens
				snapshot.Metrics.Workflow.Cost += metrics.Cost
			}
			if child.Status != ChildAgentRunning && child.Status != ChildAgentAwaitingApproval {
				continue
			}
			recovery := agentsession.AnalyzeRecovery(durable)
			for _, pending := range recovery.PendingInterrupts {
				projected := PendingInterrupt{
					TurnID: child.ParentTurnID, RunID: RunID(pending.RunID), ChildAgentID: child.ID, NodeID: child.NodeID,
					Role: string(child.Role), InterruptID: pending.InterruptID, Kind: pending.Kind, ToolCallID: pending.ToolCallID,
				}
				projectPendingInterruptPayload(&projected, pending.Kind, pending.Payload)
				snapshot.PendingInterrupts = append(snapshot.PendingInterrupts, projected)
			}
			if snapshot.RuntimeState != RuntimeRunning && snapshot.RuntimeState != RuntimeCancelling {
				for _, action := range agentsession.BuildRecoveryPlan(durable).Actions {
					projected := RecoveryAction{ID: action.ID, TurnID: child.ParentTurnID, RunID: RunID(action.RunID), Kind: string(action.Kind), Automatic: action.Automatic, Summary: boundedUTF8(action.Reason, 1024)}
					if action.Tool != nil {
						projected.ToolCallID, projected.ToolName, projected.ReplayPolicy = action.Tool.ToolCallID, boundedUTF8(action.Tool.ToolName, 256), boundedUTF8(action.Tool.ReplayPolicy, 32)
					}
					for _, decision := range action.Decisions {
						projected.Decisions = append(projected.Decisions, productRecoveryDecision(decision))
					}
					if action.Kind == agentsession.RecoveryResolveInterrupt {
						projected.Decisions = []RecoveryDecision{RecoveryAbandonTurn}
					}
					snapshot.RecoveryActions = append(snapshot.RecoveryActions, projected)
				}
			}
		}
	}
	if len(snapshot.PendingInterrupts) != 0 {
		snapshot.RuntimeState = RuntimeAwaitingApproval
	} else if len(snapshot.RecoveryActions) != 0 && snapshot.RuntimeState == RuntimeIdle {
		snapshot.RuntimeState = RuntimeInterrupted
	}
	return nil
}

func projectPendingInterruptPayload(target *PendingInterrupt, kind string, payload json.RawMessage) {
	if target == nil || len(payload) == 0 {
		return
	}
	switch kind {
	case "approval":
		projectApprovalInterrupt(target, payload)
	case planEntryApprovalKind:
		projectPlanEntryApprovalInterrupt(target, payload)
	case "plan_approval":
		projectPlanApprovalInterrupt(target, payload)
	case planReplanApprovalKind:
		projectPlanReplanApprovalInterrupt(target, payload)
	case clarificationInterruptKind:
		projectClarificationInterrupt(target, payload)
	}
}

func (s *Service) projectWorkflowSnapshot(ctx context.Context, snapshot *Snapshot, turns []Turn) error {
	if snapshot == nil || s.deps.Workflows == nil {
		return nil
	}
	var selected *Turn
	for index := range turns {
		if turns[index].WorkflowID == "" {
			continue
		}
		selected = &turns[index]
		if turns[index].Status == TurnPending || turns[index].Status == TurnRunning || turns[index].Status == TurnInterrupted {
			break
		}
	}
	if selected == nil {
		return nil
	}
	durable, err := s.deps.Workflows.LoadWorkflow(ctx, workflow.ID(selected.WorkflowID))
	if err != nil {
		return fmt.Errorf("load Coding Agent snapshot: load Workflow: %w", err)
	}
	if durable.OwnerID != string(selected.ID) || durable.Plan.ID != string(selected.PlanID) || durable.Plan.Version != selected.PlanVersion || durable.Plan.Digest != selected.PlanDigest {
		return errors.New("load Coding Agent snapshot: Workflow Plan binding is inconsistent")
	}
	strategy := ExecutionWorkflowSingle
	if durable.Strategy == workflow.StrategyMultiAgentSerial {
		strategy = ExecutionWorkflowMultiSerial
	} else if durable.Strategy == workflow.StrategyMultiAgentParallelReadOnly {
		strategy = ExecutionWorkflowMultiParallelReadOnly
	}
	value := WorkflowSnapshot{
		ID: string(durable.ID), TurnID: selected.ID, PlanID: selected.PlanID, PlanVersion: durable.Plan.Version, PlanDigest: durable.Plan.Digest,
		Strategy: strategy, Status: string(durable.Status), Revision: durable.Revision,
		MaxRuns: durable.Budget.MaxRuns, MaxAgentSteps: durable.Budget.MaxAgentSteps, UsedAgentSteps: durable.Budget.UsedAgentSteps,
	}
	for _, node := range durable.Nodes {
		value.UsedRuns += node.Attempts
		projected := WorkflowNodeSnapshot{
			ID: NodeID(node.ID), Goal: boundedUTF8(redactSensitiveText(node.Goal), maxPlanTextBytes), Role: string(node.Role), Capability: string(node.Capability),
			Executor: string(workflow.NodeExecutor(node)), PolicyVersion: node.PolicyVersion,
			Status: string(node.Status), Attempts: node.Attempts, MaxAttempts: node.MaxAttempts,
			ResultRef: boundedUTF8(redactSensitiveText(node.ResultRef), 1024), Failure: boundedUTF8(redactSensitiveText(node.Failure), 2048),
		}
		for _, dependency := range node.DependsOn {
			projected.DependsOn = append(projected.DependsOn, NodeID(dependency))
		}
		for _, criterion := range node.AcceptanceCriteria {
			projected.AcceptanceCriteria = append(projected.AcceptanceCriteria, boundedUTF8(redactSensitiveText(criterion), maxPlanTextBytes))
		}
		value.Nodes = append(value.Nodes, projected)
		switch node.Status {
		case workflow.NodeRunning:
			value.CurrentNode = NodeID(node.ID)
		case workflow.NodeCompleted:
			value.CompletedNodes++
		case workflow.NodeBlocked, workflow.NodeFailed:
			value.BlockedNodes++
			if value.WaitingReason == "" {
				value.WaitingReason = projected.Failure
			}
		}
	}
	snapshot.ActiveWorkflow = &value
	return nil
}

func (s *Service) projectPlanSnapshot(ctx context.Context, snapshot *Snapshot, turns []Turn) error {
	if snapshot == nil || s.deps.Plans == nil {
		return nil
	}
	var active *Turn
	for index := range turns {
		turn := &turns[index]
		if turn.PlanID == "" {
			continue
		}
		versions, err := s.deps.Plans.ListPlanVersions(ctx, turn.PlanID)
		if err != nil {
			return fmt.Errorf("load Coding Agent snapshot: list Plan versions: %w", err)
		}
		var previous *Plan
		for index := range versions {
			version := versions[index]
			snapshot.PlanHistory = append(snapshot.PlanHistory, PlanVersionSummary{ID: version.ID, Version: version.Version, Digest: version.Digest, Goal: boundedUTF8(redactSensitiveText(version.Goal), maxPlanTextBytes), Changes: summarizePlanChanges(previous, version), CreatedAt: version.CreatedAt})
			previous = &version
		}
		if turn.Status == TurnPending || turn.Status == TurnRunning || turn.Status == TurnInterrupted {
			active = turn
		}
	}
	if active == nil && len(turns) != 0 && turns[len(turns)-1].PlanID != "" {
		active = &turns[len(turns)-1]
	}
	if len(snapshot.PlanHistory) > 64 {
		snapshot.PlanHistory = append([]PlanVersionSummary(nil), snapshot.PlanHistory[len(snapshot.PlanHistory)-64:]...)
	}
	if active == nil || active.PlanID == "" {
		return nil
	}
	plan, err := s.deps.Plans.LoadPlan(ctx, active.PlanID, active.PlanVersion)
	if err != nil || plan.Digest != active.PlanDigest {
		return errors.New("load Coding Agent snapshot: active Plan revision is unavailable or changed")
	}
	value := projectPlanForSnapshot(plan)
	if versions, listErr := s.deps.Plans.ListPlanVersions(ctx, active.PlanID); listErr == nil {
		var previous *Plan
		for index := range versions {
			if versions[index].Version == plan.Version {
				value.Changes = summarizePlanChanges(previous, versions[index])
				break
			}
			previous = &versions[index]
		}
	}
	value.ApprovedVersion = active.ApprovedPlanVersion
	if active.WorkspaceDrift != nil {
		if active.WorkspaceDrift.PlanVersion == plan.Version && active.WorkspaceDrift.PlanDigest == plan.Digest {
			drift := *active.WorkspaceDrift
			drift.Summary = boundedUTF8(redactSensitiveText(drift.Summary), 2048)
			drift.Paths = append([]string(nil), drift.Paths...)
			value.WorkspaceDrift = &drift
		} else if active.WorkspaceDrift.Severity == WorkspaceDriftMaterial && active.WorkspaceDrift.PlanVersion+1 == plan.Version {
			value.RevisionReason = "Reapproval required because material workspace drift invalidated the previous Plan baseline."
		}
	}
	if active.PlanReplan != nil {
		if active.PlanReplan.PlanVersion == plan.Version && active.PlanReplan.PlanDigest == plan.Digest {
			replan := *active.PlanReplan
			replan.Summary = boundedUTF8(redactSensitiveText(replan.Summary), maxPlanReplanSummaryBytes)
			value.Replan = &replan
		} else if value.RevisionReason == "" && active.PlanReplan.Decision == "replan" && active.PlanReplan.PlanVersion+1 == plan.Version {
			value.RevisionReason = "Reapproval required because execution reported a material deviation from the previous Plan."
		}
	}
	snapshot.ActivePlan = &value
	snapshot.PendingPlanApproval = active.Phase == TurnPhaseAwaitingPlanApproval && active.Status == TurnInterrupted
	return nil
}

func projectPlanForSnapshot(plan Plan) PlanSnapshot {
	value := PlanSnapshot{
		ID: plan.ID, TurnID: plan.TurnID, Version: plan.Version, Digest: plan.Digest,
		Goal: boundedUTF8(redactSensitiveText(plan.Goal), maxPlanTextBytes), RecommendedStrategy: plan.RecommendedStrategy,
		WorkspaceRelevant: plan.WorkspaceRelevant, CompletionMode: plan.CompletionMode,
	}
	projectList := func(source []string) []string {
		result := make([]string, len(source))
		for index := range source {
			result[index] = boundedUTF8(redactSensitiveText(source[index]), maxPlanTextBytes)
		}
		return result
	}
	value.Scope = PlanScope{Included: projectList(plan.Scope.Included), Excluded: projectList(plan.Scope.Excluded)}
	value.Findings = projectList(plan.Findings)
	value.Assumptions = projectList(plan.Assumptions)
	value.Risks = projectList(plan.Risks)
	value.AcceptanceCriteria = projectList(plan.AcceptanceCriteria)
	value.Steps = make([]PlanStep, len(plan.Steps))
	for index, step := range plan.Steps {
		value.Steps[index] = PlanStep{
			ID: step.ID, Goal: boundedUTF8(redactSensitiveText(step.Goal), maxPlanTextBytes),
			DependsOn: append([]string(nil), step.DependsOn...), Files: append([]string(nil), step.Files...), Validation: projectList(step.Validation),
			Role: step.Role, FailureAction: step.FailureAction, MaxAttempts: step.MaxAttempts,
		}
	}
	return value
}

func (s *Service) startLegacyTurn(ctx context.Context, product Session, requestText string) (TurnResult, error) {
	runIDValue, err := newID("turn")
	if err != nil {
		return TurnResult{}, err
	}
	entryIDValue, err := newID("entry")
	if err != nil {
		return TurnResult{}, err
	}
	runID := RunID(runIDValue)
	legacyTurn := Turn{ID: TurnID(runIDValue), Phase: TurnPhaseDirect}
	environment, err := s.prepareRunEnvironment(ctx, product, legacyTurn, runID, "", CapabilityDirect)
	if err != nil {
		return TurnResult{}, fmt.Errorf("start Coding Agent turn: %w", err)
	}
	s.setState(product.ID, RuntimeRunning)
	runCtx, finishActive := s.beginActiveTurn(ctx, product.ID)
	defer finishActive()
	result, runErr := s.deps.Agent.Run(runCtx, agent.RunRequest{
		SessionID: product.AgentSessionID, Lane: sessionLane(product), RunID: agentsession.RunID(runID), UserEntryID: agentsession.EntryID(entryIDValue), SystemPrompt: environment.systemPrompt,
		Model:            llm.ModelRef{Provider: product.ProviderProfileID, Model: product.ModelID},
		UserMessage:      llm.Message{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentText, Text: requestText}}, Timestamp: time.Now().UTC()},
		UntrustedContext: environment.untrustedContext, Tools: environment.tools, ToolCallPreviewer: environment.toolCallPreviewer, Limits: s.deps.Limits,
	}, environment.events)
	if result.RunID == "" {
		result.RunID = agentsession.RunID(runID)
	}
	s.setState(product.ID, runtimeStateForResult(result, runErr))
	touchErr := s.touchSession(context.WithoutCancel(ctx), product)
	productResult := productTurnResult(TurnID(result.RunID), result)
	if runErr != nil {
		return productResult, fmt.Errorf("start Coding Agent turn: %w", runErr)
	}
	if touchErr != nil {
		return productResult, fmt.Errorf("start Coding Agent turn: update product session: %w", touchErr)
	}
	return productResult, nil
}

// CurrentRevision implements RevisionSource from durable Agent journal sequence.
func (s durableRevisionSource) CurrentRevision(ctx context.Context, _ SessionID) (uint64, error) {
	snapshot, err := s.repository.Load(ctx, s.agentSessionID)
	if err != nil {
		return 0, err
	}
	if len(snapshot.Log) == 0 {
		return 0, nil
	}
	return snapshot.Log[len(snapshot.Log)-1].Sequence, nil
}

type durableRevisionSource struct {
	repository     agentsession.Repository
	agentSessionID agentsession.ID
}

func (s *Service) operationLock(id SessionID) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	operation := s.operations[id]
	if operation == nil {
		operation = &sync.Mutex{}
		s.operations[id] = operation
	}
	return operation
}

func (s *Service) setState(id SessionID, state RuntimeState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.states[id] = state
}

func (s *Service) state(id SessionID) RuntimeState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	state := s.states[id]
	if state == "" {
		return RuntimeIdle
	}
	return state
}

// CancelTurn requests cancellation of the active operation for one product
// session. It is idempotent so UI/local-context races do not create errors.
func (s *Service) CancelTurn(ctx context.Context, id SessionID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if id == "" {
		return errors.New("cancel Coding Agent turn: session id is required")
	}
	s.mu.Lock()
	active, found := s.activeTurns[id]
	if found {
		s.states[id] = RuntimeCancelling
	}
	s.mu.Unlock()
	if found {
		active.cancel()
		return nil
	}
	if s.features.ProductTurns {
		operation := s.operationLock(id)
		operation.Lock()
		defer operation.Unlock()
		turns, err := s.deps.Turns.ListTurns(ctx, id)
		if err != nil {
			return fmt.Errorf("cancel Coding workflow: list Product Turns: %w", err)
		}
		for index := len(turns) - 1; index >= 0; index-- {
			turn := turns[index]
			if turn.Status == TurnCompleted || turn.Status == TurnCancelled || turn.Status == TurnFailed {
				continue
			}
			childIDs := append([]ChildAgentID(nil), turn.PendingPlanExploreIDs...)
			if turn.PendingPlanExploreID != "" {
				childIDs = append(childIDs, turn.PendingPlanExploreID)
			}
			if binding, active := turn.ActiveRun(); active && binding.ChildAgentID != "" {
				found := false
				for _, childID := range childIDs {
					found = found || childID == binding.ChildAgentID
				}
				if !found {
					childIDs = append(childIDs, binding.ChildAgentID)
				}
			}
			for _, childID := range childIDs {
				if childID == "" || s.deps.Children == nil {
					continue
				}
				child, loadErr := s.deps.Children.LoadChildAgent(ctx, childID)
				if loadErr != nil {
					return fmt.Errorf("cancel Coding child Agent: load state: %w", loadErr)
				}
				if child.Status == ChildAgentCreating {
					product, productErr := s.deps.Sessions.LoadSession(ctx, id)
					if productErr != nil {
						return productErr
					}
					child, loadErr = s.ensureChildAgentReady(ctx, product, child)
				}
				if loadErr == nil && child.Status != ChildAgentCompleted && child.Status != ChildAgentFailed && child.Status != ChildAgentCancelled {
					_, loadErr = s.transitionChild(ctx, child, ChildAgentCancelled, nil, "cancelled by user")
				}
				if loadErr != nil {
					return fmt.Errorf("cancel Coding child Agent: %w", loadErr)
				}
			}
			if turn.WorkflowID != "" && s.deps.Workflows != nil {
				durable, loadErr := s.deps.Workflows.LoadWorkflow(ctx, workflow.ID(turn.WorkflowID))
				if loadErr != nil {
					return fmt.Errorf("cancel Coding workflow: load state: %w", loadErr)
				}
				if durable.Status != workflow.StatusCompleted && durable.Status != workflow.StatusCancelled && durable.Status != workflow.StatusFailed {
					if _, appendErr := s.appendWorkflowTransition(ctx, durable, workflow.Event{Type: workflow.EventWorkflowCancelled, Summary: "Workflow cancelled by the user."}); appendErr != nil {
						return appendErr
					}
				}
			}
			if _, finishErr := s.cancelProductTurn(ctx, turn, "cancelled by user"); finishErr != nil {
				return finishErr
			}
			s.setState(id, RuntimeIdle)
			return nil
		}
	}
	return nil
}

func (s *Service) cancelProductTurn(ctx context.Context, turn Turn, reason string) (Turn, error) {
	now := time.Now().UTC()
	expected := turn.Revision
	if _, active := turn.ActiveRun(); active {
		for index := range turn.Runs {
			if turn.Runs[index].Status != RunBindingPending && turn.Runs[index].Status != RunBindingRunning && turn.Runs[index].Status != RunBindingInterrupted {
				continue
			}
			turn.Runs[index].Status = RunBindingCancelled
			turn.Runs[index].Reason = reason
			if turn.Runs[index].StartedAt.IsZero() {
				turn.Runs[index].StartedAt = now
			}
			turn.Runs[index].FinishedAt = now
		}
	} else if len(turn.Runs) != 0 && turn.Runs[len(turn.Runs)-1].Status == RunBindingHandedOff && (turn.PendingPlanExploreID != "" || len(turn.PendingPlanExploreIDs) != 0) {
		turn.Runs[len(turn.Runs)-1].Status = RunBindingCancelled
		turn.Runs[len(turn.Runs)-1].Reason = reason
	}
	turn.PendingPlanExploreID = ""
	turn.PendingPlanExploreIDs = nil
	turn.Status = TurnCancelled
	turn.CompletedAt = now
	turn.UpdatedAt = now
	turn.Revision++
	if err := s.deps.Turns.SaveTurn(ctx, turn, expected); err != nil {
		return turn, err
	}
	return turn, nil
}

func (s *Service) beginActiveTurn(parent context.Context, id SessionID) (context.Context, func()) {
	ctx, cancel := context.WithCancel(parent)
	s.mu.Lock()
	s.activeSeq++
	sequence := s.activeSeq
	s.activeTurns[id] = activeTurn{sequence: sequence, cancel: cancel}
	s.mu.Unlock()
	return ctx, func() {
		cancel()
		s.mu.Lock()
		if active, found := s.activeTurns[id]; found && active.sequence == sequence {
			delete(s.activeTurns, id)
		}
		s.mu.Unlock()
	}
}

func (s *Service) touchSession(ctx context.Context, product Session) error {
	product.UpdatedAt = time.Now().UTC()
	return s.deps.Sessions.SaveSession(ctx, product)
}

func visibleText(message llm.Message) string {
	var builder strings.Builder
	for _, content := range message.Content {
		if content.Type == llm.ContentText {
			builder.WriteString(content.Text)
		}
	}
	return builder.String()
}

func sessionLane(product Session) agentsession.Lane {
	if product.ActiveLane == "" {
		return agentsession.MainLane
	}
	return product.ActiveLane
}

func newID(prefix string) (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate %s id: %w", prefix, err)
	}
	return prefix + "_" + hex.EncodeToString(value), nil
}
