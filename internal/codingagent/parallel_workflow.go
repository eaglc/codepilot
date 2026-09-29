package codingagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/eaglc/codepilot/internal/agent"
	agentsession "github.com/eaglc/codepilot/internal/agent/session"
	"github.com/eaglc/codepilot/internal/llm"
	"github.com/eaglc/codepilot/internal/workflow"
)

type parallelNodeExecution struct {
	node        workflow.Node
	child       ChildAgent
	environment runEnvironment
	message     llm.Message
	result      agent.RunResult
	err         error
}

// continueParallelWorkflowLocked starts one bounded wave of dependency-ready
// readers. The caller owns the Product Session operation lock for the complete
// wave, while child Agent runtimes execute concurrently in independent journals.
func (s *Service) continueParallelWorkflowLocked(ctx context.Context, product Session, turn Turn, durable workflow.Workflow, last TurnResult) (TurnResult, bool, error) {
	runnable, err := workflow.RunnableNodes(durable)
	if err != nil {
		return last, false, fmt.Errorf("continue parallel Coding workflow: schedule: %w", err)
	}
	if durable.Strategy == workflow.StrategyMultiAgentParallelIsolatedWrite && len(runnable) == 1 {
		if node, found := workflowNode(durable, runnable[0]); found && workflow.NodeExecutor(node) == workflow.ExecutorMain {
			return last, false, nil
		}
	}
	for _, nodeID := range runnable {
		durable, err = s.appendWorkflowTransition(ctx, durable, workflow.Event{Type: workflow.EventNodeStarted, NodeID: nodeID})
		if err != nil {
			return last, true, err
		}
	}
	running := make([]workflow.Node, 0, durable.Budget.MaxConcurrency)
	for _, node := range durable.Nodes {
		if node.Status == workflow.NodeRunning {
			running = append(running, node)
		}
	}
	if len(running) == 0 {
		return last, false, nil
	}
	result, err := s.runParallelWorkflowNodesLocked(ctx, product, turn, durable, running, last)
	return result, true, err
}

func (s *Service) runParallelWorkflowNodesLocked(ctx context.Context, product Session, turn Turn, durable workflow.Workflow, nodes []workflow.Node, last TurnResult) (TurnResult, error) {
	remainingDuration := time.Duration(durable.Budget.MaxDurationSeconds)*time.Second - time.Since(durable.CreatedAt)
	if remainingDuration <= 0 {
		// Preserve the durable cancellation path instead of leaving already
		// started nodes stranded when recovery observes an expired Workflow.
		remainingDuration = time.Nanosecond
	}
	baseLimits := s.deps.Limits
	if baseLimits.MaxDuration <= 0 || baseLimits.MaxDuration > remainingDuration {
		baseLimits.MaxDuration = remainingDuration
	}
	limits, err := parallelWorkflowNodeRunLimits(baseLimits, durable.Budget, len(nodes))
	if err != nil {
		return last, err
	}
	executions := make([]parallelNodeExecution, 0, len(nodes))
	for _, node := range nodes {
		readOnly := workflow.ReadOnlyCapability(node.Capability) && len(node.Scope.WritePaths) == 0
		isolatedWrite := durable.Strategy == workflow.StrategyMultiAgentParallelIsolatedWrite && node.Capability == workflow.CapabilityImplement && node.Isolated && len(node.Scope.WritePaths) != 0
		if workflow.NodeExecutor(node) != workflow.ExecutorChild || (!readOnly && !isolatedWrite) {
			return last, fmt.Errorf("run parallel Coding workflow node %q: trusted parallel boundary is invalid", node.ID)
		}
		execution, prepareErr := s.prepareParallelNodeExecution(ctx, product, turn, durable, node)
		if prepareErr != nil {
			return last, prepareErr
		}
		executions = append(executions, execution)
	}

	s.setState(product.ID, RuntimeRunning)
	waveCtx, cancelWave := context.WithTimeout(ctx, remainingDuration)
	defer cancelWave()
	runCtx, finishActive := s.beginActiveTurn(waveCtx, product.ID)
	var wait sync.WaitGroup
	for index := range executions {
		if executions[index].child.Status == ChildAgentCompleted || executions[index].child.Status == ChildAgentFailed || executions[index].child.Status == ChildAgentCancelled {
			executions[index].result = terminalChildRunResult(executions[index].child)
			executions[index].result, executions[index].err = childWorkflowOutcome(executions[index].child, executions[index].result, nil)
			continue
		}
		wait.Add(1)
		go func(execution *parallelNodeExecution) {
			defer wait.Done()
			execution.result, execution.err = s.runParallelChild(runCtx, product, *execution, limits)
		}(&executions[index])
	}
	wait.Wait()
	finishActive()

	return s.mergeParallelWorkflowResults(context.WithoutCancel(ctx), product, turn, durable, executions, last)
}

func (s *Service) prepareParallelNodeExecution(ctx context.Context, product Session, turn Turn, durable workflow.Workflow, node workflow.Node) (parallelNodeExecution, error) {
	child, err := s.ensureWorkflowChildAgent(ctx, product, turn, durable, node)
	if err != nil {
		return parallelNodeExecution{}, err
	}
	child, err = s.ensureChildAgentReady(ctx, product, child)
	if err != nil {
		return parallelNodeExecution{}, err
	}
	turn, err = s.bindParallelChildRun(ctx, turn, child, node)
	if err != nil {
		return parallelNodeExecution{}, err
	}
	if child.Status == ChildAgentReady {
		turn, err = s.markParallelRunStarted(ctx, turn, child.RunID)
		if err != nil {
			return parallelNodeExecution{}, err
		}
		child, err = s.transitionChild(ctx, child, ChildAgentRunning, nil, "")
		if err != nil {
			return parallelNodeExecution{}, err
		}
	}
	environment, err := s.prepareChildRunEnvironment(ctx, product, turn, child)
	if err != nil {
		return parallelNodeExecution{}, err
	}
	message, err := childTaskMessage(child)
	if err != nil {
		return parallelNodeExecution{}, err
	}
	return parallelNodeExecution{node: node, child: child, environment: environment, message: message}, nil
}

func (s *Service) bindParallelChildRun(ctx context.Context, turn Turn, child ChildAgent, node workflow.Node) (Turn, error) {
	latest, err := s.deps.Turns.LoadTurn(ctx, turn.ID)
	if err != nil {
		return turn, err
	}
	// The first Workflow node closes the approval-to-execution crash gap. The
	// caller carries the already validated approval fields before that Turn
	// transition has been persisted, matching the serial P4/P5 path.
	if latest.Revision == turn.Revision && turn.Phase == TurnPhaseExecuting && turn.ApprovedPlanVersion != 0 {
		latest = turn
	}
	if _, found := latest.Run(child.RunID); found {
		return latest, nil
	}
	now := time.Now().UTC()
	expected := latest.Revision
	latest.Runs = append(latest.Runs, RunBinding{
		RunID: child.RunID, NodeID: NodeID(node.ID), ChildAgentID: child.ID,
		Phase: TurnPhaseExecuting, Profile: child.Profile, Status: RunBindingPending,
	})
	latest.Phase = TurnPhaseExecuting
	latest.Status = TurnRunning
	latest.WorkflowID = child.WorkflowID
	latest.UpdatedAt = now
	latest.Revision++
	if err := s.deps.Turns.SaveTurn(ctx, latest, expected); err != nil {
		return turn, fmt.Errorf("bind parallel child Agent %q: %w", child.ID, err)
	}
	return latest, nil
}

func (s *Service) markParallelRunStarted(ctx context.Context, turn Turn, runID agentsession.RunID) (Turn, error) {
	latest, err := s.deps.Turns.LoadTurn(ctx, turn.ID)
	if err != nil {
		return turn, err
	}
	return s.markRunStarted(ctx, latest, runID, time.Now().UTC())
}

func (s *Service) runParallelChild(ctx context.Context, product Session, execution parallelNodeExecution, limits agent.RunLimits) (agent.RunResult, error) {
	child := execution.child
	release, err := s.acquireParallelAgent(ctx)
	if err != nil {
		return agent.RunResult{RunID: child.RunID, Status: agent.RunAborted, Reason: "parallel_agent_slot_cancelled"}, err
	}
	defer release()
	var result agent.RunResult
	var runErr error
	if child.Status == ChildAgentRunning {
		snapshot, loadErr := s.deps.AgentSessions.Load(ctx, child.AgentSessionID)
		if loadErr != nil {
			return agent.RunResult{RunID: child.RunID, Status: agent.RunFailed, Reason: "load_child_recovery"}, loadErr
		}
		plan := agentsession.BuildRecoveryPlan(snapshot)
		var action *agentsession.RecoveryAction
		for index := range plan.Actions {
			if plan.Actions[index].RunID == child.RunID {
				action = &plan.Actions[index]
				break
			}
		}
		if action == nil {
			result, runErr = s.deps.Agent.Run(ctx, agent.RunRequest{
				SessionID: child.AgentSessionID, Lane: agentsession.MainLane, RunID: child.RunID,
				UserEntryID: agentsession.EntryID("entry_" + string(child.ID)), SystemPrompt: execution.environment.systemPrompt,
				Model: llm.ModelRef{Provider: product.ProviderProfileID, Model: product.ModelID}, UserMessage: execution.message,
				UntrustedContext: execution.environment.untrustedContext, UntrustedContextCategories: execution.environment.untrustedContextCategories, Tools: execution.environment.tools, Limits: limits,
			}, execution.environment.events)
		} else if action.Kind == agentsession.RecoveryResolveInterrupt || action.Kind == agentsession.RecoveryDecideTool || action.Kind == agentsession.RecoveryDecideRun {
			result, runErr = agent.RunResult{RunID: child.RunID, Status: agent.RunFailed, Reason: "parallel_recovery_requires_decision"}, errors.New("parallel read-only child recovery reached a user decision boundary")
		} else {
			result, runErr = s.deps.Agent.Recover(ctx, agent.RecoverRequest{
				SessionID: child.AgentSessionID, Lane: agentsession.MainLane, RunID: child.RunID,
				ActionID: action.ID, Decision: agentsession.RecoveryRetry, Automatic: action.Automatic, ContinueRun: true,
				SystemPrompt: execution.environment.systemPrompt, Model: llm.ModelRef{Provider: product.ProviderProfileID, Model: product.ModelID},
				UntrustedContext: execution.environment.untrustedContext, UntrustedContextCategories: execution.environment.untrustedContextCategories, Tools: execution.environment.tools, Limits: limits,
			}, execution.environment.events)
		}
	}
	if result.RunID == "" {
		result.RunID = child.RunID
	}
	child, finishErr := s.finishChildAfterRun(context.WithoutCancel(ctx), child, result, runErr)
	if finishErr != nil {
		return result, finishErr
	}
	if result.Status == agent.RunInterrupted {
		child, finishErr = s.transitionChild(context.WithoutCancel(ctx), child, ChildAgentFailed, nil, "parallel child cannot suspend the entire wave for approval")
		result.Status, result.Reason = agent.RunFailed, "parallel_approval_not_supported"
		runErr = errors.New(child.Failure)
	}
	return childWorkflowOutcome(child, result, runErr)
}

func (s *Service) mergeParallelWorkflowResults(ctx context.Context, product Session, turn Turn, durable workflow.Workflow, executions []parallelNodeExecution, last TurnResult) (TurnResult, error) {
	cancelled := false
	totalSteps := 0
	for index := range executions {
		result, runErr := executions[index].result, executions[index].err
		latestTurn, err := s.deps.Turns.LoadTurn(ctx, turn.ID)
		if err != nil {
			return last, err
		}
		latestTurn, err = s.finishProductRun(ctx, latestTurn, result, runErr)
		if err != nil {
			return last, fmt.Errorf("finish parallel child Run %q: %w", result.RunID, err)
		}
		turn = latestTurn
		totalSteps += result.Steps
		if result.Status == agent.RunAborted || errors.Is(runErr, context.Canceled) {
			cancelled = true
			continue
		}
		agentTokens, agentCost, usageErr := s.parallelChildUsage(ctx, executions[index].child)
		if usageErr != nil {
			return last, usageErr
		}
		durable, err = s.deps.Workflows.LoadWorkflow(ctx, durable.ID)
		if err != nil {
			return last, err
		}
		nodeID := executions[index].node.ID
		if workflowTerminal(durable.Status) {
			continue
		}
		if runErr != nil || result.Status == agent.RunFailed || result.Status == agent.RunLimitReached {
			reason := result.Reason
			if reason == "" && runErr != nil {
				reason = runErr.Error()
			}
			durable, err = s.appendWorkflowTransition(ctx, durable, workflow.Event{Type: workflow.EventNodeFailed, NodeID: nodeID, Summary: boundedUTF8(redactSensitiveText(reason), 1024), AgentSteps: result.Steps, AgentTokens: agentTokens, AgentCost: agentCost})
		} else if result.Status == agent.RunCompleted {
			durable, err = s.appendWorkflowTransition(ctx, durable, workflow.Event{Type: workflow.EventNodeCompleted, NodeID: nodeID, ResultRef: "child:" + string(executions[index].child.ID), AgentSteps: result.Steps, AgentTokens: agentTokens, AgentCost: agentCost})
		} else {
			err = fmt.Errorf("parallel child Run %q returned unsupported status %q", result.RunID, result.Status)
		}
		if err != nil {
			return last, err
		}
		last = productTurnResult(turn.ID, result)
	}
	if cancelled {
		durable, err := s.deps.Workflows.LoadWorkflow(ctx, durable.ID)
		if err != nil {
			return last, err
		}
		if !workflowTerminal(durable.Status) {
			if _, err := s.appendWorkflowTransition(ctx, durable, workflow.Event{Type: workflow.EventWorkflowCancelled, Summary: "Parallel Workflow cancelled while nodes were active.", AgentSteps: totalSteps}); err != nil {
				return last, err
			}
		}
		if _, err := s.cancelProductTurn(ctx, turn, "workflow_cancelled"); err != nil {
			return last, err
		}
		s.setState(product.ID, RuntimeIdle)
		last.Status, last.Reason = string(workflow.StatusCancelled), "workflow_cancelled"
		return last, nil
	}
	drift, err := s.assessParallelWorkflowWorkspace(ctx, product, turn, durable)
	if err != nil {
		return last, fmt.Errorf("verify parallel Workflow workspace after read-only wave: %w", err)
	}
	if drift.Severity != WorkspaceDriftNone {
		turn, err = s.recordWorkspaceDrift(ctx, turn, drift)
		if err != nil {
			return last, err
		}
		if err := s.publishPlanLifecycleEvent(ctx, product, turn, EventPlanDriftDetected, &drift, turn.PlanReplan); err != nil {
			return last, err
		}
	}
	if drift.Severity == WorkspaceDriftMaterial {
		durable, err = s.deps.Workflows.LoadWorkflow(ctx, durable.ID)
		if err != nil {
			return last, err
		}
		if !workflowTerminal(durable.Status) {
			if _, err := s.appendWorkflowTransition(ctx, durable, workflow.Event{Type: workflow.EventWorkflowReplanRequested, Summary: drift.Summary}); err != nil {
				return last, err
			}
		}
		s.setState(product.ID, RuntimeInterrupted)
		last.Status, last.Reason = string(workflow.StatusNeedsReplan), "workspace_drift"
		return last, nil
	}
	if err := s.touchSession(ctx, product); err != nil {
		return last, err
	}
	return s.continueWorkflowLocked(ctx, product, turn, last)
}

func (s *Service) assessParallelWorkflowWorkspace(ctx context.Context, product Session, turn Turn, durable workflow.Workflow) (WorkspaceDrift, error) {
	if durable.Strategy != workflow.StrategyMultiAgentParallelIsolatedWrite {
		return s.assessPlanWorkspace(ctx, product, turn, WorkspaceDriftDuringExecution)
	}
	children, err := s.deps.Children.ListChildAgents(ctx, turn.ID)
	if err != nil {
		return WorkspaceDrift{}, err
	}
	var integrated []ChangeSet
	for _, child := range children {
		if child.WorkflowID == string(durable.ID) && child.ManagedWorktree != nil && child.ManagedWorktree.ChangeSet != nil && !child.ManagedWorktree.ChangeSet.IntegratedAt.IsZero() {
			integrated = append(integrated, *child.ManagedWorktree.ChangeSet)
		}
	}
	if len(integrated) == 0 {
		return s.assessPlanWorkspace(ctx, product, turn, WorkspaceDriftDuringExecution)
	}
	plan, err := s.deps.Plans.LoadPlan(ctx, turn.PlanID, turn.PlanVersion)
	if err != nil || plan.Digest != turn.PlanDigest {
		return WorkspaceDrift{}, errors.New("assess isolated-write workspace: exact Plan revision is unavailable or changed")
	}
	active, err := s.deps.Worktrees.LoadWorktree(ctx, product.WorktreeID)
	if err != nil {
		return WorkspaceDrift{}, err
	}
	head, err := managedGitText(ctx, active.Root, "rev-parse", "HEAD")
	if err != nil {
		return WorkspaceDrift{}, err
	}
	paths := make([]string, 0)
	state := make([]string, 0)
	if head != plan.WorkspaceRevision.GitHead {
		paths = append(paths, ".git/HEAD")
	}
	for _, change := range integrated {
		for _, file := range change.Files {
			current, digestErr := managedFileDigest(active.Root, file.Path)
			if digestErr != nil {
				return WorkspaceDrift{}, digestErr
			}
			state = append(state, change.ID+"\x00"+file.Path+"\x00"+current)
			if current != file.AfterSHA256 {
				paths = append(paths, file.Path)
			}
		}
	}
	sort.Strings(paths)
	sort.Strings(state)
	baselineDigest, err := ComputeWorkspaceRevisionDigest(plan.WorkspaceRevision)
	if err != nil {
		return WorkspaceDrift{}, err
	}
	currentDigest := digestBytes([]byte(strings.Join(state, "\n") + "\nHEAD\x00" + head))
	now := time.Now().UTC()
	drift := WorkspaceDrift{Severity: WorkspaceDriftNone, Source: WorkspaceDriftDuringExecution, Reason: "integrated_state_unchanged", Summary: "Approved change sets remain at their recorded integrated state.", PlanVersion: plan.Version, PlanDigest: plan.Digest, BaselineDigest: baselineDigest, CurrentDigest: currentDigest, DetectedAt: now}
	if len(paths) != 0 {
		drift.Severity, drift.Reason, drift.Summary, drift.Paths = WorkspaceDriftMaterial, "integrated_target_changed", "One or more approved integration targets changed after exact integration; execution was blocked.", paths
	}
	return drift, nil
}

func (s *Service) parallelChildUsage(ctx context.Context, child ChildAgent) (int, float64, error) {
	snapshot, err := s.deps.AgentSessions.Load(ctx, child.AgentSessionID)
	if err != nil {
		return 0, 0, fmt.Errorf("load parallel child Agent usage %q: %w", child.ID, err)
	}
	metrics := projectSessionMetrics(snapshot.Entries, snapshot.Records)
	return metrics.TotalTokens, metrics.Cost, nil
}

func terminalChildRunResult(child ChildAgent) agent.RunResult {
	result := agent.RunResult{RunID: child.RunID, Status: agent.RunCompleted, Reason: "recovered_child_result"}
	if child.Result != nil {
		result.TerminalOutput, _ = json.Marshal(child.Result)
	}
	return result
}
