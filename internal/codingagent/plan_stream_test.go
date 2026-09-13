package codingagent

import (
	"strings"
	"testing"
)

func TestPlanToolCallPreviewStreamsCompletedStructuredFields(t *testing.T) {
	previewer := newPlanToolCallPreviewer()
	if text, changed := previewer.PreviewToolCall("other", "request_user_input", `{"questions":`); changed || text != "" {
		t.Fatalf("unrelated Tool produced preview: %q, %t", text, changed)
	}
	if text, changed := previewer.PreviewToolCall("plan", exitPlanModeToolName, `{"goal":"Plan a Beijing `); changed || text != "" {
		t.Fatalf("incomplete string produced preview: %q, %t", text, changed)
	}
	text, changed := previewer.PreviewToolCall("plan", "", `trip","scope":{"included":["Transport","Hotels"]},"steps":[{"id":"day-1","goal":"Visit the Great Wall","validation":["Route is feasible"]}`)
	if !changed {
		t.Fatal("completed Plan fields did not update preview")
	}
	for _, expected := range []string{"Plan (drafting", "Plan a Beijing trip", "Transport", "Hotels", "Visit the Great Wall", "Route is feasible"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("preview does not contain %q: %s", expected, text)
		}
	}
	final, changed := previewer.PreviewToolCall("plan", "", `],"risks":["Weather"],"acceptance_criteria":["Three-day itinerary"]}`)
	if !changed || !strings.Contains(final, "Weather") || !strings.Contains(final, "Three-day itinerary") {
		t.Fatalf("final preview = %q, changed=%t", final, changed)
	}
	if repeated, changed := previewer.PreviewToolCall("plan", "", ""); changed || repeated != "" {
		t.Fatalf("empty delta repeated preview: %q, %t", repeated, changed)
	}
}
