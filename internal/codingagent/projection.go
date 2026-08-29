package codingagent

import (
	"encoding/json"
	"fmt"
	"strings"

	agentsession "github.com/eaglc/codepilot/internal/agent/session"
	"github.com/eaglc/codepilot/internal/llm"
)

// ProjectSnapshot converts durable Agent data into the product-safe authoritative view.
func ProjectSnapshot(product Session, durable agentsession.Snapshot, lane agentsession.Lane, state RuntimeState, revision uint64) (Snapshot, error) {
	if product.ID == "" || product.AgentSessionID == "" || product.AgentSessionID != durable.Metadata.ID {
		return Snapshot{}, fmt.Errorf("project coding snapshot: session binding is invalid")
	}
	entries, err := agentsession.BranchEntries(durable, lane)
	if err != nil {
		return Snapshot{}, err
	}
	product.ActiveLane = lane
	snapshot := Snapshot{Revision: revision, Session: product, RuntimeState: state}
	for _, warning := range durable.Warnings {
		snapshot.RecoveryWarnings = append(snapshot.RecoveryWarnings, warning.Message)
	}
	recovery := agentsession.AnalyzeRecovery(durable)
	for _, pending := range recovery.PendingInterrupts {
		projected := PendingInterrupt{TurnID: TurnID(pending.RunID), InterruptID: pending.InterruptID, Kind: pending.Kind, ToolCallID: pending.ToolCallID}
		if pending.Kind == "approval" && len(pending.Payload) != 0 {
			projectApprovalInterrupt(&projected, pending.Payload)
		} else if pending.Kind == "plan_approval" && len(pending.Payload) != 0 {
			projectPlanApprovalInterrupt(&projected, pending.Payload)
		} else if pending.Kind == clarificationInterruptKind && len(pending.Payload) != 0 {
			projectClarificationInterrupt(&projected, pending.Payload)
		}
		snapshot.PendingInterrupts = append(snapshot.PendingInterrupts, projected)
	}
	// An active operation is durably unfinished by definition. Treating it as a
	// recovery candidate before it reaches a terminal state produces a false
	// crash-recovery prompt on every normally running turn.
	if state != RuntimeRunning && state != RuntimeCancelling {
		plan := agentsession.BuildRecoveryPlan(durable)
		for _, action := range plan.Actions {
			projected := RecoveryAction{
				ID: action.ID, TurnID: TurnID(action.RunID), Kind: string(action.Kind), Automatic: action.Automatic,
				Summary: boundedUTF8(action.Reason, 1024),
			}
			if action.Tool != nil {
				projected.ToolCallID = action.Tool.ToolCallID
				projected.ToolName = boundedUTF8(action.Tool.ToolName, 256)
				projected.ReplayPolicy = boundedUTF8(action.Tool.ReplayPolicy, 32)
			}
			for _, decision := range action.Decisions {
				projected.Decisions = append(projected.Decisions, productRecoveryDecision(decision))
			}
			if action.Kind == agentsession.RecoveryResolveInterrupt {
				projected.Decisions = []RecoveryDecision{RecoveryAbandonTurn}
			}
			snapshot.RecoveryActions = append(snapshot.RecoveryActions, projected)
		}
	}
	if len(snapshot.PendingInterrupts) != 0 {
		snapshot.RuntimeState = RuntimeAwaitingApproval
	} else if len(snapshot.RecoveryActions) != 0 && snapshot.RuntimeState == RuntimeIdle {
		snapshot.RuntimeState = RuntimeInterrupted
	}
	entrySet := make(map[agentsession.EntryID]struct{}, len(entries))
	for _, entry := range entries {
		entrySet[entry.ID] = struct{}{}
	}
	snapshot.Metrics = projectSessionMetrics(entries, durable.Records)
	if len(durable.Log) != 0 {
		for _, item := range durable.Log {
			if item.Entry != nil {
				if _, included := entrySet[item.Entry.ID]; included {
					projectEntry(&snapshot, *item.Entry)
				}
				continue
			}
			if item.Record != nil && item.Record.Lane == lane {
				projectFailure(&snapshot, *item.Record)
			}
		}
		return snapshot, nil
	}
	for _, entry := range entries {
		projectEntry(&snapshot, entry)
	}
	for _, record := range durable.Records {
		if record.Lane == lane || record.Lane == "" {
			projectFailure(&snapshot, record)
		}
	}
	return snapshot, nil
}

func projectPlanApprovalInterrupt(target *PendingInterrupt, raw json.RawMessage) {
	if target == nil || len(raw) == 0 || !json.Valid(raw) {
		return
	}
	var payload planApprovalPayload
	if json.Unmarshal(raw, &payload) != nil || payload.Kind != "coding_plan_approval_v1" || payload.Version != 1 || payload.PlanID == "" || payload.Revision == 0 || !isHexDigest(payload.Digest, 64, 64) {
		return
	}
	target.Summary = boundedUTF8(redactSensitiveText(payload.Summary), 4<<10)
	target.PlanID = payload.PlanID
	target.PlanVersion = payload.Revision
	target.PlanDigest = payload.Digest
	target.PlanCompletion = payload.CompletionMode
}

// ProjectSnapshotWithTurns adds explicit Product Turn/Run relationships while
// preserving legacy Agent-only history when no Product Turn exists.
func ProjectSnapshotWithTurns(product Session, durable agentsession.Snapshot, lane agentsession.Lane, state RuntimeState, revision uint64, turns []Turn) (Snapshot, error) {
	snapshot, err := ProjectSnapshot(product, durable, lane, state, revision)
	if err != nil {
		return Snapshot{}, err
	}
	runTurns := make(map[agentsession.RunID]TurnID)
	runProfiles := make(map[agentsession.RunID]CapabilityProfile)
	runPhases := make(map[agentsession.RunID]TurnPhase)
	runStatuses := make(map[agentsession.RunID]TurnStatus)
	for _, turn := range turns {
		for _, binding := range turn.Runs {
			runTurns[binding.RunID] = turn.ID
			runProfiles[binding.RunID] = binding.Profile
			runPhases[binding.RunID] = binding.Phase
			runStatuses[binding.RunID] = turn.Status
		}
		if turn.Status == TurnPending || turn.Status == TurnRunning || turn.Status == TurnInterrupted {
			value := TurnSnapshot{ID: turn.ID, Phase: turn.Phase, Status: turn.Status, Strategy: turn.Strategy, RunCount: len(turn.Runs), Revision: turn.Revision}
			snapshot.ActiveTurn = &value
		}
	}
	for index := range snapshot.Transcript {
		runID := agentsession.RunID(snapshot.Transcript[index].TurnID)
		if turnID, found := runTurns[runID]; found {
			snapshot.Transcript[index].TurnID = turnID
		}
		if snapshot.Transcript[index].Kind == TranscriptFailure {
			switch runPhases[runID] {
			case TurnPhasePlanning:
				snapshot.Transcript[index].Text = "Planning request failed: " + snapshot.Transcript[index].Text
			case TurnPhaseExecuting:
				snapshot.Transcript[index].Text = "Approved Plan execution failed: " + snapshot.Transcript[index].Text
			}
		}
	}
	for index := range snapshot.PendingInterrupts {
		runID := agentsession.RunID(snapshot.PendingInterrupts[index].TurnID)
		snapshot.PendingInterrupts[index].RunID = RunID(runID)
		if turnID, found := runTurns[runID]; found {
			snapshot.PendingInterrupts[index].TurnID = turnID
		}
	}
	for index := range snapshot.RecoveryActions {
		runID := agentsession.RunID(snapshot.RecoveryActions[index].TurnID)
		snapshot.RecoveryActions[index].RunID = RunID(runID)
		if turnID, found := runTurns[runID]; found {
			snapshot.RecoveryActions[index].TurnID = turnID
		}
	}
	runID := agentsession.RunID(snapshot.Metrics.LatestTurnID)
	snapshot.Metrics.LatestRunID = RunID(runID)
	if turnID, found := runTurns[runID]; found {
		snapshot.Metrics.LatestTurnID = turnID
		snapshot.Metrics.LatestPhase = runPhases[runID]
		snapshot.Metrics.LatestProfile = runProfiles[runID]
		snapshot.Metrics.LatestTurnStatus = runStatuses[runID]
	}
	return snapshot, nil
}

func projectSessionMetrics(entries []agentsession.Entry, records []agentsession.Record) SessionMetrics {
	metrics := SessionMetrics{}
	runs := make(map[agentsession.RunID]struct{})
	var latestRun agentsession.RunID
	var latestRunAt int64
	for _, entry := range entries {
		if entry.RunID != "" {
			runs[entry.RunID] = struct{}{}
			stamp := entry.Timestamp.UnixNano()
			if latestRun == "" || stamp >= latestRunAt {
				latestRun, latestRunAt = entry.RunID, stamp
			}
		}
		if entry.Type == agentsession.EntryCompaction && entry.Compaction != nil {
			addSessionUsage(&metrics, entry.Compaction.Usage)
		}
		if entry.Type == agentsession.EntryBranchSummary && entry.BranchSummary != nil {
			addSessionUsage(&metrics, entry.BranchSummary.Usage)
		}
	}
	var latestUsageSequence uint64
	for _, record := range records {
		if _, included := runs[record.RunID]; !included {
			continue
		}
		if record.Type == agentsession.RecordOperationStarted && record.Operation != nil && record.Operation.Intent == agentsession.OperationRun {
			stamp := record.Timestamp.UnixNano()
			if latestRun == "" || stamp >= latestRunAt {
				latestRun, latestRunAt = record.RunID, stamp
			}
		}
		if record.Type == agentsession.RecordUsage && record.Usage != nil {
			addSessionUsage(&metrics, record.Usage)
			if record.Sequence >= latestUsageSequence {
				latestUsageSequence = record.Sequence
				metrics.ContextTokens = record.Usage.InputTokens
				if metrics.ContextTokens == 0 {
					metrics.ContextTokens = max(0, record.Usage.TotalTokens-record.Usage.OutputTokens)
				}
			}
		}
	}
	metrics.LatestTurnID = TurnID(latestRun)
	if latestRun == "" {
		return metrics
	}
	for _, record := range records {
		if record.RunID != latestRun {
			continue
		}
		switch record.Type {
		case agentsession.RecordOperationStarted:
			if record.Operation != nil && record.Operation.Intent == agentsession.OperationRun && (metrics.StartedAt.IsZero() || record.Timestamp.Before(metrics.StartedAt)) {
				metrics.StartedAt = record.Timestamp
			}
		case agentsession.RecordStepStarted, agentsession.RecordStepFinished:
			if record.Step != nil && record.Step.Attempt > metrics.Steps {
				metrics.Steps = record.Step.Attempt
			}
		case agentsession.RecordOperationFinished:
			if record.Operation != nil && record.Timestamp.After(metrics.FinishedAt) {
				metrics.FinishedAt = record.Timestamp
			}
		}
	}
	if !metrics.StartedAt.IsZero() && !metrics.FinishedAt.IsZero() && !metrics.FinishedAt.Before(metrics.StartedAt) {
		metrics.Elapsed = metrics.FinishedAt.Sub(metrics.StartedAt)
	}
	return metrics
}

func addSessionUsage(metrics *SessionMetrics, usage *llm.Usage) {
	if metrics == nil || usage == nil {
		return
	}
	metrics.InputTokens += max(0, usage.InputTokens)
	metrics.OutputTokens += max(0, usage.OutputTokens)
	metrics.CacheReadTokens += max(0, usage.CacheReadTokens)
	metrics.CacheWriteTokens += max(0, usage.CacheWriteTokens)
	metrics.ReasoningTokens += max(0, usage.ReasoningTokens)
	total := usage.TotalTokens
	if total <= 0 {
		total = usage.InputTokens + usage.OutputTokens
	}
	metrics.TotalTokens += max(0, total)
	if usage.Cost > 0 {
		metrics.Cost += usage.Cost
	}
}

func productRecoveryDecision(value agentsession.RecoveryDecision) RecoveryDecision {
	switch value {
	case agentsession.RecoveryRetry:
		return RecoveryRetry
	case agentsession.RecoveryConfirmExecuted:
		return RecoveryConfirmExecuted
	case agentsession.RecoveryMarkFailed:
		return RecoveryMarkFailed
	case agentsession.RecoveryAbandonRun:
		return RecoveryAbandonTurn
	default:
		return RecoveryDecision(value)
	}
}

func projectApprovalInterrupt(target *PendingInterrupt, raw json.RawMessage) {
	if target == nil || len(raw) == 0 || !json.Valid(raw) {
		return
	}
	var payload struct {
		Kind          string   `json:"kind"`
		Version       int      `json:"version"`
		Summary       string   `json:"summary"`
		PlanID        string   `json:"plan_id"`
		Command       string   `json:"command"`
		Patch         string   `json:"patch"`
		Files         []string `json:"files"`
		Path          string   `json:"path"`
		ToolName      string   `json:"tool_name"`
		Language      string   `json:"language"`
		Program       string   `json:"program"`
		Arguments     []string `json:"arguments"`
		GrantToolName string   `json:"grant_tool_name"`
		RequestedTool string   `json:"requested_tool"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		return
	}
	target.Summary = boundedUTF8(redactSensitiveText(payload.Summary), 4<<10)
	if payload.Version != 1 {
		return
	}
	if payload.Kind == "coding_sensitive_read_approval_v1" {
		path := strings.TrimSpace(payload.Path)
		if (payload.ToolName == "read_file" || payload.ToolName == "git_diff") && path != "" && len(path) <= 4096 && !strings.ContainsAny(path, "\r\n\x00") {
			target.Proposed = &ProposedChange{Kind: "sensitive_read", Summary: target.Summary, Path: path}
		}
		return
	}
	if payload.Kind == "coding_check_approval_v1" {
		planID := strings.TrimSpace(payload.PlanID)
		command := strings.TrimSpace(payload.Command)
		if planID != "" && len(planID) <= 256 && !strings.ContainsAny(planID, "\r\n\x00") && command != "" && len(command) <= 4096 && !strings.ContainsAny(command, "\r\n\x00") {
			target.Proposed = &ProposedChange{Kind: "check", Summary: target.Summary, PlanID: planID, Command: command}
			target.CanGrantSession = true
		}
		return
	}
	if payload.Kind == "coding_lsp_start_approval_v1" {
		languageID := strings.TrimSpace(payload.Language)
		program := strings.TrimSpace(payload.Program)
		validProgram := languageID == "go" && program == "gopls" || languageID == "python" && (program == "pyright-langserver" || program == "basedpyright-langserver") || languageID == "node" && program == "typescript-language-server"
		validArguments := (languageID == "go" && len(payload.Arguments) == 1 && payload.Arguments[0] == "serve") || ((languageID == "python" || languageID == "node") && len(payload.Arguments) == 1 && payload.Arguments[0] == "--stdio")
		if validProgram && validArguments && payload.GrantToolName == "language_server" && isNavigationTool(payload.RequestedTool) {
			target.Proposed = &ProposedChange{Kind: "lsp", Summary: target.Summary, Language: languageID, Command: program + " " + payload.Arguments[0]}
			target.CanGrantSession = true
		}
		return
	}
	if payload.Kind != "coding_patch_approval_v1" || strings.TrimSpace(payload.Patch) == "" {
		return
	}
	files := make([]string, 0, min(len(payload.Files), 128))
	for _, file := range payload.Files {
		file = strings.TrimSpace(file)
		if file == "" || len(file) > 4096 || strings.ContainsAny(file, "\r\n\x00") {
			continue
		}
		files = append(files, file)
		if len(files) == 128 {
			break
		}
	}
	patch := boundedUTF8(redactSensitiveText(payload.Patch), 2<<20)
	added, deleted := diffLineCounts(patch)
	target.Proposed = &ProposedChange{
		Kind: "patch", Summary: target.Summary, Diff: InlineDiff{Text: patch, Files: files},
		AddedLines: added, DeletedLines: deleted,
	}
	target.CanGrantSession = len(files) != 0
}

func diffLineCounts(patch string) (added, deleted int) {
	for _, line := range strings.Split(patch, "\n") {
		switch {
		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
			added++
		case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
			deleted++
		}
	}
	return added, deleted
}

func projectEntry(snapshot *Snapshot, entry agentsession.Entry) {
	if snapshot == nil {
		return
	}
	switch entry.Type {
	case agentsession.EntryMessage:
		if entry.Message == nil {
			return
		}
		snapshot.Transcript = append(snapshot.Transcript, projectMessage(entry)...)
	case agentsession.EntryCompaction:
		if entry.Compaction != nil {
			snapshot.Transcript = append(snapshot.Transcript, TranscriptItem{
				ID: string(entry.ID), TurnID: TurnID(entry.RunID), Role: TranscriptRoleSystem, Kind: TranscriptCompaction,
				Text: boundedUTF8(redactSensitiveText(entry.Compaction.Summary), 32<<10), Timestamp: entry.Timestamp,
			})
		}
	}
}

func projectFailure(snapshot *Snapshot, record agentsession.Record) {
	if snapshot == nil || record.Type != agentsession.RecordOperationFinished || record.Operation == nil || record.Operation.Outcome != "failed" {
		return
	}
	message := strings.TrimSpace(record.Operation.ErrorMessage)
	message = redactSensitiveText(productFailureMessage(record.Operation.ErrorCode, message))
	snapshot.Transcript = append(snapshot.Transcript, TranscriptItem{
		ID: string(record.ID), TurnID: TurnID(record.RunID), Role: TranscriptRoleSystem, Kind: TranscriptFailure,
		Text: boundedUTF8(message, 8<<10), Timestamp: record.Timestamp,
	})
}

func productFailureMessage(code, message string) string {
	lower := strings.ToLower(message)
	if strings.Contains(lower, "127.0.0.1:11434") && (strings.Contains(lower, "connection refused") || strings.Contains(lower, "actively refused")) {
		return "Cannot connect to Ollama at 127.0.0.1:11434. Start Ollama or select another configured provider."
	}
	message = strings.TrimPrefix(message, "receive Eino stream: ")
	message = strings.TrimPrefix(message, "complete Eino model: ")
	if message != "" {
		return message
	}
	if code == "model_step" {
		return "The model request failed before a response was produced."
	}
	return "The turn failed before the model produced a response."
}

func projectMessage(entry agentsession.Entry) []TranscriptItem {
	message := entry.Message
	role := TranscriptRole(message.Role)
	items := make([]TranscriptItem, 0, len(message.Content))
	for index, content := range message.Content {
		item := TranscriptItem{ID: transcriptItemID(entry.ID, index), SourceEntryID: string(entry.ID), TurnID: TurnID(entry.RunID), Role: role, Timestamp: entry.Timestamp}
		switch content.Type {
		case llm.ContentText:
			item.Kind = TranscriptText
			item.Text = boundedUTF8(redactSensitiveText(content.Text), 64<<10)
			if message.Role == llm.RoleTool {
				item.Kind = TranscriptToolResult
				item.Tool = &TranscriptTool{
					CallID: message.ToolCallID, Name: message.ToolName, Status: toolMessageStatus(message),
					Summary: toolSummary(content.Text), Detail: boundedUTF8(redactSensitiveText(content.Text), 64<<10), IsError: message.IsError,
				}
				projectToolDetails(item.Tool, message.Details)
			}
		case llm.ContentImage:
			item.Kind = TranscriptImage
			item.MIMEType = content.MIMEType
			item.Text = "[image]"
		case llm.ContentThinking:
			item.Kind = TranscriptThinking
			item.Text = ""
		case llm.ContentToolCall:
			item.Kind = TranscriptToolCall
			item.Tool = &TranscriptTool{CallID: content.ToolCall.ID, Name: content.ToolCall.Name, Status: "requested"}
		default:
			continue
		}
		items = append(items, item)
	}
	return items
}

func projectToolDetails(target *TranscriptTool, raw json.RawMessage) {
	if target == nil || len(raw) == 0 || !json.Valid(raw) {
		return
	}
	var envelope struct {
		Kind      string `json:"kind"`
		Detail    string `json:"detail"`
		Path      string `json:"path"`
		StartLine int    `json:"start_line"`
		EndLine   int    `json:"end_line"`
		Diff      *struct {
			Text  string   `json:"text"`
			Files []string `json:"files"`
		} `json:"diff"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return
	}
	if envelope.Detail != "" {
		target.Detail = boundedUTF8(redactSensitiveText(envelope.Detail), 64<<10)
	}
	if target.Name == "read_file" {
		if path := displayToolPath(envelope.Path); path != "" && envelope.StartLine > 0 && envelope.EndLine >= envelope.StartLine {
			target.Resources = []ToolResource{{Path: path, StartLine: envelope.StartLine, EndLine: envelope.EndLine}}
		}
	}
	if (envelope.Kind != "coding_patch_v1" && envelope.Kind != "coding_tool_artifact_v1") || envelope.Diff == nil || envelope.Diff.Text == "" {
		return
	}
	target.Diff = &InlineDiff{Text: boundedUTF8(redactSensitiveText(envelope.Diff.Text), 2<<20), Files: append([]string(nil), envelope.Diff.Files...)}
	target.Resources = projectDiffResources(target.Diff.Text, target.Diff.Files)
}

func displayToolPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 4096 || strings.ContainsAny(value, "\r\n\x00") {
		return ""
	}
	return value
}

func projectDiffResources(diff string, files []string) []ToolResource {
	resources := make([]ToolResource, 0, min(len(files), 128))
	byPath := make(map[string]int)
	for _, file := range files {
		file = displayToolPath(file)
		if file == "" {
			continue
		}
		byPath[file] = len(resources)
		resources = append(resources, ToolResource{Path: file})
	}
	var current string
	var oldPath string
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "--- ") {
			oldPath = diffDisplayPath(strings.TrimPrefix(line, "--- "))
			continue
		}
		if strings.HasPrefix(line, "+++ ") {
			current = diffDisplayPath(strings.TrimPrefix(line, "+++ "))
			if current == "/dev/null" {
				current = oldPath
			}
			oldPath = ""
			continue
		}
		index, found := byPath[current]
		if !found {
			continue
		}
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			resources[index].AddedLines++
		} else if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
			resources[index].DeletedLines++
		}
	}
	if len(resources) == 1 && resources[0].AddedLines == 0 && resources[0].DeletedLines == 0 {
		for _, line := range strings.Split(diff, "\n") {
			if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
				resources[0].AddedLines++
			} else if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
				resources[0].DeletedLines++
			}
		}
	}
	return resources
}

func diffDisplayPath(value string) string {
	value = strings.TrimSpace(value)
	if index := strings.IndexByte(value, '\t'); index >= 0 {
		value = value[:index]
	}
	value = strings.TrimPrefix(value, "a/")
	value = strings.TrimPrefix(value, "b/")
	return displayToolPath(value)
}

func toolSummary(value string) string {
	value = strings.TrimSpace(redactSensitiveText(value))
	if index := strings.IndexByte(value, '\n'); index >= 0 {
		value = value[:index]
	}
	return boundedUTF8(value, 240)
}

func transcriptItemID(entryID agentsession.EntryID, index int) string {
	if index == 0 {
		return string(entryID)
	}
	return fmt.Sprintf("%s:%d", entryID, index)
}

func toolMessageStatus(message *llm.Message) string {
	if message.IsError {
		return "error"
	}
	return "completed"
}
