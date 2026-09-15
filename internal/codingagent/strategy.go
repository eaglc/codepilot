package codingagent

import (
	"fmt"

	"github.com/eaglc/codepilot/internal/workflow"
)

const (
	StrategyRecommendationVersion uint32 = 1
	StrategyEvaluationSetVersion         = "execution-strategy-eval-v1"
	MaxAdaptiveAgents                    = 4
)

// StrategyReasonCode is a stable, presentation-safe explanation produced by
// the trusted strategy policy. It never contains model reasoning.
type StrategyReasonCode string

const (
	StrategyReasonDirectDefault          StrategyReasonCode = "direct_default"
	StrategyReasonDeliverableOnly        StrategyReasonCode = "deliverable_only"
	StrategyReasonDependencyTracking     StrategyReasonCode = "dependency_tracking"
	StrategyReasonRoleIsolation          StrategyReasonCode = "role_isolation"
	StrategyReasonParallelReadBenefit    StrategyReasonCode = "parallel_read_benefit"
	StrategyReasonParallelWriteBenefit   StrategyReasonCode = "parallel_write_benefit"
	StrategyReasonUserPrefersSingle      StrategyReasonCode = "user_prefers_single"
	StrategyReasonWorkflowDisabled       StrategyReasonCode = "workflow_disabled"
	StrategyReasonSubagentsDisabled      StrategyReasonCode = "subagents_disabled"
	StrategyReasonParallelReadDisabled   StrategyReasonCode = "parallel_read_disabled"
	StrategyReasonParallelWriteDisabled  StrategyReasonCode = "parallel_write_disabled"
	StrategyReasonInsufficientSeparation StrategyReasonCode = "insufficient_separation"
	StrategyReasonResourceLimit          StrategyReasonCode = "resource_limit"
)

// StrategyRecommendation records the model proposal and the independently
// evaluated product recommendation. RecommendedStrategy on Plan is the approval
// default; the durable Turn records the user's actual execution choice.
type StrategyRecommendation struct {
	Version              uint32               `json:"version"`
	PolicyVersion        uint32               `json:"policy_version"`
	EvaluationSet        string               `json:"evaluation_set"`
	ProposedStrategy     ExecutionStrategy    `json:"proposed_strategy"`
	SelectedStrategy     ExecutionStrategy    `json:"selected_strategy"`
	ReasonCodes          []StrategyReasonCode `json:"reason_codes"`
	Summary              string               `json:"summary"`
	EstimatedAgents      int                  `json:"estimated_agents"`
	EstimatedConcurrency int                  `json:"estimated_concurrency"`
	IndependentStreams   int                  `json:"independent_streams"`
	Workstreams          []string             `json:"workstreams,omitempty"`
	AutoEligible         bool                 `json:"auto_eligible"`
}

// ExecutionPreferences are trusted product inputs. They can reduce resource
// use but never expand a Plan, permissions, or enabled capabilities.
type ExecutionPreferences struct {
	PreferSingleAgent bool
	MaxParallelAgents int
}

type strategyPolicy struct {
	Enabled       bool
	Workflows     bool
	Subagents     bool
	ParallelRead  bool
	ParallelWrite bool
	Preferences   ExecutionPreferences
}

func recommendExecutionStrategy(submission PlanSubmission, revision WorkspaceRevision, policy strategyPolicy) (ExecutionStrategy, StrategyRecommendation) {
	proposed := submission.RecommendedStrategy
	selected := proposed
	reason := primaryStrategyReason(proposed)
	streams, workstreams := independentPlanStreams(submission, proposed)

	if submission.CompletionMode == PlanCompletionDeliverable {
		selected, reason = ExecutionSingle, StrategyReasonDeliverableOnly
	} else if policy.Preferences.PreferSingleAgent {
		selected, reason = ExecutionSingle, StrategyReasonUserPrefersSingle
	} else if isWorkflowStrategy(selected) && !policy.Workflows {
		selected, reason = ExecutionSingle, StrategyReasonWorkflowDisabled
	} else if isMultiAgentStrategy(selected) && !policy.Subagents {
		selected, reason = fallbackWorkflowStrategy(policy), StrategyReasonSubagentsDisabled
	} else if policy.Enabled && isMultiAgentStrategy(selected) && estimatedMultiAgentCount(submission) > MaxAdaptiveAgents {
		selected, reason = lowOverheadWorkflowStrategy(submission, policy), StrategyReasonResourceLimit
	} else if selected == ExecutionWorkflowMultiParallelReadOnly && (!policy.ParallelRead || policy.Preferences.MaxParallelAgents < 2) {
		selected, reason = serialFallbackStrategy(submission, policy), StrategyReasonParallelReadDisabled
	} else if selected == ExecutionWorkflowMultiParallelIsolatedWrite && (!policy.ParallelWrite || policy.Preferences.MaxParallelAgents < 2) {
		selected, reason = serialFallbackStrategy(submission, policy), StrategyReasonParallelWriteDisabled
	} else if policy.Enabled && selected == ExecutionWorkflowSingle && len(submission.Steps) < 2 {
		selected, reason = ExecutionSingle, StrategyReasonInsufficientSeparation
	} else if policy.Enabled && selected == ExecutionWorkflowMultiSerial && !serialMultiAgentBenefit(submission) {
		selected, reason = lowOverheadWorkflowStrategy(submission, policy), StrategyReasonInsufficientSeparation
	} else if policy.Enabled && parallelExecutionStrategy(selected) && streams < 2 {
		selected, reason = serialFallbackStrategy(submission, policy), StrategyReasonInsufficientSeparation
	}

	if selected == ExecutionWorkflowMultiParallelIsolatedWrite && (revision.GitHead == "" || len(revision.ChangedPaths) != 0) {
		selected, reason = serialFallbackStrategy(submission, policy), StrategyReasonInsufficientSeparation
	}
	if !policy.Subagents && isMultiAgentStrategy(selected) {
		selected = fallbackWorkflowStrategy(policy)
	}
	if !policy.Workflows && isWorkflowStrategy(selected) {
		selected = ExecutionSingle
	}

	agents, concurrency := 1, 1
	if isMultiAgentStrategy(selected) {
		agents = estimatedMultiAgentCount(submission)
	}
	if !parallelExecutionStrategy(selected) {
		streams = 1
		workstreams = nil
	} else {
		concurrency = min(2, streams)
		if policy.Preferences.MaxParallelAgents > 0 {
			concurrency = min(concurrency, policy.Preferences.MaxParallelAgents)
		}
		if len(workstreams) > agents {
			workstreams = workstreams[:agents]
		}
	}
	recommendation := StrategyRecommendation{
		Version: StrategyRecommendationVersion, PolicyVersion: 1, EvaluationSet: StrategyEvaluationSetVersion,
		ProposedStrategy: proposed, SelectedStrategy: selected, ReasonCodes: []StrategyReasonCode{reason}, Summary: strategyRecommendationSummary(selected, reason),
		EstimatedAgents: agents, EstimatedConcurrency: concurrency, IndependentStreams: streams, Workstreams: workstreams,
		AutoEligible: policy.Enabled && selected == proposed && calibratedStrategy(selected, streams) && (selected != ExecutionWorkflowMultiSerial || serialMultiAgentBenefit(submission)),
	}
	return selected, recommendation
}

func estimatedMultiAgentCount(submission PlanSubmission) int {
	// Multi-Agent compilation delegates every submitted step and adds bounded
	// final Validate and Review children. Integrate nodes remain on the parent.
	return len(submission.Steps) + 2
}

func fallbackWorkflowStrategy(policy strategyPolicy) ExecutionStrategy {
	if policy.Workflows {
		return ExecutionWorkflowSingle
	}
	return ExecutionSingle
}

func lowOverheadWorkflowStrategy(submission PlanSubmission, policy strategyPolicy) ExecutionStrategy {
	if policy.Workflows && len(submission.Steps) >= 2 {
		return ExecutionWorkflowSingle
	}
	return ExecutionSingle
}

func serialFallbackStrategy(submission PlanSubmission, policy strategyPolicy) ExecutionStrategy {
	if policy.Subagents && serialMultiAgentBenefit(submission) {
		return ExecutionWorkflowMultiSerial
	}
	return lowOverheadWorkflowStrategy(submission, policy)
}

func serialMultiAgentBenefit(submission PlanSubmission) bool {
	if len(submission.Steps) < 2 {
		return false
	}
	roles := make(map[workflow.Role]bool)
	for _, step := range submission.Steps {
		role := step.Role
		if role == "" {
			role = workflow.RoleImplement
		}
		roles[role] = true
	}
	if len(roles) >= 2 {
		return true
	}
	for left := 0; left < len(submission.Steps); left++ {
		for right := left + 1; right < len(submission.Steps); right++ {
			if len(submission.Steps[left].Files) != 0 && len(submission.Steps[right].Files) != 0 &&
				planStepsCanRunTogether(submission.Steps[left], submission.Steps[right], ExecutionWorkflowMultiParallelIsolatedWrite) {
				return true
			}
		}
	}
	return false
}

func isMultiAgentStrategy(value ExecutionStrategy) bool {
	return value == ExecutionWorkflowMultiSerial || parallelExecutionStrategy(value)
}

func primaryStrategyReason(value ExecutionStrategy) StrategyReasonCode {
	switch value {
	case ExecutionWorkflowSingle:
		return StrategyReasonDependencyTracking
	case ExecutionWorkflowMultiSerial:
		return StrategyReasonRoleIsolation
	case ExecutionWorkflowMultiParallelReadOnly:
		return StrategyReasonParallelReadBenefit
	case ExecutionWorkflowMultiParallelIsolatedWrite:
		return StrategyReasonParallelWriteBenefit
	default:
		return StrategyReasonDirectDefault
	}
}

func calibratedStrategy(value ExecutionStrategy, streams int) bool {
	if value == ExecutionSingle || value == ExecutionWorkflowSingle || value == ExecutionWorkflowMultiSerial {
		return true
	}
	return streams >= 2
}

func strategyRecommendationSummary(selected ExecutionStrategy, reason StrategyReasonCode) string {
	switch reason {
	case StrategyReasonDeliverableOnly:
		return "The Plan itself is the deliverable, so no execution Workflow is needed."
	case StrategyReasonUserPrefersSingle:
		return "Single-Agent execution is recommended because the user prefers the lowest orchestration and resource overhead."
	case StrategyReasonWorkflowDisabled:
		return "Single-Agent execution is recommended because new Workflow creation is disabled."
	case StrategyReasonSubagentsDisabled:
		return "A single-Agent path is recommended because new child Agent delegation is disabled."
	case StrategyReasonParallelReadDisabled:
		return "Serial execution is recommended because parallel read-only Agents are disabled or capped below two."
	case StrategyReasonParallelWriteDisabled:
		return "Serial execution is recommended because isolated parallel-write Agents are disabled or capped below two."
	case StrategyReasonInsufficientSeparation:
		return "Serial execution is recommended because the approved steps do not provide two safely independent workstreams."
	case StrategyReasonResourceLimit:
		return "A single-Agent Workflow is recommended because the proposed delegation would exceed the calibrated four-Agent resource envelope."
	case StrategyReasonDependencyTracking:
		return "A single-Agent Workflow is recommended for durable dependency tracking and recovery."
	case StrategyReasonRoleIsolation:
		return "A serial multi-Agent Workflow is recommended for bounded role and context isolation."
	case StrategyReasonParallelReadBenefit:
		return "Parallel read-only Agents are recommended because independent read-only workstreams can reduce elapsed time without write conflicts."
	case StrategyReasonParallelWriteBenefit:
		return "Isolated parallel-write Agents are recommended because independent file scopes can be changed concurrently and integrated under exact approval."
	default:
		return fmt.Sprintf("%s is recommended as the lowest-overhead execution strategy for this Plan.", selected)
	}
}

func independentPlanStreams(submission PlanSubmission, strategy ExecutionStrategy) (int, []string) {
	if !parallelExecutionStrategy(strategy) {
		return 1, nil
	}
	dependencies := make(map[string][]string, len(submission.Steps))
	for _, step := range submission.Steps {
		dependencies[step.ID] = append([]string(nil), step.DependsOn...)
	}
	var candidates []PlanStep
	for _, step := range submission.Steps {
		role := step.Role
		if role == "" {
			role = workflow.RoleImplement
		}
		if strategy == ExecutionWorkflowMultiParallelReadOnly && role != workflow.RoleExplore && role != workflow.RoleValidate && role != workflow.RoleReview {
			continue
		}
		if strategy == ExecutionWorkflowMultiParallelIsolatedWrite && role != workflow.RoleImplement {
			continue
		}
		candidates = append(candidates, step)
	}
	selected := make([]PlanStep, 0, len(candidates))
	for _, candidate := range candidates {
		compatible := true
		for _, existing := range selected {
			if planStepDependsOn(candidate.ID, existing.ID, dependencies, nil) || planStepDependsOn(existing.ID, candidate.ID, dependencies, nil) || !planStepsCanRunTogether(candidate, existing, strategy) {
				compatible = false
				break
			}
		}
		if compatible {
			selected = append(selected, candidate)
		}
	}
	workstreams := make([]string, len(selected))
	for index := range selected {
		workstreams[index] = selected[index].ID + ": " + selected[index].Goal
	}
	return len(selected), workstreams
}

func planStepDependsOn(id, target string, dependencies map[string][]string, seen map[string]bool) bool {
	if seen == nil {
		seen = make(map[string]bool)
	}
	if seen[id] {
		return false
	}
	seen[id] = true
	for _, dependency := range dependencies[id] {
		if dependency == target || planStepDependsOn(dependency, target, dependencies, seen) {
			return true
		}
	}
	return false
}

func planStepsCanRunTogether(left, right PlanStep, strategy ExecutionStrategy) bool {
	if strategy == ExecutionWorkflowMultiParallelReadOnly {
		return true
	}
	leftNode := workflow.Node{Executor: workflow.ExecutorChild, Isolated: true, Scope: workflow.Scope{ReadPaths: left.Files, WritePaths: left.Files}}
	rightNode := workflow.Node{Executor: workflow.ExecutorChild, Isolated: true, Scope: workflow.Scope{ReadPaths: right.Files, WritePaths: right.Files}}
	return workflow.NodesConcurrent(leftNode, rightNode)
}

func validateStrategyRecommendation(value StrategyRecommendation, selected ExecutionStrategy) error {
	if value.Version == 0 {
		if value.PolicyVersion == 0 && value.EvaluationSet == "" && value.ProposedStrategy == "" && value.SelectedStrategy == "" && len(value.ReasonCodes) == 0 && value.Summary == "" && value.EstimatedAgents == 0 && value.EstimatedConcurrency == 0 && value.IndependentStreams == 0 && len(value.Workstreams) == 0 && !value.AutoEligible {
			return nil // Compatibility with Plans persisted before P8.
		}
		return fmt.Errorf("Coding plan strategy recommendation is invalid")
	}
	if value.Version != StrategyRecommendationVersion || value.PolicyVersion == 0 || value.EvaluationSet != StrategyEvaluationSetVersion || !validExecutionStrategy(value.ProposedStrategy) || value.SelectedStrategy != selected || !validExecutionStrategy(selected) {
		return fmt.Errorf("Coding plan strategy recommendation is invalid")
	}
	if len(value.ReasonCodes) != 1 || value.Summary == "" || value.EstimatedAgents < 1 || value.EstimatedAgents > maxPlanSteps+2 || value.EstimatedConcurrency < 1 || value.EstimatedConcurrency > value.EstimatedAgents || value.IndependentStreams < 1 || value.IndependentStreams > workflow.MaxNodes {
		return fmt.Errorf("Coding plan strategy recommendation bounds are invalid")
	}
	if err := validatePlanText("strategy recommendation summary", value.Summary, true); err != nil {
		return err
	}
	if !validStrategyReason(value.ReasonCodes[0]) || parallelExecutionStrategy(selected) && value.IndependentStreams < 2 || len(value.Workstreams) > value.IndependentStreams || value.AutoEligible && value.EstimatedAgents > MaxAdaptiveAgents {
		return fmt.Errorf("Coding plan strategy recommendation evidence is invalid")
	}
	seen := make(map[string]bool, len(value.Workstreams))
	for _, item := range value.Workstreams {
		if validatePlanText("strategy workstream", item, true) != nil || seen[item] {
			return fmt.Errorf("Coding plan strategy workstreams are invalid")
		}
		seen[item] = true
	}
	return nil
}

func validStrategyReason(value StrategyReasonCode) bool {
	switch value {
	case StrategyReasonDirectDefault, StrategyReasonDeliverableOnly, StrategyReasonDependencyTracking, StrategyReasonRoleIsolation,
		StrategyReasonParallelReadBenefit, StrategyReasonParallelWriteBenefit, StrategyReasonUserPrefersSingle,
		StrategyReasonWorkflowDisabled, StrategyReasonSubagentsDisabled, StrategyReasonParallelReadDisabled,
		StrategyReasonParallelWriteDisabled, StrategyReasonInsufficientSeparation, StrategyReasonResourceLimit:
		return true
	default:
		return false
	}
}
