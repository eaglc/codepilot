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
	enterPlanModeToolName         = "enter_plan_mode"
	planEntryApprovalKind         = "plan_entry_approval"
	maxPlanEntrySummaryBytes      = 1024
	maxDeclinedPlanEntryReasonSet = 16
)

// PlanEntryReasonCode identifies a bounded, user-facing reason for suggesting
// Plan mode without persisting or exposing model chain-of-thought.
type PlanEntryReasonCode string

const (
	PlanEntryMaterialAmbiguity      PlanEntryReasonCode = "material_ambiguity"
	PlanEntryCrossModuleChange      PlanEntryReasonCode = "cross_module_change"
	PlanEntryOrderedDependencies    PlanEntryReasonCode = "ordered_dependencies"
	PlanEntryArchitectureTradeoff   PlanEntryReasonCode = "architecture_tradeoff"
	PlanEntryMigrationCompatibility PlanEntryReasonCode = "migration_or_compatibility"
	PlanEntrySecurityPermissions    PlanEntryReasonCode = "security_or_permissions"
	PlanEntryHighRollbackCost       PlanEntryReasonCode = "high_rollback_cost"
	PlanEntryComplexValidation      PlanEntryReasonCode = "complex_validation"
	PlanEntryWorkflowCandidate      PlanEntryReasonCode = "workflow_candidate"
	PlanEntryComplexityEscalation   PlanEntryReasonCode = "complexity_escalation"
)

// PlanEntrySuggestion is the durable, bounded proposal shown before a Direct
// task can switch into the read-only Planning profile.
type PlanEntrySuggestion struct {
	ReasonCode  PlanEntryReasonCode `json:"reason_code"`
	Summary     string              `json:"summary"`
	Digest      string              `json:"digest"`
	SuggestedAt time.Time           `json:"suggested_at"`
}

type planEntrySubmission struct {
	ReasonCode PlanEntryReasonCode `json:"reason_code"`
	Summary    string              `json:"summary"`
}

type planEntryApprovalPayload struct {
	Kind       string              `json:"kind"`
	Version    int                 `json:"version"`
	ReasonCode PlanEntryReasonCode `json:"reason_code"`
	Summary    string              `json:"summary"`
	Digest     string              `json:"digest"`
}

type enterPlanModeTool struct {
	turns    TurnRepository
	turnID   TurnID
	allowNew bool
}

func (*enterPlanModeTool) Definition() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        enterPlanModeToolName,
		Description: "Suggest switching this Direct task into read-only Plan mode when material complexity or risk makes user review valuable. The user must approve the switch; this call cannot change permissions.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"reason_code":{"type":"string","enum":["material_ambiguity","cross_module_change","ordered_dependencies","architecture_tradeoff","migration_or_compatibility","security_or_permissions","high_rollback_cost","complex_validation","workflow_candidate","complexity_escalation"]},"summary":{"type":"string","minLength":1,"maxLength":1024}},"required":["reason_code","summary"],"additionalProperties":false}`),
	}
}

func (*enterPlanModeTool) ReplayPolicy() tool.ReplayPolicy { return tool.ReplayIdempotent }

func (*enterPlanModeTool) ControlPolicy() tool.ControlPolicy {
	return tool.ControlPolicy{Exclusive: true, HandoffAfterResolution: true}
}

func (t *enterPlanModeTool) Execute(ctx context.Context, call tool.Call, _ tool.ProgressSink) (tool.Result, error) {
	if t == nil || t.turns == nil || t.turnID == "" {
		return tool.Result{}, errors.New("suggest Coding plan entry: trusted Turn scope is incomplete")
	}
	var submission planEntrySubmission
	decoder := json.NewDecoder(bytes.NewReader(call.Arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&submission); err != nil {
		return planEntryInvalidResult("The Plan mode suggestion is not valid structured data."), nil
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return planEntryInvalidResult("The Plan mode suggestion contains trailing data."), nil
	}
	submission.Summary = strings.TrimSpace(submission.Summary)
	if err := validatePlanEntrySubmission(submission); err != nil {
		return planEntryInvalidResult(err.Error()), nil
	}
	turn, err := t.turns.LoadTurn(ctx, t.turnID)
	if err != nil {
		return tool.Result{}, fmt.Errorf("suggest Coding plan entry: load Product Turn: %w", err)
	}
	digest := computePlanEntryDigest(submission.ReasonCode, submission.Summary)
	if turn.Phase == TurnPhaseAwaitingPlanEntryApproval && turn.PlanEntrySuggestion != nil && turn.PlanEntrySuggestion.Digest == digest {
		return planEntryApprovalInterruptResult(*turn.PlanEntrySuggestion), nil
	}
	if !t.allowNew {
		return planEntryInvalidResult("New Plan mode suggestions are disabled for this session."), nil
	}
	if turn.Phase != TurnPhaseDirect || turn.Status != TurnRunning {
		return planEntryInvalidResult("The Product Turn is not in the Direct phase."), nil
	}
	binding, found := turn.ActiveRun()
	if !found || binding.Profile != CapabilityDirect || binding.Phase != TurnPhaseDirect {
		return planEntryInvalidResult("The Product Turn has no active Direct Run."), nil
	}
	for _, declined := range turn.DeclinedPlanReasons {
		if declined == submission.ReasonCode {
			return planEntryInvalidResult("The user already declined this Plan suggestion reason. Continue the Direct task unless materially new risk fits a different reason code."), nil
		}
	}
	suggestion := PlanEntrySuggestion{ReasonCode: submission.ReasonCode, Summary: submission.Summary, Digest: digest, SuggestedAt: time.Now().UTC()}
	expected := turn.Revision
	turn.Phase = TurnPhaseAwaitingPlanEntryApproval
	turn.PlanEntrySuggestion = &suggestion
	turn.UpdatedAt = suggestion.SuggestedAt
	turn.Revision++
	if err := t.turns.SaveTurn(ctx, turn, expected); err != nil {
		latest, loadErr := t.turns.LoadTurn(ctx, t.turnID)
		if loadErr != nil || latest.Phase != TurnPhaseAwaitingPlanEntryApproval || latest.PlanEntrySuggestion == nil || !reflect.DeepEqual(*latest.PlanEntrySuggestion, suggestion) {
			return tool.Result{}, fmt.Errorf("suggest Coding plan entry: bind Product Turn: %w", err)
		}
	}
	return planEntryApprovalInterruptResult(suggestion), nil
}

func (t *enterPlanModeTool) Resume(ctx context.Context, _ tool.Call, interrupt tool.Interrupt, resolution tool.Result, _ tool.ProgressSink) (tool.Result, error) {
	var payload planEntryApprovalPayload
	if json.Unmarshal(interrupt.Payload, &payload) != nil || payload.Kind != "coding_plan_entry_approval_v1" || payload.Version != 1 || !validPlanEntryReason(payload.ReasonCode) || !isHexDigest(payload.Digest, 64, 64) {
		return tool.Result{}, errors.New("resume Coding plan entry: durable approval payload is invalid")
	}
	turn, err := t.turns.LoadTurn(ctx, t.turnID)
	if err != nil {
		return tool.Result{}, fmt.Errorf("resume Coding plan entry: load Product Turn: %w", err)
	}
	if turn.PlanEntrySuggestion == nil || turn.PlanEntrySuggestion.ReasonCode != payload.ReasonCode || turn.PlanEntrySuggestion.Summary != payload.Summary || turn.PlanEntrySuggestion.Digest != payload.Digest {
		return tool.Result{}, errors.New("resume Coding plan entry: decision does not match the current suggestion")
	}
	if resolution.Status == tool.ResultDenied {
		if turn.Phase != TurnPhaseDirect {
			return tool.Result{}, errors.New("resume Coding plan entry: declined suggestion did not return to Direct mode")
		}
		resolution.Content = []llm.Content{{Type: llm.ContentText, Text: "The user declined Plan mode. Continue the original task directly; this is not a cancellation or permission change."}}
		resolution.Details = json.RawMessage(`{"decision":"declined"}`)
		return resolution, nil
	}
	if turn.Phase != TurnPhaseAwaitingPlanEntryApproval {
		return tool.Result{}, errors.New("resume Coding plan entry: Product Turn is not waiting for this decision")
	}
	switch resolution.Status {
	case tool.ResultCompleted:
		resolution.Content = []llm.Content{{Type: llm.ContentText, Text: "The user approved switching this task into read-only Plan mode. Return control to the coordinator; do not continue Direct execution."}}
		resolution.Details = json.RawMessage(`{"decision":"approved"}`)
		return resolution, nil
	case tool.ResultCancelled:
		return tool.Result{Status: tool.ResultCompleted, Content: []llm.Content{{Type: llm.ContentText, Text: "The user cancelled the task instead of entering Plan mode."}}, Details: json.RawMessage(`{"decision":"cancelled"}`)}, nil
	default:
		return tool.Result{}, fmt.Errorf("resume Coding plan entry: unsupported resolution %q", resolution.Status)
	}
}

func planEntryApprovalInterruptResult(value PlanEntrySuggestion) tool.Result {
	payload := planEntryApprovalPayload{
		Kind: "coding_plan_entry_approval_v1", Version: 1, ReasonCode: value.ReasonCode,
		Summary: value.Summary, Digest: value.Digest,
	}
	encoded, _ := json.Marshal(payload)
	return tool.Result{
		Status:    tool.ResultInterrupted,
		Content:   []llm.Content{{Type: llm.ContentText, Text: "Plan mode was suggested and is waiting for the user's decision."}},
		Details:   encoded,
		Interrupt: &tool.Interrupt{ID: planEntryApprovalInterruptID(value), Kind: planEntryApprovalKind, Payload: encoded},
	}
}

func validatePlanEntrySubmission(value planEntrySubmission) error {
	if !validPlanEntryReason(value.ReasonCode) {
		return errors.New("The Plan mode suggestion reason is unsupported.")
	}
	if value.Summary == "" || !utf8.ValidString(value.Summary) || len(value.Summary) > maxPlanEntrySummaryBytes || strings.ContainsAny(value.Summary, "\r\n\x00") {
		return errors.New("The Plan mode suggestion summary must be one bounded line.")
	}
	return nil
}

func validatePlanEntrySuggestion(value PlanEntrySuggestion) error {
	if err := validatePlanEntrySubmission(planEntrySubmission{ReasonCode: value.ReasonCode, Summary: value.Summary}); err != nil {
		return err
	}
	if !isHexDigest(value.Digest, 64, 64) || value.Digest != computePlanEntryDigest(value.ReasonCode, value.Summary) || value.SuggestedAt.IsZero() {
		return errors.New("Coding Plan entry suggestion digest or timestamp is invalid")
	}
	return nil
}

func validateTurnPlanEntry(value Turn) error {
	if value.PlanEntrySuggestion != nil {
		if err := validatePlanEntrySuggestion(*value.PlanEntrySuggestion); err != nil {
			return err
		}
	}
	if value.Phase == TurnPhaseAwaitingPlanEntryApproval && value.PlanEntrySuggestion == nil {
		return errors.New("Coding turn awaiting Plan entry requires an exact suggestion")
	}
	if len(value.DeclinedPlanReasons) > maxDeclinedPlanEntryReasonSet {
		return errors.New("Coding turn declined Plan reason history exceeds its limit")
	}
	seen := make(map[PlanEntryReasonCode]struct{}, len(value.DeclinedPlanReasons))
	for _, reason := range value.DeclinedPlanReasons {
		if !validPlanEntryReason(reason) {
			return fmt.Errorf("Coding turn declined Plan reason %q is unsupported", reason)
		}
		if _, found := seen[reason]; found {
			return fmt.Errorf("Coding turn declined Plan reason %q is duplicated", reason)
		}
		seen[reason] = struct{}{}
	}
	return nil
}

func validateTurnPlanEntryTransition(previous, next Turn) error {
	if len(next.DeclinedPlanReasons) < len(previous.DeclinedPlanReasons) || len(next.DeclinedPlanReasons) > len(previous.DeclinedPlanReasons)+1 {
		return errors.New("Coding turn declined Plan reason history must be preserved and appended one at a time")
	}
	for index := range previous.DeclinedPlanReasons {
		if previous.DeclinedPlanReasons[index] != next.DeclinedPlanReasons[index] {
			return errors.New("Coding turn declined Plan reason history changed")
		}
	}
	if len(next.DeclinedPlanReasons) != len(previous.DeclinedPlanReasons) {
		if previous.Phase != TurnPhaseAwaitingPlanEntryApproval || next.Phase != TurnPhaseDirect || previous.PlanEntrySuggestion == nil || next.DeclinedPlanReasons[len(next.DeclinedPlanReasons)-1] != previous.PlanEntrySuggestion.ReasonCode {
			return errors.New("Coding turn can record a declined Plan reason only when returning to Direct mode")
		}
	}
	if !reflect.DeepEqual(previous.PlanEntrySuggestion, next.PlanEntrySuggestion) {
		if previous.Phase != TurnPhaseDirect || next.Phase != TurnPhaseAwaitingPlanEntryApproval || next.PlanEntrySuggestion == nil {
			return errors.New("Coding turn Plan entry suggestion can change only at a new Direct-to-approval boundary")
		}
	}
	return nil
}

func validPlanEntryReason(value PlanEntryReasonCode) bool {
	switch value {
	case PlanEntryMaterialAmbiguity, PlanEntryCrossModuleChange, PlanEntryOrderedDependencies,
		PlanEntryArchitectureTradeoff, PlanEntryMigrationCompatibility, PlanEntrySecurityPermissions,
		PlanEntryHighRollbackCost, PlanEntryComplexValidation, PlanEntryWorkflowCandidate,
		PlanEntryComplexityEscalation:
		return true
	default:
		return false
	}
}

func computePlanEntryDigest(reason PlanEntryReasonCode, summary string) string {
	digest := sha256.Sum256([]byte(string(reason) + "\x00" + summary))
	return hex.EncodeToString(digest[:])
}

func planEntryApprovalInterruptID(value PlanEntrySuggestion) string {
	return fmt.Sprintf("plan-entry:%s:%s", value.ReasonCode, value.Digest[:16])
}

func planEntryInvalidResult(message string) tool.Result {
	return tool.Result{Status: tool.ResultInvalid, Content: []llm.Content{{Type: llm.ContentText, Text: message}}}
}
