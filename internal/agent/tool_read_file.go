package agent

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/eaglc/codepilot/internal/session"
	"github.com/eaglc/codepilot/internal/tool"
)

var _ tool.Tool = (*readFileTool)(nil)

// readFileTool prevents the model from supplying or replacing the trusted root.
type readFileTool struct {
	scope      session.TurnScope
	workspaces WorkspaceTools
}

type readFileArguments struct {
	Path      string `json:"path"`
	StartLine int    `json:"start_line,omitempty"`
	LineCount int    `json:"line_count,omitempty"`
}

type readFileOutput struct {
	Path            string `json:"path"`
	Content         string `json:"content"`
	StartLine       int    `json:"start_line"`
	EndLine         int    `json:"end_line"`
	TotalLines      int    `json:"total_lines"`
	TotalLinesKnown bool   `json:"total_lines_known"`
	Truncated       bool   `json:"truncated"`
}

func (t *readFileTool) Definition() tool.Definition {
	return definition("read_file", "Read a bounded line range from one non-sensitive worktree file.", `{
  "type":"object",
  "properties":{
    "path":{"type":"string","minLength":1,"maxLength":4096,"description":"Worktree-relative file path."},
    "start_line":{"type":"integer","minimum":0,"description":"Optional one-based start line; zero starts at line one."},
    "line_count":{"type":"integer","minimum":0,"maximum":10000,"description":"Optional number of lines; zero uses the workspace default."}
  },
  "required":["path"],
  "additionalProperties":false
}`)
}

func (t *readFileTool) Invoke(ctx context.Context, arguments json.RawMessage) (tool.Result, error) {
	var parsed readFileArguments
	if invalid := decodeToolArguments(arguments, &parsed); invalid != nil {
		return *invalid, nil
	}
	if strings.TrimSpace(parsed.Path) == "" || len(parsed.Path) > 4096 || parsed.StartLine < 0 || parsed.LineCount < 0 || parsed.LineCount > 10000 {
		return invalidArgument("The file path or line range is outside the declared bounds.")
	}
	result, err := t.workspaces.ReadFile(ctx, ReadFileRequest{
		WorktreeRoot: t.scope.WorktreeRoot,
		Path:         parsed.Path,
		StartLine:    parsed.StartLine,
		LineCount:    parsed.LineCount,
	})
	if err != nil {
		return normalizeToolError(ctx, err)
	}
	output := readFileOutput{
		Path:            result.Path,
		Content:         result.Content,
		StartLine:       result.StartLine,
		EndLine:         result.EndLine,
		TotalLines:      result.TotalLines,
		TotalLinesKnown: result.TotalLinesKnown,
		Truncated:       result.Truncated,
	}
	return completedJSONResult(output, t.scope.Limits.ToolResultMaxBytes), nil
}
