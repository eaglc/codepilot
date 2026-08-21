package agent

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/eaglc/codepilot/internal/session"
	"github.com/eaglc/codepilot/internal/tool"
)

var _ tool.Tool = (*gitDiffTool)(nil)

// gitDiffTool combines a trusted worktree with evidence produced during this
// turn so session diffs cannot claim unrelated user changes.
type gitDiffTool struct {
	scope      session.TurnScope
	workspaces WorkspaceTools
	state      *turnToolState
}

type gitDiffArguments struct {
	Kind  session.DiffKind `json:"kind"`
	Files []string         `json:"files,omitempty"`
}

type diffOutput struct {
	Kind      session.DiffKind `json:"kind"`
	Text      string           `json:"text"`
	Files     []diffFileOutput `json:"files"`
	Truncated bool             `json:"truncated"`
	Drifted   bool             `json:"drifted"`
}

type diffFileOutput struct {
	Path      string `json:"path"`
	Status    string `json:"status"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
}

func (t *gitDiffTool) Definition() tool.Definition {
	return definition("git_diff", "Read a proposed, current-turn session, or whole-worktree diff without exposing sensitive files.", `{
  "type":"object",
  "properties":{
    "kind":{"type":"string","enum":["proposed","session","workspace"]},
    "files":{"type":"array","maxItems":500,"items":{"type":"string","minLength":1,"maxLength":4096}}
  },
  "required":["kind"],
  "additionalProperties":false
}`)
}

func (t *gitDiffTool) Invoke(ctx context.Context, arguments json.RawMessage) (tool.Result, error) {
	var parsed gitDiffArguments
	if invalid := decodeToolArguments(arguments, &parsed); invalid != nil {
		return *invalid, nil
	}
	if parsed.Kind != session.DiffProposed && parsed.Kind != session.DiffSession && parsed.Kind != session.DiffWorkspace {
		return invalidArgument("The diff kind must be proposed, session, or workspace.")
	}
	if len(parsed.Files) > 500 {
		return invalidArgument("The diff selects too many files.")
	}
	for _, pathValue := range parsed.Files {
		if err := ensureString(pathValue, "diff file path", 4096); err != nil {
			return invalidArgument("A diff file path is empty or exceeds its size limit.")
		}
	}
	if parsed.Kind == session.DiffProposed {
		if len(parsed.Files) != 0 {
			return invalidArgument("Proposed diffs do not accept a file filter.")
		}
		value, exists := t.state.proposedDiff()
		if !exists {
			value = session.DiffResult{Kind: session.DiffProposed}
		}
		return completedJSONResult(newDiffOutput(value), t.scope.Limits.ToolResultMaxBytes), nil
	}
	request := session.DiffRequest{
		WorktreeRoot: t.scope.WorktreeRoot,
		SessionID:    t.scope.SessionID,
		Kind:         parsed.Kind,
		Files:        append([]string(nil), parsed.Files...),
	}
	if parsed.Kind == session.DiffSession {
		files, hashes := t.state.patchSnapshot()
		request.Files = intersectToolPaths(parsed.Files, files)
		request.ExpectedHashes = hashes
	}
	result, err := t.workspaces.ReadDiff(ctx, request)
	if err != nil {
		return normalizeToolError(ctx, err)
	}
	return completedJSONResult(newDiffOutput(result), t.scope.Limits.ToolResultMaxBytes), nil
}

func newDiffOutput(value session.DiffResult) diffOutput {
	output := diffOutput{
		Kind:      value.Kind,
		Text:      value.Text,
		Files:     make([]diffFileOutput, 0, len(value.Files)),
		Truncated: value.Truncated,
		Drifted:   value.Drifted,
	}
	for _, file := range value.Files {
		output.Files = append(output.Files, diffFileOutput{Path: file.Path, Status: file.Status, Additions: file.Additions, Deletions: file.Deletions})
	}
	return output
}

func intersectToolPaths(requested []string, available []string) []string {
	if len(requested) == 0 {
		return append([]string(nil), available...)
	}
	allowed := make(map[string]struct{}, len(available))
	for _, value := range available {
		allowed[value] = struct{}{}
	}
	result := make([]string, 0, len(requested))
	seen := make(map[string]struct{}, len(requested))
	for _, value := range requested {
		if _, exists := allowed[value]; !exists {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
