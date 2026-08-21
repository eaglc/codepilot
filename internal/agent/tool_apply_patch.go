package agent

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/eaglc/codepilot/internal/session"
	"github.com/eaglc/codepilot/internal/tool"
)

var _ tool.Tool = (*applyPatchTool)(nil)

// applyPatchTool supplies the trusted turn identity and permission mode; the
// model controls only the patch body and its stated intent.
type applyPatchTool struct {
	scope      session.TurnScope
	workspaces WorkspaceTools
	state      *turnToolState
}

type applyPatchArguments struct {
	Patch  string `json:"patch"`
	Intent string `json:"intent"`
}

type applyPatchOutput struct {
	Applied      bool               `json:"applied"`
	Denied       bool               `json:"denied"`
	Reason       string             `json:"reason,omitempty"`
	ProposedDiff diffOutput         `json:"proposed_diff"`
	PatchRecord  *patchRecordOutput `json:"patch_record,omitempty"`
}

type patchRecordOutput struct {
	ID        string              `json:"id"`
	SessionID string              `json:"session_id"`
	TurnID    string              `json:"turn_id"`
	Files     []patchedFileOutput `json:"files"`
	AppliedAt time.Time           `json:"applied_at"`
}

type patchedFileOutput struct {
	Path       string `json:"path"`
	BeforeHash string `json:"before_hash"`
	AfterHash  string `json:"after_hash"`
}

func (t *applyPatchTool) Definition() tool.Definition {
	return definition("apply_patch", "Validate and propose one unified diff, then apply it only when the current permission policy allows.", `{
  "type":"object",
  "properties":{
    "patch":{"type":"string","minLength":1,"maxLength":1048576,"description":"Unified diff text."},
    "intent":{"type":"string","minLength":1,"maxLength":500,"description":"Concise reason for the change."}
  },
  "required":["patch","intent"],
  "additionalProperties":false
}`)
}

func (t *applyPatchTool) Invoke(ctx context.Context, arguments json.RawMessage) (tool.Result, error) {
	var parsed applyPatchArguments
	if invalid := decodeToolArguments(arguments, &parsed); invalid != nil {
		return *invalid, nil
	}
	if strings.TrimSpace(parsed.Patch) == "" || len(parsed.Patch) > 1<<20 || strings.TrimSpace(parsed.Intent) == "" || len([]rune(parsed.Intent)) > 500 {
		return invalidArgument("The patch or intent is empty or exceeds its declared size limit.")
	}
	result, err := t.workspaces.ApplyPatch(ctx, ApplyPatchRequest{
		WorktreeRoot:   t.scope.WorktreeRoot,
		SessionID:      t.scope.SessionID,
		TurnID:         t.scope.TurnID,
		PermissionMode: t.scope.PermissionMode,
		Patch:          parsed.Patch,
		Intent:         parsed.Intent,
	})
	if result.ProposedDiff.Kind != "" {
		t.state.recordProposed(result.ProposedDiff)
	}
	output := newApplyPatchOutput(result)
	if err != nil {
		normalized, normalizedErr := normalizeToolError(ctx, err)
		if normalized.Status == tool.ResultInterrupted {
			encoded := completedJSONResult(output, t.scope.Limits.ToolResultMaxBytes)
			normalized.Data = encoded.Data
		}
		return normalized, normalizedErr
	}
	if result.Denied {
		return deniedToolResult(result.Reason, output, t.scope.Limits.ToolResultMaxBytes), nil
	}
	if result.Applied {
		t.state.recordPatch(result.PatchRecord)
	}
	return completedJSONResult(output, t.scope.Limits.ToolResultMaxBytes), nil
}

func newApplyPatchOutput(value ApplyPatchResult) applyPatchOutput {
	output := applyPatchOutput{
		Applied:      value.Applied,
		Denied:       value.Denied,
		Reason:       value.Reason,
		ProposedDiff: newDiffOutput(value.ProposedDiff),
	}
	if value.PatchRecord.ID != "" {
		record := patchRecordOutput{
			ID:        string(value.PatchRecord.ID),
			SessionID: string(value.PatchRecord.SessionID),
			TurnID:    string(value.PatchRecord.TurnID),
			Files:     make([]patchedFileOutput, 0, len(value.PatchRecord.Files)),
			AppliedAt: value.PatchRecord.AppliedAt,
		}
		for _, file := range value.PatchRecord.Files {
			record.Files = append(record.Files, patchedFileOutput{Path: file.Path, BeforeHash: file.BeforeHash, AfterHash: file.AfterHash})
		}
		output.PatchRecord = &record
	}
	return output
}
