package codingtools

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/eaglc/codepilot/internal/llm"
	"github.com/eaglc/codepilot/internal/tool"
)

type gitLogTool struct {
	root      string
	maxOutput int
}

func (*gitLogTool) Definition() llm.ToolDefinition {
	return llm.ToolDefinition{Name: "git_log", Description: "List bounded recent commit metadata without reading patches or changing Git state.", InputSchema: json.RawMessage(`{"type":"object","properties":{"limit":{"type":"integer","minimum":1,"maximum":50}},"additionalProperties":false}`)}
}

func (*gitLogTool) ReplayPolicy() tool.ReplayPolicy { return tool.ReplaySafe }

func (t *gitLogTool) Execute(ctx context.Context, call tool.Call, _ tool.ProgressSink) (tool.Result, error) {
	var arguments struct {
		Limit int `json:"limit"`
	}
	if err := decodeArguments(call.Arguments, &arguments); err != nil {
		return invalidResult(err.Error()), nil
	}
	if arguments.Limit == 0 {
		arguments.Limit = 20
	}
	if arguments.Limit < 1 || arguments.Limit > 50 {
		return invalidResult("limit must be between 1 and 50."), nil
	}
	return runGit(ctx, t.root, t.maxOutput, "log", "--no-decorate", "--date=iso-strict", "--pretty=format:%H%x09%ad%x09%an%x09%s", "-n", strconv.Itoa(arguments.Limit))
}

type gitBranchesTool struct {
	root      string
	maxOutput int
}

func (*gitBranchesTool) Definition() llm.ToolDefinition {
	return llm.ToolDefinition{Name: "git_branches", Description: "List local and remote branch references without switching, creating, or deleting branches.", InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)}
}

func (*gitBranchesTool) ReplayPolicy() tool.ReplayPolicy { return tool.ReplaySafe }

func (t *gitBranchesTool) Execute(ctx context.Context, call tool.Call, _ tool.ProgressSink) (tool.Result, error) {
	var arguments struct{}
	if err := decodeArguments(call.Arguments, &arguments); err != nil {
		return invalidResult(err.Error()), nil
	}
	return runGit(ctx, t.root, t.maxOutput, "for-each-ref", "--sort=refname", "--format=%(HEAD)%09%(refname:short)%09%(objectname)%09%(upstream:short)", "refs/heads", "refs/remotes")
}

type gitShowCommitTool struct {
	root      string
	maxOutput int
}

func (*gitShowCommitTool) Definition() llm.ToolDefinition {
	return llm.ToolDefinition{Name: "git_show_commit", Description: "Show metadata and message for one exact full commit object ID. This tool never stages files or creates commits.", InputSchema: json.RawMessage(`{"type":"object","properties":{"commit_id":{"type":"string","minLength":40,"maxLength":64}},"required":["commit_id"],"additionalProperties":false}`)}
}

func (*gitShowCommitTool) ReplayPolicy() tool.ReplayPolicy { return tool.ReplaySafe }

func (t *gitShowCommitTool) Execute(ctx context.Context, call tool.Call, _ tool.ProgressSink) (tool.Result, error) {
	var arguments struct {
		CommitID string `json:"commit_id"`
	}
	if err := decodeArguments(call.Arguments, &arguments); err != nil {
		return invalidResult(err.Error()), nil
	}
	commitID, err := fullObjectID(arguments.CommitID)
	if err != nil {
		return invalidResult(err.Error()), nil
	}
	return runGit(ctx, t.root, t.maxOutput, "show", "--no-patch", "--no-decorate", "--date=iso-strict", "--pretty=fuller", commitID, "--")
}

func fullObjectID(value string) (string, error) {
	value = strings.TrimSpace(value)
	if len(value) != 40 && len(value) != 64 {
		return "", errors.New("commit_id must be a full 40- or 64-character hexadecimal object ID")
	}
	if _, err := hex.DecodeString(value); err != nil {
		return "", errors.New("commit_id must be a full hexadecimal object ID")
	}
	return strings.ToLower(value), nil
}
