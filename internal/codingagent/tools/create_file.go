package codingtools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/eaglc/codepilot/internal/codingagent"
	"github.com/eaglc/codepilot/internal/llm"
	"github.com/eaglc/codepilot/internal/tool"
)

type createFileTool struct {
	root          string
	maxTargetSize int64
	patch         *applyPatchTool
}

type createFileArguments struct {
	Path       string `json:"path"`
	Content    string `json:"content"`
	LineEnding string `json:"line_ending"`
	Intent     string `json:"intent"`
}

type preparedFileCreation struct {
	arguments createFileArguments
	absolute  string
	relative  string
	content   string
	diff      string
	state     map[string]string
}

func (*createFileTool) Definition() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "create_file",
		Description: "Create one new UTF-8 text file inside the worktree and create any missing parent directories. The target must not already exist; use edit_file or replace_file for existing files. Use real project files to establish directory structure because Git does not preserve empty directories. Changes are previewed and permission-controlled.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","minLength":1},"content":{"type":"string"},"line_ending":{"type":"string","enum":["lf","crlf"]},"intent":{"type":"string"}},"required":["path","content"],"additionalProperties":false}`),
	}
}

func (*createFileTool) ReplayPolicy() tool.ReplayPolicy { return tool.ReplayNever }

func (t *createFileTool) Execute(ctx context.Context, call tool.Call, _ tool.ProgressSink) (tool.Result, error) {
	prepared, result, err := t.prepare(call.Arguments)
	if err != nil || result.Status != "" {
		return result, err
	}
	return t.apply(ctx, prepared, prepared.state)
}

func (t *createFileTool) PermissionRequirement(_ context.Context, call tool.Call) (permissionRequirement, tool.Result, error) {
	prepared, result, err := t.prepare(call.Arguments)
	if err != nil || result.Status != "" {
		return permissionRequirement{}, result, err
	}
	files := []string{prepared.relative}
	approval, err := t.patch.approval(call, patchArguments{Patch: prepared.diff, Intent: prepared.arguments.Intent}, files, prepared.state)
	if err != nil {
		return permissionRequirement{}, tool.Result{}, err
	}
	return permissionRequirement{
		required: true,
		request: codingagent.PermissionRequest{
			ToolName: "create_file", Action: codingagent.PermissionActionModify, Paths: files,
		},
		automatic:       t.patch.validateAutomaticPatch(prepared.diff, files) == nil,
		readOnlyMessage: "Workspace edits are disabled for this session.",
		approval:        approval,
	}, tool.Result{}, nil
}

func (t *createFileTool) Resume(ctx context.Context, call tool.Call, interrupt tool.Interrupt, resolution tool.Result, _ tool.ProgressSink) (tool.Result, error) {
	if resolution.Status != tool.ResultCompleted {
		return resolution.Clone(), nil
	}
	if interrupt.Kind != "approval" {
		return failedResult("The pending file creation has an unsupported interrupt type."), nil
	}
	var payload patchApprovalPayload
	if err := json.Unmarshal(interrupt.Payload, &payload); err != nil || payload.Kind != "coding_patch_approval_v1" || payload.Version != 1 {
		return failedResult("The saved file-creation approval request is invalid."), nil
	}
	if payload.Digest == "" || payload.Digest != approvalDigest(payload) || interrupt.ID != approvalID(call, payload.Digest) {
		return failedResult("The saved file-creation approval request failed its integrity check."), nil
	}
	current, err := t.patch.fileState(payload.Files)
	if err != nil {
		return failedResult(err.Error()), nil
	}
	if !equalFileState(current, payload.BeforeState) {
		return failedResult("The worktree changed after approval was requested; the file was not created."), nil
	}
	prepared, result, err := t.prepare(call.Arguments)
	if err != nil {
		return tool.Result{}, err
	}
	if result.Status != "" {
		return failedResult("The original file creation is no longer valid."), nil
	}
	if len(payload.Files) != 1 || payload.Files[0] != prepared.relative || payload.Patch != prepared.diff || payload.Intent != prepared.arguments.Intent {
		return failedResult("The approved file creation does not match the original tool call."), nil
	}
	return t.apply(ctx, prepared, payload.BeforeState)
}

func (t *createFileTool) prepare(raw json.RawMessage) (preparedFileCreation, tool.Result, error) {
	if t == nil || t.patch == nil || t.patch.security == nil || strings.TrimSpace(t.root) == "" {
		return preparedFileCreation{}, tool.Result{}, errors.New("create_file is not configured")
	}
	var arguments createFileArguments
	if err := decodeArguments(raw, &arguments); err != nil {
		return preparedFileCreation{}, invalidResult(err.Error()), nil
	}
	arguments.Path = strings.TrimSpace(strings.ReplaceAll(arguments.Path, "\\", "/"))
	arguments.LineEnding = strings.ToLower(strings.TrimSpace(arguments.LineEnding))
	arguments.Intent = strings.TrimSpace(arguments.Intent)
	if arguments.Path == "" || strings.ContainsAny(arguments.Path, "\r\n") {
		return preparedFileCreation{}, invalidResult("create_file requires a non-empty worktree-relative path."), nil
	}
	if arguments.LineEnding == "" {
		arguments.LineEnding = "lf"
	}
	if arguments.LineEnding != "lf" && arguments.LineEnding != "crlf" {
		return preparedFileCreation{}, invalidResult("line_ending must be lf or crlf."), nil
	}
	if !utf8.ValidString(arguments.Content) || strings.ContainsRune(arguments.Content, 0) {
		return preparedFileCreation{}, invalidResult("create_file accepts only valid UTF-8 content without NUL bytes."), nil
	}
	content := convertLogicalLineEndings(arguments.Content, "\n")
	if arguments.LineEnding == "crlf" {
		content = convertLogicalLineEndings(arguments.Content, "\r\n")
	}
	if int64(len(content)) > t.maxTargetSize {
		return preparedFileCreation{}, invalidResult(fmt.Sprintf("The new file content exceeds the %d-byte limit.", t.maxTargetSize)), nil
	}
	if err := validatePatchPath(t.root, arguments.Path); err != nil {
		return preparedFileCreation{}, invalidResult(err.Error()), nil
	}
	absolute := filepath.Clean(filepath.Join(t.root, filepath.FromSlash(arguments.Path)))
	relative, err := filepath.Rel(t.root, absolute)
	if err != nil {
		return preparedFileCreation{}, invalidResult("The new file path could not be resolved inside the worktree."), nil
	}
	relative = filepath.ToSlash(relative)
	if err := validatePatchPath(t.root, relative); err != nil {
		return preparedFileCreation{}, invalidResult(err.Error()), nil
	}
	if t.patch.security.IsSensitivePath(relative) {
		return preparedFileCreation{}, deniedResult("Sensitive file " + relative + " cannot be modified by the Agent."), nil
	}
	if _, err := os.Lstat(absolute); err == nil {
		return preparedFileCreation{}, invalidResult("The create_file target already exists; use edit_file or replace_file instead."), nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return preparedFileCreation{}, invalidResult("The create_file target is unavailable."), nil
	}
	diff := newFilePreview(relative, content)
	if len(diff) > t.patch.maxBytes {
		return preparedFileCreation{}, invalidResult(fmt.Sprintf("The generated creation preview exceeds the %d-byte limit.", t.patch.maxBytes)), nil
	}
	if t.patch.security.ContainsSecret(diff) {
		return preparedFileCreation{}, deniedResult("New files containing recognized secret values are blocked; create the secret locally outside CodePilot."), nil
	}
	state, err := t.patch.fileState([]string{relative})
	if err != nil {
		return preparedFileCreation{}, tool.Result{}, err
	}
	return preparedFileCreation{
		arguments: arguments, absolute: absolute, relative: relative, content: content, diff: diff, state: state,
	}, tool.Result{}, nil
}

func (t *createFileTool) apply(ctx context.Context, prepared preparedFileCreation, expected map[string]string) (tool.Result, error) {
	if err := ctx.Err(); err != nil {
		return tool.Result{}, err
	}
	current, err := t.patch.fileState([]string{prepared.relative})
	if err != nil {
		return failedResult(err.Error()), nil
	}
	if !equalFileState(current, expected) || current[prepared.relative] != "missing" {
		return failedResult("The worktree changed while the file creation was being prepared; the file was not created."), nil
	}
	details, err := json.Marshal(patchToolDetails{
		Kind: patchDetailKind, Detail: "Created 1 file.", Diff: &patchDiffDetail{Text: prepared.diff, Files: []string{prepared.relative}},
	})
	if err != nil {
		return tool.Result{}, err
	}
	if err := os.MkdirAll(filepath.Dir(prepared.absolute), 0o755); err != nil {
		return failedResult("The parent directories could not be created: " + boundedText(err.Error(), 4096)), nil
	}
	if err := validatePatchPath(t.root, prepared.relative); err != nil {
		return failedResult("The new file path became unsafe before it could be written."), nil
	}
	file, err := os.OpenFile(prepared.absolute, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return failedResult("The new file could not be created: " + boundedText(err.Error(), 4096)), nil
	}
	created := true
	defer func() {
		if created {
			_ = os.Remove(prepared.absolute)
		}
	}()
	if _, err := file.WriteString(prepared.content); err != nil {
		_ = file.Close()
		return failedResult("The new file could not be written: " + boundedText(err.Error(), 4096)), nil
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return failedResult("The new file could not be synchronized: " + boundedText(err.Error(), 4096)), nil
	}
	if err := file.Close(); err != nil {
		return failedResult("The new file could not be finalized: " + boundedText(err.Error(), 4096)), nil
	}
	created = false
	return completedResult("Created "+prepared.relative, details), nil
}

func newFilePreview(relative, content string) string {
	var preview strings.Builder
	preview.WriteString("--- /dev/null\n+++ b/")
	preview.WriteString(relative)
	preview.WriteByte('\n')
	if content == "" {
		return preview.String()
	}
	logical := strings.ReplaceAll(content, "\r\n", "\n")
	lineCount := strings.Count(logical, "\n")
	if !strings.HasSuffix(logical, "\n") {
		lineCount++
	}
	preview.WriteString(fmt.Sprintf("@@ -0,0 +1,%d @@\n", lineCount))
	segments := strings.SplitAfter(logical, "\n")
	for index, segment := range segments {
		if segment == "" && index == len(segments)-1 {
			continue
		}
		preview.WriteByte('+')
		preview.WriteString(segment)
		if !strings.HasSuffix(segment, "\n") {
			preview.WriteString("\n\\ No newline at end of file\n")
		}
	}
	return preview.String()
}

var _ tool.ResumableTool = (*createFileTool)(nil)
