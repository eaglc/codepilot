package codingagent

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestClarificationSchemaMatchesRuntimeIDRules(t *testing.T) {
	definition := (&clarificationTool{}).Definition()
	schema := string(definition.InputSchema)
	for _, expected := range []string{`"questions"`, `"minItems":1`, `"maxItems":3`, `"pattern":"^[a-z0-9-]{1,64}$"`, `"selection_mode"`, `"single"`, `"multiple"`} {
		if !strings.Contains(schema, expected) {
			t.Fatalf("clarification schema does not contain %q: %s", expected, schema)
		}
	}
	prompt := clarificationFixture()
	prompt.Questions[0].ID = "invalid_id"
	if err := validateClarificationPrompt(prompt); err == nil || !strings.Contains(err.Error(), "lowercase letters, digits, or hyphens") {
		t.Fatalf("underscore id error = %v", err)
	}
}

func TestClarificationRecommendationMetadataIsNormalizedWithoutRejectingRequest(t *testing.T) {
	prompt := clarificationFixture()
	for index := range prompt.Questions[0].Options {
		prompt.Questions[0].Options[index].Recommended = false
	}
	prompt = normalizeClarificationPrompt(prompt)
	if err := validateClarificationPrompt(prompt); err != nil {
		t.Fatalf("request without recommendation = %v", err)
	}
	prompt.Questions[0].Options[0].Recommended = true
	prompt.Questions[0].Options[1].Recommended = true
	prompt = normalizeClarificationPrompt(prompt)
	if err := validateClarificationPrompt(prompt); err != nil {
		t.Fatalf("request with repeated recommendation = %v", err)
	}
	if !prompt.Questions[0].Options[0].Recommended || prompt.Questions[0].Options[1].Recommended {
		t.Fatalf("single-choice recommendations were not normalized: %#v", prompt.Questions[0].Options)
	}
}

func TestLegacyClarificationPayloadProjectsAndAcceptsCurrentAnswers(t *testing.T) {
	request := clarificationFixture().Questions[0]
	raw, err := json.Marshal(clarificationPayloadV1{
		Kind: "coding_plan_clarification_v1", Version: 1, Request: request,
	})
	if err != nil {
		t.Fatal(err)
	}
	pending := PendingInterrupt{}
	projectClarificationInterrupt(&pending, raw)
	if pending.Clarification == nil || len(pending.Clarification.Questions) != 1 || pending.Clarification.Questions[0].ID != request.ID {
		t.Fatalf("legacy clarification projection = %#v", pending.Clarification)
	}
	details, err := EncodeClarificationAnswer(request, ClarificationAnswer{QuestionID: request.ID, OptionID: "current"})
	if err != nil {
		t.Fatal(err)
	}
	answers, err := decodeClarificationAnswers(details)
	if err != nil {
		t.Fatal(err)
	}
	prompt, err := decodeClarificationPrompt(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateClarificationAnswers(prompt, answers); err != nil {
		t.Fatalf("current answer for legacy request = %v", err)
	}
}

func TestClarificationAnswersRequireOneOrderedAnswerPerQuestion(t *testing.T) {
	prompt := clarificationFixture()
	prompt.Questions = append(prompt.Questions, ClarificationRequest{
		ID: "scope", Header: "Scope", Question: "Which scope should apply?",
		Options: []ClarificationOption{
			{ID: "focused", Label: "Focused", Description: "Limit the initial scope.", Recommended: true},
			{ID: "broad", Label: "Broad", Description: "Cover more cases."},
		},
	})
	if _, err := EncodeClarificationAnswers(prompt, []ClarificationAnswer{{QuestionID: "compatibility", OptionID: "current"}}); err == nil {
		t.Fatal("incomplete answer set was accepted")
	}
	if _, err := EncodeClarificationAnswers(prompt, []ClarificationAnswer{
		{QuestionID: "scope", OptionID: "focused"},
		{QuestionID: "compatibility", OptionID: "current"},
	}); err == nil {
		t.Fatal("out-of-order answer set was accepted")
	}
}

func TestMultipleClarificationAcceptsCombinedPredefinedAndOtherChoices(t *testing.T) {
	prompt := clarificationFixture()
	prompt.Questions[0].SelectionMode = ClarificationSelectionMultiple
	details, err := EncodeClarificationAnswers(prompt, []ClarificationAnswer{{
		QuestionID: "compatibility", OptionIDs: []string{"current", ClarificationOtherOptionID}, OtherText: "Also support the preview channel",
	}})
	if err != nil {
		t.Fatal(err)
	}
	answers, err := decodeClarificationAnswers(details)
	if err != nil || len(answers) != 1 || len(answers[0].OptionIDs) != 2 {
		t.Fatalf("multiple answer = %#v, %v", answers, err)
	}
	prompt.Questions[0].SelectionMode = ClarificationSelectionSingle
	if _, err := EncodeClarificationAnswers(prompt, answers); err == nil {
		t.Fatal("single-choice question accepted multiple options")
	}
}

func clarificationFixture() ClarificationPrompt {
	return ClarificationPrompt{Questions: []ClarificationRequest{{
		ID: "compatibility", Header: "Compatibility", Question: "Which compatibility target should apply?",
		SelectionMode: ClarificationSelectionSingle,
		Options: []ClarificationOption{
			{ID: "current", Label: "Current version", Description: "Keep the implementation focused.", Recommended: true},
			{ID: "legacy", Label: "Legacy versions", Description: "Preserve older behavior."},
		},
	}}}
}
