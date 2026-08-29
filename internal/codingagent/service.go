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
	"github.com/eaglc/codepilot/internal/llm"
	"github.com/eaglc/codepilot/internal/tool"
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
	ProductTurns bool
	PlanMode     bool
}

// DefaultFeatureFlags returns the current stable product defaults.
func DefaultFeatureFlags() FeatureFlags {
	return FeatureFlags{ProductTurns: true, PlanMode: true}
}

// ToolScope contains immutable trusted Coding facts captured before model-controlled execution.
type ToolScope struct {
	Profile          CapabilityProfile
	TurnID           TurnID
	RunID            RunID
	SessionID        SessionID
	WorkspaceID      WorkspaceID
	WorktreeID       WorktreeID
	WorktreeRoot     string
	PermissionMode   PermissionMode
	PermissionGrants []PermissionGrant
	SensitivePaths   []string
}

// ToolFactory creates the exact Coding tool set available to one turn.
type ToolFactory interface {
	CreateTools(ctx context.Context, scope ToolScope) (*tool.Registry, error)
}

// PromptScope contains trusted Coding facts used to build a system prompt.
type PromptScope struct {
	Profile        CapabilityProfile
	TurnID         TurnID
	RunID          RunID
	WorkspaceID    WorkspaceID
	WorktreeID     WorktreeID
	WorktreeRoot   string
	ToolNames      []string
	SensitivePaths []string
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
	AgentSessions agentsession.Repository
	Worktrees     WorktreeReader
	Workspaces    WorkspaceController
	Agent         AgentRunner
	Tools         ToolFactory
	Prompts       PromptBuilder
	Events        EventSink
	Providers     ProviderManager
	Limits        agent.RunLimits
	Features      *FeatureFlags
}

// Service owns Coding session lifecycle while delegating model/tool loops to generic Agent.
type Service struct {
	deps        Dependencies
	mu          sync.RWMutex
	states      map[SessionID]RuntimeState
	operations  map[SessionID]*sync.Mutex
	activeTurns map[SessionID]activeTurn
	activeSeq   uint64
	active      SessionID
	eventSeq    uint64
	features    FeatureFlags
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
	if !features.ProductTurns {
		features.PlanMode = false
	}
	if deps.Sessions == nil || (features.ProductTurns && deps.Turns == nil) || (features.PlanMode && deps.Plans == nil) || deps.AgentSessions == nil || deps.Worktrees == nil || deps.Agent == nil || deps.Tools == nil || deps.Prompts == nil || deps.Events == nil {
		return nil, errors.New("create Coding Agent service: dependencies are incomplete")
	}
	return &Service{deps: deps, features: features, states: make(map[SessionID]RuntimeState), operations: make(map[SessionID]*sync.Mutex), activeTurns: make(map[SessionID]activeTurn)}, nil
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
		UntrustedContext: environment.untrustedContext, Tools: environment.tools, Limits: s.deps.Limits,
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
	durableInterrupts, loadInterruptErr := s.deps.AgentSessions.Load(ctx, product.AgentSessionID)
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
	planApproval := s.features.ProductTurns && pendingKind == "plan_approval" && turn.Phase == TurnPhaseAwaitingPlanApproval && planProfile
	clarification := s.features.ProductTurns && pendingKind == clarificationInterruptKind && turn.Phase == TurnPhasePlanning && planProfile
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
	if planApproval && request.GrantScope == PermissionGrantSession {
		return TurnResult{}, errors.New("resume Coding Agent turn: Plan approval cannot create a permission grant")
	}
	if planApproval && request.Decision == ResolutionDenied {
		expected := turn.Revision
		turn.Phase = TurnPhasePlanning
		turn.UpdatedAt = time.Now().UTC()
		turn.Revision++
		if err := s.deps.Turns.SaveTurn(ctx, turn, expected); err != nil {
			return TurnResult{}, fmt.Errorf("resume Coding Agent turn: begin Plan revision: %w", err)
		}
	}
	if request.GrantScope != PermissionGrantOnce && request.GrantScope != PermissionGrantSession {
		return TurnResult{}, fmt.Errorf("resume Coding Agent turn: unsupported grant scope %q", request.GrantScope)
	}
	if request.GrantScope == PermissionGrantSession {
		if request.Decision != ResolutionApproved {
			return TurnResult{}, errors.New("resume Coding Agent turn: a session grant requires an approved decision")
		}
		durable, loadErr := s.deps.AgentSessions.Load(ctx, product.AgentSessionID)
		if loadErr != nil {
			return TurnResult{}, fmt.Errorf("resume Coding Agent turn: load pending approval: %w", loadErr)
		}
		grant, grantErr := deriveSessionGrant(product, durable, request, binding.RunID, time.Now().UTC())
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
	environment, err := s.prepareRunEnvironment(ctx, product, turn, RunID(binding.RunID), "", binding.Profile)
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
	s.setState(product.ID, RuntimeRunning)
	runCtx, finishActive := s.beginActiveTurn(ctx, product.ID)
	defer finishActive()
	result, resumeErr := s.deps.Agent.Resume(runCtx, agent.ResumeRequest{
		SessionID: product.AgentSessionID, Lane: sessionLane(product), RunID: binding.RunID, InterruptID: request.InterruptID,
		Resolution: resolution, SystemPrompt: environment.systemPrompt,
		Model: llm.ModelRef{Provider: product.ProviderProfileID, Model: product.ModelID}, UntrustedContext: environment.untrustedContext, Tools: environment.tools, Limits: s.deps.Limits,
	}, environment.events)
	if result.RunID == "" {
		result.RunID = binding.RunID
	}
	planCompletion := PlanCompletionExecute
	if planApproval {
		if s.deps.Plans == nil {
			return TurnResult{}, errors.New("resume Coding Agent turn: Plan repository is unavailable")
		}
		plan, loadErr := s.deps.Plans.LoadPlan(context.WithoutCancel(ctx), turn.PlanID, turn.PlanVersion)
		if loadErr != nil || plan.Digest != turn.PlanDigest {
			return TurnResult{}, errors.New("resume Coding Agent turn: current Plan revision is unavailable or changed")
		}
		planCompletion = plan.CompletionMode
	}
	if planApproval && request.Decision == ResolutionCancelled && result.Status == agent.RunHandedOff {
		result.Status = agent.RunAborted
		result.Reason = "plan_cancelled"
	}
	if planApproval && request.Decision == ResolutionApproved && planCompletion == PlanCompletionDeliverable && result.Status == agent.RunHandedOff {
		result.Status = agent.RunCompleted
		result.Reason = "plan_delivered"
	}
	var finishErr error
	if s.features.ProductTurns {
		turn, finishErr = s.refreshProductTurn(context.WithoutCancel(ctx), turn)
		if finishErr == nil {
			turn, finishErr = s.finishProductRun(context.WithoutCancel(ctx), turn, result, resumeErr)
		}
		s.setState(product.ID, runtimeStateForTurn(turn))
	} else {
		s.setState(product.ID, runtimeStateForResult(result, resumeErr))
	}
	touchErr := s.touchSession(context.WithoutCancel(ctx), product)
	productResult := productTurnResult(turn.ID, result)
	if resumeErr != nil {
		return productResult, fmt.Errorf("resume Coding Agent turn: %w", resumeErr)
	}
	if finishErr != nil {
		return productResult, fmt.Errorf("resume Coding Agent turn: persist Product Turn: %w", finishErr)
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
	return snapshot, nil
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
		for _, version := range versions {
			snapshot.PlanHistory = append(snapshot.PlanHistory, PlanVersionSummary{ID: version.ID, Version: version.Version, Digest: version.Digest, Goal: boundedUTF8(redactSensitiveText(version.Goal), maxPlanTextBytes), CreatedAt: version.CreatedAt})
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
		UntrustedContext: environment.untrustedContext, Tools: environment.tools, Limits: s.deps.Limits,
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
	}
	return nil
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
