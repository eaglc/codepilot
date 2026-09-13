package codingagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/eaglc/codepilot/internal/codingagent/roleprofile"
	"github.com/eaglc/codepilot/internal/llm"
	"github.com/eaglc/codepilot/internal/tool"
	"github.com/eaglc/codepilot/internal/workflow"
)

const delegatePlanExploreToolName = "delegate_plan_explore"

type delegatePlanExploreTool struct {
	children ChildAgentRepository
	turns    TurnRepository
	roles    *roleprofile.Registry
	session  SessionID
	turnID   TurnID
}

func (*delegatePlanExploreTool) Definition() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        delegatePlanExploreToolName,
		Description: "Delegate one bounded, serial, read-only workspace exploration to an independent child Agent. Use at most once and only when a high-complexity Plan needs focused evidence. This exclusive call returns control to the parent coordinator; it never grants writes.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"goal":{"type":"string"},"read_paths":{"type":"array","items":{"type":"string"}},"acceptance_criteria":{"type":"array","items":{"type":"string"}}},"required":["goal","read_paths","acceptance_criteria"],"additionalProperties":false}`),
	}
}

func (*delegatePlanExploreTool) ReplayPolicy() tool.ReplayPolicy { return tool.ReplayIdempotent }

func (*delegatePlanExploreTool) ControlPolicy() tool.ControlPolicy {
	return tool.ControlPolicy{Exclusive: true, HandoffAfterExecution: true}
}

func (t *delegatePlanExploreTool) Execute(ctx context.Context, call tool.Call, _ tool.ProgressSink) (tool.Result, error) {
	if t == nil || t.children == nil || t.turns == nil || t.roles == nil || t.session == "" || t.turnID == "" || strings.TrimSpace(call.ID) == "" {
		return tool.Result{}, errors.New("delegate Plan exploration: trusted parent scope is incomplete")
	}
	var arguments struct {
		Goal               string   `json:"goal"`
		ReadPaths          []string `json:"read_paths"`
		AcceptanceCriteria []string `json:"acceptance_criteria"`
	}
	decoder := json.NewDecoder(bytes.NewReader(call.Arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&arguments); err != nil {
		return planExploreInvalid("The Plan exploration request is not valid structured data."), nil
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return planExploreInvalid("The Plan exploration request contains trailing data."), nil
	}
	arguments.Goal = strings.TrimSpace(arguments.Goal)
	for index := range arguments.ReadPaths {
		normalized, err := NormalizePlanPath(arguments.ReadPaths[index])
		if err != nil {
			return planExploreInvalid("Plan exploration read paths must be normalized worktree-relative paths."), nil
		}
		arguments.ReadPaths[index] = normalized
	}
	sort.Strings(arguments.ReadPaths)
	arguments.ReadPaths = compactStrings(arguments.ReadPaths)
	task := AgentTask{Goal: arguments.Goal, ReadPaths: arguments.ReadPaths, AcceptanceCriteria: arguments.AcceptanceCriteria}
	definition, err := t.roles.ResolveRole(workflow.RoleExplore)
	if err != nil || definition.Profile != roleprofile.ProfileExplore {
		return tool.Result{}, errors.New("delegate Plan exploration: Explore role policy is unavailable")
	}
	if err := ValidateAgentTaskWithPolicy(task, workflow.RoleExplore, definition); err != nil {
		return planExploreInvalid(err.Error()), nil
	}
	turn, err := t.turns.LoadTurn(ctx, t.turnID)
	if err != nil {
		return tool.Result{}, fmt.Errorf("delegate Plan exploration: load parent Turn: %w", err)
	}
	if turn.SessionID != t.session || turn.Phase != TurnPhasePlanning || turn.Status != TurnRunning || turn.Strategy != ExecutionSingle {
		return planExploreInvalid("Plan exploration is available only from a running read-only Planning turn."), nil
	}
	binding, active := turn.ActiveRun()
	if !active || binding.Profile != CapabilityPlanWorkspace || binding.ChildAgentID != "" {
		return planExploreInvalid("Plan exploration requires the parent read-only workspace Planning Agent."), nil
	}
	children, err := t.children.ListChildAgents(ctx, turn.ID)
	if err != nil {
		return tool.Result{}, fmt.Errorf("delegate Plan exploration: list prior children: %w", err)
	}
	id := PlanExploreChildAgentID(turn.ID, call.ID)
	for _, child := range children {
		if child.Kind != ChildAgentPlanExplore {
			continue
		}
		if child.ID != id || !reflect.DeepEqual(child.Task, task) {
			return planExploreInvalid("This Plan turn already used its one read-only exploration child."), nil
		}
		if child.Status == ChildAgentCompleted || child.Status == ChildAgentFailed || child.Status == ChildAgentCancelled {
			return planExploreAccepted(child), nil
		}
		return t.bindPlanExplore(ctx, turn, child)
	}
	now := time.Now().UTC()
	child := ChildAgent{
		ID: id, Kind: ChildAgentPlanExplore, ParentSessionID: t.session, ParentTurnID: turn.ID,
		Role: workflow.RoleExplore, Profile: CapabilityProfile(definition.Profile), PolicyVersion: definition.PolicyVersion, Task: task,
		AgentSessionID: ChildAgentSessionID(id), RunID: ChildAgentRunID(id), Attempt: 1,
		Status: ChildAgentCreating, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := t.children.CreateChildAgent(ctx, child); err != nil {
		return tool.Result{}, fmt.Errorf("delegate Plan exploration: persist creation intent: %w", err)
	}
	return t.bindPlanExplore(ctx, turn, child)
}

func (t *delegatePlanExploreTool) bindPlanExplore(ctx context.Context, turn Turn, child ChildAgent) (tool.Result, error) {
	if turn.PendingPlanExploreID == "" {
		expected := turn.Revision
		turn.PendingPlanExploreID = child.ID
		turn.UpdatedAt = time.Now().UTC()
		turn.Revision++
		if err := t.turns.SaveTurn(ctx, turn, expected); err != nil {
			latest, loadErr := t.turns.LoadTurn(ctx, turn.ID)
			if loadErr != nil || latest.PendingPlanExploreID != child.ID {
				return tool.Result{}, fmt.Errorf("delegate Plan exploration: bind parent Turn: %w", err)
			}
		}
	} else if turn.PendingPlanExploreID != child.ID {
		return planExploreInvalid("Another Plan exploration is already pending."), nil
	}
	return planExploreAccepted(child), nil
}

func planExploreAccepted(child ChildAgent) tool.Result {
	details, _ := json.Marshal(struct {
		ChildAgentID ChildAgentID `json:"child_agent_id"`
	}{ChildAgentID: child.ID})
	return tool.Result{Status: tool.ResultCompleted, Content: []llm.Content{{Type: llm.ContentText, Text: "The read-only exploration task is durably queued. Return control to the parent coordinator."}}, Details: details}
}

func planExploreInvalid(message string) tool.Result {
	return tool.Result{Status: tool.ResultInvalid, Content: []llm.Content{{Type: llm.ContentText, Text: message}}}
}
