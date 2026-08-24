package codingtools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	gotextdiff "github.com/hexops/gotextdiff"
	"github.com/hexops/gotextdiff/myers"
	"github.com/hexops/gotextdiff/span"

	"github.com/eaglc/codepilot/internal/codingagent"
	"github.com/eaglc/codepilot/internal/llm"
	"github.com/eaglc/codepilot/internal/tool"
)

type replaceFileTool struct {
	root          string
	maxTargetSize int64
	patch         *applyPatchTool
}

type replaceFileArguments struct {
	Path           string `json:"path"`
	Content        string `json:"content"`
	ExpectedSHA256 string `json:"expected_sha256"`
	LineEnding     string `json:"line_ending"`
	Intent         string `json:"intent"`
}

type preparedFileReplacement struct {
	arguments replaceFileArguments
	edit      preparedFileEdit
}

func (*replaceFileTool) Definition() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "replace_file",
		Description: "Replace the complete contents of one existing UTF-8 worktree file. Use this for intentional whole-file rewrites instead of constructing a large unified diff. Line endings default to the file's existing style. expected_sha256 is optional optimistic concurrency protection; changes are previewed and permission-controlled.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","minLength":1},"content":{"type":"string"},"expected_sha256":{"type":"string","pattern":"^[0-9a-fA-F]{64}$"},"line_ending":{"type":"string","enum":["preserve","lf","crlf"]},"intent":{"type":"string"}},"required":["path","content"],"additionalProperties":false}`),
	}
}

func (*replaceFileTool) ReplayPolicy() tool.ReplayPolicy { return tool.ReplayNever }

func (t *replaceFileTool) Execute(ctx context.Context, call tool.Call, _ tool.ProgressSink) (tool.Result, error) {
	prepared, result, err := t.prepare(call.Arguments)
	if err != nil || result.Status != "" {
		return result, err
	}
	return t.apply(ctx, prepared.edit, prepared.edit.state)
}

func (t *replaceFileTool) PermissionRequirement(_ context.Context, call tool.Call) (permissionRequirement, tool.Result, error) {
	prepared, result, err := t.prepare(call.Arguments)
	if err != nil || result.Status != "" {
		return permissionRequirement{}, result, err
	}
	files := []string{prepared.edit.relative}
	approval, err := t.patch.approval(call, patchArguments{Patch: prepared.edit.diff, Intent: prepared.arguments.Intent}, files, prepared.edit.state)
	if err != nil {
		return permissionRequirement{}, tool.Result{}, err
	}
	return permissionRequirement{
		required: true,
		request: codingagent.PermissionRequest{
			ToolName: "replace_file", Action: codingagent.PermissionActionModify, Paths: files,
		},
		automatic:       t.patch.validateAutomaticPatch(prepared.edit.diff, files) == nil,
		readOnlyMessage: "Workspace edits are disabled for this session.",
		approval:        approval,
	}, tool.Result{}, nil
}

func (t *replaceFileTool) Resume(ctx context.Context, call tool.Call, interrupt tool.Interrupt, resolution tool.Result, _ tool.ProgressSink) (tool.Result, error) {
	if resolution.Status != tool.ResultCompleted {
		return resolution.Clone(), nil
	}
	if interrupt.Kind != "approval" {
		return failedResult("The pending file replacement has an unsupported interrupt type."), nil
	}
	var payload patchApprovalPayload
	if err := json.Unmarshal(interrupt.Payload, &payload); err != nil || payload.Kind != "coding_patch_approval_v1" || payload.Version != 1 {
		return failedResult("The saved file-replacement approval request is invalid."), nil
	}
	if payload.Digest == "" || payload.Digest != approvalDigest(payload) || interrupt.ID != approvalID(call, payload.Digest) {
		return failedResult("The saved file-replacement approval request failed its integrity check."), nil
	}
	current, err := t.patch.fileState(payload.Files)
	if err != nil {
		return failedResult(err.Error()), nil
	}
	if !equalFileState(current, payload.BeforeState) {
		return failedResult("The worktree changed after approval was requested; the file was not replaced."), nil
	}
	prepared, result, err := t.prepare(call.Arguments)
	if err != nil {
		return tool.Result{}, err
	}
	if result.Status != "" {
		return failedResult("The original file replacement is no longer valid."), nil
	}
	if len(payload.Files) != 1 || payload.Files[0] != prepared.edit.relative || payload.Patch != prepared.edit.diff || payload.Intent != prepared.arguments.Intent {
		return failedResult("The approved file replacement does not match the original tool call."), nil
	}
	return t.apply(ctx, prepared.edit, payload.BeforeState)
}

func (t *replaceFileTool) prepare(raw json.RawMessage) (preparedFileReplacement, tool.Result, error) {
	if t.patch == nil || t.patch.security == nil {
		return preparedFileReplacement{}, tool.Result{}, fmt.Errorf("replace_file is not configured")
	}
	var arguments replaceFileArguments
	if err := decodeArguments(raw, &arguments); err != nil {
		return preparedFileReplacement{}, invalidResult(err.Error()), nil
	}
	arguments.Path = strings.TrimSpace(strings.ReplaceAll(arguments.Path, "\\", "/"))
	arguments.ExpectedSHA256 = strings.ToLower(strings.TrimSpace(arguments.ExpectedSHA256))
	arguments.LineEnding = strings.ToLower(strings.TrimSpace(arguments.LineEnding))
	arguments.Intent = strings.TrimSpace(arguments.Intent)
	if arguments.Path == "" {
		return preparedFileReplacement{}, invalidResult("replace_file requires a non-empty worktree-relative path."), nil
	}
	if arguments.LineEnding == "" {
		arguments.LineEnding = "preserve"
	}
	if arguments.LineEnding != "preserve" && arguments.LineEnding != "lf" && arguments.LineEnding != "crlf" {
		return preparedFileReplacement{}, invalidResult("line_ending must be preserve, lf, or crlf."), nil
	}
	if arguments.ExpectedSHA256 != "" {
		decoded, err := hex.DecodeString(arguments.ExpectedSHA256)
		if err != nil || len(decoded) != sha256.Size {
			return preparedFileReplacement{}, invalidResult("expected_sha256 must be one 64-character hexadecimal SHA-256 value."), nil
		}
	}
	if !utf8.ValidString(arguments.Content) || strings.ContainsRune(arguments.Content, 0) {
		return preparedFileReplacement{}, invalidResult("replace_file accepts only valid UTF-8 content without NUL bytes."), nil
	}
	if t.patch.security.IsSensitivePath(arguments.Path) {
		return preparedFileReplacement{}, deniedResult("Sensitive file " + arguments.Path + " cannot be modified by the Agent."), nil
	}
	absolute, relative, err := secureExistingPath(t.root, arguments.Path)
	if err != nil {
		return preparedFileReplacement{}, invalidResult(err.Error()), nil
	}
	relative = filepath.ToSlash(relative)
	if err := validatePatchPath(t.root, relative); err != nil {
		return preparedFileReplacement{}, invalidResult(err.Error()), nil
	}
	info, err := os.Stat(absolute)
	if err != nil || !info.Mode().IsRegular() {
		return preparedFileReplacement{}, invalidResult("The replacement target is not an available regular file."), nil
	}
	if info.Size() > t.maxTargetSize {
		return preparedFileReplacement{}, invalidResult(fmt.Sprintf("The replacement target exceeds the %d-byte limit.", t.maxTargetSize)), nil
	}
	content, err := os.ReadFile(absolute)
	if err != nil {
		return preparedFileReplacement{}, tool.Result{}, fmt.Errorf("read replacement target %s: %w", relative, err)
	}
	if !utf8.Valid(content) || strings.IndexByte(string(content), 0) >= 0 {
		return preparedFileReplacement{}, invalidResult("replace_file can modify only UTF-8 text files."), nil
	}
	digest := sha256.Sum256(content)
	if arguments.ExpectedSHA256 != "" && arguments.ExpectedSHA256 != hex.EncodeToString(digest[:]) {
		return preparedFileReplacement{}, failedResult("The file has changed since it was read; replace_file did not overwrite it."), nil
	}
	before := string(content)
	lineEnding := "\n"
	switch arguments.LineEnding {
	case "preserve":
		lineEnding = preferredLineEnding(before)
	case "crlf":
		lineEnding = "\r\n"
	}
	after := convertLogicalLineEndings(arguments.Content, lineEnding)
	if int64(len(after)) > t.maxTargetSize {
		return preparedFileReplacement{}, invalidResult(fmt.Sprintf("The replacement content exceeds the %d-byte limit.", t.maxTargetSize)), nil
	}
	if after == before {
		return preparedFileReplacement{}, invalidResult("The replacement content is identical to the current file."), nil
	}
	edits := myers.ComputeEdits(span.URIFromPath(relative), before, after)
	diff := normalizeGeneratedDiff(fmt.Sprint(gotextdiff.ToUnified("a/"+relative, "b/"+relative, before, edits)))
	if strings.TrimSpace(diff) == "" || len(diff) > t.patch.maxBytes {
		return preparedFileReplacement{}, invalidResult("The generated replacement preview is empty or exceeds the patch limit."), nil
	}
	if t.patch.security.ContainsSecret(diff) {
		return preparedFileReplacement{}, deniedResult("File replacements containing recognized secret values are blocked; edit the secret locally outside CodePilot."), nil
	}
	state, err := t.patch.fileState([]string{relative})
	if err != nil {
		return preparedFileReplacement{}, tool.Result{}, err
	}
	prepared := preparedFileEdit{
		arguments: editFileArguments{Path: arguments.Path, Intent: arguments.Intent}, absolute: absolute, relative: relative,
		before: before, after: after, diff: diff, mode: info.Mode().Perm(), state: state,
	}
	return preparedFileReplacement{arguments: arguments, edit: prepared}, tool.Result{}, nil
}

func (t *replaceFileTool) apply(ctx context.Context, edit preparedFileEdit, expected map[string]string) (tool.Result, error) {
	current, err := t.patch.fileState([]string{edit.relative})
	if err != nil {
		return failedResult(err.Error()), nil
	}
	if !equalFileState(current, expected) {
		return failedResult("The worktree changed while the file replacement was being prepared; the file was not modified."), nil
	}
	content, err := os.ReadFile(edit.absolute)
	if err != nil || string(content) != edit.before {
		return failedResult("The replacement target changed before it could be written; the file was not modified."), nil
	}
	if err := ctx.Err(); err != nil {
		return tool.Result{}, err
	}
	if err := replaceFile(edit.absolute, []byte(edit.after), edit.mode); err != nil {
		return failedResult("The replacement could not be written: " + boundedText(err.Error(), 4096)), nil
	}
	details, err := json.Marshal(patchToolDetails{
		Kind: patchDetailKind, Detail: "Replaced 1 file.", Diff: &patchDiffDetail{Text: edit.diff, Files: []string{edit.relative}},
	})
	if err != nil {
		return tool.Result{}, err
	}
	return completedResult("Replaced "+edit.relative, details), nil
}

var _ tool.ResumableTool = (*replaceFileTool)(nil)
