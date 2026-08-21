package workspace

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/eaglc/codepilot/internal/agent"
	"github.com/eaglc/codepilot/internal/approval"
	"github.com/eaglc/codepilot/internal/session"
)

func TestServiceRunChecksRequiresApprovalThenReturnsEvidence(t *testing.T) {
	root := newCommandFixture(t, "package fixture\n")
	authorizer := approval.NewService()
	executor := &recordingCommandExecutor{result: CommandResult{ExitCode: 0, Stdout: "ok\n", Duration: time.Second}}
	service := newCheckTestService(t, authorizer, executor)
	request := checkRequest(root, session.PermissionAsk)

	result, err := service.RunChecks(context.Background(), request)
	var approvalRequired *session.ApprovalRequiredError
	if !errors.As(err, &approvalRequired) || result.Outcome != session.CheckNotRun || executor.calls != 0 {
		t.Fatalf("approval prompt: result=%#v calls=%d err=%v", result, executor.calls, err)
	}
	approvalRequest := approvalRequired.Request
	if approvalRequest.Action.Command == nil || approvalRequest.Action.Command.Program != "go" {
		t.Fatalf("approval omitted command details: %#v", approvalRequest)
	}
	if err := authorizer.Resolve(context.Background(), session.ApprovalResolution{
		RequestID: approvalRequest.ID,
		SessionID: approvalRequest.SessionID,
		TurnID:    approvalRequest.TurnID,
		Decision:  session.ApprovalDecision{Kind: session.ApprovalAllowOnce},
	}); err != nil {
		t.Fatalf("resolve approval: %v", err)
	}
	if _, err := authorizer.WaitDecision(context.Background(), approvalRequest.ID); err != nil {
		t.Fatalf("wait approval: %v", err)
	}

	result, err = service.RunChecks(context.Background(), request)
	if err != nil {
		t.Fatalf("run approved check: %v", err)
	}
	if result.Outcome != session.CheckPassed || result.ExitCode != 0 || result.Stdout != "ok\n" || executor.calls != 1 {
		t.Fatalf("unexpected check evidence: %#v calls=%d", result, executor.calls)
	}
}

func TestServiceRunChecksPolicyNeverStartsDeniedOrPendingCommand(t *testing.T) {
	root := newCommandFixture(t, "package fixture\n")
	tests := []struct {
		name        string
		mode        session.PermissionMode
		wantOutcome session.CheckOutcome
		wantError   session.ErrorCode
	}{
		{name: "read only denies", mode: session.PermissionReadOnly, wantOutcome: session.CheckDenied},
		{name: "ask prompts", mode: session.PermissionAsk, wantOutcome: session.CheckNotRun, wantError: session.ErrApprovalRequired},
		{name: "auto edit still prompts", mode: session.PermissionAutoEdit, wantOutcome: session.CheckNotRun, wantError: session.ErrApprovalRequired},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			executor := &recordingCommandExecutor{result: CommandResult{ExitCode: 0}}
			service := newCheckTestService(t, approval.NewService(), executor)
			result, err := service.RunChecks(context.Background(), checkRequest(root, test.mode))
			if result.Outcome != test.wantOutcome || errorCode(err) != test.wantError || executor.calls != 0 {
				t.Fatalf("result=%#v calls=%d err=%v", result, executor.calls, err)
			}
		})
	}
}

func TestServiceRunChecksRejectsUntrustedPlanFieldsBeforeAuthorization(t *testing.T) {
	root := newCommandFixture(t, "package fixture\n")
	authorizer := &recordingPatchAuthorizer{}
	executor := &recordingCommandExecutor{}
	service := newCheckTestService(t, authorizer, executor)
	tests := []struct {
		name   string
		mutate func(*agent.RunChecksRequest)
	}{
		{name: "absolute command directory", mutate: func(request *agent.RunChecksRequest) { request.Command.Dir = root }},
		{name: "shell executable", mutate: func(request *agent.RunChecksRequest) { request.Command.Program = "cmd" }},
		{name: "escaping argument", mutate: func(request *agent.RunChecksRequest) { request.Command.Args = []string{"test", "../outside"} }},
		{name: "secret environment", mutate: func(request *agent.RunChecksRequest) { request.Command.EnvAllowlist = []string{"API_TOKEN"} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := checkRequest(root, session.PermissionAsk)
			test.mutate(&request)
			if _, err := service.RunChecks(context.Background(), request); errorCode(err) != session.ErrInvalidInput {
				t.Fatalf("untrusted plan error = %v", err)
			}
		})
	}
	if authorizer.calls != 0 || executor.calls != 0 {
		t.Fatalf("invalid plans reached a side-effect boundary: authorizer=%d executor=%d", authorizer.calls, executor.calls)
	}
}

func TestServiceRunChecksClassifiesExecutorResults(t *testing.T) {
	root := newCommandFixture(t, "package fixture\n")
	tests := []struct {
		name   string
		result CommandResult
		err    error
		want   session.CheckOutcome
	}{
		{name: "failure", result: CommandResult{ExitCode: 1, Stderr: "failed", Truncated: true}, want: session.CheckFailed},
		{name: "timeout", result: CommandResult{ExitCode: -1, TimedOut: true}, want: session.CheckTimedOut},
		{name: "unavailable", err: errors.New("runtime missing"), want: session.CheckUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			executor := &recordingCommandExecutor{result: test.result, err: test.err}
			service := newCheckTestService(t, &recordingPatchAuthorizer{}, executor)
			result, err := service.RunChecks(context.Background(), checkRequest(root, session.PermissionAsk))
			if err != nil || result.Outcome != test.want {
				t.Fatalf("result=%#v err=%v", result, err)
			}
		})
	}
}

type recordingCommandExecutor struct {
	result CommandResult
	err    error
	calls  int
	spec   CommandSpec
}

func (e *recordingCommandExecutor) Run(_ context.Context, spec CommandSpec) (CommandResult, error) {
	e.calls++
	e.spec = spec
	return e.result, e.err
}

func (e *recordingCommandExecutor) Start(context.Context, ProcessSpec) (CommandProcess, error) {
	return nil, errors.New("unexpected process start")
}

func newCheckTestService(t *testing.T, authorizer ActionAuthorizer, executor CommandExecutor) *Service {
	t.Helper()
	service, err := NewService(Dependencies{Authorizer: authorizer, Executor: executor, Limits: DefaultLimits()})
	if err != nil {
		t.Fatalf("create check service: %v", err)
	}
	return service
}

func checkRequest(root string, mode session.PermissionMode) agent.RunChecksRequest {
	return agent.RunChecksRequest{
		WorktreeRoot:   root,
		SessionID:      "session_check",
		TurnID:         "turn_check",
		PermissionMode: mode,
		Command: agent.CheckCommand{
			ID:             "go-test-all",
			Program:        "go",
			Args:           []string{"test", "./..."},
			Dir:            ".",
			EnvAllowlist:   []string{"GOCACHE", "GOMODCACHE", "GOTMPDIR"},
			Timeout:        time.Minute,
			MaxOutputBytes: 1 << 20,
		},
	}
}
