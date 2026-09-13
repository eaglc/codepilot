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
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/eaglc/codepilot/internal/llm"
	"github.com/eaglc/codepilot/internal/tool"
)

const (
	requestPlanReplanToolName = "request_plan_replan"
	planReplanApprovalKind    = "plan_replan_approval"
	maxPlanReplanSummaryBytes = 1024
)

// PlanReplanReasonCode identifies a bounded execution deviation without
// persisting model chain-of-thought.
type PlanReplanReasonCode string

const (
	PlanReplanInvalidAssumption PlanReplanReasonCode = "invalid_assumption"
	PlanReplanScopeExpanded     PlanReplanReasonCode = "material_scope_change"
	PlanReplanNewHighRiskAction PlanReplanReasonCode = "new_high_risk_action"
	PlanReplanStrategyChange    PlanReplanReasonCode = "strategy_change"
	PlanReplanWorkspaceDrift    PlanReplanReasonCode = "workspace_drift"
)

// PlanReplanRequest is the durable, bounded execution checkpoint shown to the
// user before the Agent may leave an approved Plan's scope.
type PlanReplanRequest struct {
	ReasonCode  PlanReplanReasonCode `json:"reason_code"`
	Summary     string               `json:"summary"`
	Digest      string               `json:"digest"`
	PlanVersion uint64               `json:"plan_version"`
	PlanDigest  string               `json:"plan_digest"`
	RequestedAt time.Time            `json:"requested_at"`
	Decision    string               `json:"decision,omitempty"`
	ResolvedAt  time.Time            `json:"resolved_at,omitempty"`
}

type planReplanSubmission struct {
	ReasonCode PlanReplanReasonCode `json:"reason_code"`
	Summary    string               `json:"summary"`
}

type planReplanApprovalPayload struct {
	Kind        string               `json:"kind"`
	Version     int                  `json:"version"`
	ReasonCode  PlanReplanReasonCode `json:"reason_code"`
	Summary     string               `json:"summary"`
	Digest      string               `json:"digest"`
	PlanVersion uint64               `json:"plan_version"`
	PlanDigest  string               `json:"plan_digest"`
}

type requestPlanReplanTool struct {
	turns  TurnRepository
	turnID TurnID
}

func (*requestPlanReplanTool) Definition() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        requestPlanReplanToolName,
		Description: "Pause approved Plan execution when a material assumption, scope, risk, strategy, or workspace change requires user-reviewed replanning. This call cannot expand scope or grant permission.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"reason_code":{"type":"string","enum":["invalid_assumption","material_scope_change","new_high_risk_action","strategy_change","workspace_drift"]},"summary":{"type":"string","minLength":1,"maxLength":1024}},"required":["reason_code","summary"],"additionalProperties":false}`),
	}
}

func (*requestPlanReplanTool) ReplayPolicy() tool.ReplayPolicy { return tool.ReplayIdempotent }

func (*requestPlanReplanTool) ControlPolicy() tool.ControlPolicy {
	return tool.ControlPolicy{Exclusive: true, HandoffAfterResolution: true}
}

func (t *requestPlanReplanTool) Execute(ctx context.Context, call tool.Call, _ tool.ProgressSink) (tool.Result, error) {
	if t == nil || t.turns == nil || t.turnID == "" {
		return tool.Result{}, errors.New("request Coding plan revision: trusted Turn scope is incomplete")
	}
	var submission planReplanSubmission
	decoder := json.NewDecoder(bytes.NewReader(call.Arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&submission); err != nil {
		return planReplanInvalidResult("The replan request is not valid structured data."), nil
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return planReplanInvalidResult("The replan request contains trailing data."), nil
	}
	submission.Summary = strings.TrimSpace(submission.Summary)
	if err := validatePlanReplanSubmission(submission); err != nil {
		return planReplanInvalidResult(err.Error()), nil
	}
	turn, err := t.turns.LoadTurn(ctx, t.turnID)
	if err != nil {
		return tool.Result{}, fmt.Errorf("request Coding plan revision: load Product Turn: %w", err)
	}
	digest := computePlanReplanDigest(submission.ReasonCode, submission.Summary, turn.PlanVersion, turn.PlanDigest)
	if turn.Phase == TurnPhaseNeedsReplan && turn.PlanReplan != nil && turn.PlanReplan.Digest == digest {
		return planReplanApprovalInterruptResult(*turn.PlanReplan), nil
	}
	if turn.Phase != TurnPhaseExecuting || turn.Status != TurnRunning || turn.PlanVersion == 0 || !isHexDigest(turn.PlanDigest, 64, 64) {
		return planReplanInvalidResult("The Product Turn is not executing an approved Plan."), nil
	}
	binding, found := turn.ActiveRun()
	if !found || binding.Profile != CapabilityDirect || binding.Phase != TurnPhaseExecuting {
		return planReplanInvalidResult("The Product Turn has no active approved Plan execution Run."), nil
	}
	now := time.Now().UTC()
	request := PlanReplanRequest{
		ReasonCode: submission.ReasonCode, Summary: submission.Summary, Digest: digest,
		PlanVersion: turn.PlanVersion, PlanDigest: turn.PlanDigest, RequestedAt: now,
	}
	expected := turn.Revision
	turn.Phase = TurnPhaseNeedsReplan
	turn.PlanReplan = &request
	turn.PlanReplanCount++
	turn.UpdatedAt = now
	turn.Revision++
	if err := t.turns.SaveTurn(ctx, turn, expected); err != nil {
		latest, loadErr := t.turns.LoadTurn(ctx, t.turnID)
		if loadErr != nil || latest.Phase != TurnPhaseNeedsReplan || latest.PlanReplan == nil || !reflect.DeepEqual(*latest.PlanReplan, request) {
			return tool.Result{}, fmt.Errorf("request Coding plan revision: persist decision boundary: %w", err)
		}
	}
	return planReplanApprovalInterruptResult(request), nil
}

func (t *requestPlanReplanTool) Resume(ctx context.Context, _ tool.Call, interrupt tool.Interrupt, resolution tool.Result, _ tool.ProgressSink) (tool.Result, error) {
	var payload planReplanApprovalPayload
	if json.Unmarshal(interrupt.Payload, &payload) != nil || payload.Kind != "coding_plan_replan_approval_v1" || payload.Version != 1 || !validPlanReplanReason(payload.ReasonCode) || !isHexDigest(payload.Digest, 64, 64) || payload.PlanVersion == 0 || !isHexDigest(payload.PlanDigest, 64, 64) {
		return tool.Result{}, errors.New("resume Coding plan revision: durable request payload is invalid")
	}
	turn, err := t.turns.LoadTurn(ctx, t.turnID)
	if err != nil {
		return tool.Result{}, fmt.Errorf("resume Coding plan revision: load Product Turn: %w", err)
	}
	if turn.PlanReplan == nil || turn.PlanReplan.ReasonCode != payload.ReasonCode || turn.PlanReplan.Summary != payload.Summary || turn.PlanReplan.Digest != payload.Digest || turn.PlanReplan.PlanVersion != payload.PlanVersion || turn.PlanReplan.PlanDigest != payload.PlanDigest {
		return tool.Result{}, errors.New("resume Coding plan revision: decision does not match the current replan request")
	}
	switch resolution.Status {
	case tool.ResultCompleted:
		if turn.Phase != TurnPhaseNeedsReplan {
			return tool.Result{}, errors.New("resume Coding plan revision: Product Turn is not waiting to replan")
		}
		resolution.Content = []llm.Content{{Type: llm.ContentText, Text: "The user approved returning to read-only planning. Hand control to the coordinator without further execution side effects."}}
		resolution.Details = json.RawMessage(`{"decision":"replan"}`)
		return resolution, nil
	case tool.ResultDenied:
		if turn.Phase != TurnPhaseExecuting {
			return tool.Result{}, errors.New("resume Coding plan revision: Product Turn is not approved to continue execution")
		}
		resolution.Content = []llm.Content{{Type: llm.ContentText, Text: "The user chose to continue the exact approved Plan. Do not expand scope; request replanning again if the material deviation remains."}}
		resolution.Details = json.RawMessage(`{"decision":"continue"}`)
		return resolution, nil
	case tool.ResultCancelled:
		if turn.Phase != TurnPhaseNeedsReplan {
			return tool.Result{}, errors.New("resume Coding plan revision: Product Turn is not waiting for cancellation")
		}
		return tool.Result{Status: tool.ResultCompleted, Content: []llm.Content{{Type: llm.ContentText, Text: "The user cancelled the task at the replan boundary."}}, Details: json.RawMessage(`{"decision":"cancelled"}`)}, nil
	default:
		return tool.Result{}, fmt.Errorf("resume Coding plan revision: unsupported resolution %q", resolution.Status)
	}
}

func planReplanApprovalInterruptResult(value PlanReplanRequest) tool.Result {
	payload := planReplanApprovalPayload{
		Kind: "coding_plan_replan_approval_v1", Version: 1, ReasonCode: value.ReasonCode, Summary: value.Summary,
		Digest: value.Digest, PlanVersion: value.PlanVersion, PlanDigest: value.PlanDigest,
	}
	encoded, _ := json.Marshal(payload)
	return tool.Result{
		Status:    tool.ResultInterrupted,
		Content:   []llm.Content{{Type: llm.ContentText, Text: "Approved Plan execution paused because a material deviation may require replanning."}},
		Details:   encoded,
		Interrupt: &tool.Interrupt{ID: planReplanApprovalInterruptID(value), Kind: planReplanApprovalKind, Payload: encoded},
	}
}

func validatePlanReplanSubmission(value planReplanSubmission) error {
	if !validPlanReplanReason(value.ReasonCode) {
		return errors.New("The replan reason is unsupported.")
	}
	if value.Summary == "" || !utf8.ValidString(value.Summary) || len(value.Summary) > maxPlanReplanSummaryBytes || strings.ContainsAny(value.Summary, "\r\n\x00") {
		return errors.New("The replan summary must be one bounded line.")
	}
	return nil
}

func validPlanReplanReason(value PlanReplanReasonCode) bool {
	switch value {
	case PlanReplanInvalidAssumption, PlanReplanScopeExpanded, PlanReplanNewHighRiskAction, PlanReplanStrategyChange, PlanReplanWorkspaceDrift:
		return true
	default:
		return false
	}
}

func computePlanReplanDigest(reason PlanReplanReasonCode, summary string, version uint64, planDigest string) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d\x00%s", reason, summary, version, planDigest)))
	return hex.EncodeToString(digest[:])
}

func planReplanApprovalInterruptID(value PlanReplanRequest) string {
	return fmt.Sprintf("plan-replan:%d:%s", value.PlanVersion, value.Digest[:16])
}

func planReplanInvalidResult(message string) tool.Result {
	return tool.Result{Status: tool.ResultInvalid, Content: []llm.Content{{Type: llm.ContentText, Text: message}}}
}

func validateTurnPlanLifecycle(value Turn) error {
	hasApproval := value.ApprovedPlanVersion != 0 || value.ApprovedPlanDigest != "" || !value.ApprovedAt.IsZero()
	completeApproval := value.ApprovedPlanVersion != 0 && isHexDigest(value.ApprovedPlanDigest, 64, 64) && !value.ApprovedAt.IsZero()
	if hasApproval != completeApproval || value.ApprovedPlanVersion > value.PlanVersion {
		return errors.New("Coding turn approved Plan reference is incomplete or ahead of the current version")
	}
	if (value.Phase == TurnPhaseExecuting || value.Phase == TurnPhaseNeedsReplan) && (!completeApproval || value.ApprovedPlanVersion != value.PlanVersion || value.ApprovedPlanDigest != value.PlanDigest) {
		return errors.New("Coding turn execution must bind the exact approved Plan revision")
	}
	if value.WorkspaceDrift != nil {
		drift := value.WorkspaceDrift
		if drift.Severity != WorkspaceDriftInformational && drift.Severity != WorkspaceDriftMaterial {
			return errors.New("Coding turn workspace drift severity is invalid")
		}
		if drift.Source != WorkspaceDriftAtApproval && drift.Source != WorkspaceDriftAtExecution && drift.Source != WorkspaceDriftDuringExecution {
			return errors.New("Coding turn workspace drift source is invalid")
		}
		if drift.Reason == "" || drift.Summary == "" || len(drift.Summary) > 2048 || strings.ContainsAny(drift.Summary, "\r\n\x00") || drift.PlanVersion == 0 || drift.PlanVersion > value.PlanVersion || !isHexDigest(drift.PlanDigest, 64, 64) || !isHexDigest(drift.BaselineDigest, 64, 64) || !isHexDigest(drift.CurrentDigest, 64, 64) || drift.DetectedAt.IsZero() {
			return errors.New("Coding turn workspace drift record is incomplete")
		}
		previous := ""
		for _, path := range drift.Paths {
			normalized, err := NormalizePlanPath(path)
			if err != nil || normalized != path || path <= previous {
				return errors.New("Coding turn workspace drift paths are invalid or unordered")
			}
			previous = path
		}
		if value.WorkspaceDriftCount == 0 {
			return errors.New("Coding turn workspace drift count is missing")
		}
	} else if value.WorkspaceDriftCount != 0 {
		return errors.New("Coding turn workspace drift count has no audit record")
	}
	if value.PlanReplan != nil {
		request := value.PlanReplan
		if err := validatePlanReplanSubmission(planReplanSubmission{ReasonCode: request.ReasonCode, Summary: request.Summary}); err != nil {
			return err
		}
		if !isHexDigest(request.Digest, 64, 64) || request.Digest != computePlanReplanDigest(request.ReasonCode, request.Summary, request.PlanVersion, request.PlanDigest) || request.PlanVersion == 0 || request.PlanVersion > value.PlanVersion || !isHexDigest(request.PlanDigest, 64, 64) || request.RequestedAt.IsZero() {
			return errors.New("Coding turn replan request is incomplete")
		}
		if request.Decision != "" && request.Decision != "replan" && request.Decision != "continue" && request.Decision != "cancelled" {
			return errors.New("Coding turn replan decision is unsupported")
		}
		if (request.Decision == "") != request.ResolvedAt.IsZero() {
			return errors.New("Coding turn replan decision and resolution time must agree")
		}
		if value.PlanReplanCount == 0 {
			return errors.New("Coding turn replan count is missing")
		}
	} else if value.PlanReplanCount != 0 {
		return errors.New("Coding turn replan count has no audit record")
	}
	return nil
}

func validateTurnPlanLifecycleTransition(previous, next Turn) error {
	if previous.WorkspaceDriftCount > next.WorkspaceDriftCount || next.WorkspaceDriftCount > previous.WorkspaceDriftCount+1 || previous.PlanReplanCount > next.PlanReplanCount || next.PlanReplanCount > previous.PlanReplanCount+1 {
		return errors.New("Coding turn Plan drift or replan counters must advance at most once")
	}
	if previous.ApprovedPlanVersion != 0 {
		if previous.ApprovedPlanVersion != next.ApprovedPlanVersion || previous.ApprovedPlanDigest != next.ApprovedPlanDigest || !previous.ApprovedAt.Equal(next.ApprovedAt) {
			if next.ApprovedPlanVersion != next.PlanVersion || next.ApprovedPlanDigest != next.PlanDigest || next.ApprovedAt.IsZero() {
				return errors.New("Coding turn approved Plan binding changed without exact current approval")
			}
		}
	} else if next.ApprovedPlanVersion != 0 && (previous.Phase != TurnPhaseAwaitingPlanApproval || next.Phase != TurnPhaseExecuting) {
		return errors.New("Coding turn can record initial Plan approval only when execution starts")
	}
	if previous.PlanReplan != nil && next.PlanReplan != nil && previous.PlanReplan.Digest == next.PlanReplan.Digest {
		if previous.PlanReplan.Decision != "" && !reflect.DeepEqual(previous.PlanReplan, next.PlanReplan) {
			return errors.New("Coding turn resolved replan record is immutable")
		}
	}
	return nil
}
