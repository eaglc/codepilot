package codingagent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"time"

	"github.com/eaglc/codepilot/internal/llm"
	"github.com/eaglc/codepilot/internal/tool"
)

const exitPlanModeToolName = "exit_plan_mode"

type exitPlanModeTool struct {
	plans        PlanRepository
	turns        TurnRepository
	turnID       TurnID
	worktreeID   WorktreeID
	worktreeRoot string
}

type planApprovalPayload struct {
	Kind           string             `json:"kind"`
	Version        int                `json:"version"`
	PlanID         PlanID             `json:"plan_id"`
	Revision       uint64             `json:"plan_version"`
	Digest         string             `json:"digest"`
	Summary        string             `json:"summary"`
	CompletionMode PlanCompletionMode `json:"completion_mode"`
}

func (*exitPlanModeTool) Definition() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        exitPlanModeToolName,
		Description: "Submit a complete structured plan for product validation and user review. Declare whether it depends on this workspace and whether approval should execute it or finish the task. This exclusive control call never grants write permission.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"goal":{"type":"string"},"scope":{"type":"object","properties":{"included":{"type":"array","items":{"type":"string"}},"excluded":{"type":"array","items":{"type":"string"}}},"required":["included"],"additionalProperties":false},"findings":{"type":"array","items":{"type":"string"}},"assumptions":{"type":"array","items":{"type":"string"}},"risks":{"type":"array","items":{"type":"string"}},"steps":{"type":"array","items":{"type":"object","properties":{"id":{"type":"string"},"goal":{"type":"string"},"depends_on":{"type":"array","items":{"type":"string"}},"files":{"type":"array","items":{"type":"string"}},"validation":{"type":"array","items":{"type":"string"}}},"required":["id","goal","validation"],"additionalProperties":false}},"acceptance_criteria":{"type":"array","items":{"type":"string"}},"recommended_strategy":{"type":"string","enum":["single"]},"workspace_relevant":{"type":"boolean"},"completion_mode":{"type":"string","enum":["execute","deliverable"]}},"required":["goal","scope","findings","risks","steps","acceptance_criteria","recommended_strategy","workspace_relevant","completion_mode"],"additionalProperties":false}`),
	}
}

func (*exitPlanModeTool) ReplayPolicy() tool.ReplayPolicy { return tool.ReplayIdempotent }

func (*exitPlanModeTool) ControlPolicy() tool.ControlPolicy {
	return tool.ControlPolicy{Exclusive: true, HandoffAfterResolution: true}
}

func (t *exitPlanModeTool) Execute(ctx context.Context, call tool.Call, _ tool.ProgressSink) (tool.Result, error) {
	if t == nil || t.plans == nil || t.turns == nil || t.turnID == "" || t.worktreeID == "" || strings.TrimSpace(t.worktreeRoot) == "" {
		return tool.Result{}, errors.New("submit Coding plan: trusted Plan scope is incomplete")
	}
	var submission PlanSubmission
	decoder := json.NewDecoder(bytes.NewReader(call.Arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&submission); err != nil {
		return planInvalidResult("The Plan submission is not valid structured data."), nil
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return planInvalidResult("The Plan submission contains trailing data."), nil
	}
	if err := normalizePlanSubmission(&submission); err != nil {
		return planInvalidResult(err.Error()), nil
	}
	if err := validatePlanSubmission(submission); err != nil {
		return planInvalidResult(err.Error()), nil
	}
	turn, err := t.turns.LoadTurn(ctx, t.turnID)
	if err != nil {
		return tool.Result{}, fmt.Errorf("submit Coding plan: load Product Turn: %w", err)
	}
	if turn.Phase == TurnPhaseAwaitingPlanApproval && turn.PlanID != "" {
		existing, loadErr := t.plans.LoadPlan(ctx, turn.PlanID, turn.PlanVersion)
		if loadErr == nil && existing.Digest == turn.PlanDigest && reflect.DeepEqual(planSubmissionFromPlan(existing), submission) {
			return planApprovalInterruptResult(existing), nil
		}
	}
	if turn.Phase != TurnPhasePlanning || turn.Status != TurnRunning {
		return planInvalidResult("The Product Turn is not in the read-only Planning phase."), nil
	}
	binding, found := turn.ActiveRun()
	if !found {
		return planInvalidResult("The Product Turn has no active Planning Run."), nil
	}
	if submission.WorkspaceRelevant && binding.Profile != CapabilityPlanWorkspace {
		return planInvalidResult("Request read-only workspace context before submitting a workspace-relevant Plan."), nil
	}
	workspaceRevision := WorkspaceRevision{}
	if submission.WorkspaceRelevant {
		workspaceRevision, err = capturePlanWorkspaceRevision(ctx, t.worktreeID, t.worktreeRoot)
		if err != nil {
			return tool.Result{}, err
		}
	}
	planID := deterministicPlanID(turn.ID)
	if turn.PlanVersion == ^uint64(0) {
		return planInvalidResult("The Plan revision limit has been reached."), nil
	}
	version := turn.PlanVersion + 1
	now := time.Now().UTC()
	value := Plan{
		ID: planID, TurnID: turn.ID, Version: version, Goal: submission.Goal, Scope: submission.Scope,
		Findings: submission.Findings, Assumptions: submission.Assumptions, Risks: submission.Risks,
		Steps: submission.Steps, AcceptanceCriteria: submission.AcceptanceCriteria,
		RecommendedStrategy: submission.RecommendedStrategy, WorkspaceRelevant: submission.WorkspaceRelevant,
		CompletionMode: submission.CompletionMode, WorkspaceRevision: workspaceRevision, CreatedAt: now,
	}
	value.Digest, err = ComputePlanDigest(value)
	if err != nil {
		return tool.Result{}, err
	}
	versions, err := t.plans.ListPlanVersions(ctx, planID)
	if err != nil {
		return tool.Result{}, fmt.Errorf("submit Coding plan: list versions: %w", err)
	}
	if len(versions) >= int(version) {
		existing := versions[version-1]
		if !reflect.DeepEqual(planSubmissionFromPlan(existing), submission) {
			return tool.Result{}, errors.New("submit Coding plan: an existing durable version has different content")
		}
		value = existing
	} else {
		if len(versions) != int(version-1) {
			return tool.Result{}, errors.New("submit Coding plan: durable version sequence is inconsistent")
		}
		if err := t.plans.CreatePlanVersion(ctx, value); err != nil {
			return tool.Result{}, fmt.Errorf("submit Coding plan: persist immutable version: %w", err)
		}
	}
	expected := turn.Revision
	turn.PlanID, turn.PlanVersion, turn.PlanDigest = value.ID, value.Version, value.Digest
	turn.Phase = TurnPhaseAwaitingPlanApproval
	turn.UpdatedAt = now
	turn.Revision++
	if err := t.turns.SaveTurn(ctx, turn, expected); err != nil {
		latest, loadErr := t.turns.LoadTurn(ctx, t.turnID)
		if loadErr != nil || latest.PlanID != value.ID || latest.PlanVersion != value.Version || latest.PlanDigest != value.Digest || latest.Phase != TurnPhaseAwaitingPlanApproval {
			return tool.Result{}, fmt.Errorf("submit Coding plan: bind Product Turn: %w", err)
		}
	}
	return planApprovalInterruptResult(value), nil
}

func planApprovalInterruptResult(value Plan) tool.Result {
	payload := planApprovalPayload{
		Kind: "coding_plan_approval_v1", Version: 1, PlanID: value.ID, Revision: value.Version,
		Digest: value.Digest, Summary: "Review Plan v" + fmt.Sprint(value.Version) + ": " + value.Goal,
		CompletionMode: value.CompletionMode,
	}
	encoded, _ := json.Marshal(payload)
	return tool.Result{
		Status:    tool.ResultInterrupted,
		Content:   []llm.Content{{Type: llm.ContentText, Text: "The structured Plan is saved and waiting for user approval."}},
		Details:   encoded,
		Interrupt: &tool.Interrupt{ID: planApprovalInterruptID(value), Kind: "plan_approval", Payload: encoded},
	}
}

func (t *exitPlanModeTool) Resume(ctx context.Context, _ tool.Call, interrupt tool.Interrupt, resolution tool.Result, _ tool.ProgressSink) (tool.Result, error) {
	var payload planApprovalPayload
	if json.Unmarshal(interrupt.Payload, &payload) != nil || payload.Kind != "coding_plan_approval_v1" || payload.Version != 1 || payload.PlanID == "" || payload.Revision == 0 || !isHexDigest(payload.Digest, 64, 64) {
		return tool.Result{}, errors.New("resume Coding plan approval: durable approval payload is invalid")
	}
	turn, err := t.turns.LoadTurn(ctx, t.turnID)
	if err != nil {
		return tool.Result{}, fmt.Errorf("resume Coding plan approval: load Product Turn: %w", err)
	}
	expectedPhase := turn.Phase == TurnPhaseAwaitingPlanApproval
	if resolution.Status == tool.ResultDenied {
		expectedPhase = turn.Phase == TurnPhasePlanning
	}
	if !expectedPhase || turn.PlanID != payload.PlanID || turn.PlanVersion != payload.Revision || turn.PlanDigest != payload.Digest {
		return tool.Result{}, errors.New("resume Coding plan approval: decision does not match the current Plan revision")
	}
	plan, err := t.plans.LoadPlan(ctx, payload.PlanID, payload.Revision)
	if err != nil || plan.Digest != payload.Digest {
		return tool.Result{}, errors.New("resume Coding plan approval: immutable Plan revision is unavailable or changed")
	}
	switch resolution.Status {
	case tool.ResultCompleted:
		message := "The user approved this exact Plan revision. Return control to the coordinator for execution; write permissions remain unchanged."
		if plan.CompletionMode == PlanCompletionDeliverable {
			message = "The user accepted this exact Plan as the requested deliverable. Return control to the coordinator and finish without starting an execution run."
		}
		resolution.Content = []llm.Content{{Type: llm.ContentText, Text: message}}
		resolution.Details = json.RawMessage(`{"decision":"approved"}`)
		return resolution, nil
	case tool.ResultDenied:
		if len(resolution.Content) == 0 {
			resolution.Content = []llm.Content{{Type: llm.ContentText, Text: "The user requested a revised Plan. Continue read-only planning and submit a new version."}}
		}
		resolution.Details = json.RawMessage(`{"decision":"declined"}`)
		return resolution, nil
	case tool.ResultCancelled:
		return tool.Result{Status: tool.ResultCompleted, Content: []llm.Content{{Type: llm.ContentText, Text: "The user cancelled the Plan task. Return control to the coordinator without execution."}}, Details: json.RawMessage(`{"decision":"cancelled"}`)}, nil
	default:
		return tool.Result{}, fmt.Errorf("resume Coding plan approval: unsupported resolution %q", resolution.Status)
	}
}

func normalizePlanSubmission(value *PlanSubmission) error {
	if value == nil {
		return errors.New("The Plan submission is missing.")
	}
	for stepIndex := range value.Steps {
		for fileIndex := range value.Steps[stepIndex].Files {
			normalized, err := NormalizePlanPath(value.Steps[stepIndex].Files[fileIndex])
			if err != nil {
				return fmt.Errorf("Plan step %q has an invalid file scope", value.Steps[stepIndex].ID)
			}
			value.Steps[stepIndex].Files[fileIndex] = normalized
		}
	}
	return nil
}

func capturePlanWorkspaceRevision(ctx context.Context, worktreeID WorktreeID, root string) (WorkspaceRevision, error) {
	head, _ := runReadOnlyGit(ctx, root, "rev-parse", "HEAD")
	status, err := runReadOnlyGit(ctx, root, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return WorkspaceRevision{}, fmt.Errorf("capture Coding plan workspace revision: %w", err)
	}
	digest := sha256.Sum256([]byte(status))
	return WorkspaceRevision{WorktreeID: worktreeID, GitHead: strings.TrimSpace(head), StatusDigest: hex.EncodeToString(digest[:]), RecordedAt: time.Now().UTC()}, nil
}

func runReadOnlyGit(ctx context.Context, root string, arguments ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", append([]string{"-C", root, "--no-optional-locks"}, arguments...)...)
	command.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	output, err := command.Output()
	if err != nil {
		return "", errors.New("Git could not read the current worktree revision")
	}
	if len(output) > 1<<20 {
		return "", errors.New("Git revision output exceeded its size limit")
	}
	return string(output), nil
}

func deterministicPlanID(turnID TurnID) PlanID {
	digest := sha256.Sum256([]byte(turnID))
	return PlanID("plan_" + hex.EncodeToString(digest[:16]))
}

func planApprovalInterruptID(value Plan) string {
	return fmt.Sprintf("plan-approval:%s:%d:%s", value.ID, value.Version, value.Digest[:16])
}

func planSubmissionFromPlan(value Plan) PlanSubmission {
	return PlanSubmission{
		Goal: value.Goal, Scope: value.Scope, Findings: value.Findings, Assumptions: value.Assumptions,
		Risks: value.Risks, Steps: value.Steps, AcceptanceCriteria: value.AcceptanceCriteria,
		RecommendedStrategy: value.RecommendedStrategy, WorkspaceRelevant: value.WorkspaceRelevant,
		CompletionMode: value.CompletionMode,
	}
}

func planInvalidResult(message string) tool.Result {
	return tool.Result{Status: tool.ResultInvalid, Content: []llm.Content{{Type: llm.ContentText, Text: message}}}
}

func mergeToolRegistry(registry *tool.Registry, extra ...tool.Tool) (*tool.Registry, error) {
	if registry == nil {
		return nil, errors.New("merge Coding tools: base registry is nil")
	}
	tools := make([]tool.Tool, 0, len(registry.Definitions())+len(extra))
	for _, definition := range registry.Definitions() {
		executable, found := registry.Lookup(definition.Name)
		if !found {
			return nil, fmt.Errorf("merge Coding tools: registered tool %q is unavailable", definition.Name)
		}
		tools = append(tools, executable)
	}
	tools = append(tools, extra...)
	return tool.NewRegistry(tools...)
}
