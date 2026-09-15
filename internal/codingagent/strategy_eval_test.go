package codingagent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	agentsession "github.com/eaglc/codepilot/internal/agent/session"
	"github.com/eaglc/codepilot/internal/llm"
	"github.com/eaglc/codepilot/internal/tool"
	"github.com/eaglc/codepilot/internal/workflow"
)

func TestExecutionStrategyEvaluationSet(t *testing.T) {
	type evalCase struct {
		ID                string             `json:"id"`
		Benefit           string             `json:"benefit"`
		Proposed          ExecutionStrategy  `json:"proposed_strategy"`
		Steps             []PlanStep         `json:"steps"`
		GitHead           string             `json:"git_head"`
		ChangedPaths      []string           `json:"changed_paths"`
		ParallelWrite     *bool              `json:"parallel_write"`
		PreferSingle      bool               `json:"prefer_single"`
		Expected          ExecutionStrategy  `json:"expected_strategy"`
		ExpectedReason    StrategyReasonCode `json:"expected_reason"`
		MinimumStreams    int                `json:"minimum_streams"`
		MaxParallelAgents int                `json:"max_parallel_agents"`
	}
	var fixture struct {
		SchemaVersion int `json:"schema_version"`
		PolicyVersion int `json:"policy_version"`
		Thresholds    struct {
			MaxSimpleMultiAgentRate  float64 `json:"max_simple_multi_agent_rate"`
			MaxUnsafeParallelRate    float64 `json:"max_unsafe_parallel_rate"`
			MaxParallelAgents        int     `json:"max_parallel_agents"`
			MaxResourceAmplification int     `json:"max_resource_amplification"`
		} `json:"release_thresholds"`
		Cases []evalCase `json:"cases"`
	}
	raw, err := os.ReadFile(filepath.Join("testdata", "execution_strategy_eval.golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.SchemaVersion != 1 || fixture.PolicyVersion != 1 || fixture.Thresholds.MaxSimpleMultiAgentRate != 0 || fixture.Thresholds.MaxUnsafeParallelRate != 0 || fixture.Thresholds.MaxParallelAgents != 4 || fixture.Thresholds.MaxResourceAmplification > 4 {
		t.Fatalf("execution strategy release thresholds = %#v", fixture.Thresholds)
	}
	if len(fixture.Cases) < 10 {
		t.Fatalf("execution strategy evaluation coverage = %d cases", len(fixture.Cases))
	}
	seen := make(map[string]bool, len(fixture.Cases))
	simpleCases, simpleMultiAgent := 0, 0
	unsafeParallelCases, unsafeParallel := 0, 0
	for _, test := range fixture.Cases {
		t.Run(test.ID, func(t *testing.T) {
			if test.ID == "" || seen[test.ID] || test.Benefit == "" || !validExecutionStrategy(test.Proposed) || !validExecutionStrategy(test.Expected) {
				t.Fatalf("invalid or duplicate evaluation case: %#v", test)
			}
			seen[test.ID] = true
			parallelWrite := true
			if test.ParallelWrite != nil {
				parallelWrite = *test.ParallelWrite
			}
			maxParallel := test.MaxParallelAgents
			if maxParallel == 0 {
				maxParallel = fixture.Thresholds.MaxParallelAgents
			}
			policy := strategyPolicy{
				Enabled: true, Workflows: true, Subagents: true, ParallelRead: true, ParallelWrite: parallelWrite,
				Preferences: ExecutionPreferences{PreferSingleAgent: test.PreferSingle, MaxParallelAgents: maxParallel},
			}
			submission := PlanSubmission{RecommendedStrategy: test.Proposed, CompletionMode: PlanCompletionExecute, Steps: test.Steps}
			selected, recommendation := recommendExecutionStrategy(submission, WorkspaceRevision{GitHead: test.GitHead, ChangedPaths: test.ChangedPaths}, policy)
			if selected != test.Expected || recommendation.SelectedStrategy != selected || len(recommendation.ReasonCodes) != 1 || recommendation.ReasonCodes[0] != test.ExpectedReason || recommendation.IndependentStreams < max(1, test.MinimumStreams) {
				t.Fatalf("recommendation = %q %#v", selected, recommendation)
			}
			if err := validateStrategyRecommendation(recommendation, selected); err != nil {
				t.Fatal(err)
			}
			if recommendation.EstimatedAgents > fixture.Thresholds.MaxResourceAmplification || recommendation.EstimatedConcurrency > min(maxParallel, fixture.Thresholds.MaxParallelAgents) {
				t.Fatalf("estimated resources = %d Agents / %d concurrent", recommendation.EstimatedAgents, recommendation.EstimatedConcurrency)
			}
			if test.Benefit == "lowest_overhead" {
				simpleCases++
				if isMultiAgentStrategy(selected) {
					simpleMultiAgent++
				}
			}
			if parallelExecutionStrategy(test.Proposed) && !parallelExecutionStrategy(test.Expected) {
				unsafeParallelCases++
				if parallelExecutionStrategy(selected) {
					unsafeParallel++
				}
			}
		})
	}
	if simpleCases == 0 || float64(simpleMultiAgent)/float64(simpleCases) > fixture.Thresholds.MaxSimpleMultiAgentRate {
		t.Fatalf("simple-task multi-Agent recommendation rate = %d/%d", simpleMultiAgent, simpleCases)
	}
	if unsafeParallelCases == 0 || float64(unsafeParallel)/float64(unsafeParallelCases) > fixture.Thresholds.MaxUnsafeParallelRate {
		t.Fatalf("unsafe parallel recommendation rate = %d/%d", unsafeParallel, unsafeParallelCases)
	}
}

func TestExecutionStrategyUsesTrustedConflictMatrixAndPreferences(t *testing.T) {
	submission := PlanSubmission{
		RecommendedStrategy: ExecutionWorkflowMultiParallelIsolatedWrite,
		CompletionMode:      PlanCompletionExecute,
		Steps: []PlanStep{
			{ID: "one", Goal: "Change one subtree.", Files: []string{"internal/API"}, Role: workflow.RoleImplement},
			{ID: "two", Goal: "Change an overlapping subtree.", Files: []string{"internal/api/handler.go"}, Role: workflow.RoleImplement},
		},
	}
	selected, recommendation := recommendExecutionStrategy(submission, WorkspaceRevision{GitHead: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, strategyPolicy{
		Enabled: true, Workflows: true, Subagents: true, ParallelRead: true, ParallelWrite: true,
		Preferences: ExecutionPreferences{MaxParallelAgents: 2},
	})
	want := ExecutionWorkflowMultiParallelIsolatedWrite
	if runtime.GOOS == "windows" {
		want = ExecutionWorkflowSingle
	}
	if selected != want || recommendation.AutoEligible != (want == ExecutionWorkflowMultiParallelIsolatedWrite) {
		t.Fatalf("platform path recommendation = %q %#v; want %q", selected, recommendation, want)
	}
}

func TestExitPlanModeAppliesTrustedSingleAgentPreference(t *testing.T) {
	root := initializePlanWorkspaceGit(t)
	now := time.Now().UTC()
	repository := &strategyTestRepository{turn: Turn{
		ID: "turn-p8", SessionID: "session-p8", RequestText: "compare two areas", Phase: TurnPhasePlanning, Status: TurnRunning,
		Strategy: ExecutionSingle, Revision: 1, CreatedAt: now, UpdatedAt: now,
		Runs: []RunBinding{{RunID: "run-p8", Phase: TurnPhasePlanning, Profile: CapabilityPlanWorkspace, Status: RunBindingRunning, StartedAt: now}},
	}}
	submission := PlanSubmission{
		Goal: "Inspect two independent areas.", Scope: PlanScope{Included: []string{"planned.txt", "unrelated.txt"}},
		Findings: []string{"Both files exist."}, Risks: []string{"Parallel work uses additional resources."},
		Steps: []PlanStep{
			{ID: "planned", Goal: "Inspect the planned file.", Files: []string{"planned.txt"}, Validation: []string{"Report evidence."}, Role: workflow.RoleExplore},
			{ID: "unrelated", Goal: "Inspect the unrelated file.", Files: []string{"unrelated.txt"}, Validation: []string{"Report evidence."}, Role: workflow.RoleExplore},
		},
		AcceptanceCriteria: []string{"Both areas are covered."}, RecommendedStrategy: ExecutionWorkflowMultiParallelReadOnly,
		WorkspaceRelevant: true, CompletionMode: PlanCompletionExecute,
	}
	arguments, _ := json.Marshal(submission)
	exit := &exitPlanModeTool{
		plans: repository, turns: repository, turnID: repository.turn.ID, worktreeID: "worktree-p8", worktreeRoot: root,
		strategyPolicy: strategyPolicy{Enabled: true, Workflows: true, Subagents: true, ParallelRead: true, ParallelWrite: true, Preferences: ExecutionPreferences{PreferSingleAgent: true, MaxParallelAgents: 2}},
	}
	result, err := exit.Execute(context.Background(), tool.Call{ID: "submit-p8", Name: exitPlanModeToolName, Arguments: arguments}, nil)
	if err != nil || result.Status != tool.ResultInterrupted || repository.plan.StrategyRecommendation == nil {
		t.Fatalf("P8 Plan submission = %#v, plan=%#v, err=%v", result, repository.plan, err)
	}
	if repository.plan.RecommendedStrategy != ExecutionSingle || repository.plan.StrategyRecommendation.ProposedStrategy != ExecutionWorkflowMultiParallelReadOnly || repository.plan.StrategyRecommendation.ReasonCodes[0] != StrategyReasonUserPrefersSingle {
		t.Fatalf("trusted Plan recommendation = %#v", repository.plan.StrategyRecommendation)
	}
	if err := ValidatePlan(repository.plan); err != nil {
		t.Fatal(err)
	}
}

func TestDisabledAdaptiveStrategyPreservesSafeProposalWithoutAutoEligibility(t *testing.T) {
	submission := PlanSubmission{
		RecommendedStrategy: ExecutionWorkflowMultiParallelReadOnly, CompletionMode: PlanCompletionExecute,
		Steps: []PlanStep{
			{ID: "first", Goal: "Inspect first.", Role: workflow.RoleExplore},
			{ID: "second", Goal: "Inspect after first.", DependsOn: []string{"first"}, Role: workflow.RoleReview},
			{ID: "third", Goal: "Inspect third.", Role: workflow.RoleExplore},
			{ID: "fourth", Goal: "Inspect fourth.", Role: workflow.RoleExplore},
			{ID: "fifth", Goal: "Inspect fifth.", Role: workflow.RoleExplore},
		},
	}
	selected, recommendation := recommendExecutionStrategy(submission, WorkspaceRevision{}, strategyPolicy{
		Enabled: false, Workflows: true, Subagents: true, ParallelRead: true, ParallelWrite: true,
		Preferences: ExecutionPreferences{MaxParallelAgents: 2},
	})
	if selected != submission.RecommendedStrategy || recommendation.AutoEligible {
		t.Fatalf("disabled adaptive recommendation = %q %#v", selected, recommendation)
	}
	if err := validateStrategyRecommendation(recommendation, selected); err != nil {
		t.Fatalf("disabled adaptive recommendation should remain persistable: %v", err)
	}
}

func TestStrategyOutcomeMetricsCompareQualityTimeAndResources(t *testing.T) {
	now := time.Now().UTC()
	turns := []Turn{
		{ID: "single", Strategy: ExecutionSingle, Status: TurnCompleted, CreatedAt: now, CompletedAt: now.Add(time.Second), Runs: []RunBinding{{RunID: "run-single", Status: RunBindingCompleted, Steps: 2}}},
		{ID: "multi", Strategy: ExecutionWorkflowMultiSerial, Status: TurnFailed, CreatedAt: now, CompletedAt: now.Add(2 * time.Second), PlanReplanCount: 1, Runs: []RunBinding{{RunID: "run-multi", NodeID: "node", Status: RunBindingFailed, Steps: 3}}},
	}
	durable := agentsession.Snapshot{Records: []agentsession.Record{
		{RunID: "run-single", Type: agentsession.RecordUsage, Usage: &llm.Usage{TotalTokens: 100, Cost: 0.1}},
		{RunID: "run-multi", Type: agentsession.RecordUsage, Usage: &llm.Usage{TotalTokens: 200, Cost: 0.2}},
	}}
	outcomes := projectStrategyOutcomeMetrics(durable, turns)
	if len(outcomes) != 2 || outcomes[0].Strategy != ExecutionSingle || outcomes[0].CompletedTurns != 1 || outcomes[0].Elapsed != time.Second || outcomes[0].TotalTokens != 100 || outcomes[1].Strategy != ExecutionWorkflowMultiSerial || outcomes[1].FailedTurns != 1 || outcomes[1].FailedRuns != 1 || outcomes[1].Replans != 1 || outcomes[1].TotalTokens != 200 {
		t.Fatalf("strategy outcomes = %#v", outcomes)
	}
	metrics := StrategyMetrics{Outcomes: outcomes}
	addStrategyOutcomeUsage(&metrics, ExecutionWorkflowMultiSerial, 50, 0.05)
	if metrics.Outcomes[1].TotalTokens != 250 || metrics.Outcomes[1].Cost != 0.25 {
		t.Fatalf("strategy child usage = %#v", metrics.Outcomes[1])
	}
}

func TestPlanRejectsTamperedP8Recommendation(t *testing.T) {
	plan := validTestPlan(t)
	recommendation := StrategyRecommendation{
		Version: StrategyRecommendationVersion, PolicyVersion: 1, EvaluationSet: StrategyEvaluationSetVersion,
		ProposedStrategy: ExecutionSingle, SelectedStrategy: ExecutionWorkflowSingle,
		ReasonCodes: []StrategyReasonCode{StrategyReasonDirectDefault}, Summary: "Tampered.", EstimatedAgents: 1, EstimatedConcurrency: 1, IndependentStreams: 1,
	}
	plan.StrategyRecommendation = &recommendation
	plan.Digest, _ = ComputePlanDigest(plan)
	if err := ValidatePlan(plan); err == nil {
		t.Fatal("Plan accepted a recommendation whose selected strategy did not match the authoritative Plan strategy")
	}
}

func TestLegacyPlanDigestRemainsValidWithoutP8Recommendation(t *testing.T) {
	plan := validTestPlan(t)
	plan.StrategyRecommendation = nil
	plan.Digest, _ = ComputePlanDigest(plan)
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	var reopened Plan
	if err := json.Unmarshal(raw, &reopened); err != nil {
		t.Fatal(err)
	}
	if reopened.StrategyRecommendation != nil {
		t.Fatal("legacy Plan unexpectedly gained a P8 recommendation")
	}
	if err := ValidatePlan(reopened); err != nil {
		t.Fatal(err)
	}
}

type strategyTestRepository struct {
	turn Turn
	plan Plan
}

func (r *strategyTestRepository) CreateTurn(context.Context, Turn) error         { return nil }
func (r *strategyTestRepository) LoadTurn(context.Context, TurnID) (Turn, error) { return r.turn, nil }
func (r *strategyTestRepository) ListTurns(context.Context, SessionID) ([]Turn, error) {
	return []Turn{r.turn}, nil
}
func (r *strategyTestRepository) SaveTurn(_ context.Context, value Turn, expected uint64) error {
	if expected != r.turn.Revision {
		return ErrTurnConflict
	}
	r.turn = value
	return nil
}
func (r *strategyTestRepository) CreatePlanVersion(_ context.Context, value Plan) error {
	r.plan = value
	return nil
}
func (r *strategyTestRepository) LoadPlan(context.Context, PlanID, uint64) (Plan, error) {
	return r.plan, nil
}
func (r *strategyTestRepository) ListPlanVersions(context.Context, PlanID) ([]Plan, error) {
	if r.plan.ID == "" {
		return nil, nil
	}
	return []Plan{r.plan}, nil
}
