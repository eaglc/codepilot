package prompt

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eaglc/codepilot/internal/codingagent"
)

func TestPlanEntryEvaluationSetReleaseCoverage(t *testing.T) {
	type evalCase struct {
		ID               string                          `json:"id"`
		Category         string                          `json:"category"`
		Request          string                          `json:"request"`
		ExpectSuggestion bool                            `json:"expect_suggestion"`
		ReasonCode       codingagent.PlanEntryReasonCode `json:"reason_code"`
	}
	var fixture struct {
		SchemaVersion     int `json:"schema_version"`
		ReleaseThresholds struct {
			MaxSimpleSuggestionRate float64 `json:"max_simple_unnecessary_suggestion_rate"`
			MaxHighRiskMissRate     float64 `json:"max_high_risk_missed_suggestion_rate"`
		} `json:"release_thresholds"`
		Cases []evalCase `json:"cases"`
	}
	raw, err := os.ReadFile(filepath.Join("testdata", "plan_entry_eval.golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.SchemaVersion != 1 || fixture.ReleaseThresholds.MaxSimpleSuggestionRate <= 0 || fixture.ReleaseThresholds.MaxSimpleSuggestionRate > 0.15 || fixture.ReleaseThresholds.MaxHighRiskMissRate != 0 {
		t.Fatalf("Plan entry release thresholds = %#v", fixture.ReleaseThresholds)
	}
	allowed := map[codingagent.PlanEntryReasonCode]bool{
		codingagent.PlanEntryMaterialAmbiguity: true, codingagent.PlanEntryCrossModuleChange: true,
		codingagent.PlanEntryOrderedDependencies: true, codingagent.PlanEntryArchitectureTradeoff: true,
		codingagent.PlanEntryMigrationCompatibility: true, codingagent.PlanEntrySecurityPermissions: true,
		codingagent.PlanEntryHighRollbackCost: true, codingagent.PlanEntryComplexValidation: true,
		codingagent.PlanEntryWorkflowCandidate: true, codingagent.PlanEntryComplexityEscalation: true,
	}
	seenIDs := make(map[string]bool, len(fixture.Cases))
	coveredReasons := make(map[codingagent.PlanEntryReasonCode]bool, len(allowed))
	simpleCases, highRiskCases := 0, 0
	for _, test := range fixture.Cases {
		if strings.TrimSpace(test.ID) == "" || seenIDs[test.ID] || strings.TrimSpace(test.Request) == "" {
			t.Fatalf("invalid or duplicate Plan entry evaluation case %#v", test)
		}
		seenIDs[test.ID] = true
		switch test.Category {
		case "simple":
			simpleCases++
			if test.ExpectSuggestion || test.ReasonCode != "" {
				t.Fatalf("simple evaluation case must remain Direct: %#v", test)
			}
		case "high_risk":
			highRiskCases++
			if !test.ExpectSuggestion || !allowed[test.ReasonCode] {
				t.Fatalf("high-risk evaluation case requires an allowed reason: %#v", test)
			}
			coveredReasons[test.ReasonCode] = true
		default:
			t.Fatalf("unsupported Plan entry evaluation category %q", test.Category)
		}
	}
	if simpleCases < 4 || highRiskCases < 8 || len(coveredReasons) != len(allowed) {
		t.Fatalf("Plan entry evaluation coverage: simple=%d high-risk=%d reasons=%d/%d", simpleCases, highRiskCases, len(coveredReasons), len(allowed))
	}
}
