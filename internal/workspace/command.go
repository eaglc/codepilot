package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/eaglc/codepilot/internal/agent"
	"github.com/eaglc/codepilot/internal/session"
)

// CommandSpec describes one pre-approved executable without a shell string.
type CommandSpec struct {
	ID             string
	Program        string
	Args           []string
	Dir            string
	EnvAllowlist   []string
	Timeout        time.Duration
	MaxOutputBytes int
}

// CommandResult contains bounded process evidence.
type CommandResult struct {
	ExitCode  int
	Stdout    string
	Stderr    string
	Duration  time.Duration
	TimedOut  bool
	Truncated bool
}

// RunChecks authorizes and executes one trusted language check plan.
func (s *Service) RunChecks(ctx context.Context, request agent.RunChecksRequest) (agent.RunChecksResult, error) {
	if err := validateRunChecksRequest(request); err != nil {
		return agent.RunChecksResult{}, err
	}
	if s.authorizer == nil || s.executor == nil {
		return agent.RunChecksResult{}, workspaceAppError(session.ErrInvalidState, "workspace.run_checks", "Check authorization or execution is unavailable.", nil)
	}
	root, err := s.verifiedWorktreeRoot(ctx, request.WorktreeRoot)
	if err != nil {
		return agent.RunChecksResult{}, err
	}
	directory, err := resolveCheckDirectory(root, request.Command.Dir)
	if err != nil {
		return agent.RunChecksResult{}, err
	}
	spec := CommandSpec{
		ID:             request.Command.ID,
		Program:        request.Command.Program,
		Args:           append([]string(nil), request.Command.Args...),
		Dir:            directory,
		EnvAllowlist:   append([]string(nil), request.Command.EnvAllowlist...),
		Timeout:        request.Command.Timeout,
		MaxOutputBytes: request.Command.MaxOutputBytes,
	}
	if _, err := validateCommandSpec(ctx, spec); err != nil {
		return agent.RunChecksResult{}, err
	}
	fingerprint := commandFingerprint(request, root, spec)
	action := session.Action{
		ID:           "action_check_" + fingerprint[:16],
		SessionID:    request.SessionID,
		TurnID:       request.TurnID,
		Kind:         session.ActionRunCheck,
		WorktreeRoot: root,
		Summary:      boundedSummary("Run check "+spec.ID+": "+spec.Program+" "+strings.Join(spec.Args, " "), 500),
		Fingerprint:  fingerprint,
		Command: &session.CommandAction{
			Program: spec.Program,
			Args:    append([]string(nil), spec.Args...),
			Timeout: spec.Timeout,
		},
	}
	authorization, err := s.authorizer.Authorize(ctx, request.PermissionMode, action)
	if err != nil {
		return agent.RunChecksResult{}, err
	}
	switch authorization.Outcome {
	case session.AuthorizationDeny:
		return agent.RunChecksResult{
			PlanID:  spec.ID,
			Outcome: session.CheckDenied,
			Summary: "The project check was denied by the current permission policy.",
			Denied:  true,
			Reason:  authorization.Reason,
		}, nil
	case session.AuthorizationPrompt:
		if authorization.Request == nil {
			return agent.RunChecksResult{}, workspaceAppError(session.ErrInternal, "workspace.run_checks", "Check approval could not be requested.", nil)
		}
		return agent.RunChecksResult{PlanID: spec.ID, Outcome: session.CheckNotRun}, &session.ApprovalRequiredError{Request: *authorization.Request}
	case session.AuthorizationAllow:
	default:
		return agent.RunChecksResult{}, workspaceAppError(session.ErrInternal, "workspace.run_checks", "Check authorization returned an invalid outcome.", nil)
	}

	commandResult, err := s.executor.Run(ctx, spec)
	if err != nil {
		if ctx.Err() != nil {
			return agent.RunChecksResult{PlanID: spec.ID, Outcome: session.CheckCancelled, Summary: "The project check was cancelled."}, err
		}
		return agent.RunChecksResult{
			PlanID:  spec.ID,
			Outcome: session.CheckUnavailable,
			Summary: "The approved project check could not be started in the local environment.",
		}, nil
	}
	result := agent.RunChecksResult{
		PlanID:    spec.ID,
		ExitCode:  commandResult.ExitCode,
		Stdout:    commandResult.Stdout,
		Stderr:    commandResult.Stderr,
		Duration:  commandResult.Duration,
		TimedOut:  commandResult.TimedOut,
		Truncated: commandResult.Truncated,
	}
	switch {
	case commandResult.TimedOut:
		result.Outcome = session.CheckTimedOut
		result.Summary = "The approved project check exceeded its timeout."
	case commandResult.ExitCode == 0:
		result.Outcome = session.CheckPassed
		result.Summary = "The approved project check passed."
	default:
		result.Outcome = session.CheckFailed
		result.Summary = "The approved project check reported a failure."
	}
	return result, nil
}

func validateRunChecksRequest(request agent.RunChecksRequest) error {
	if strings.TrimSpace(request.WorktreeRoot) == "" || request.SessionID == "" || request.TurnID == "" {
		return workspaceAppError(session.ErrInvalidInput, "workspace.run_checks", "Worktree, session, and turn identifiers are required.", nil)
	}
	switch request.PermissionMode {
	case session.PermissionReadOnly, session.PermissionAsk, session.PermissionAutoEdit:
		return nil
	default:
		return workspaceAppError(session.ErrInvalidInput, "workspace.run_checks", "The check permission mode is invalid.", nil)
	}
}

func resolveCheckDirectory(root string, relative string) (string, error) {
	if strings.TrimSpace(relative) == "" || filepath.Clean(relative) == "." {
		return root, nil
	}
	if filepath.IsAbs(relative) || filepath.VolumeName(relative) != "" {
		return "", workspaceAppError(session.ErrInvalidInput, "workspace.run_checks", "Check directories must be worktree-relative.", nil)
	}
	absolute, _, err := securePath(root, relative, false)
	if err != nil {
		return "", err
	}
	directory, err := canonicalExistingDirectory(absolute)
	if err != nil {
		return "", err
	}
	return directory, nil
}

func commandFingerprint(request agent.RunChecksRequest, root string, spec CommandSpec) string {
	digest := sha256.New()
	environment := append([]string(nil), spec.EnvAllowlist...)
	sort.Strings(environment)
	values := []string{
		"run-check",
		string(request.SessionID),
		string(request.TurnID),
		filepath.Clean(root),
		spec.ID,
		spec.Program,
		filepath.Clean(spec.Dir),
		spec.Timeout.String(),
		strconv.Itoa(spec.MaxOutputBytes),
		"arguments",
	}
	values = append(values, spec.Args...)
	values = append(values, "environment")
	values = append(values, environment...)
	for _, value := range values {
		_, _ = io.WriteString(digest, value)
		_, _ = digest.Write([]byte{0})
	}
	return hex.EncodeToString(digest.Sum(nil))
}
