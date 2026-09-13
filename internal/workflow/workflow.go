// Package workflow owns provider-neutral durable Workflow state and serial
// scheduling rules. Product-specific Plan compilation remains in codingagent.
package workflow

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"
)

const (
	MaxNodes           = 64
	MaxScopePaths      = 128
	MaxAcceptanceItems = 128
	maxTextBytes       = 16 << 10
)

var (
	ErrNotFound = errors.New("workflow not found")
	ErrConflict = errors.New("workflow revision conflict")
)

type ID string
type NodeID string

type Strategy string

const (
	StrategySingleAgent      Strategy = "workflow_single"
	StrategyMultiAgentSerial Strategy = "workflow_multi_serial"
)

// Executor identifies whether the parent Agent or an independent child Agent
// owns one node. Empty remains a backwards-compatible alias for parent Agent.
type Executor string

const (
	ExecutorMain  Executor = "main"
	ExecutorChild Executor = "child"
)

type Status string

const (
	StatusPending     Status = "pending"
	StatusRunning     Status = "running"
	StatusBlocked     Status = "blocked"
	StatusNeedsReplan Status = "needs_replan"
	StatusCompleted   Status = "completed"
	StatusCancelled   Status = "cancelled"
	StatusFailed      Status = "failed"
)

type NodeStatus string

const (
	NodePending   NodeStatus = "pending"
	NodeRunning   NodeStatus = "running"
	NodeCompleted NodeStatus = "completed"
	NodeFailed    NodeStatus = "failed"
	NodeBlocked   NodeStatus = "blocked"
	NodeCancelled NodeStatus = "cancelled"
)

type Role string

const (
	RoleExplore   Role = "explore"
	RoleImplement Role = "implement"
	RoleValidate  Role = "validate"
	RoleReview    Role = "review"
	RoleIntegrate Role = "integrate"
)

type Capability string

const (
	CapabilityExplore   Capability = "explore"
	CapabilityImplement Capability = "implement"
	CapabilityValidate  Capability = "validate"
	CapabilityReview    Capability = "review"
	CapabilityIntegrate Capability = "integrate"
)

type FailureAction string

const (
	FailureRetry        FailureAction = "retry"
	FailureBlock        FailureAction = "block"
	FailureReplan       FailureAction = "replan"
	FailureTerminate    FailureAction = "terminate"
	FailureFallbackMain FailureAction = "fallback_main"
)

type PlanReference struct {
	ID      string `json:"id"`
	Version uint64 `json:"version"`
	Digest  string `json:"digest"`
}

type Budget struct {
	MaxNodes       int `json:"max_nodes"`
	MaxRuns        int `json:"max_runs"`
	MaxAttempts    int `json:"max_attempts"`
	MaxAgentSteps  int `json:"max_agent_steps"`
	UsedAgentSteps int `json:"used_agent_steps,omitempty"`
}

type Scope struct {
	ReadPaths  []string `json:"read_paths,omitempty"`
	WritePaths []string `json:"write_paths,omitempty"`
}

type Node struct {
	ID                 NodeID        `json:"id"`
	Goal               string        `json:"goal"`
	DependsOn          []NodeID      `json:"depends_on,omitempty"`
	Role               Role          `json:"role"`
	Capability         Capability    `json:"capability"`
	Executor           Executor      `json:"executor,omitempty"`
	Delegated          bool          `json:"delegated,omitempty"`
	PolicyVersion      uint32        `json:"policy_version,omitempty"`
	Scope              Scope         `json:"scope"`
	AcceptanceCriteria []string      `json:"acceptance_criteria"`
	FailureAction      FailureAction `json:"failure_action"`
	MaxAttempts        int           `json:"max_attempts"`
	Status             NodeStatus    `json:"status"`
	Attempts           int           `json:"attempts"`
	ResultRef          string        `json:"result_ref,omitempty"`
	Failure            string        `json:"failure,omitempty"`
	StartedAt          time.Time     `json:"started_at,omitempty"`
	FinishedAt         time.Time     `json:"finished_at,omitempty"`
}

type Workflow struct {
	ID          ID            `json:"id"`
	OwnerID     string        `json:"owner_id"`
	Plan        PlanReference `json:"plan"`
	Strategy    Strategy      `json:"strategy"`
	Status      Status        `json:"status"`
	Budget      Budget        `json:"budget"`
	Nodes       []Node        `json:"nodes"`
	Revision    uint64        `json:"revision"`
	CreatedAt   time.Time     `json:"created_at"`
	UpdatedAt   time.Time     `json:"updated_at"`
	CompletedAt time.Time     `json:"completed_at,omitempty"`
}

type EventType string

const (
	EventWorkflowStarted         EventType = "workflow_started"
	EventNodeStarted             EventType = "node_started"
	EventNodeCompleted           EventType = "node_completed"
	EventNodeFailed              EventType = "node_failed"
	EventNodeRetryScheduled      EventType = "node_retry_scheduled"
	EventNodeFallbackScheduled   EventType = "node_fallback_scheduled"
	EventNodeBlocked             EventType = "node_blocked"
	EventWorkflowBlocked         EventType = "workflow_blocked"
	EventWorkflowReplanRequested EventType = "workflow_replan_requested"
	EventWorkflowCompleted       EventType = "workflow_completed"
	EventWorkflowCancelled       EventType = "workflow_cancelled"
	EventWorkflowFailed          EventType = "workflow_failed"
)

type Event struct {
	ID         string    `json:"id"`
	Type       EventType `json:"type"`
	NodeID     NodeID    `json:"node_id,omitempty"`
	ResultRef  string    `json:"result_ref,omitempty"`
	Summary    string    `json:"summary,omitempty"`
	AgentSteps int       `json:"agent_steps,omitempty"`
	OccurredAt time.Time `json:"occurred_at"`
}

type Repository interface {
	CreateWorkflow(ctx context.Context, value Workflow) error
	LoadWorkflow(ctx context.Context, id ID) (Workflow, error)
	ListWorkflows(ctx context.Context, ownerID string) ([]Workflow, error)
	AppendWorkflowEvent(ctx context.Context, id ID, expectedRevision uint64, event Event) (Workflow, error)
}

func Validate(value Workflow) error {
	if !validIdentifier(string(value.ID)) || strings.TrimSpace(value.OwnerID) == "" {
		return errors.New("workflow identity and owner are required")
	}
	if strings.TrimSpace(value.Plan.ID) == "" || value.Plan.Version == 0 || !hexDigest(value.Plan.Digest, 64) {
		return errors.New("workflow requires an exact immutable Plan reference")
	}
	if value.Strategy != StrategySingleAgent && value.Strategy != StrategyMultiAgentSerial {
		return fmt.Errorf("workflow strategy %q is unsupported", value.Strategy)
	}
	if !validStatus(value.Status) || value.Revision == 0 || value.CreatedAt.IsZero() || value.UpdatedAt.IsZero() || value.UpdatedAt.Before(value.CreatedAt) {
		return errors.New("workflow status, revision, and ordered timestamps are required")
	}
	terminal := value.Status == StatusCompleted || value.Status == StatusCancelled || value.Status == StatusFailed
	if terminal != !value.CompletedAt.IsZero() || (!value.CompletedAt.IsZero() && value.CompletedAt.Before(value.CreatedAt)) {
		return errors.New("workflow terminal status and completion timestamp must agree")
	}
	if len(value.Nodes) == 0 || len(value.Nodes) > MaxNodes {
		return fmt.Errorf("workflow requires between 1 and %d nodes", MaxNodes)
	}
	if value.Budget.MaxNodes < len(value.Nodes) || value.Budget.MaxNodes > MaxNodes || value.Budget.MaxRuns < len(value.Nodes) || value.Budget.MaxAttempts <= 0 || value.Budget.MaxRuns < value.Budget.MaxAttempts || value.Budget.MaxAgentSteps <= 0 || value.Budget.UsedAgentSteps < 0 || value.Budget.UsedAgentSteps > value.Budget.MaxAgentSteps {
		return errors.New("workflow budget does not cover its bounded nodes and attempts")
	}
	ids := make(map[NodeID]struct{}, len(value.Nodes))
	running, usedRuns, childNodes := 0, 0, 0
	for index := range value.Nodes {
		node := value.Nodes[index]
		if err := validateNode(node, value.Budget); err != nil {
			return fmt.Errorf("workflow node %d: %w", index, err)
		}
		if _, exists := ids[node.ID]; exists {
			return fmt.Errorf("workflow node %q is duplicated", node.ID)
		}
		ids[node.ID] = struct{}{}
		if node.Status == NodeRunning {
			running++
		}
		usedRuns += node.Attempts
		if value.Strategy == StrategySingleAgent && (NodeExecutor(node) != ExecutorMain || node.Delegated) {
			return errors.New("single-Agent workflow cannot assign a node to a child Agent")
		}
		if node.FailureAction == FailureFallbackMain && (value.Strategy != StrategyMultiAgentSerial || !node.Delegated) {
			return errors.New("fallback-to-main is valid only for an initially delegated multi-Agent node")
		}
		if node.FailureAction == FailureFallbackMain && node.Role == RoleExplore {
			return errors.New("an Explore node cannot fall back to the main Agent")
		}
		if node.FailureAction == FailureFallbackMain && node.MaxAttempts < 2 {
			return errors.New("fallback-to-main requires budget for a main-Agent attempt")
		}
		if value.Strategy == StrategyMultiAgentSerial && node.Role == RoleExplore && NodeExecutor(node) != ExecutorChild {
			return errors.New("Workflow Explore nodes must be assigned to child Agents")
		}
		if NodeExecutor(node) == ExecutorChild || node.Delegated {
			childNodes++
		}
	}
	if value.Strategy == StrategyMultiAgentSerial && childNodes == 0 {
		return errors.New("multi-Agent serial workflow requires at least one child Agent node")
	}
	if running > 1 {
		return errors.New("single-Agent workflow cannot contain multiple running nodes")
	}
	if usedRuns > value.Budget.MaxRuns {
		return errors.New("workflow consumed more Agent runs than its parent budget")
	}
	for _, node := range value.Nodes {
		seen := make(map[NodeID]struct{}, len(node.DependsOn))
		for _, dependency := range node.DependsOn {
			if dependency == node.ID {
				return fmt.Errorf("workflow node %q cannot depend on itself", node.ID)
			}
			if _, exists := ids[dependency]; !exists {
				return fmt.Errorf("workflow node %q has missing dependency %q", node.ID, dependency)
			}
			if _, exists := seen[dependency]; exists {
				return fmt.Errorf("workflow node %q repeats dependency %q", node.ID, dependency)
			}
			seen[dependency] = struct{}{}
		}
	}
	if hasCycle(value.Nodes) {
		return errors.New("workflow dependencies contain a cycle")
	}
	if value.Status == StatusPending {
		for _, node := range value.Nodes {
			if node.Status != NodePending || node.Attempts != 0 || !node.StartedAt.IsZero() || !node.FinishedAt.IsZero() {
				return errors.New("pending workflow must contain only untouched pending nodes")
			}
		}
	}
	if value.Status == StatusCompleted {
		for _, node := range value.Nodes {
			if node.Status != NodeCompleted {
				return errors.New("completed workflow requires every node to be completed")
			}
		}
	}
	return nil
}

func validateNode(node Node, budget Budget) error {
	if !validIdentifier(string(node.ID)) || validateText(node.Goal, true) != nil {
		return errors.New("identity and normalized goal are required")
	}
	if !validRoleCapability(node.Role, node.Capability) {
		return fmt.Errorf("role %q and capability %q are not an allowed pair", node.Role, node.Capability)
	}
	if NodeExecutor(node) != ExecutorMain && NodeExecutor(node) != ExecutorChild {
		return fmt.Errorf("node executor %q is unsupported", node.Executor)
	}
	if err := validateScope(node.Scope); err != nil {
		return err
	}
	if (node.Capability == CapabilityExplore || node.Capability == CapabilityValidate || node.Capability == CapabilityReview) && len(node.Scope.WritePaths) != 0 {
		return errors.New("read-only node capability cannot declare write scope")
	}
	if len(node.AcceptanceCriteria) == 0 || len(node.AcceptanceCriteria) > MaxAcceptanceItems {
		return errors.New("acceptance criteria are required and bounded")
	}
	seenCriteria := make(map[string]struct{}, len(node.AcceptanceCriteria))
	for _, criterion := range node.AcceptanceCriteria {
		if err := validateText(criterion, true); err != nil {
			return err
		}
		if _, exists := seenCriteria[criterion]; exists {
			return errors.New("acceptance criteria contain a duplicate")
		}
		seenCriteria[criterion] = struct{}{}
	}
	if node.FailureAction != FailureRetry && node.FailureAction != FailureBlock && node.FailureAction != FailureReplan && node.FailureAction != FailureTerminate && node.FailureAction != FailureFallbackMain {
		return fmt.Errorf("failure action %q is unsupported", node.FailureAction)
	}
	if node.MaxAttempts <= 0 || node.MaxAttempts > budget.MaxAttempts || node.Attempts < 0 || node.Attempts > node.MaxAttempts {
		return errors.New("attempt limits are invalid")
	}
	if !validNodeStatus(node.Status) {
		return fmt.Errorf("status %q is unsupported", node.Status)
	}
	started := node.Attempts > 0
	if started != !node.StartedAt.IsZero() {
		return errors.New("attempt count and start timestamp must agree")
	}
	finished := node.Status == NodeCompleted || node.Status == NodeFailed || node.Status == NodeBlocked || node.Status == NodeCancelled
	if finished != !node.FinishedAt.IsZero() {
		return errors.New("terminal node status and finish timestamp must agree")
	}
	if !node.FinishedAt.IsZero() && node.FinishedAt.Before(node.StartedAt) {
		return errors.New("node cannot finish before it starts")
	}
	if node.Status == NodeCompleted && strings.TrimSpace(node.ResultRef) == "" {
		return errors.New("completed node requires a result reference")
	}
	if node.Status != NodeCompleted && node.ResultRef != "" {
		return errors.New("only a completed node can carry a result reference")
	}
	if node.Status == NodeFailed && strings.TrimSpace(node.Failure) == "" {
		return errors.New("failed node requires a bounded failure summary")
	}
	if node.Failure != "" && validateText(node.Failure, false) != nil {
		return errors.New("node failure summary is invalid")
	}
	return nil
}

// NodeExecutor returns the effective executor while preserving P4 records that
// predate the explicit field.
func NodeExecutor(node Node) Executor {
	if node.Executor == "" {
		return ExecutorMain
	}
	return node.Executor
}

func validateScope(scope Scope) error {
	if len(scope.ReadPaths) > MaxScopePaths || len(scope.WritePaths) > MaxScopePaths {
		return errors.New("scope path count exceeds its limit")
	}
	for label, values := range map[string][]string{"read": scope.ReadPaths, "write": scope.WritePaths} {
		seen := make(map[string]struct{}, len(values))
		for _, value := range values {
			normalized, err := NormalizePath(value)
			if err != nil || normalized != value {
				return fmt.Errorf("%s scope path %q is not canonical", label, value)
			}
			if _, exists := seen[value]; exists {
				return fmt.Errorf("%s scope repeats path %q", label, value)
			}
			seen[value] = struct{}{}
		}
	}
	return nil
}

func NormalizePath(value string) (string, error) {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	if value == "" || strings.HasPrefix(value, "/") || strings.ContainsAny(value, "\x00\r\n") {
		return "", errors.New("workflow scope requires a worktree-relative path")
	}
	cleaned := path.Clean(value)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.Contains(cleaned, ":") {
		return "", errors.New("workflow scope path escapes the worktree or is not portable")
	}
	return cleaned, nil
}

func ApplyEvent(current Workflow, event Event) (Workflow, error) {
	if err := Validate(current); err != nil {
		return Workflow{}, fmt.Errorf("apply workflow event to invalid state: %w", err)
	}
	if !validIdentifier(event.ID) || event.OccurredAt.IsZero() || event.OccurredAt.Before(current.UpdatedAt) || event.AgentSteps < 0 {
		return Workflow{}, errors.New("workflow event identity and ordered timestamp are required")
	}
	if event.AgentSteps != 0 && event.Type != EventNodeCompleted && event.Type != EventNodeFailed && event.Type != EventWorkflowCancelled {
		return Workflow{}, errors.New("only a terminal Agent Run event can consume Workflow steps")
	}
	next := Clone(current)
	nodeIndex := func(required bool) (int, error) {
		if event.NodeID == "" {
			if required {
				return -1, errors.New("workflow event requires a node identity")
			}
			return -1, nil
		}
		for index := range next.Nodes {
			if next.Nodes[index].ID == event.NodeID {
				return index, nil
			}
		}
		return -1, fmt.Errorf("workflow event references unknown node %q", event.NodeID)
	}
	terminalAt := func() { next.CompletedAt = event.OccurredAt }
	consumeAgentSteps := func() error {
		if next.Budget.UsedAgentSteps+event.AgentSteps > next.Budget.MaxAgentSteps {
			return errors.New("workflow Agent step budget was exceeded")
		}
		next.Budget.UsedAgentSteps += event.AgentSteps
		return nil
	}
	switch event.Type {
	case EventWorkflowStarted:
		if current.Status != StatusPending || event.NodeID != "" {
			return Workflow{}, errors.New("workflow can start only once from pending")
		}
		next.Status = StatusRunning
	case EventNodeStarted:
		index, err := nodeIndex(true)
		if err != nil {
			return Workflow{}, err
		}
		if current.Status != StatusRunning || current.Nodes[index].Status != NodePending || activeNode(current) != "" || !dependenciesCompleted(current, current.Nodes[index]) || totalAttempts(current) >= current.Budget.MaxRuns || current.Budget.UsedAgentSteps >= current.Budget.MaxAgentSteps {
			return Workflow{}, fmt.Errorf("workflow node %q is not runnable", event.NodeID)
		}
		next.Nodes[index].Status = NodeRunning
		next.Nodes[index].Attempts++
		if next.Nodes[index].Attempts > next.Nodes[index].MaxAttempts {
			return Workflow{}, fmt.Errorf("workflow node %q exceeded its attempt limit", event.NodeID)
		}
		if next.Nodes[index].StartedAt.IsZero() {
			next.Nodes[index].StartedAt = event.OccurredAt
		}
		next.Nodes[index].FinishedAt = time.Time{}
	case EventNodeCompleted:
		index, err := nodeIndex(true)
		if err != nil {
			return Workflow{}, err
		}
		if current.Status != StatusRunning || current.Nodes[index].Status != NodeRunning || strings.TrimSpace(event.ResultRef) == "" {
			return Workflow{}, fmt.Errorf("workflow node %q cannot complete", event.NodeID)
		}
		if err := consumeAgentSteps(); err != nil {
			return Workflow{}, err
		}
		next.Nodes[index].Status = NodeCompleted
		next.Nodes[index].ResultRef = strings.TrimSpace(event.ResultRef)
		next.Nodes[index].Failure = ""
		next.Nodes[index].FinishedAt = event.OccurredAt
	case EventNodeFailed:
		index, err := nodeIndex(true)
		if err != nil {
			return Workflow{}, err
		}
		if current.Status != StatusRunning || current.Nodes[index].Status != NodeRunning || validateText(event.Summary, true) != nil {
			return Workflow{}, fmt.Errorf("workflow node %q cannot fail without a bounded reason", event.NodeID)
		}
		if err := consumeAgentSteps(); err != nil {
			return Workflow{}, err
		}
		next.Nodes[index].Status = NodeFailed
		next.Nodes[index].Failure = event.Summary
		next.Nodes[index].FinishedAt = event.OccurredAt
	case EventNodeRetryScheduled:
		index, err := nodeIndex(true)
		if err != nil {
			return Workflow{}, err
		}
		node := current.Nodes[index]
		if current.Status != StatusRunning || node.Status != NodeFailed || node.FailureAction != FailureRetry || node.Attempts >= node.MaxAttempts {
			return Workflow{}, fmt.Errorf("workflow node %q cannot be retried", event.NodeID)
		}
		next.Nodes[index].Status = NodePending
		next.Nodes[index].FinishedAt = time.Time{}
	case EventNodeFallbackScheduled:
		index, err := nodeIndex(true)
		if err != nil {
			return Workflow{}, err
		}
		node := current.Nodes[index]
		if current.Status != StatusRunning || current.Strategy != StrategyMultiAgentSerial || node.Status != NodeFailed || node.FailureAction != FailureFallbackMain || NodeExecutor(node) != ExecutorChild || node.Attempts >= node.MaxAttempts {
			return Workflow{}, fmt.Errorf("workflow node %q cannot fall back to the main Agent", event.NodeID)
		}
		next.Nodes[index].Status = NodePending
		next.Nodes[index].Executor = ExecutorMain
		next.Nodes[index].Failure = ""
		next.Nodes[index].FinishedAt = time.Time{}
	case EventNodeBlocked:
		index, err := nodeIndex(true)
		if err != nil {
			return Workflow{}, err
		}
		if current.Status != StatusRunning || (current.Nodes[index].Status != NodeFailed && current.Nodes[index].Status != NodePending) {
			return Workflow{}, fmt.Errorf("workflow node %q cannot be blocked", event.NodeID)
		}
		next.Nodes[index].Status = NodeBlocked
		next.Nodes[index].FinishedAt = event.OccurredAt
		if strings.TrimSpace(event.Summary) != "" {
			next.Nodes[index].Failure = event.Summary
		}
	case EventWorkflowBlocked:
		if current.Status != StatusRunning || activeNode(current) != "" || !hasNodeStatus(current, NodeBlocked, NodeFailed) {
			return Workflow{}, errors.New("workflow cannot be marked blocked")
		}
		next.Status = StatusBlocked
	case EventWorkflowReplanRequested:
		if current.Status != StatusRunning && current.Status != StatusBlocked {
			return Workflow{}, errors.New("workflow cannot request replan from its current state")
		}
		next.Status = StatusNeedsReplan
	case EventWorkflowCompleted:
		if current.Status != StatusRunning || activeNode(current) != "" || !allNodes(current, NodeCompleted) {
			return Workflow{}, errors.New("workflow cannot complete before every node")
		}
		next.Status = StatusCompleted
		terminalAt()
	case EventWorkflowCancelled:
		if isTerminal(current.Status) {
			return Workflow{}, errors.New("terminal workflow cannot be cancelled again")
		}
		if err := consumeAgentSteps(); err != nil {
			return Workflow{}, err
		}
		next.Status = StatusCancelled
		for index := range next.Nodes {
			if next.Nodes[index].Status == NodePending || next.Nodes[index].Status == NodeRunning || next.Nodes[index].Status == NodeFailed {
				next.Nodes[index].Status = NodeCancelled
				next.Nodes[index].FinishedAt = event.OccurredAt
			}
		}
		terminalAt()
	case EventWorkflowFailed:
		if isTerminal(current.Status) {
			return Workflow{}, errors.New("terminal workflow cannot fail again")
		}
		if validateText(event.Summary, true) != nil {
			return Workflow{}, errors.New("failed workflow requires a bounded reason")
		}
		next.Status = StatusFailed
		for index := range next.Nodes {
			if next.Nodes[index].Status == NodePending {
				next.Nodes[index].Status = NodeCancelled
				next.Nodes[index].FinishedAt = event.OccurredAt
			} else if next.Nodes[index].Status == NodeRunning {
				next.Nodes[index].Status = NodeFailed
				next.Nodes[index].Failure = event.Summary
				next.Nodes[index].FinishedAt = event.OccurredAt
			}
		}
		terminalAt()
	default:
		return Workflow{}, fmt.Errorf("workflow event type %q is unsupported", event.Type)
	}
	next.Revision++
	next.UpdatedAt = event.OccurredAt
	if err := Validate(next); err != nil {
		return Workflow{}, fmt.Errorf("workflow event %q produced invalid state: %w", event.Type, err)
	}
	return next, nil
}

func Clone(value Workflow) Workflow {
	clone := value
	clone.Nodes = make([]Node, len(value.Nodes))
	for index := range value.Nodes {
		clone.Nodes[index] = value.Nodes[index]
		clone.Nodes[index].DependsOn = append([]NodeID(nil), value.Nodes[index].DependsOn...)
		clone.Nodes[index].Scope.ReadPaths = append([]string(nil), value.Nodes[index].Scope.ReadPaths...)
		clone.Nodes[index].Scope.WritePaths = append([]string(nil), value.Nodes[index].Scope.WritePaths...)
		clone.Nodes[index].AcceptanceCriteria = append([]string(nil), value.Nodes[index].AcceptanceCriteria...)
	}
	return clone
}

func validIdentifier(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '-' || character == '_' || character == '.' {
			continue
		}
		return false
	}
	return true
}

func validateText(value string, required bool) error {
	trimmed := strings.TrimSpace(value)
	if required && trimmed == "" {
		return errors.New("text is required")
	}
	if value != trimmed || len(value) > maxTextBytes || strings.ContainsRune(value, 0) {
		return errors.New("text is not normalized or exceeds its limit")
	}
	return nil
}

func hexDigest(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, character := range value {
		if character >= '0' && character <= '9' || character >= 'a' && character <= 'f' {
			continue
		}
		return false
	}
	return true
}

func validRoleCapability(role Role, capability Capability) bool {
	return role == RoleExplore && capability == CapabilityExplore ||
		role == RoleImplement && capability == CapabilityImplement ||
		role == RoleValidate && capability == CapabilityValidate ||
		role == RoleReview && capability == CapabilityReview ||
		role == RoleIntegrate && capability == CapabilityIntegrate
}

func validStatus(value Status) bool {
	switch value {
	case StatusPending, StatusRunning, StatusBlocked, StatusNeedsReplan, StatusCompleted, StatusCancelled, StatusFailed:
		return true
	default:
		return false
	}
}

func validNodeStatus(value NodeStatus) bool {
	switch value {
	case NodePending, NodeRunning, NodeCompleted, NodeFailed, NodeBlocked, NodeCancelled:
		return true
	default:
		return false
	}
}

func hasCycle(nodes []Node) bool {
	dependencies := make(map[NodeID][]NodeID, len(nodes))
	for _, node := range nodes {
		dependencies[node.ID] = append([]NodeID(nil), node.DependsOn...)
		sort.Slice(dependencies[node.ID], func(left, right int) bool { return dependencies[node.ID][left] < dependencies[node.ID][right] })
	}
	visiting := make(map[NodeID]bool, len(nodes))
	visited := make(map[NodeID]bool, len(nodes))
	var visit func(NodeID) bool
	visit = func(id NodeID) bool {
		if visiting[id] {
			return true
		}
		if visited[id] {
			return false
		}
		visiting[id] = true
		for _, dependency := range dependencies[id] {
			if visit(dependency) {
				return true
			}
		}
		visiting[id] = false
		visited[id] = true
		return false
	}
	for _, node := range nodes {
		if visit(node.ID) {
			return true
		}
	}
	return false
}

func activeNode(value Workflow) NodeID {
	for _, node := range value.Nodes {
		if node.Status == NodeRunning {
			return node.ID
		}
	}
	return ""
}

func dependenciesCompleted(value Workflow, node Node) bool {
	states := make(map[NodeID]NodeStatus, len(value.Nodes))
	for _, candidate := range value.Nodes {
		states[candidate.ID] = candidate.Status
	}
	for _, dependency := range node.DependsOn {
		if states[dependency] != NodeCompleted {
			return false
		}
	}
	return true
}

func hasNodeStatus(value Workflow, statuses ...NodeStatus) bool {
	allowed := make(map[NodeStatus]bool, len(statuses))
	for _, status := range statuses {
		allowed[status] = true
	}
	for _, node := range value.Nodes {
		if allowed[node.Status] {
			return true
		}
	}
	return false
}

func allNodes(value Workflow, status NodeStatus) bool {
	for _, node := range value.Nodes {
		if node.Status != status {
			return false
		}
	}
	return true
}

func isTerminal(value Status) bool {
	return value == StatusCompleted || value == StatusCancelled || value == StatusFailed
}

func totalAttempts(value Workflow) int {
	total := 0
	for _, node := range value.Nodes {
		total += node.Attempts
	}
	return total
}
