package agent

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/eaglc/codepilot/internal/language"
	"github.com/eaglc/codepilot/internal/session"
	"github.com/eaglc/codepilot/internal/tool"
)

var _ tool.Tool = (*runChecksTool)(nil)

// runChecksTool lets the model select an ID but resolves that ID to a trusted,
// prevalidated command captured from the active language profile.
type runChecksTool struct {
	scope      session.TurnScope
	workspaces WorkspaceTools
	state      *turnToolState
	plans      map[string]language.CheckPlan
	definition tool.Definition
}

type runChecksArguments struct {
	PlanID string `json:"plan_id"`
}

type runChecksOutput struct {
	PlanID     string               `json:"plan_id"`
	Outcome    session.CheckOutcome `json:"outcome"`
	Summary    string               `json:"summary"`
	ExitCode   int                  `json:"exit_code"`
	Stdout     string               `json:"stdout"`
	Stderr     string               `json:"stderr"`
	DurationMS int64                `json:"duration_ms"`
	TimedOut   bool                 `json:"timed_out"`
	Truncated  bool                 `json:"truncated"`
	Denied     bool                 `json:"denied"`
	Reason     string               `json:"reason,omitempty"`
}

// newRunChecksTool derives the input enum from the trusted plan map so unknown
// or model-authored commands cannot satisfy the tool schema.
func newRunChecksTool(scope session.TurnScope, workspaces WorkspaceTools, state *turnToolState, plans map[string]language.CheckPlan) (*runChecksTool, error) {
	planIDs := make([]string, 0, len(plans))
	for planID := range plans {
		planIDs = append(planIDs, planID)
	}
	sort.Strings(planIDs)
	schema, err := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"plan_id": map[string]any{"type": "string", "enum": planIDs, "description": "One check plan ID from the active language profile."},
		},
		"required":             []string{"plan_id"},
		"additionalProperties": false,
	})
	if err != nil {
		return nil, err
	}
	return &runChecksTool{
		scope:      scope,
		workspaces: workspaces,
		state:      state,
		plans:      plans,
		definition: tool.Definition{Name: "run_checks", Description: "Run one trusted check plan from the active language profile after approval.", InputSchema: schema},
	}, nil
}

func (t *runChecksTool) Definition() tool.Definition {
	value := t.definition
	value.InputSchema = append(json.RawMessage(nil), t.definition.InputSchema...)
	return value
}

func (t *runChecksTool) Invoke(ctx context.Context, arguments json.RawMessage) (tool.Result, error) {
	var parsed runChecksArguments
	if invalid := decodeToolArguments(arguments, &parsed); invalid != nil {
		return *invalid, nil
	}
	plan, exists := t.plans[parsed.PlanID]
	if !exists || strings.TrimSpace(parsed.PlanID) == "" {
		return invalidArgument("The requested check plan is not available for this turn.")
	}
	command := plan.Command
	command.Args = append([]string(nil), plan.Command.Args...)
	command.EnvAllowlist = append([]string(nil), plan.Command.EnvAllowlist...)
	if command.Timeout <= 0 || command.Timeout > t.scope.Limits.CommandTimeout {
		command.Timeout = t.scope.Limits.CommandTimeout
	}
	if command.MaxOutputBytes <= 0 || command.MaxOutputBytes > t.scope.Limits.CommandOutputMaxBytes {
		command.MaxOutputBytes = t.scope.Limits.CommandOutputMaxBytes
	}
	result, err := t.workspaces.RunChecks(ctx, RunChecksRequest{
		WorktreeRoot:   t.scope.WorktreeRoot,
		SessionID:      t.scope.SessionID,
		TurnID:         t.scope.TurnID,
		PermissionMode: t.scope.PermissionMode,
		Command:        command,
	})
	if err != nil {
		return normalizeToolError(ctx, err)
	}
	t.state.recordCheck(result)
	output := runChecksOutput{
		PlanID:     result.PlanID,
		Outcome:    result.Outcome,
		Summary:    result.Summary,
		ExitCode:   result.ExitCode,
		Stdout:     result.Stdout,
		Stderr:     result.Stderr,
		DurationMS: result.Duration.Milliseconds(),
		TimedOut:   result.TimedOut,
		Truncated:  result.Truncated,
		Denied:     result.Denied,
		Reason:     result.Reason,
	}
	if result.Denied {
		return deniedToolResult(result.Reason, output, t.scope.Limits.ToolResultMaxBytes), nil
	}
	return completedJSONResult(output, t.scope.Limits.ToolResultMaxBytes), nil
}
