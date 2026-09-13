package codingagent

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"
)

func (s *Service) assessPlanWorkspace(ctx context.Context, product Session, turn Turn, source WorkspaceDriftSource) (WorkspaceDrift, error) {
	if s.deps.Plans == nil || turn.PlanID == "" || turn.PlanVersion == 0 {
		return WorkspaceDrift{}, errors.New("assess Coding plan workspace: exact Plan revision is unavailable")
	}
	plan, err := s.deps.Plans.LoadPlan(ctx, turn.PlanID, turn.PlanVersion)
	if err != nil || plan.Digest != turn.PlanDigest {
		return WorkspaceDrift{}, errors.New("assess Coding plan workspace: exact Plan revision is unavailable or changed")
	}
	if !plan.WorkspaceRelevant {
		return WorkspaceDrift{Severity: WorkspaceDriftNone}, nil
	}
	worktree, err := s.deps.Worktrees.LoadWorktree(ctx, product.WorktreeID)
	if err != nil {
		return WorkspaceDrift{}, fmt.Errorf("assess Coding plan workspace: load worktree: %w", err)
	}
	current, err := capturePlanWorkspaceRevision(ctx, product.WorktreeID, worktree.Root, planRelevantPaths(planSubmissionFromPlan(plan)))
	if err != nil {
		return WorkspaceDrift{}, err
	}
	return classifyWorkspaceDrift(plan, current, source, time.Now().UTC())
}

func (s *Service) recordWorkspaceDrift(ctx context.Context, turn Turn, drift WorkspaceDrift) (Turn, error) {
	if drift.Severity == WorkspaceDriftNone {
		return turn, nil
	}
	if turn.WorkspaceDrift != nil && turn.WorkspaceDrift.Source == drift.Source && turn.WorkspaceDrift.CurrentDigest == drift.CurrentDigest && turn.WorkspaceDrift.PlanVersion == drift.PlanVersion && turn.WorkspaceDrift.PlanDigest == drift.PlanDigest {
		return turn, nil
	}
	expected := turn.Revision
	turn.WorkspaceDrift = &drift
	turn.WorkspaceDriftCount++
	turn.UpdatedAt = drift.DetectedAt
	turn.Revision++
	if err := s.deps.Turns.SaveTurn(ctx, turn, expected); err != nil {
		return turn, fmt.Errorf("record Coding plan workspace drift: %w", err)
	}
	return turn, nil
}

func resolvePlanReplan(turn *Turn, decision string, now time.Time) error {
	if turn == nil || turn.PlanReplan == nil || turn.PlanReplan.Decision != "" {
		return errors.New("resolve Coding plan replan: current request is unavailable or already resolved")
	}
	copy := *turn.PlanReplan
	copy.Decision = decision
	copy.ResolvedAt = now
	turn.PlanReplan = &copy
	return nil
}

func summarizePlanChanges(previous *Plan, current Plan) []string {
	if previous == nil {
		return []string{"Initial Plan version"}
	}
	var changes []string
	if previous.Goal != current.Goal {
		changes = append(changes, "Goal changed")
	}
	if !reflect.DeepEqual(previous.Scope, current.Scope) {
		changes = append(changes, "Scope changed")
	}
	if !reflect.DeepEqual(previous.Findings, current.Findings) || !reflect.DeepEqual(previous.Assumptions, current.Assumptions) {
		changes = append(changes, "Findings or assumptions changed")
	}
	if !reflect.DeepEqual(previous.Risks, current.Risks) {
		changes = append(changes, "Risks changed")
	}
	if !reflect.DeepEqual(previous.Steps, current.Steps) {
		changes = append(changes, "Implementation steps changed")
	}
	if !reflect.DeepEqual(previous.AcceptanceCriteria, current.AcceptanceCriteria) {
		changes = append(changes, "Acceptance criteria changed")
	}
	if previous.RecommendedStrategy != current.RecommendedStrategy || previous.CompletionMode != current.CompletionMode {
		changes = append(changes, "Execution recommendation changed")
	}
	if previous.WorkspaceRelevant != current.WorkspaceRelevant || previous.WorkspaceRevision.StatusDigest != current.WorkspaceRevision.StatusDigest || previous.WorkspaceRevision.DiffDigest != current.WorkspaceRevision.DiffDigest || !reflect.DeepEqual(previous.WorkspaceRevision.RelevantPaths, current.WorkspaceRevision.RelevantPaths) {
		changes = append(changes, "Workspace baseline refreshed")
	}
	if len(changes) == 0 {
		changes = append(changes, "Plan metadata refreshed")
	}
	return changes
}
