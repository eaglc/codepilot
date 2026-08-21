package approval

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"strings"
	"sync"
	"time"

	"github.com/eaglc/codepilot/internal/session"
)

var _ session.Authorizer = (*Service)(nil)

// pendingApproval has one exact decision channel. The waiting flag prevents
// multiple consumers from racing to receive the same user resolution.
type pendingApproval struct {
	request  session.ApprovalRequest
	decision chan session.ApprovalDecision
	resolved bool
	waiting  bool
}

// Service owns process-local pending approvals and exact temporary grants.
type Service struct {
	mu      sync.Mutex
	pending map[session.ApprovalRequestID]*pendingApproval
	grants  map[session.SessionID]map[string]struct{}
	once    map[session.SessionID]map[string]int
	closed  bool
	now     func() time.Time
	newID   func() (session.ApprovalRequestID, error)
}

// NewService creates an empty approval policy service.
func NewService() *Service {
	return &Service{
		pending: make(map[session.ApprovalRequestID]*pendingApproval),
		grants:  make(map[session.SessionID]map[string]struct{}),
		once:    make(map[session.SessionID]map[string]int),
		now:     func() time.Time { return time.Now().UTC() },
		newID:   newApprovalRequestID,
	}
}

// Authorize applies the permission matrix and creates a prompt when needed.
func (s *Service) Authorize(ctx context.Context, mode session.PermissionMode, action session.Action) (session.Authorization, error) {
	if err := ctx.Err(); err != nil {
		return session.Authorization{}, err
	}
	if err := validateAuthorizationInput(mode, action); err != nil {
		return session.Authorization{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return session.Authorization{}, approvalError(session.ErrInvalidState, "approval.authorize", "Approval service is closed.")
	}
	outcome := basePolicy(mode, action.Kind)
	if outcome == session.AuthorizationDeny {
		return session.Authorization{Outcome: outcome, Reason: denialReason(mode, action.Kind)}, nil
	}
	if outcome == session.AuthorizationAllow {
		return session.Authorization{Outcome: outcome}, nil
	}
	if s.consumeOnceLocked(action.SessionID, action.Fingerprint) || s.hasGrantLocked(action.SessionID, action.Fingerprint) {
		return session.Authorization{Outcome: session.AuthorizationAllow}, nil
	}
	for _, value := range s.pending {
		if value.request.SessionID == action.SessionID && value.request.TurnID == action.TurnID && value.request.Action.Fingerprint == action.Fingerprint {
			request := cloneApprovalRequest(value.request)
			return session.Authorization{Outcome: session.AuthorizationPrompt, Request: &request}, nil
		}
	}
	requestID, err := s.newID()
	if err != nil {
		return session.Authorization{}, approvalError(session.ErrInternal, "approval.authorize", "Approval request could not be created.")
	}
	request := session.ApprovalRequest{
		ID:        requestID,
		SessionID: action.SessionID,
		TurnID:    action.TurnID,
		Action:    cloneAction(action),
		CreatedAt: s.now().UTC(),
	}
	s.pending[requestID] = &pendingApproval{request: request, decision: make(chan session.ApprovalDecision, 1)}
	cloned := cloneApprovalRequest(request)
	return session.Authorization{Outcome: session.AuthorizationPrompt, Request: &cloned}, nil
}

// WaitDecision waits for one exact resolution and installs its temporary grant.
func (s *Service) WaitDecision(ctx context.Context, requestID session.ApprovalRequestID) (session.ApprovalDecision, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return session.ApprovalDecision{}, approvalError(session.ErrInvalidState, "approval.wait", "Approval service is closed.")
	}
	pending, exists := s.pending[requestID]
	if exists && pending.waiting {
		s.mu.Unlock()
		return session.ApprovalDecision{}, approvalError(session.ErrConflict, "approval.wait", "Approval request already has a waiter.")
	}
	if exists {
		pending.waiting = true
	}
	s.mu.Unlock()
	if !exists {
		return session.ApprovalDecision{}, approvalError(session.ErrNotFound, "approval.wait", "Approval request was not found.")
	}

	select {
	case <-ctx.Done():
		s.mu.Lock()
		if s.pending[requestID] == pending {
			delete(s.pending, requestID)
		}
		s.mu.Unlock()
		return session.ApprovalDecision{}, ctx.Err()
	case decision := <-pending.decision:
		s.mu.Lock()
		if s.pending[requestID] == pending {
			delete(s.pending, requestID)
			if decision.Kind != session.ApprovalCancelled {
				s.recordDecisionLocked(pending.request, decision)
			}
		}
		s.mu.Unlock()
		return decision, nil
	}
}

// Resolve validates that a UI decision belongs to the exact pending turn.
func (s *Service) Resolve(ctx context.Context, resolution session.ApprovalResolution) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if resolution.RequestID == "" || resolution.SessionID == "" || resolution.TurnID == "" {
		return approvalError(session.ErrInvalidInput, "approval.resolve", "Approval resolution identifiers are required.")
	}
	if !validDecision(resolution.Decision.Kind) {
		return approvalError(session.ErrInvalidInput, "approval.resolve", "Approval decision is invalid.")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return approvalError(session.ErrInvalidState, "approval.resolve", "Approval service is closed.")
	}
	pending, exists := s.pending[resolution.RequestID]
	if !exists {
		return approvalError(session.ErrNotFound, "approval.resolve", "Approval request was not found.")
	}
	if pending.request.SessionID != resolution.SessionID || pending.request.TurnID != resolution.TurnID {
		return approvalError(session.ErrConflict, "approval.resolve", "Approval resolution does not match the pending turn.")
	}
	if pending.resolved {
		return approvalError(session.ErrConflict, "approval.resolve", "Approval request has already been resolved.")
	}
	decision := resolution.Decision
	decision.DecidedAt = s.now().UTC()
	pending.resolved = true
	pending.decision <- decision
	return nil
}

// ClearSession cancels pending requests and removes all temporary grants.
func (s *Service) ClearSession(ctx context.Context, sessionID session.SessionID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if sessionID == "" {
		return approvalError(session.ErrInvalidInput, "approval.clear_session", "Session ID is required.")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.cancelPendingLocked(sessionID)
	delete(s.grants, sessionID)
	delete(s.once, sessionID)
	return nil
}

// Close cancels every waiter and clears process-local authorization state.
func (s *Service) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	for id, pending := range s.pending {
		delete(s.pending, id)
		if !pending.resolved {
			pending.resolved = true
			pending.decision <- session.ApprovalDecision{Kind: session.ApprovalCancelled, DecidedAt: s.now().UTC()}
		}
	}
	clear(s.grants)
	clear(s.once)
	return nil
}

func (s *Service) cancelPendingLocked(sessionID session.SessionID) {
	for id, pending := range s.pending {
		if pending.request.SessionID != sessionID {
			continue
		}
		delete(s.pending, id)
		if !pending.resolved {
			pending.resolved = true
			pending.decision <- session.ApprovalDecision{Kind: session.ApprovalCancelled, DecidedAt: s.now().UTC()}
		}
	}
}

func validateAuthorizationInput(mode session.PermissionMode, action session.Action) error {
	switch mode {
	case session.PermissionReadOnly, session.PermissionAsk, session.PermissionAutoEdit:
	default:
		return approvalError(session.ErrInvalidInput, "approval.authorize", "Permission mode is invalid.")
	}
	if action.ID == "" || action.SessionID == "" || action.TurnID == "" || strings.TrimSpace(action.WorktreeRoot) == "" || strings.TrimSpace(action.Fingerprint) == "" {
		return approvalError(session.ErrInvalidInput, "approval.authorize", "Action identifiers and fingerprint are required.")
	}
	switch action.Kind {
	case session.ActionRead:
		if action.Patch != nil || action.Command != nil {
			return approvalError(session.ErrInvalidInput, "approval.authorize", "Read action payload is invalid.")
		}
	case session.ActionApplyPatch:
		if action.Patch == nil || action.Command != nil || strings.TrimSpace(action.Patch.Patch) == "" || len(action.Patch.Files) == 0 {
			return approvalError(session.ErrInvalidInput, "approval.authorize", "Patch action payload is invalid.")
		}
	case session.ActionRunCheck, session.ActionStartLanguageServer:
		if action.Command == nil || action.Patch != nil || strings.TrimSpace(action.Command.Program) == "" {
			return approvalError(session.ErrInvalidInput, "approval.authorize", "Command action payload is invalid.")
		}
	default:
		return approvalError(session.ErrInvalidInput, "approval.authorize", "Action kind is invalid.")
	}
	return nil
}

func validDecision(kind session.ApprovalDecisionKind) bool {
	return kind == session.ApprovalAllowOnce || kind == session.ApprovalAllowSession || kind == session.ApprovalDeny
}

func cloneApprovalRequest(value session.ApprovalRequest) session.ApprovalRequest {
	value.Action = cloneAction(value.Action)
	return value
}

func cloneAction(value session.Action) session.Action {
	if value.Patch != nil {
		patch := *value.Patch
		patch.Files = append([]string(nil), value.Patch.Files...)
		value.Patch = &patch
	}
	if value.Command != nil {
		command := *value.Command
		command.Args = append([]string(nil), value.Command.Args...)
		value.Command = &command
	}
	return value
}

func newApprovalRequestID() (session.ApprovalRequestID, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(value)
	return session.ApprovalRequestID("apr_" + strings.ToLower(encoded)), nil
}

func approvalError(code session.ErrorCode, operation string, message string) error {
	return &session.AppError{Code: code, Operation: operation, UserMessage: message}
}
