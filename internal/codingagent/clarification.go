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
	"strings"

	"github.com/eaglc/codepilot/internal/llm"
	"github.com/eaglc/codepilot/internal/tool"
)

const (
	clarificationToolName      = "request_user_input"
	clarificationInterruptKind = "clarification"
	ClarificationOtherOptionID = "other"
	maxClarificationTextBytes  = 4 << 10
	maxClarificationQuestions  = 3
)

// ClarificationSelectionMode controls whether one or several choices can be
// selected for a question.
type ClarificationSelectionMode string

const (
	ClarificationSelectionSingle   ClarificationSelectionMode = "single"
	ClarificationSelectionMultiple ClarificationSelectionMode = "multiple"
)

// ClarificationOption is one mutually exclusive answer presented to the user.
type ClarificationOption struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description"`
	Recommended bool   `json:"recommended"`
}

// ClarificationRequest is one material Plan decision that cannot be safely
// inferred from repository facts or a reversible default.
type ClarificationRequest struct {
	ID            string                     `json:"id"`
	Header        string                     `json:"header"`
	Question      string                     `json:"question"`
	SelectionMode ClarificationSelectionMode `json:"selection_mode,omitempty"`
	Options       []ClarificationOption      `json:"options"`
}

// ClarificationPrompt groups the material decisions that are already known at
// one Planning boundary. The UI presents them one at a time while the runtime
// persists and resumes them as one durable interruption.
type ClarificationPrompt struct {
	Questions []ClarificationRequest `json:"questions"`
}

// ClarificationAnswer is the user's selected option or free-form alternative.
type ClarificationAnswer struct {
	QuestionID string `json:"question_id"`
	// OptionID preserves compatibility with v1/v2 single-choice answers.
	OptionID  string   `json:"option_id,omitempty"`
	OptionIDs []string `json:"option_ids,omitempty"`
	OtherText string   `json:"other_text,omitempty"`
}

type clarificationPayloadV1 struct {
	Kind    string               `json:"kind"`
	Version int                  `json:"version"`
	Request ClarificationRequest `json:"request"`
}

type clarificationPayloadV2 struct {
	Kind    string              `json:"kind"`
	Version int                 `json:"version"`
	Prompt  ClarificationPrompt `json:"prompt"`
}

type clarificationPayloadV3 struct {
	Kind    string              `json:"kind"`
	Version int                 `json:"version"`
	Prompt  ClarificationPrompt `json:"prompt"`
}

type clarificationAnswerPayloadV1 struct {
	Kind    string              `json:"kind"`
	Version int                 `json:"version"`
	Answer  ClarificationAnswer `json:"answer"`
}

type clarificationAnswerPayloadV2 struct {
	Kind    string                `json:"kind"`
	Version int                   `json:"version"`
	Answers []ClarificationAnswer `json:"answers"`
}

type clarificationAnswerPayloadV3 struct {
	Kind    string                `json:"kind"`
	Version int                   `json:"version"`
	Answers []ClarificationAnswer `json:"answers"`
}

type clarificationTool struct {
	turns  TurnRepository
	turnID TurnID
}

func (*clarificationTool) Definition() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        clarificationToolName,
		Description: "Pause read-only planning to resolve 1-3 related material user decisions already known at this point. Choose single when options are mutually exclusive and multiple when several options may be combined. Each question must provide 2-4 choices with concise tradeoffs. Mark the recommended option for single choice or the recommended set for multiple choice when there is a useful default. The UI presents questions one at a time and always adds an Other option for free-form input. Do not use this for facts discoverable from available evidence or for low-impact reversible defaults.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"questions":{"type":"array","minItems":1,"maxItems":3,"items":{"type":"object","properties":{"id":{"type":"string","pattern":"^[a-z0-9-]{1,64}$"},"header":{"type":"string","minLength":1},"question":{"type":"string","minLength":1},"selection_mode":{"type":"string","enum":["single","multiple"]},"options":{"type":"array","minItems":2,"maxItems":4,"items":{"type":"object","properties":{"id":{"type":"string","pattern":"^[a-z0-9-]{1,64}$"},"label":{"type":"string","minLength":1},"description":{"type":"string","minLength":1},"recommended":{"type":"boolean"}},"required":["id","label","description"],"additionalProperties":false}}},"required":["id","header","question","options"],"additionalProperties":false}}},"required":["questions"],"additionalProperties":false}`),
	}
}

func (*clarificationTool) ReplayPolicy() tool.ReplayPolicy { return tool.ReplayIdempotent }

func (*clarificationTool) ControlPolicy() tool.ControlPolicy {
	return tool.ControlPolicy{Exclusive: true}
}

func (t *clarificationTool) Execute(ctx context.Context, call tool.Call, _ tool.ProgressSink) (tool.Result, error) {
	if t == nil || t.turns == nil || t.turnID == "" {
		return tool.Result{}, errors.New("request Plan clarification: trusted Turn scope is incomplete")
	}
	var prompt ClarificationPrompt
	decoder := json.NewDecoder(bytes.NewReader(call.Arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&prompt); err != nil {
		return clarificationInvalidResult("The clarification request is not valid structured data."), nil
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return clarificationInvalidResult("The clarification request contains trailing data."), nil
	}
	prompt = normalizeClarificationPrompt(prompt)
	if err := validateClarificationPrompt(prompt); err != nil {
		return clarificationInvalidResult(err.Error()), nil
	}
	turn, err := t.turns.LoadTurn(ctx, t.turnID)
	if err != nil {
		return tool.Result{}, fmt.Errorf("request Plan clarification: load Product Turn: %w", err)
	}
	if turn.Phase != TurnPhasePlanning || turn.Status != TurnRunning {
		return clarificationInvalidResult("The Product Turn is not in the read-only Planning phase."), nil
	}
	payload := clarificationPayloadV3{Kind: "coding_plan_clarification_v3", Version: 3, Prompt: prompt}
	encoded, _ := json.Marshal(payload)
	digest := sha256.Sum256(encoded)
	return tool.Result{
		Status:  tool.ResultInterrupted,
		Content: []llm.Content{{Type: llm.ContentText, Text: "Planning is waiting for the user's choice."}},
		Details: encoded,
		Interrupt: &tool.Interrupt{
			ID:      "clarification:" + prompt.Questions[0].ID + ":" + hex.EncodeToString(digest[:8]),
			Kind:    clarificationInterruptKind,
			Payload: encoded,
		},
	}, nil
}

func (t *clarificationTool) Resume(ctx context.Context, _ tool.Call, interrupt tool.Interrupt, resolution tool.Result, _ tool.ProgressSink) (tool.Result, error) {
	prompt, err := decodeClarificationPrompt(interrupt.Payload)
	if err != nil {
		return tool.Result{}, errors.New("resume Plan clarification: durable request payload is invalid")
	}
	turn, err := t.turns.LoadTurn(ctx, t.turnID)
	if err != nil {
		return tool.Result{}, fmt.Errorf("resume Plan clarification: load Product Turn: %w", err)
	}
	if turn.Phase != TurnPhasePlanning {
		return tool.Result{}, errors.New("resume Plan clarification: Product Turn is no longer Planning")
	}
	if resolution.Status != tool.ResultCompleted {
		return tool.Result{}, errors.New("resume Plan clarification: a selected or free-form answer is required")
	}
	answers, err := decodeClarificationAnswers(resolution.Details)
	if err != nil {
		return tool.Result{}, errors.New("resume Plan clarification: answer payload is invalid")
	}
	if err := validateClarificationAnswers(prompt, answers); err != nil {
		return tool.Result{}, fmt.Errorf("resume Plan clarification: %w", err)
	}
	var summary strings.Builder
	summary.WriteString("The user answered the Plan clarification:")
	for index, request := range prompt.Questions {
		answerText := clarificationAnswerText(request, answers[index])
		summary.WriteString("\n- ")
		summary.WriteString(request.Question)
		summary.WriteString(": ")
		summary.WriteString(answerText)
	}
	return tool.Result{
		Status:  tool.ResultCompleted,
		Content: []llm.Content{{Type: llm.ContentText, Text: summary.String()}},
		Details: append(json.RawMessage(nil), resolution.Details...),
	}, nil
}

// EncodeClarificationAnswers validates and encodes one complete UI answer set
// for ResumeTurn.
func EncodeClarificationAnswers(prompt ClarificationPrompt, answers []ClarificationAnswer) (json.RawMessage, error) {
	prompt = normalizeClarificationPrompt(prompt)
	if err := validateClarificationPrompt(prompt); err != nil {
		return nil, err
	}
	if err := validateClarificationAnswers(prompt, answers); err != nil {
		return nil, err
	}
	canonical := make([]ClarificationAnswer, len(answers))
	for index, answer := range answers {
		optionIDs, err := clarificationAnswerOptionIDs(answer)
		if err != nil {
			return nil, err
		}
		canonical[index] = ClarificationAnswer{QuestionID: answer.QuestionID, OptionIDs: optionIDs, OtherText: answer.OtherText}
	}
	return json.Marshal(clarificationAnswerPayloadV3{Kind: "coding_plan_clarification_answer_v3", Version: 3, Answers: canonical})
}

// EncodeClarificationAnswer keeps single-question callers source-compatible
// while encoding the current durable answer format.
func EncodeClarificationAnswer(request ClarificationRequest, answer ClarificationAnswer) (json.RawMessage, error) {
	return EncodeClarificationAnswers(ClarificationPrompt{Questions: []ClarificationRequest{request}}, []ClarificationAnswer{answer})
}

func validateClarificationPrompt(value ClarificationPrompt) error {
	if len(value.Questions) < 1 || len(value.Questions) > maxClarificationQuestions {
		return fmt.Errorf("Plan clarification requires between 1 and %d questions", maxClarificationQuestions)
	}
	seen := make(map[string]struct{}, len(value.Questions))
	for _, request := range value.Questions {
		if err := validateClarificationRequest(request); err != nil {
			return err
		}
		if _, exists := seen[request.ID]; exists {
			return errors.New("Plan clarification question ids must be unique")
		}
		seen[request.ID] = struct{}{}
	}
	return nil
}

func validateClarificationRequest(value ClarificationRequest) error {
	if !validPlanStepID(value.ID) || value.ID == ClarificationOtherOptionID {
		return errors.New("Plan clarification id must use 1-64 lowercase letters, digits, or hyphens")
	}
	if err := validateClarificationText("header", value.Header, true); err != nil {
		return err
	}
	if err := validateClarificationText("question", value.Question, true); err != nil {
		return err
	}
	if value.SelectionMode != ClarificationSelectionSingle && value.SelectionMode != ClarificationSelectionMultiple {
		return errors.New("Plan clarification selection_mode must be single or multiple")
	}
	if len(value.Options) < 2 || len(value.Options) > 4 {
		return errors.New("Plan clarification requires between 2 and 4 choices")
	}
	seen := make(map[string]struct{}, len(value.Options))
	for _, option := range value.Options {
		if !validPlanStepID(option.ID) || option.ID == ClarificationOtherOptionID {
			return errors.New("Plan clarification choice id must use 1-64 lowercase letters, digits, or hyphens and cannot be other")
		}
		if _, exists := seen[option.ID]; exists {
			return errors.New("Plan clarification choice ids must be unique")
		}
		seen[option.ID] = struct{}{}
		if err := validateClarificationText("choice label", option.Label, true); err != nil {
			return err
		}
		if err := validateClarificationText("choice description", option.Description, true); err != nil {
			return err
		}
	}
	return nil
}

func validateClarificationAnswers(prompt ClarificationPrompt, answers []ClarificationAnswer) error {
	if len(answers) != len(prompt.Questions) {
		return errors.New("every Plan clarification question requires exactly one answer")
	}
	for index, request := range prompt.Questions {
		if err := validateClarificationAnswer(request, answers[index]); err != nil {
			return fmt.Errorf("question %q: %w", request.ID, err)
		}
	}
	return nil
}

func validateClarificationAnswer(request ClarificationRequest, answer ClarificationAnswer) error {
	if answer.QuestionID != request.ID {
		return errors.New("answer does not match the current question")
	}
	optionIDs, err := clarificationAnswerOptionIDs(answer)
	if err != nil {
		return err
	}
	if request.SelectionMode == ClarificationSelectionSingle && len(optionIDs) != 1 {
		return errors.New("a single-choice question requires exactly one selected option")
	}
	if request.SelectionMode == ClarificationSelectionMultiple && (len(optionIDs) < 1 || len(optionIDs) > len(request.Options)+1) {
		return errors.New("a multiple-choice question requires at least one selected option")
	}
	available := make(map[string]struct{}, len(request.Options))
	for _, option := range request.Options {
		available[option.ID] = struct{}{}
	}
	seen := make(map[string]struct{}, len(optionIDs))
	hasOther := false
	for _, optionID := range optionIDs {
		if _, exists := seen[optionID]; exists {
			return errors.New("answer choices must be unique")
		}
		seen[optionID] = struct{}{}
		if optionID == ClarificationOtherOptionID {
			hasOther = true
			continue
		}
		if _, exists := available[optionID]; !exists {
			return errors.New("answer choice is not available")
		}
	}
	if hasOther {
		return validateClarificationText("free-form answer", answer.OtherText, true)
	}
	if strings.TrimSpace(answer.OtherText) != "" {
		return errors.New("free-form text requires the other choice")
	}
	return nil
}

func clarificationAnswerOptionIDs(answer ClarificationAnswer) ([]string, error) {
	if answer.OptionID != "" && len(answer.OptionIDs) != 0 {
		return nil, errors.New("answer cannot use both option_id and option_ids")
	}
	if answer.OptionID != "" {
		return []string{answer.OptionID}, nil
	}
	return append([]string(nil), answer.OptionIDs...), nil
}

func validateClarificationText(label, value string, required bool) error {
	trimmed := strings.TrimSpace(value)
	if required && trimmed == "" {
		return fmt.Errorf("Plan clarification %s is required", label)
	}
	if value != trimmed || len(value) > maxClarificationTextBytes || strings.ContainsRune(value, 0) {
		return fmt.Errorf("Plan clarification %s is not normalized or exceeds its size limit", label)
	}
	return nil
}

func clarificationInvalidResult(message string) tool.Result {
	return tool.Result{Status: tool.ResultInvalid, Content: []llm.Content{{Type: llm.ContentText, Text: message}}}
}

func decodeClarificationPrompt(raw json.RawMessage) (ClarificationPrompt, error) {
	var envelope struct {
		Kind    string `json:"kind"`
		Version int    `json:"version"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return ClarificationPrompt{}, errors.New("invalid clarification payload")
	}
	var prompt ClarificationPrompt
	switch {
	case envelope.Kind == "coding_plan_clarification_v3" && envelope.Version == 3:
		var payload clarificationPayloadV3
		if json.Unmarshal(raw, &payload) != nil {
			return ClarificationPrompt{}, errors.New("invalid clarification payload")
		}
		prompt = payload.Prompt
	case envelope.Kind == "coding_plan_clarification_v2" && envelope.Version == 2:
		var payload clarificationPayloadV2
		if json.Unmarshal(raw, &payload) != nil {
			return ClarificationPrompt{}, errors.New("invalid clarification payload")
		}
		prompt = payload.Prompt
	case envelope.Kind == "coding_plan_clarification_v1" && envelope.Version == 1:
		var payload clarificationPayloadV1
		if json.Unmarshal(raw, &payload) != nil {
			return ClarificationPrompt{}, errors.New("invalid clarification payload")
		}
		prompt = ClarificationPrompt{Questions: []ClarificationRequest{payload.Request}}
	default:
		return ClarificationPrompt{}, errors.New("unsupported clarification payload")
	}
	prompt = normalizeClarificationPrompt(prompt)
	if err := validateClarificationPrompt(prompt); err != nil {
		return ClarificationPrompt{}, err
	}
	return prompt, nil
}

func decodeClarificationAnswers(raw json.RawMessage) ([]ClarificationAnswer, error) {
	var envelope struct {
		Kind    string `json:"kind"`
		Version int    `json:"version"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return nil, errors.New("invalid clarification answer payload")
	}
	switch {
	case envelope.Kind == "coding_plan_clarification_answer_v3" && envelope.Version == 3:
		var payload clarificationAnswerPayloadV3
		if json.Unmarshal(raw, &payload) != nil {
			return nil, errors.New("invalid clarification answer payload")
		}
		return payload.Answers, nil
	case envelope.Kind == "coding_plan_clarification_answer_v2" && envelope.Version == 2:
		var payload clarificationAnswerPayloadV2
		if json.Unmarshal(raw, &payload) != nil {
			return nil, errors.New("invalid clarification answer payload")
		}
		return payload.Answers, nil
	case envelope.Kind == "coding_plan_clarification_answer_v1" && envelope.Version == 1:
		var payload clarificationAnswerPayloadV1
		if json.Unmarshal(raw, &payload) != nil {
			return nil, errors.New("invalid clarification answer payload")
		}
		return []ClarificationAnswer{payload.Answer}, nil
	default:
		return nil, errors.New("unsupported clarification answer payload")
	}
}

func clarificationAnswerText(request ClarificationRequest, answer ClarificationAnswer) string {
	optionIDs, _ := clarificationAnswerOptionIDs(answer)
	labels := make([]string, 0, len(optionIDs))
	for _, optionID := range optionIDs {
		if optionID == ClarificationOtherOptionID {
			labels = append(labels, "Other: "+answer.OtherText)
			continue
		}
		for _, option := range request.Options {
			if option.ID == optionID {
				labels = append(labels, option.Label)
				break
			}
		}
	}
	return strings.Join(labels, ", ")
}

func normalizeClarificationPrompt(prompt ClarificationPrompt) ClarificationPrompt {
	prompt.Questions = append([]ClarificationRequest(nil), prompt.Questions...)
	for questionIndex := range prompt.Questions {
		request := &prompt.Questions[questionIndex]
		if request.SelectionMode == "" {
			request.SelectionMode = ClarificationSelectionSingle
		}
		request.Options = append([]ClarificationOption(nil), request.Options...)
		if request.SelectionMode != ClarificationSelectionSingle {
			continue
		}
		foundRecommendation := false
		for optionIndex := range request.Options {
			if !request.Options[optionIndex].Recommended {
				continue
			}
			if foundRecommendation {
				request.Options[optionIndex].Recommended = false
				continue
			}
			foundRecommendation = true
		}
	}
	return prompt
}

func projectClarificationInterrupt(target *PendingInterrupt, raw json.RawMessage) {
	if target == nil || len(raw) == 0 || !json.Valid(raw) {
		return
	}
	prompt, err := decodeClarificationPrompt(raw)
	if err != nil {
		return
	}
	prompt.Questions = append([]ClarificationRequest(nil), prompt.Questions...)
	for questionIndex := range prompt.Questions {
		request := &prompt.Questions[questionIndex]
		request.Header = boundedUTF8(redactSensitiveText(request.Header), maxClarificationTextBytes)
		request.Question = boundedUTF8(redactSensitiveText(request.Question), maxClarificationTextBytes)
		request.Options = append([]ClarificationOption(nil), request.Options...)
		for optionIndex := range request.Options {
			request.Options[optionIndex].Label = boundedUTF8(redactSensitiveText(request.Options[optionIndex].Label), maxClarificationTextBytes)
			request.Options[optionIndex].Description = boundedUTF8(redactSensitiveText(request.Options[optionIndex].Description), maxClarificationTextBytes)
		}
	}
	target.Summary = prompt.Questions[0].Question
	target.Clarification = &prompt
}
