package approval

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/eaglc/codepilot/internal/session"
)

func TestServicePermissionMatrix(t *testing.T) {
	tests := []struct {
		name string
		mode session.PermissionMode
		kind session.ActionKind
		want session.AuthorizationOutcome
	}{
		{name: "read in read-only", mode: session.PermissionReadOnly, kind: session.ActionRead, want: session.AuthorizationAllow},
		{name: "patch in read-only", mode: session.PermissionReadOnly, kind: session.ActionApplyPatch, want: session.AuthorizationDeny},
		{name: "check in read-only", mode: session.PermissionReadOnly, kind: session.ActionRunCheck, want: session.AuthorizationDeny},
		{name: "patch in ask", mode: session.PermissionAsk, kind: session.ActionApplyPatch, want: session.AuthorizationPrompt},
		{name: "check in ask", mode: session.PermissionAsk, kind: session.ActionRunCheck, want: session.AuthorizationPrompt},
		{name: "patch in auto-edit", mode: session.PermissionAutoEdit, kind: session.ActionApplyPatch, want: session.AuthorizationAllow},
		{name: "check in auto-edit", mode: session.PermissionAutoEdit, kind: session.ActionRunCheck, want: session.AuthorizationPrompt},
		{name: "language server in read-only", mode: session.PermissionReadOnly, kind: session.ActionStartLanguageServer, want: session.AuthorizationPrompt},
		{name: "language server in ask", mode: session.PermissionAsk, kind: session.ActionStartLanguageServer, want: session.AuthorizationPrompt},
		{name: "language server in auto-edit", mode: session.PermissionAutoEdit, kind: session.ActionStartLanguageServer, want: session.AuthorizationPrompt},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := NewService()
			action := testAction(test.kind, "fingerprint-"+test.name)
			authorization, err := service.Authorize(context.Background(), test.mode, action)
			if err != nil {
				t.Fatalf("authorize: %v", err)
			}
			if authorization.Outcome != test.want {
				t.Fatalf("outcome = %s, want %s", authorization.Outcome, test.want)
			}
			if test.want == session.AuthorizationPrompt && authorization.Request == nil {
				t.Fatal("prompt did not include an approval request")
			}
		})
	}
}

func TestServiceAllowOnceIsConsumedExactlyOnce(t *testing.T) {
	service := NewService()
	action := testAction(session.ActionApplyPatch, "patch-fingerprint")
	authorization, err := service.Authorize(context.Background(), session.PermissionAsk, action)
	if err != nil {
		t.Fatalf("authorize prompt: %v", err)
	}
	request := authorization.Request
	if err := service.Resolve(context.Background(), session.ApprovalResolution{
		RequestID: request.ID,
		SessionID: request.SessionID,
		TurnID:    request.TurnID,
		Decision:  session.ApprovalDecision{Kind: session.ApprovalAllowOnce},
	}); err != nil {
		t.Fatalf("resolve approval: %v", err)
	}
	decision, err := service.WaitDecision(context.Background(), request.ID)
	if err != nil || decision.Kind != session.ApprovalAllowOnce || decision.DecidedAt.IsZero() {
		t.Fatalf("wait decision: decision=%#v err=%v", decision, err)
	}

	authorization, err = service.Authorize(context.Background(), session.PermissionAsk, action)
	if err != nil || authorization.Outcome != session.AuthorizationAllow {
		t.Fatalf("allow-once was not consumed: authorization=%#v err=%v", authorization, err)
	}
	authorization, err = service.Authorize(context.Background(), session.PermissionAsk, action)
	if err != nil || authorization.Outcome != session.AuthorizationPrompt {
		t.Fatalf("allow-once permitted a second action: authorization=%#v err=%v", authorization, err)
	}
}

func TestServiceSessionGrantPersistsUntilClear(t *testing.T) {
	service := NewService()
	action := testAction(session.ActionRunCheck, "check-fingerprint")
	authorization, err := service.Authorize(context.Background(), session.PermissionAutoEdit, action)
	if err != nil {
		t.Fatalf("authorize prompt: %v", err)
	}
	request := authorization.Request
	if err := service.Resolve(context.Background(), session.ApprovalResolution{
		RequestID: request.ID,
		SessionID: request.SessionID,
		TurnID:    request.TurnID,
		Decision:  session.ApprovalDecision{Kind: session.ApprovalAllowSession},
	}); err != nil {
		t.Fatalf("resolve approval: %v", err)
	}
	if _, err := service.WaitDecision(context.Background(), request.ID); err != nil {
		t.Fatalf("wait decision: %v", err)
	}
	for range 2 {
		authorization, err = service.Authorize(context.Background(), session.PermissionAutoEdit, action)
		if err != nil || authorization.Outcome != session.AuthorizationAllow {
			t.Fatalf("session grant was not reused: authorization=%#v err=%v", authorization, err)
		}
	}
	if err := service.ClearSession(context.Background(), action.SessionID); err != nil {
		t.Fatalf("clear session: %v", err)
	}
	authorization, err = service.Authorize(context.Background(), session.PermissionAutoEdit, action)
	if err != nil || authorization.Outcome != session.AuthorizationPrompt {
		t.Fatalf("cleared grant remained active: authorization=%#v err=%v", authorization, err)
	}
}

func TestServiceResolveRejectsStaleTurnAndDuplicateDecision(t *testing.T) {
	service := NewService()
	authorization, err := service.Authorize(context.Background(), session.PermissionAsk, testAction(session.ActionApplyPatch, "fingerprint"))
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	request := authorization.Request
	err = service.Resolve(context.Background(), session.ApprovalResolution{
		RequestID: request.ID,
		SessionID: request.SessionID,
		TurnID:    "turn_stale",
		Decision:  session.ApprovalDecision{Kind: session.ApprovalAllowOnce},
	})
	if errorCode(err) != session.ErrConflict {
		t.Fatalf("stale turn error = %v", err)
	}
	resolution := session.ApprovalResolution{
		RequestID: request.ID,
		SessionID: request.SessionID,
		TurnID:    request.TurnID,
		Decision:  session.ApprovalDecision{Kind: session.ApprovalDeny},
	}
	if err := service.Resolve(context.Background(), resolution); err != nil {
		t.Fatalf("resolve valid decision: %v", err)
	}
	if err := service.Resolve(context.Background(), resolution); errorCode(err) != session.ErrConflict {
		t.Fatalf("duplicate resolution error = %v", err)
	}
}

func TestServiceClearAndCloseWakeWaiters(t *testing.T) {
	t.Run("clear session", func(t *testing.T) {
		service := NewService()
		authorization, err := service.Authorize(context.Background(), session.PermissionAsk, testAction(session.ActionApplyPatch, "clear"))
		if err != nil {
			t.Fatalf("authorize: %v", err)
		}
		result := make(chan session.ApprovalDecision, 1)
		go func() {
			decision, _ := service.WaitDecision(context.Background(), authorization.Request.ID)
			result <- decision
		}()
		waitForApprovalWaiter(t, service, authorization.Request.ID)
		if err := service.ClearSession(context.Background(), authorization.Request.SessionID); err != nil {
			t.Fatalf("clear session: %v", err)
		}
		select {
		case decision := <-result:
			if decision.Kind != session.ApprovalCancelled {
				t.Fatalf("clear decision = %s", decision.Kind)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("clear session did not wake waiter")
		}
	})

	t.Run("close", func(t *testing.T) {
		service := NewService()
		authorization, err := service.Authorize(context.Background(), session.PermissionAsk, testAction(session.ActionRunCheck, "close"))
		if err != nil {
			t.Fatalf("authorize: %v", err)
		}
		result := make(chan session.ApprovalDecision, 1)
		go func() {
			decision, _ := service.WaitDecision(context.Background(), authorization.Request.ID)
			result <- decision
		}()
		waitForApprovalWaiter(t, service, authorization.Request.ID)
		if err := service.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
		select {
		case decision := <-result:
			if decision.Kind != session.ApprovalCancelled {
				t.Fatalf("close decision = %s", decision.Kind)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("close did not wake waiter")
		}
	})
}

func waitForApprovalWaiter(t *testing.T, service *Service, requestID session.ApprovalRequestID) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		service.mu.Lock()
		pending := service.pending[requestID]
		waiting := pending != nil && pending.waiting
		service.mu.Unlock()
		if waiting {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("approval waiter did not start")
}

func TestServiceWaitCancellationRemovesPendingRequest(t *testing.T) {
	service := NewService()
	authorization, err := service.Authorize(context.Background(), session.PermissionAsk, testAction(session.ActionApplyPatch, "cancel"))
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = service.WaitDecision(ctx, authorization.Request.ID)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("wait cancellation error = %v", err)
	}
	err = service.Resolve(context.Background(), session.ApprovalResolution{
		RequestID: authorization.Request.ID,
		SessionID: authorization.Request.SessionID,
		TurnID:    authorization.Request.TurnID,
		Decision:  session.ApprovalDecision{Kind: session.ApprovalAllowOnce},
	})
	if errorCode(err) != session.ErrNotFound {
		t.Fatalf("cancelled request remained pending: %v", err)
	}
}

func testAction(kind session.ActionKind, fingerprint string) session.Action {
	action := session.Action{
		ID:           "action_test",
		SessionID:    "session_test",
		TurnID:       "turn_test",
		Kind:         kind,
		WorktreeRoot: "C:/workspace/repo",
		Fingerprint:  fingerprint,
	}
	switch kind {
	case session.ActionApplyPatch:
		action.Patch = &session.PatchAction{Patch: "diff", Files: []string{"main.go"}}
	case session.ActionRunCheck, session.ActionStartLanguageServer:
		action.Command = &session.CommandAction{Program: "go", Args: []string{"test", "./..."}, Timeout: time.Minute}
	}
	return action
}

func errorCode(err error) session.ErrorCode {
	var appError *session.AppError
	if errors.As(err, &appError) {
		return appError.Code
	}
	return ""
}
