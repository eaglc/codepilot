package agent

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/eaglc/codepilot/internal/contextmanager"
	"github.com/eaglc/codepilot/internal/provider"
	"github.com/eaglc/codepilot/internal/session"
)

var _ session.CodingAgent = (*CodingAgent)(nil)

// CodingAgent owns one AgentInvoker and orchestrates coding-specific tools,
// language policy, approval recovery, and evidence for a bound session.
type CodingAgent struct {
	mu        sync.Mutex
	closeOnce sync.Once

	config     session.CodingAgentConfig
	workspaces WorkspaceTools
	languages  LanguageResolver
	authorizer session.Authorizer
	invoker    AgentInvoker
	codeIntel  CodeNavigator
	contexts   *contextmanager.Manager
	active     bool
	closed     bool
	closeErr   error
}

// RunTurn executes one coding turn. The returned result contains evidence only;
// Session remains responsible for deriving verified/unverified status.
func (a *CodingAgent) RunTurn(ctx context.Context, request session.TurnRequest, events session.EventSink) (session.TurnResult, error) {
	result := emptyTurnResult()
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if a == nil {
		return result, errors.New("run coding turn: agent is nil")
	}
	if err := a.validateTurnRequest(request, events); err != nil {
		return result, err
	}
	if err := a.beginTurn(); err != nil {
		return result, err
	}
	defer a.finishTurn()

	profile, err := a.languages.ResolveLanguage(ctx, request.Scope.WorktreeRoot)
	if err != nil {
		return result, fmt.Errorf("run coding turn: resolve language: %w", err)
	}
	prompt, err := buildSystemPrompt(profile)
	if err != nil {
		return result, err
	}
	state := newTurnToolState()
	registry, err := buildToolRegistry(request.Scope, profile, toolsetDependencies{Workspaces: a.workspaces, State: state, CodeIntel: a.codeIntel})
	if err != nil {
		return result, err
	}
	adapter, err := newCodingEventAdapter(request.Scope, events)
	if err != nil {
		return result, err
	}
	managedContext, err := a.contexts.Process(ctx, contextRequestFromTurn(request, prompt))
	if err != nil {
		return result, fmt.Errorf("run coding turn: process model context: %w", err)
	}
	messages, err := invocationMessagesFromContext(managedContext, request.UserMessage)
	if err != nil {
		return result, err
	}
	input := InvocationInput{
		ID:           string(request.Scope.TurnID),
		CheckpointID: string(request.Scope.TurnID),
		Model: provider.ModelRef{
			Provider: string(request.Scope.ProviderProfileID),
			Model:    request.Scope.ModelID,
		},
		SystemPrompt: managedContext.SystemPrompt,
		Messages:     messages,
		Tools:        registry,
		Limits: InvocationLimits{
			MaxSteps:    request.Scope.Limits.MaxSteps,
			MaxDuration: request.Scope.Limits.MaxTurnDuration,
		},
	}

	evidence := newEvidencePublisher(request.Scope, events)
	invocation, invokeErr := a.invoker.Invoke(ctx, input, adapter)
	if err := finishInvocationSegment(ctx, adapter, evidence, state); err != nil {
		invokeErr = errors.Join(invokeErr, err)
	}
	result = turnResultFromInvocation(invocation, state)
	if invokeErr != nil {
		return result, invokeErr
	}

	for invocation.Status == InvocationInterrupted {
		approval, err := decodeApprovalRequest(request.Scope, invocation.Interrupt)
		if err != nil {
			a.cancelInterrupt(request.Scope, invocation, adapter)
			return result, err
		}
		if err := publishApprovalRequested(ctx, events, request.Scope, approval); err != nil {
			a.cancelInterrupt(request.Scope, invocation, adapter)
			return result, err
		}

		decision, waitErr := a.authorizer.WaitDecision(ctx, approval.ID)
		if waitErr != nil {
			cancelled := session.ApprovalDecision{Kind: session.ApprovalCancelled, DecidedAt: time.Now().UTC()}
			publishErr := publishApprovalResolved(context.WithoutCancel(ctx), events, request.Scope, approval, cancelled)
			a.cancelInterrupt(request.Scope, invocation, adapter)
			return result, errors.Join(waitErr, publishErr)
		}
		if err := publishApprovalResolved(ctx, events, request.Scope, approval, decision); err != nil {
			a.cancelInterrupt(request.Scope, invocation, adapter)
			return result, err
		}
		recordApprovalEvidence(state, approval, decision)
		response, err := interruptResponseFromDecision(decision)
		if err != nil {
			a.cancelInterrupt(request.Scope, invocation, adapter)
			return result, err
		}
		invocation, invokeErr = a.invoker.Resume(ctx, ResumeInput{
			CheckpointID: string(request.Scope.TurnID),
			InterruptID:  invocation.Interrupt.ID,
			Response:     response,
		}, adapter)
		if err := finishInvocationSegment(ctx, adapter, evidence, state); err != nil {
			invokeErr = errors.Join(invokeErr, err)
		}
		result = turnResultFromInvocation(invocation, state)
		if invokeErr != nil {
			return result, invokeErr
		}
	}

	switch invocation.Status {
	case InvocationCompleted, InvocationCancelled, InvocationLimitReached:
		return result, nil
	default:
		return result, fmt.Errorf("run coding turn: invoker returned unsupported status %q", invocation.Status)
	}
}

// Close cancels the owned invoker, discards resumable state, closes code
// navigation for the bound worktree, and is idempotent.
func (a *CodingAgent) Close() error {
	if a == nil {
		return nil
	}
	a.closeOnce.Do(func() {
		a.mu.Lock()
		a.closed = true
		invoker := a.invoker
		a.mu.Unlock()
		var closeErrors []error
		if invoker != nil {
			closeErrors = append(closeErrors, invoker.Close())
		}
		if a.codeIntel != nil {
			closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
			closeErrors = append(closeErrors, a.codeIntel.CloseWorktree(closeCtx, a.config.WorktreeID))
			cancel()
		}
		a.closeErr = errors.Join(closeErrors...)
	})
	return a.closeErr
}

func (a *CodingAgent) beginTurn() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return errors.New("run coding turn: agent is closed")
	}
	if a.active {
		return errors.New("run coding turn: another turn is active")
	}
	a.active = true
	return nil
}

func (a *CodingAgent) finishTurn() {
	a.mu.Lock()
	a.active = false
	a.mu.Unlock()
}

func (a *CodingAgent) validateTurnRequest(request session.TurnRequest, events session.EventSink) error {
	if isNilDependency(events) {
		return errors.New("run coding turn: event sink is required")
	}
	scope := request.Scope
	config := a.config
	if scope.TurnID == "" || scope.SessionID != config.SessionID || scope.WorkspaceID != config.WorkspaceID || scope.WorktreeID != config.WorktreeID || scope.ProviderProfileID != config.ProviderProfileID || scope.ModelID != config.ModelID || scope.WorktreeRoot != config.WorktreeRoot || scope.Limits != config.Limits {
		return errors.New("run coding turn: scope does not match the bound agent")
	}
	if !filepath.IsAbs(scope.WorktreeRoot) || filepath.Clean(scope.WorktreeRoot) != scope.WorktreeRoot {
		return errors.New("run coding turn: worktree root is invalid")
	}
	switch scope.PermissionMode {
	case session.PermissionReadOnly, session.PermissionAsk, session.PermissionAutoEdit:
	default:
		return errors.New("run coding turn: permission mode is invalid")
	}
	if err := validateTurnMessage(request.UserMessage, scope, true); err != nil {
		return err
	}
	for _, message := range request.History {
		if err := validateTurnMessage(message, scope, false); err != nil {
			return err
		}
	}
	return nil
}

func validateTurnMessage(message session.Message, scope session.TurnScope, current bool) error {
	if message.ID == "" || message.SessionID != scope.SessionID || message.TurnID == "" || strings.TrimSpace(message.Content) == "" {
		return errors.New("run coding turn: conversation contains an invalid message")
	}
	if message.Role != session.RoleUser && message.Role != session.RoleAssistant {
		return errors.New("run coding turn: conversation contains an unsupported role")
	}
	if current && (message.Role != session.RoleUser || message.TurnID != scope.TurnID) {
		return errors.New("run coding turn: current message does not match the turn")
	}
	return nil
}

func contextRequestFromTurn(request session.TurnRequest, systemPrompt string) contextmanager.Request {
	messages := make([]contextmanager.Message, 0, len(request.History)+1)
	for _, message := range append(append([]session.Message(nil), request.History...), request.UserMessage) {
		role := contextmanager.RoleUser
		if message.Role == session.RoleAssistant {
			role = contextmanager.RoleAssistant
		}
		messages = append(messages, contextmanager.Message{
			ID: string(message.ID), Role: role, Content: message.Content, Current: message.ID == request.UserMessage.ID,
		})
	}
	return contextmanager.Request{
		Scope: contextmanager.Scope{
			SessionID: string(request.Scope.SessionID), TurnID: string(request.Scope.TurnID), WorktreeRoot: request.Scope.WorktreeRoot,
			ProviderProfileID: string(request.Scope.ProviderProfileID), ModelID: request.Scope.ModelID,
		},
		SystemPrompt: systemPrompt,
		Messages:     messages,
	}
}

func invocationMessagesFromContext(value contextmanager.Result, current session.Message) ([]InvocationMessage, error) {
	if strings.TrimSpace(value.SystemPrompt) == "" {
		return nil, errors.New("run coding turn: context manager returned an empty system prompt")
	}
	values := make([]InvocationMessage, 0, len(value.Messages))
	currentMessages := 0
	for _, message := range value.Messages {
		if strings.TrimSpace(message.Content) == "" {
			return nil, errors.New("run coding turn: context manager returned an empty message")
		}
		role := InvocationRole("")
		switch message.Role {
		case contextmanager.RoleUser:
			role = InvocationRoleUser
		case contextmanager.RoleAssistant:
			role = InvocationRoleAssistant
		default:
			return nil, errors.New("run coding turn: context manager returned an unsupported role")
		}
		if message.Current {
			if role != InvocationRoleUser {
				return nil, errors.New("run coding turn: context manager changed the current message role")
			}
			if message.ID != string(current.ID) || message.Content != current.Content {
				return nil, errors.New("run coding turn: context manager changed the current user message")
			}
			currentMessages++
		}
		values = append(values, InvocationMessage{Role: role, Content: message.Content})
	}
	if currentMessages != 1 {
		return nil, errors.New("run coding turn: context manager must preserve exactly one current user message")
	}
	return values, nil
}

func emptyTurnResult() session.TurnResult {
	return session.TurnResult{CheckSummary: session.CheckSummary{Outcome: session.CheckNotRun}}
}

func turnResultFromInvocation(invocation InvocationResult, state *turnToolState) session.TurnResult {
	patches, checks := state.snapshot()
	if invocation.Status == InvocationCancelled {
		checks = session.CheckSummary{Outcome: session.CheckCancelled, Summary: "The coding turn was cancelled."}
	}
	return session.TurnResult{
		FinalText:         invocation.FinalText,
		Steps:             invocation.Steps,
		TerminationReason: invocation.TerminationReason,
		CheckSummary:      checks,
		AppliedPatches:    patches,
	}
}

func finishInvocationSegment(ctx context.Context, adapter *codingEventAdapter, evidence *evidencePublisher, state *turnToolState) error {
	return errors.Join(adapter.Flush(ctx), evidence.Publish(ctx, state))
}

func publishApprovalRequested(ctx context.Context, events session.EventSink, scope session.TurnScope, request session.ApprovalRequest) error {
	return events.Publish(ctx, session.Event{
		SessionID: scope.SessionID,
		TurnID:    scope.TurnID,
		Kind:      session.EventApprovalRequested,
		Payload:   session.EventPayload{Approval: &session.ApprovalEventPayload{Request: &request}},
	})
}

func publishApprovalResolved(ctx context.Context, events session.EventSink, scope session.TurnScope, request session.ApprovalRequest, decision session.ApprovalDecision) error {
	return events.Publish(ctx, session.Event{
		SessionID: scope.SessionID,
		TurnID:    scope.TurnID,
		Kind:      session.EventApprovalResolved,
		Payload: session.EventPayload{Approval: &session.ApprovalEventPayload{
			Request: &request, Decision: &decision,
		}},
	})
}

func interruptResponseFromDecision(decision session.ApprovalDecision) (InterruptResponse, error) {
	switch decision.Kind {
	case session.ApprovalAllowOnce, session.ApprovalAllowSession:
		return InterruptResponse{Kind: InterruptApproved}, nil
	case session.ApprovalDeny:
		return InterruptResponse{Kind: InterruptRejected}, nil
	case session.ApprovalCancelled:
		return InterruptResponse{Kind: InterruptCancelled}, nil
	default:
		return InterruptResponse{}, errors.New("run coding turn: approval decision is invalid")
	}
}

// recordApprovalEvidence captures a denied check at the business layer because
// Eino correctly returns a denial without re-invoking the side-effecting tool.
func recordApprovalEvidence(state *turnToolState, request session.ApprovalRequest, decision session.ApprovalDecision) {
	if request.Action.Kind == session.ActionRunCheck && decision.Kind == session.ApprovalDeny {
		state.recordCheck(RunChecksResult{
			Outcome: session.CheckDenied,
			Summary: "The project check was denied by the user.",
			Denied:  true,
		})
	}
}

// cancelInterrupt gives the invoker a bounded terminal response so an approval
// wait failure does not leave a replayable checkpoint attached to this agent.
func (a *CodingAgent) cancelInterrupt(scope session.TurnScope, invocation InvocationResult, events InvocationEventSink) {
	if invocation.Interrupt == nil {
		return
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, _ = a.invoker.Resume(cleanupCtx, ResumeInput{
		CheckpointID: string(scope.TurnID),
		InterruptID:  invocation.Interrupt.ID,
		Response:     InterruptResponse{Kind: InterruptCancelled},
	}, events)
}

// evidencePublisher emits each proposal and applied patch once even when an
// invocation crosses multiple approval resumptions.
type evidencePublisher struct {
	scope          session.TurnScope
	events         session.EventSink
	proposedMarker string
	patches        map[session.PatchID]struct{}
}

func newEvidencePublisher(scope session.TurnScope, events session.EventSink) *evidencePublisher {
	return &evidencePublisher{scope: scope, events: events, patches: make(map[session.PatchID]struct{})}
}

func (p *evidencePublisher) Publish(ctx context.Context, state *turnToolState) error {
	if proposed, exists := state.proposedDiff(); exists {
		marker := fmt.Sprintf("%s\x00%t\x00%t\x00%d", proposed.Text, proposed.Truncated, proposed.Drifted, len(proposed.Files))
		if marker != p.proposedMarker {
			proposedCopy := proposed
			proposedCopy.Files = append([]session.DiffFile(nil), proposed.Files...)
			if err := p.publish(ctx, session.Event{Kind: session.EventDiffChanged, Payload: session.EventPayload{Diff: &session.DiffEventPayload{Kind: session.DiffProposed, Result: &proposedCopy}}}); err != nil {
				return err
			}
			p.proposedMarker = marker
		}
	}
	// snapshot also returns the turn's check summary, which is reported only
	// through the final turn result rather than as per-patch evidence.
	patches, _ := state.snapshot()
	for _, patch := range patches {
		if _, exists := p.patches[patch.ID]; exists {
			continue
		}
		if patch.SessionID != p.scope.SessionID || patch.TurnID != p.scope.TurnID {
			return errors.New("publish coding evidence: patch belongs to another turn")
		}
		if err := p.publish(ctx, session.Event{Kind: session.EventPatchApplied, Payload: session.EventPayload{Patch: &session.PatchEventPayload{Record: patch}}}); err != nil {
			return err
		}
		for _, kind := range []session.DiffKind{session.DiffSession, session.DiffWorkspace} {
			if err := p.publish(ctx, session.Event{Kind: session.EventDiffChanged, Payload: session.EventPayload{Diff: &session.DiffEventPayload{Kind: kind}}}); err != nil {
				return err
			}
		}
		p.patches[patch.ID] = struct{}{}
	}
	return nil
}

func (p *evidencePublisher) publish(ctx context.Context, event session.Event) error {
	event.SessionID = p.scope.SessionID
	event.TurnID = p.scope.TurnID
	return p.events.Publish(ctx, event)
}
