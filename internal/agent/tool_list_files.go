package agent

import (
	"context"
	"encoding/json"

	"github.com/eaglc/codepilot/internal/session"
	"github.com/eaglc/codepilot/internal/tool"
)

var _ tool.Tool = (*listFilesTool)(nil)

// listFilesTool binds model-controlled filters to the trusted current worktree.
type listFilesTool struct {
	scope      session.TurnScope
	workspaces WorkspaceTools
}

type listFilesArguments struct {
	Pattern string `json:"pattern,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}

type listFilesOutput struct {
	Files     []fileInfoOutput `json:"files"`
	Truncated bool             `json:"truncated"`
}

type fileInfoOutput struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

func (t *listFilesTool) Definition() tool.Definition {
	return definition("list_files", "List bounded, non-sensitive files visible in the current Git worktree.", `{
  "type":"object",
  "properties":{
    "pattern":{"type":"string","maxLength":1024,"description":"Optional worktree-relative glob."},
    "limit":{"type":"integer","minimum":0,"maximum":500,"description":"Optional maximum number of files; zero uses the workspace default."}
  },
  "additionalProperties":false
}`)
}

func (t *listFilesTool) Invoke(ctx context.Context, arguments json.RawMessage) (tool.Result, error) {
	var parsed listFilesArguments
	if invalid := decodeToolArguments(arguments, &parsed); invalid != nil {
		return *invalid, nil
	}
	if len(parsed.Pattern) > 1024 || parsed.Limit < 0 || parsed.Limit > 500 {
		return invalidArgument("The file pattern or limit is outside the declared bounds.")
	}
	result, err := t.workspaces.ListFiles(ctx, ListFilesRequest{WorktreeRoot: t.scope.WorktreeRoot, Pattern: parsed.Pattern, Limit: parsed.Limit})
	if err != nil {
		return normalizeToolError(ctx, err)
	}
	output := listFilesOutput{Files: make([]fileInfoOutput, 0, len(result.Files)), Truncated: result.Truncated}
	for _, file := range result.Files {
		output.Files = append(output.Files, fileInfoOutput{Path: file.Path, Size: file.Size})
	}
	return completedJSONResult(output, t.scope.Limits.ToolResultMaxBytes), nil
}
