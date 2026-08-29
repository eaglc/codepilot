package codingagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/eaglc/codepilot/internal/llm"
	"github.com/eaglc/codepilot/internal/tool"
)

const workspaceContextToolName = "request_workspace_context"

type workspaceContextTool struct {
	turns  TurnRepository
	turnID TurnID
}

func (*workspaceContextTool) Definition() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        workspaceContextToolName,
		Description: "Declare that the requested Plan materially depends on facts from the current workspace and hand off to a read-only workspace planning profile. Do not call this for general knowledge, content, travel, writing, or other workspace-independent plans.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"reason":{"type":"string"}},"required":["reason"],"additionalProperties":false}`),
	}
}

func (*workspaceContextTool) ReplayPolicy() tool.ReplayPolicy { return tool.ReplayIdempotent }

func (*workspaceContextTool) ControlPolicy() tool.ControlPolicy {
	return tool.ControlPolicy{Exclusive: true, HandoffAfterExecution: true}
}

func (t *workspaceContextTool) Execute(ctx context.Context, call tool.Call, _ tool.ProgressSink) (tool.Result, error) {
	if t == nil || t.turns == nil || t.turnID == "" {
		return tool.Result{}, errors.New("request workspace context: trusted Turn scope is incomplete")
	}
	var arguments struct {
		Reason string `json:"reason"`
	}
	decoder := json.NewDecoder(bytes.NewReader(call.Arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&arguments); err != nil {
		return workspaceContextInvalidResult("The workspace relevance request is not valid structured data."), nil
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return workspaceContextInvalidResult("The workspace relevance request contains trailing data."), nil
	}
	arguments.Reason = strings.TrimSpace(arguments.Reason)
	if arguments.Reason == "" || len(arguments.Reason) > 1024 || strings.ContainsRune(arguments.Reason, 0) {
		return workspaceContextInvalidResult("A concise workspace relevance reason is required."), nil
	}
	turn, err := t.turns.LoadTurn(ctx, t.turnID)
	if err != nil {
		return tool.Result{}, fmt.Errorf("request workspace context: load Product Turn: %w", err)
	}
	binding, found := turn.ActiveRun()
	if !found || turn.Phase != TurnPhasePlanning || turn.Status != TurnRunning || binding.Profile != CapabilityPlan {
		return workspaceContextInvalidResult("Workspace context can only be requested from the initial read-only Plan profile."), nil
	}
	return tool.Result{
		Status:  tool.ResultCompleted,
		Content: []llm.Content{{Type: llm.ContentText, Text: "The Plan depends on the current workspace. Return control so the coordinator can enable read-only workspace context."}},
		Details: json.RawMessage(`{"workspace_relevant":true}`),
	}, nil
}

func workspaceContextInvalidResult(message string) tool.Result {
	return tool.Result{Status: tool.ResultInvalid, Content: []llm.Content{{Type: llm.ContentText, Text: message}}}
}
