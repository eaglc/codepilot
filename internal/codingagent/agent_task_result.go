package codingagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/eaglc/codepilot/internal/codingagent/roleprofile"
	"github.com/eaglc/codepilot/internal/llm"
	"github.com/eaglc/codepilot/internal/tool"
	"github.com/eaglc/codepilot/internal/workflow"
)

const submitAgentTaskResultToolName = "submit_agent_task_result"

type submitAgentTaskResultTool struct {
	role   workflow.Role
	policy roleprofile.ResultPolicy
	now    func() time.Time
}

func (*submitAgentTaskResultTool) Definition() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        submitAgentTaskResultToolName,
		Description: "Submit the one bounded structured result for this delegated task. This exclusive call ends the child Agent Run only after validation; ordinary assistant text cannot complete the task.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"status":{"type":"string","enum":["succeeded","blocked","failed"]},"conclusion":{"type":"string"},"changes":{"type":"array","items":{"type":"string"}},"evidence":{"type":"array","items":{"type":"object","properties":{"source_id":{"type":"string"},"summary":{"type":"string"},"artifact_refs":{"type":"array","items":{"type":"string"}}},"required":["source_id","summary"],"additionalProperties":false}},"validation":{"type":"array","items":{"type":"string"}},"artifact_refs":{"type":"array","items":{"type":"string"}},"unresolved":{"type":"array","items":{"type":"string"}}},"required":["status","conclusion","evidence"],"additionalProperties":false}`),
	}
}

func (*submitAgentTaskResultTool) ReplayPolicy() tool.ReplayPolicy { return tool.ReplayIdempotent }

func (*submitAgentTaskResultTool) TerminalOutputPolicy() tool.TerminalOutputPolicy {
	return tool.TerminalOutputPolicy{MaxBytes: 64 << 10}
}

func (t *submitAgentTaskResultTool) Execute(ctx context.Context, call tool.Call, _ tool.ProgressSink) (tool.Result, error) {
	if err := ctx.Err(); err != nil {
		return tool.Result{}, err
	}
	if t == nil || t.role == "" {
		return tool.Result{}, errors.New("submit child Agent result: trusted role is unavailable")
	}
	var value AgentTaskResult
	decoder := json.NewDecoder(bytes.NewReader(call.Arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return agentTaskResultInvalid("The delegated result is not valid structured data."), nil
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return agentTaskResultInvalid("The delegated result contains trailing data."), nil
	}
	clock := t.now
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	value.SubmittedAt = clock().UTC()
	if !t.policy.AllowChanges && len(value.Changes) != 0 {
		return agentTaskResultInvalid(fmt.Sprintf("The %s role cannot report workspace changes.", t.role)), nil
	}
	if value.Status == AgentTaskSucceeded && t.policy.RequireValidationOnSuccess && len(value.Validation) == 0 {
		return agentTaskResultInvalid(fmt.Sprintf("The %s role must report validation evidence on success.", t.role)), nil
	}
	if err := ValidateAgentTaskResult(value); err != nil {
		return agentTaskResultInvalid(err.Error()), nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return tool.Result{}, fmt.Errorf("submit child Agent result: encode validated result: %w", err)
	}
	return tool.Result{
		Status:  tool.ResultCompleted,
		Content: []llm.Content{{Type: llm.ContentText, Text: "The structured delegated result was accepted by the Agent runtime."}},
		Details: encoded,
	}, nil
}

func agentTaskResultInvalid(message string) tool.Result {
	return tool.Result{Status: tool.ResultInvalid, Content: []llm.Content{{Type: llm.ContentText, Text: message}}}
}
