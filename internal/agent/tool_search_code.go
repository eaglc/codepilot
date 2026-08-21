package agent

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/eaglc/codepilot/internal/session"
	"github.com/eaglc/codepilot/internal/tool"
)

var _ tool.Tool = (*searchCodeTool)(nil)

// searchCodeTool binds bounded search arguments to the trusted current worktree.
type searchCodeTool struct {
	scope      session.TurnScope
	workspaces WorkspaceTools
}

type searchCodeArguments struct {
	Query string `json:"query"`
	Regex bool   `json:"regex,omitempty"`
	Glob  string `json:"glob,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

type searchCodeOutput struct {
	Matches   []searchMatchOutput `json:"matches"`
	Truncated bool                `json:"truncated"`
}

type searchMatchOutput struct {
	Path   string `json:"path"`
	Line   int    `json:"line"`
	Column int    `json:"column"`
	Text   string `json:"text"`
}

func (t *searchCodeTool) Definition() tool.Definition {
	return definition("search_code", "Search bounded source lines in non-sensitive files in the current worktree.", `{
  "type":"object",
  "properties":{
    "query":{"type":"string","minLength":1,"maxLength":1024},
    "regex":{"type":"boolean","default":false},
    "glob":{"type":"string","maxLength":1024,"description":"Optional worktree-relative file glob."},
    "limit":{"type":"integer","minimum":0,"maximum":200,"description":"Zero uses the workspace default."}
  },
  "required":["query"],
  "additionalProperties":false
}`)
}

func (t *searchCodeTool) Invoke(ctx context.Context, arguments json.RawMessage) (tool.Result, error) {
	var parsed searchCodeArguments
	if invalid := decodeToolArguments(arguments, &parsed); invalid != nil {
		return *invalid, nil
	}
	if strings.TrimSpace(parsed.Query) == "" || len(parsed.Query) > 1024 || len(parsed.Glob) > 1024 || parsed.Limit < 0 || parsed.Limit > 200 {
		return invalidArgument("The search query, glob, or limit is outside the declared bounds.")
	}
	result, err := t.workspaces.SearchCode(ctx, SearchCodeRequest{
		WorktreeRoot: t.scope.WorktreeRoot,
		Query:        parsed.Query,
		Regex:        parsed.Regex,
		Glob:         parsed.Glob,
		Limit:        parsed.Limit,
	})
	if err != nil {
		return normalizeToolError(ctx, err)
	}
	output := searchCodeOutput{Matches: make([]searchMatchOutput, 0, len(result.Matches)), Truncated: result.Truncated}
	for _, match := range result.Matches {
		output.Matches = append(output.Matches, searchMatchOutput{Path: match.Path, Line: match.Line, Column: match.Column, Text: match.Text})
	}
	return completedJSONResult(output, t.scope.Limits.ToolResultMaxBytes), nil
}
