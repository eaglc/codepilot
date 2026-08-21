package agent

import (
	"context"
	"encoding/json"

	"github.com/eaglc/codepilot/internal/session"
	"github.com/eaglc/codepilot/internal/tool"
)

var _ tool.Tool = (*gitStatusTool)(nil)

// gitStatusTool exposes status only for the worktree captured at turn start.
type gitStatusTool struct {
	scope      session.TurnScope
	workspaces WorkspaceTools
}

type gitStatusArguments struct{}

type gitStatusOutput struct {
	Branch        string                 `json:"branch"`
	HeadCommit    string                 `json:"head_commit"`
	Entries       []gitStatusEntryOutput `json:"entries"`
	Dirty         bool                   `json:"dirty"`
	HiddenEntries int                    `json:"hidden_entries"`
	Truncated     bool                   `json:"truncated"`
}

type gitStatusEntryOutput struct {
	Path   string `json:"path"`
	Status string `json:"status"`
}

func (t *gitStatusTool) Definition() tool.Definition {
	return definition("git_status", "Read bounded, non-sensitive Git status for the current worktree.", `{
  "type":"object",
  "properties":{},
  "additionalProperties":false
}`)
}

func (t *gitStatusTool) Invoke(ctx context.Context, arguments json.RawMessage) (tool.Result, error) {
	var parsed gitStatusArguments
	if invalid := decodeToolArguments(arguments, &parsed); invalid != nil {
		return *invalid, nil
	}
	result, err := t.workspaces.GitStatus(ctx, GitStatusRequest{WorktreeRoot: t.scope.WorktreeRoot})
	if err != nil {
		return normalizeToolError(ctx, err)
	}
	output := gitStatusOutput{
		Branch:        result.Branch,
		HeadCommit:    result.HeadCommit,
		Entries:       make([]gitStatusEntryOutput, 0, len(result.Entries)),
		Dirty:         result.Dirty,
		HiddenEntries: result.HiddenEntries,
		Truncated:     result.Truncated,
	}
	for _, entry := range result.Entries {
		output.Entries = append(output.Entries, gitStatusEntryOutput{Path: entry.Path, Status: entry.Status})
	}
	return completedJSONResult(output, t.scope.Limits.ToolResultMaxBytes), nil
}
