package workflow

import (
	"errors"
	"fmt"
)

type ActionKind string

const (
	ActionNone          ActionKind = "none"
	ActionStartWorkflow ActionKind = "start_workflow"
	ActionStartNode     ActionKind = "start_node"
	ActionRetryNode     ActionKind = "retry_node"
	ActionFallbackNode  ActionKind = "fallback_node"
	ActionBlockNode     ActionKind = "block_node"
	ActionBlockWorkflow ActionKind = "block_workflow"
	ActionRequestReplan ActionKind = "request_replan"
	ActionFailWorkflow  ActionKind = "fail_workflow"
	ActionComplete      ActionKind = "complete_workflow"
	ActionWait          ActionKind = "wait"
)

type Action struct {
	Kind   ActionKind
	NodeID NodeID
	Reason string
}

// NextAction computes the only trusted serial transition that may follow the
// durable Workflow state. It never mutates the Workflow.
func NextAction(value Workflow) (Action, error) {
	if err := Validate(value); err != nil {
		return Action{}, err
	}
	switch value.Status {
	case StatusPending:
		return Action{Kind: ActionStartWorkflow}, nil
	case StatusCompleted, StatusCancelled, StatusFailed, StatusNeedsReplan:
		return Action{Kind: ActionNone}, nil
	case StatusBlocked:
		return Action{Kind: ActionWait, Reason: "workflow is blocked pending a product decision"}, nil
	}
	if active := activeNode(value); active != "" {
		return Action{Kind: ActionWait, NodeID: active}, nil
	}
	if allNodes(value, NodeCompleted) {
		return Action{Kind: ActionComplete}, nil
	}
	for _, node := range value.Nodes {
		if node.Status != NodeFailed {
			continue
		}
		switch node.FailureAction {
		case FailureRetry:
			if node.Attempts < node.MaxAttempts && value.Budget.UsedAgentSteps < value.Budget.MaxAgentSteps && totalAttempts(value) < value.Budget.MaxRuns {
				return Action{Kind: ActionRetryNode, NodeID: node.ID, Reason: node.Failure}, nil
			}
			return Action{Kind: ActionFailWorkflow, NodeID: node.ID, Reason: "node exhausted its retry or parent Workflow budget: " + node.Failure}, nil
		case FailureBlock:
			return Action{Kind: ActionBlockNode, NodeID: node.ID, Reason: node.Failure}, nil
		case FailureReplan:
			return Action{Kind: ActionRequestReplan, NodeID: node.ID, Reason: node.Failure}, nil
		case FailureTerminate:
			return Action{Kind: ActionFailWorkflow, NodeID: node.ID, Reason: node.Failure}, nil
		case FailureFallbackMain:
			if NodeExecutor(node) == ExecutorChild && node.Attempts < node.MaxAttempts && value.Budget.UsedAgentSteps < value.Budget.MaxAgentSteps && totalAttempts(value) < value.Budget.MaxRuns {
				return Action{Kind: ActionFallbackNode, NodeID: node.ID, Reason: node.Failure}, nil
			}
			return Action{Kind: ActionFailWorkflow, NodeID: node.ID, Reason: "node could not fall back to the main Agent within its parent Workflow budget: " + node.Failure}, nil
		}
	}
	if value.Budget.UsedAgentSteps >= value.Budget.MaxAgentSteps {
		return Action{Kind: ActionFailWorkflow, Reason: "workflow exhausted its Agent step budget"}, nil
	}
	if value.Strategy == StrategyMultiAgentParallelReadOnly && (value.Budget.UsedTotalTokens >= value.Budget.MaxTotalTokens || value.Budget.UsedCost >= value.Budget.MaxCost) {
		return Action{Kind: ActionFailWorkflow, Reason: "workflow exhausted its Agent token or cost budget"}, nil
	}
	if totalAttempts(value) >= value.Budget.MaxRuns {
		return Action{Kind: ActionFailWorkflow, Reason: "workflow exhausted its Agent Run budget"}, nil
	}
	for _, node := range value.Nodes {
		if node.Status == NodePending && dependenciesCompleted(value, node) {
			return Action{Kind: ActionStartNode, NodeID: node.ID}, nil
		}
	}
	for _, node := range value.Nodes {
		if node.Status != NodePending {
			continue
		}
		for _, dependency := range node.DependsOn {
			candidate, found := findNode(value, dependency)
			if found && (candidate.Status == NodeBlocked || candidate.Status == NodeCancelled || candidate.Status == NodeFailed) {
				return Action{Kind: ActionBlockNode, NodeID: node.ID, Reason: fmt.Sprintf("dependency %s did not complete", dependency)}, nil
			}
		}
	}
	if hasNodeStatus(value, NodeBlocked) {
		return Action{Kind: ActionBlockWorkflow, Reason: "one or more nodes are blocked"}, nil
	}
	return Action{}, errors.New("workflow has no valid serial scheduling action")
}

// RunnableNodes returns a stable, bounded set of dependency-ready nodes. For
// serial strategies it returns at most one node. Parallel Workflows fill only
// the currently available concurrency slots.
func RunnableNodes(value Workflow) ([]NodeID, error) {
	if err := Validate(value); err != nil {
		return nil, err
	}
	if value.Status != StatusRunning {
		return nil, nil
	}
	capacity := 1
	if value.Strategy == StrategyMultiAgentParallelReadOnly {
		capacity = value.Budget.MaxConcurrency - runningNodeCount(value)
	}
	if capacity <= 0 || totalAttempts(value) >= value.Budget.MaxRuns || value.Budget.UsedAgentSteps >= value.Budget.MaxAgentSteps {
		return nil, nil
	}
	remainingSteps := value.Budget.MaxAgentSteps - value.Budget.UsedAgentSteps
	if capacity > remainingSteps {
		capacity = remainingSteps
	}
	if value.Strategy == StrategyMultiAgentParallelReadOnly {
		remainingTokens := value.Budget.MaxTotalTokens - value.Budget.UsedTotalTokens
		if capacity > remainingTokens {
			capacity = remainingTokens
		}
		if value.Budget.UsedCost >= value.Budget.MaxCost {
			capacity = 0
		}
	}
	if capacity <= 0 {
		return nil, nil
	}
	remainingRuns := value.Budget.MaxRuns - totalAttempts(value)
	if capacity > remainingRuns {
		capacity = remainingRuns
	}
	result := make([]NodeID, 0, capacity)
	for _, node := range value.Nodes {
		if node.Status == NodePending && dependenciesCompleted(value, node) {
			result = append(result, node.ID)
			if len(result) == capacity {
				break
			}
		}
	}
	return result, nil
}

func findNode(value Workflow, id NodeID) (Node, bool) {
	for _, node := range value.Nodes {
		if node.ID == id {
			return node, true
		}
	}
	return Node{}, false
}
