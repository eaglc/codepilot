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
	if totalAttempts(value) >= value.Budget.MaxRuns {
		return Action{Kind: ActionFailWorkflow, Reason: "workflow exhausted its Agent Run budget"}, nil
	}
	for _, node := range value.Nodes {
		if node.Status == NodePending && dependenciesCompleted(value, node) {
			return Action{Kind: ActionStartNode, NodeID: node.ID}, nil
		}
	}
	if hasNodeStatus(value, NodeBlocked) {
		return Action{Kind: ActionBlockWorkflow, Reason: "one or more nodes are blocked"}, nil
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
	return Action{}, errors.New("workflow has no valid serial scheduling action")
}

func findNode(value Workflow, id NodeID) (Node, bool) {
	for _, node := range value.Nodes {
		if node.ID == id {
			return node, true
		}
	}
	return Node{}, false
}
