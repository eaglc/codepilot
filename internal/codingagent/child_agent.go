package codingagent

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	agentsession "github.com/eaglc/codepilot/internal/agent/session"
	"github.com/eaglc/codepilot/internal/codingagent/roleprofile"
	"github.com/eaglc/codepilot/internal/workflow"
)

const (
	maxChildTaskTextBytes = 16 << 10
	maxChildTaskItems     = 64
)

// ChildAgentID identifies one durable, parent-owned delegated execution.
type ChildAgentID string

// ChildAgentKind distinguishes Plan exploration from an execution Workflow node.
type ChildAgentKind string

const (
	ChildAgentPlanExplore  ChildAgentKind = "plan_explore"
	ChildAgentWorkflowNode ChildAgentKind = "workflow_node"
)

// ChildAgentStatus is the durable lifecycle of one independent Agent session.
type ChildAgentStatus string

const (
	ChildAgentCreating         ChildAgentStatus = "creating"
	ChildAgentReady            ChildAgentStatus = "ready"
	ChildAgentRunning          ChildAgentStatus = "running"
	ChildAgentAwaitingApproval ChildAgentStatus = "awaiting_approval"
	ChildAgentCompleted        ChildAgentStatus = "completed"
	ChildAgentFailed           ChildAgentStatus = "failed"
	ChildAgentCancelled        ChildAgentStatus = "cancelled"
)

// AgentTaskEvidence is a bounded dependency or result fact. It never carries a
// child transcript or grants capabilities.
type AgentTaskEvidence struct {
	SourceID     string   `json:"source_id"`
	Summary      string   `json:"summary"`
	ArtifactRefs []string `json:"artifact_refs,omitempty"`
}

// AgentTask is the complete capability-bounded input for a child Agent.
type AgentTask struct {
	Goal               string              `json:"goal"`
	ReadPaths          []string            `json:"read_paths,omitempty"`
	WritePaths         []string            `json:"write_paths,omitempty"`
	DependencyEvidence []AgentTaskEvidence `json:"dependency_evidence,omitempty"`
	AcceptanceCriteria []string            `json:"acceptance_criteria"`
}

// AgentTaskResultStatus is the child-reported node outcome. Trusted Coding
// coordination still validates it before advancing the Workflow.
type AgentTaskResultStatus string

const (
	AgentTaskSucceeded AgentTaskResultStatus = "succeeded"
	AgentTaskBlocked   AgentTaskResultStatus = "blocked"
	AgentTaskFailed    AgentTaskResultStatus = "failed"
)

// AgentTaskResult is the only structured output accepted from a child Agent.
type AgentTaskResult struct {
	Status       AgentTaskResultStatus `json:"status"`
	Conclusion   string                `json:"conclusion"`
	Changes      []string              `json:"changes,omitempty"`
	Evidence     []AgentTaskEvidence   `json:"evidence"`
	Validation   []string              `json:"validation,omitempty"`
	ArtifactRefs []string              `json:"artifact_refs,omitempty"`
	Unresolved   []string              `json:"unresolved,omitempty"`
	SubmittedAt  time.Time             `json:"submitted_at"`
}

// ChildAgent is the durable product binding between a parent task and one
// independent generic Agent session/Run.
type ChildAgent struct {
	ID              ChildAgentID       `json:"id"`
	Kind            ChildAgentKind     `json:"kind"`
	ParentSessionID SessionID          `json:"parent_session_id"`
	ParentTurnID    TurnID             `json:"parent_turn_id"`
	WorkflowID      string             `json:"workflow_id,omitempty"`
	NodeID          NodeID             `json:"node_id,omitempty"`
	PlanID          PlanID             `json:"plan_id,omitempty"`
	PlanVersion     uint64             `json:"plan_version,omitempty"`
	PlanDigest      string             `json:"plan_digest,omitempty"`
	Role            workflow.Role      `json:"role"`
	Profile         CapabilityProfile  `json:"profile"`
	PolicyVersion   uint32             `json:"policy_version,omitempty"`
	Task            AgentTask          `json:"task"`
	AgentSessionID  agentsession.ID    `json:"agent_session_id"`
	RunID           agentsession.RunID `json:"run_id"`
	Attempt         int                `json:"attempt"`
	Status          ChildAgentStatus   `json:"status"`
	Result          *AgentTaskResult   `json:"result,omitempty"`
	ManagedWorktree *ManagedWorktree   `json:"managed_worktree,omitempty"`
	Failure         string             `json:"failure,omitempty"`
	Revision        uint64             `json:"revision"`
	CreatedAt       time.Time          `json:"created_at"`
	UpdatedAt       time.Time          `json:"updated_at"`
	StartedAt       time.Time          `json:"started_at,omitempty"`
	CompletedAt     time.Time          `json:"completed_at,omitempty"`
}

// WorkflowChildAgentID returns an attempt-stable identity, preventing duplicate
// child creation across coordinator crashes.
func WorkflowChildAgentID(workflowID string, nodeID NodeID, attempt int) ChildAgentID {
	return childAgentIdentity("workflow", workflowID, string(nodeID), fmt.Sprint(attempt))
}

// PlanExploreChildAgentID returns a tool-call-stable Plan exploration identity.
func PlanExploreChildAgentID(turnID TurnID, callID string) ChildAgentID {
	return childAgentIdentity("plan", string(turnID), callID)
}

func childAgentIdentity(parts ...string) ChildAgentID {
	digest := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return ChildAgentID("child_" + hex.EncodeToString(digest[:16]))
}

// ChildAgentSessionID derives the independent generic Agent session identity.
func ChildAgentSessionID(id ChildAgentID) agentsession.ID {
	return agentsession.ID("agent_" + string(id))
}

// ChildAgentRunID derives the one active Run identity for one delegated attempt.
func ChildAgentRunID(id ChildAgentID) agentsession.RunID {
	return agentsession.RunID("run_" + string(id))
}

// CloneChildAgent returns a defensive copy for repository boundaries.
func CloneChildAgent(value ChildAgent) ChildAgent {
	clone := value
	clone.Task.ReadPaths = append([]string(nil), value.Task.ReadPaths...)
	clone.Task.WritePaths = append([]string(nil), value.Task.WritePaths...)
	clone.Task.AcceptanceCriteria = append([]string(nil), value.Task.AcceptanceCriteria...)
	clone.Task.DependencyEvidence = cloneTaskEvidence(value.Task.DependencyEvidence)
	if value.Result != nil {
		result := *value.Result
		result.Changes = append([]string(nil), value.Result.Changes...)
		result.Evidence = cloneTaskEvidence(value.Result.Evidence)
		result.Validation = append([]string(nil), value.Result.Validation...)
		result.ArtifactRefs = append([]string(nil), value.Result.ArtifactRefs...)
		result.Unresolved = append([]string(nil), value.Result.Unresolved...)
		clone.Result = &result
	}
	if value.ManagedWorktree != nil {
		managed := *value.ManagedWorktree
		managed.BaseChangeSetIDs = append([]string(nil), value.ManagedWorktree.BaseChangeSetIDs...)
		if value.ManagedWorktree.ChangeSet != nil {
			change := *value.ManagedWorktree.ChangeSet
			change.BaseChangeSetIDs = append([]string(nil), value.ManagedWorktree.ChangeSet.BaseChangeSetIDs...)
			change.Files = append([]ChangeFile(nil), value.ManagedWorktree.ChangeSet.Files...)
			change.Validation = append([]string(nil), value.ManagedWorktree.ChangeSet.Validation...)
			managed.ChangeSet = &change
		}
		clone.ManagedWorktree = &managed
	}
	return clone
}

func cloneTaskEvidence(values []AgentTaskEvidence) []AgentTaskEvidence {
	if values == nil {
		return nil
	}
	result := make([]AgentTaskEvidence, len(values))
	for index := range values {
		result[index] = values[index]
		result[index].ArtifactRefs = append([]string(nil), values[index].ArtifactRefs...)
	}
	return result
}

// ValidateChildAgent validates one durable child projection.
func ValidateChildAgent(value ChildAgent) error {
	if value.ID == "" || value.ParentSessionID == "" || value.ParentTurnID == "" || value.AgentSessionID == "" || value.RunID == "" || value.Attempt <= 0 || value.Revision == 0 || value.CreatedAt.IsZero() || value.UpdatedAt.Before(value.CreatedAt) {
		return errors.New("child Agent identity, parent, attempt, revision, and timestamps are required")
	}
	if value.AgentSessionID != ChildAgentSessionID(value.ID) || value.RunID != ChildAgentRunID(value.ID) {
		return errors.New("child Agent session and Run identities are inconsistent")
	}
	if !validChildRoleProfile(value.Role, value.Profile) || !validChildStatus(value.Status) {
		return errors.New("child Agent role, profile, or status is unsupported")
	}
	if value.Kind == ChildAgentWorkflowNode {
		if value.WorkflowID == "" || value.NodeID == "" || value.PlanID == "" || value.PlanVersion == 0 || !isHexDigest(value.PlanDigest, 64, 64) {
			return errors.New("Workflow child Agent requires exact Workflow, Node, and Plan identity")
		}
	} else if value.Kind == ChildAgentPlanExplore {
		if value.WorkflowID != "" || value.NodeID != "" || value.Role != workflow.RoleExplore || value.Profile != CapabilityExplore {
			return errors.New("Plan exploration child Agent must be an unbound Explore role")
		}
	} else {
		return errors.New("child Agent kind is unsupported")
	}
	if err := ValidateAgentTask(value.Task, value.Role); err != nil {
		return err
	}
	terminal := value.Status == ChildAgentCompleted || value.Status == ChildAgentFailed || value.Status == ChildAgentCancelled
	if terminal != !value.CompletedAt.IsZero() || (!value.StartedAt.IsZero() && value.StartedAt.Before(value.CreatedAt)) || (!value.CompletedAt.IsZero() && value.CompletedAt.Before(value.StartedAt)) {
		return errors.New("child Agent lifecycle timestamps do not match status")
	}
	if value.Status == ChildAgentCompleted {
		if value.Result == nil || value.Result.Status != AgentTaskSucceeded || value.Failure != "" {
			return errors.New("completed child Agent requires one successful structured result")
		}
	} else if value.Result != nil {
		if value.Status != ChildAgentFailed || value.Result.Status == AgentTaskSucceeded {
			return errors.New("only a failed child Agent may retain a non-success structured result")
		}
	}
	if value.Result != nil {
		if err := ValidateAgentTaskResult(*value.Result); err != nil {
			return err
		}
	}
	if value.ManagedWorktree != nil {
		if value.Kind != ChildAgentWorkflowNode || value.Role != workflow.RoleImplement || value.Profile != CapabilityImplement {
			return errors.New("managed worktree is valid only for a Workflow Implement child")
		}
		if err := validateManagedWorktree(*value.ManagedWorktree); err != nil {
			return err
		}
		managedStatus := value.ManagedWorktree.Status
		switch value.Status {
		case ChildAgentCreating:
			if managedStatus != ManagedWorktreePending {
				return errors.New("creating isolated child requires a pending managed worktree")
			}
		case ChildAgentReady, ChildAgentRunning, ChildAgentAwaitingApproval:
			if managedStatus != ManagedWorktreeReady {
				return errors.New("active isolated child requires a ready managed worktree")
			}
		case ChildAgentCompleted:
			if managedStatus != ManagedWorktreeCaptured && managedStatus != ManagedWorktreeCleanupPending && managedStatus != ManagedWorktreeCleaned {
				return errors.New("completed isolated child requires a captured change set")
			}
		case ChildAgentFailed, ChildAgentCancelled:
			if managedStatus != ManagedWorktreeRetained {
				return errors.New("failed or cancelled isolated child must retain its managed worktree")
			}
		}
	}
	if len(value.Failure) > 4096 {
		return errors.New("child Agent failure exceeds its bound")
	}
	return nil
}

// ValidateAgentTask validates capability scope before a child session exists.
func ValidateAgentTask(value AgentTask, role workflow.Role) error {
	roles, err := roleprofile.NewDefaultRegistry()
	if err != nil {
		return err
	}
	definition, err := roles.ResolveRole(role)
	if err != nil {
		return err
	}
	return ValidateAgentTaskWithPolicy(value, role, definition)
}

// ValidateAgentTaskWithPolicy validates task scope against the exact role
// policy version selected by the Coordinator.
func ValidateAgentTaskWithPolicy(value AgentTask, role workflow.Role, definition roleprofile.Definition) error {
	if definition.Role != role {
		return errors.New("child Agent task role policy does not match its role")
	}
	if strings.TrimSpace(value.Goal) == "" || len(value.Goal) > maxChildTaskTextBytes || len(value.AcceptanceCriteria) == 0 || len(value.AcceptanceCriteria) > maxChildTaskItems || len(value.ReadPaths) > maxChildTaskItems || len(value.WritePaths) > maxChildTaskItems || len(value.DependencyEvidence) > maxChildTaskItems {
		return errors.New("child Agent task goal, scope, evidence, and acceptance criteria must be bounded")
	}
	if definition.Prompt.ReadOnly && len(value.WritePaths) != 0 {
		return fmt.Errorf("child Agent role %q cannot receive write scope", role)
	}
	if err := validateChildPaths(value.ReadPaths); err != nil {
		return fmt.Errorf("child Agent read scope: %w", err)
	}
	if err := validateChildPaths(value.WritePaths); err != nil {
		return fmt.Errorf("child Agent write scope: %w", err)
	}
	for _, criterion := range value.AcceptanceCriteria {
		if strings.TrimSpace(criterion) == "" || len(criterion) > maxChildTaskTextBytes {
			return errors.New("child Agent acceptance criterion is empty or too large")
		}
	}
	for _, evidence := range value.DependencyEvidence {
		if err := validateTaskEvidence(evidence); err != nil {
			return err
		}
	}
	return nil
}

// ValidateAgentTaskResult validates the product-owned terminal output schema.
func ValidateAgentTaskResult(value AgentTaskResult) error {
	if value.Status != AgentTaskSucceeded && value.Status != AgentTaskBlocked && value.Status != AgentTaskFailed {
		return errors.New("child Agent result status is unsupported")
	}
	if strings.TrimSpace(value.Conclusion) == "" || len(value.Conclusion) > maxChildTaskTextBytes || len(value.Changes) > maxChildTaskItems || len(value.Evidence) == 0 || len(value.Evidence) > maxChildTaskItems || len(value.Validation) > maxChildTaskItems || len(value.ArtifactRefs) > maxChildTaskItems || len(value.Unresolved) > maxChildTaskItems || value.SubmittedAt.IsZero() {
		return errors.New("child Agent result conclusion, evidence, and lists must be bounded")
	}
	for _, evidence := range value.Evidence {
		if err := validateTaskEvidence(evidence); err != nil {
			return err
		}
	}
	for _, values := range [][]string{value.Changes, value.Validation, value.ArtifactRefs, value.Unresolved} {
		for _, item := range values {
			if strings.TrimSpace(item) == "" || len(item) > maxChildTaskTextBytes {
				return errors.New("child Agent result contains an empty or oversized item")
			}
		}
	}
	return nil
}

// ValidateChildAgentTransition enforces monotonic CAS lifecycle updates.
func ValidateChildAgentTransition(previous, next ChildAgent) error {
	if err := ValidateChildAgent(previous); err != nil {
		return err
	}
	if err := ValidateChildAgent(next); err != nil {
		return err
	}
	stable := previous.ID == next.ID && previous.Kind == next.Kind && previous.ParentSessionID == next.ParentSessionID && previous.ParentTurnID == next.ParentTurnID && previous.WorkflowID == next.WorkflowID && previous.NodeID == next.NodeID && previous.PlanID == next.PlanID && previous.PlanVersion == next.PlanVersion && previous.PlanDigest == next.PlanDigest && previous.Role == next.Role && previous.Profile == next.Profile && previous.PolicyVersion == next.PolicyVersion && equalAgentTask(previous.Task, next.Task) && previous.AgentSessionID == next.AgentSessionID && previous.RunID == next.RunID && previous.Attempt == next.Attempt && previous.CreatedAt.Equal(next.CreatedAt)
	if !stable || next.Revision != previous.Revision+1 || next.UpdatedAt.Before(previous.UpdatedAt) {
		return errors.New("child Agent immutable identity changed or revision did not advance")
	}
	allowed := previous.Status == ChildAgentCreating && next.Status == ChildAgentReady ||
		previous.Status == ChildAgentReady && (next.Status == ChildAgentRunning || next.Status == ChildAgentCancelled) ||
		previous.Status == ChildAgentRunning && (next.Status == ChildAgentAwaitingApproval || next.Status == ChildAgentCompleted || next.Status == ChildAgentFailed || next.Status == ChildAgentCancelled) ||
		previous.Status == ChildAgentAwaitingApproval && (next.Status == ChildAgentRunning || next.Status == ChildAgentCompleted || next.Status == ChildAgentFailed || next.Status == ChildAgentCancelled) ||
		previous.Status == ChildAgentCompleted && next.Status == ChildAgentCompleted && managedIntegrationTransition(previous.ManagedWorktree, next.ManagedWorktree)
	if !allowed {
		return fmt.Errorf("child Agent status cannot transition from %q to %q", previous.Status, next.Status)
	}
	if !managedChildTransition(previous, next) {
		return errors.New("child Agent managed worktree transition is invalid")
	}
	return nil
}

func equalAgentTask(left, right AgentTask) bool {
	if left.Goal != right.Goal || !equalStrings(left.ReadPaths, right.ReadPaths) || !equalStrings(left.WritePaths, right.WritePaths) || !equalStrings(left.AcceptanceCriteria, right.AcceptanceCriteria) || len(left.DependencyEvidence) != len(right.DependencyEvidence) {
		return false
	}
	for index := range left.DependencyEvidence {
		if left.DependencyEvidence[index].SourceID != right.DependencyEvidence[index].SourceID || left.DependencyEvidence[index].Summary != right.DependencyEvidence[index].Summary || !equalStrings(left.DependencyEvidence[index].ArtifactRefs, right.DependencyEvidence[index].ArtifactRefs) {
			return false
		}
	}
	return true
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func managedChildTransition(previous, next ChildAgent) bool {
	if previous.ManagedWorktree == nil || next.ManagedWorktree == nil {
		return previous.ManagedWorktree == nil && next.ManagedWorktree == nil
	}
	before, after := previous.ManagedWorktree, next.ManagedWorktree
	if before.ID != after.ID || before.BaselineCommit != after.BaselineCommit {
		return false
	}
	if before.Status == after.Status {
		return equalManagedWorktree(*before, *after)
	}
	return before.Status == ManagedWorktreePending && after.Status == ManagedWorktreeReady ||
		before.Status == ManagedWorktreeReady && (after.Status == ManagedWorktreeCaptured || after.Status == ManagedWorktreeRetained) ||
		before.Status == ManagedWorktreeCaptured && after.Status == ManagedWorktreeCleanupPending ||
		before.Status == ManagedWorktreeCleanupPending && after.Status == ManagedWorktreeCleaned
}

func equalManagedWorktree(left, right ManagedWorktree) bool {
	if left.ID != right.ID || left.Root != right.Root || left.GitCommonDir != right.GitCommonDir || left.BaselineCommit != right.BaselineCommit || left.Status != right.Status || !equalStrings(left.BaseChangeSetIDs, right.BaseChangeSetIDs) || !left.PreparedAt.Equal(right.PreparedAt) || !left.CapturedAt.Equal(right.CapturedAt) || !left.CleanedAt.Equal(right.CleanedAt) {
		return false
	}
	if left.ChangeSet == nil || right.ChangeSet == nil {
		return left.ChangeSet == nil && right.ChangeSet == nil
	}
	return equalChangeSet(*left.ChangeSet, *right.ChangeSet)
}

func equalChangeSet(left, right ChangeSet) bool {
	if left.Version != right.Version || left.ID != right.ID || left.ChildAgentID != right.ChildAgentID || left.WorkflowID != right.WorkflowID || left.NodeID != right.NodeID || left.BaselineCommit != right.BaselineCommit || left.Patch != right.Patch || left.PatchSHA256 != right.PatchSHA256 || !equalStrings(left.BaseChangeSetIDs, right.BaseChangeSetIDs) || !equalStrings(left.Validation, right.Validation) || !left.CreatedAt.Equal(right.CreatedAt) || !left.IntegratedAt.Equal(right.IntegratedAt) || len(left.Files) != len(right.Files) {
		return false
	}
	for index := range left.Files {
		if left.Files[index] != right.Files[index] {
			return false
		}
	}
	return true
}

func managedIntegrationTransition(previous, next *ManagedWorktree) bool {
	return previous != nil && next != nil && (previous.Status == ManagedWorktreeCaptured && next.Status == ManagedWorktreeCleanupPending || previous.Status == ManagedWorktreeCleanupPending && next.Status == ManagedWorktreeCleaned)
}

func validChildRoleProfile(role workflow.Role, profile CapabilityProfile) bool {
	if string(role) != string(profile) {
		return false
	}
	return role == workflow.RoleExplore || role == workflow.RoleImplement || role == workflow.RoleValidate || role == workflow.RoleReview || role == workflow.RoleIntegrate
}

func validChildStatus(value ChildAgentStatus) bool {
	return value == ChildAgentCreating || value == ChildAgentReady || value == ChildAgentRunning || value == ChildAgentAwaitingApproval || value == ChildAgentCompleted || value == ChildAgentFailed || value == ChildAgentCancelled
}

func validateChildPaths(values []string) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) == "" || strings.Contains(value, "\\") || path.IsAbs(value) || path.Clean(value) != value || value == "." || value == ".." || strings.HasPrefix(value, "../") || len(value) > 4096 {
			return errors.New("paths must be normalized worktree-relative paths")
		}
		if _, exists := seen[value]; exists {
			return errors.New("paths must be unique")
		}
		seen[value] = struct{}{}
	}
	if !sort.StringsAreSorted(values) {
		return errors.New("paths must use stable sorted order")
	}
	return nil
}

func validateTaskEvidence(value AgentTaskEvidence) error {
	if strings.TrimSpace(value.SourceID) == "" || strings.TrimSpace(value.Summary) == "" || len(value.SourceID) > 256 || len(value.Summary) > maxChildTaskTextBytes || len(value.ArtifactRefs) > maxChildTaskItems {
		return errors.New("child Agent evidence identity and summary must be bounded")
	}
	for _, reference := range value.ArtifactRefs {
		if strings.TrimSpace(reference) == "" || len(reference) > 4096 {
			return errors.New("child Agent artifact reference is empty or too large")
		}
	}
	return nil
}
