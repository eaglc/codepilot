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

// ToolScope contains immutable trusted Coding facts captured before model-controlled execution.
type ToolScope struct {
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
	AgentSessions agentsession.Repository
	Worktrees     WorktreeReader
	Workspaces    WorkspaceController
	Agent         AgentRunner
	Tools         ToolFactory
	Prompts       PromptBuilder
	Events        EventSink
	Providers     ProviderManager
	Limits        agent.RunLimits
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
}

// NewService validates and creates a Coding Agent product service.
func NewService(deps Dependencies) (*Service, error) {
	if deps.Sessions == nil || deps.AgentSessions == nil || deps.Worktrees == nil || deps.Agent == nil || deps.Tools == nil || deps.Prompts == nil || deps.Events == nil {
		return nil, errors.New("create Coding Agent service: dependencies are incomplete")
	}
	return &Service{deps: deps, states: make(map[SessionID]RuntimeState), operations: make(map[SessionID]*sync.Mutex), activeTurns: make(map[SessionID]activeTurn)}, nil
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

// TurnRequest starts one complete user-triggered Coding Agent turn.
type TurnRequest struct {
	SessionID SessionID
	Text      string
}

// TurnResult contains product-level terminal facts without lower-layer messages or events.
type TurnResult struct {
	TurnID        TurnID
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
	worktree, err := s.deps.Worktrees.LoadWorktree(ctx, product.WorktreeID)
	if err != nil {
		return TurnResult{}, fmt.Errorf("start Coding Agent turn: load worktree: %w", err)
	}
	scope := ToolScope{
		SessionID: product.ID, WorkspaceID: product.WorkspaceID, WorktreeID: product.WorktreeID,
		WorktreeRoot: worktree.Root, PermissionMode: product.PermissionMode, PermissionGrants: clonePermissionGrants(product.PermissionGrants), SensitivePaths: append([]string(nil), product.SensitivePaths...),
	}
	tools, err := s.deps.Tools.CreateTools(ctx, scope)
	if err != nil {
		return TurnResult{}, fmt.Errorf("start Coding Agent turn: create tools: %w", err)
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
		return TurnResult{}, fmt.Errorf("start Coding Agent turn: build prompt: %w", err)
	}
	revisions := durableRevisionSource{repository: s.deps.AgentSessions, agentSessionID: product.AgentSessionID}
	eventAdapter, err := NewAgentEventAdapter(product.ID, s.deps.Events, revisions)
	if err != nil {
		return TurnResult{}, err
	}
	s.setState(product.ID, RuntimeRunning)
	runCtx, finishActive := s.beginActiveTurn(ctx, product.ID)
	defer finishActive()
	result, runErr := s.deps.Agent.Run(runCtx, agent.RunRequest{
		SessionID: product.AgentSessionID, Lane: sessionLane(product), SystemPrompt: systemPrompt,
		Model:            llm.ModelRef{Provider: product.ProviderProfileID, Model: product.ModelID},
		UserMessage:      llm.Message{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentText, Text: strings.TrimSpace(request.Text)}}, Timestamp: time.Now().UTC()},
		UntrustedContext: untrustedContext, Tools: tools, Limits: s.deps.Limits,
	}, eventAdapter)
	state := RuntimeIdle
	if result.Status == agent.RunInterrupted {
		state = RuntimeAwaitingApproval
	}
	s.setState(product.ID, state)
	touchErr := s.touchSession(context.WithoutCancel(ctx), product)
	response := ""
	if result.FinalMessage != nil {
		response = visibleText(*result.FinalMessage)
	}
	productResult := TurnResult{TurnID: TurnID(result.RunID), Status: string(result.Status), Response: response, Steps: result.Steps, Reason: result.Reason}
	if result.Interrupt != nil {
		productResult.InterruptID = result.Interrupt.ID
		productResult.InterruptKind = result.Interrupt.Kind
	}
	if runErr != nil {
		return productResult, fmt.Errorf("start Coding Agent turn: %w", runErr)
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
	if request.GrantScope == "" {
		request.GrantScope = PermissionGrantOnce
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
		grant, grantErr := deriveSessionGrant(product, durable, request, time.Now().UTC())
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
	worktree, err := s.deps.Worktrees.LoadWorktree(ctx, product.WorktreeID)
	if err != nil {
		return TurnResult{}, fmt.Errorf("resume Coding Agent turn: load worktree: %w", err)
	}
	tools, err := s.deps.Tools.CreateTools(ctx, toolScope(product, worktree))
	if err != nil {
		return TurnResult{}, fmt.Errorf("resume Coding Agent turn: create tools: %w", err)
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
		return TurnResult{}, fmt.Errorf("resume Coding Agent turn: build prompt: %w", err)
	}
	resolution, err := productResolution(request)
	if err != nil {
		return TurnResult{}, err
	}
	revisions := durableRevisionSource{repository: s.deps.AgentSessions, agentSessionID: product.AgentSessionID}
	eventAdapter, err := NewAgentEventAdapter(product.ID, s.deps.Events, revisions)
	if err != nil {
		return TurnResult{}, err
	}
	s.setState(product.ID, RuntimeRunning)
	runCtx, finishActive := s.beginActiveTurn(ctx, product.ID)
	defer finishActive()
	result, resumeErr := s.deps.Agent.Resume(runCtx, agent.ResumeRequest{
		SessionID: product.AgentSessionID, Lane: sessionLane(product), RunID: agentsession.RunID(request.TurnID), InterruptID: request.InterruptID,
		Resolution: resolution, SystemPrompt: systemPrompt,
		Model: llm.ModelRef{Provider: product.ProviderProfileID, Model: product.ModelID}, UntrustedContext: untrustedContext, Tools: tools, Limits: s.deps.Limits,
	}, eventAdapter)
	state := RuntimeIdle
	if result.Status == agent.RunInterrupted {
		state = RuntimeAwaitingApproval
	}
	s.setState(product.ID, state)
	touchErr := s.touchSession(context.WithoutCancel(ctx), product)
	response := ""
	if result.FinalMessage != nil {
		response = visibleText(*result.FinalMessage)
	}
	productResult := TurnResult{TurnID: TurnID(result.RunID), Status: string(result.Status), Response: response, Steps: result.Steps, Reason: result.Reason}
	if result.Interrupt != nil {
		productResult.InterruptID = result.Interrupt.ID
		productResult.InterruptKind = result.Interrupt.Kind
	}
	if resumeErr != nil {
		return productResult, fmt.Errorf("resume Coding Agent turn: %w", resumeErr)
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
	return ProjectSnapshot(product, durable, sessionLane(product), s.state(id), revision)
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
