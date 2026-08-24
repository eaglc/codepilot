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

	gotextdiff "github.com/hexops/gotextdiff"
	"github.com/hexops/gotextdiff/myers"
	"github.com/hexops/gotextdiff/span"

	"github.com/eaglc/codepilot/internal/codingagent"
	"github.com/eaglc/codepilot/internal/llm"
	"github.com/eaglc/codepilot/internal/tool"
)

type editFileTool struct {
	root          string
	maxTargetSize int64
	patch         *applyPatchTool
}

type editFileArguments struct {
	Path    string `json:"path"`
	OldText string `json:"old_text"`
	NewText string `json:"new_text"`
	Intent  string `json:"intent"`
}

type preparedFileEdit struct {
	arguments editFileArguments
	absolute  string
	relative  string
	before    string
	after     string
	diff      string
	mode      os.FileMode
	state     map[string]string
}

func (*editFileTool) Definition() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "edit_file",
		Description: "Replace one exact text occurrence in one existing UTF-8 worktree file. Read the file first and copy old_text exactly. The file is edited directly without translating the replacement through git apply; the product permission policy may require approval.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","minLength":1},"old_text":{"type":"string","minLength":1},"new_text":{"type":"string"},"intent":{"type":"string"}},"required":["path","old_text","new_text"],"additionalProperties":false}`),
	}
}

func (*editFileTool) ReplayPolicy() tool.ReplayPolicy { return tool.ReplayNever }

func (t *editFileTool) Execute(ctx context.Context, call tool.Call, _ tool.ProgressSink) (tool.Result, error) {
	edit, result, err := t.prepare(call.Arguments)
	if err != nil || result.Status != "" {
		return result, err
	}
	return t.apply(ctx, edit, edit.state)
}

func (t *editFileTool) PermissionRequirement(_ context.Context, call tool.Call) (permissionRequirement, tool.Result, error) {
	edit, result, err := t.prepare(call.Arguments)
	if err != nil || result.Status != "" {
		return permissionRequirement{}, result, err
	}
	files := []string{edit.relative}
	approval, err := t.patch.approval(call, patchArguments{Patch: edit.diff, Intent: edit.arguments.Intent}, files, edit.state)
	if err != nil {
		return permissionRequirement{}, tool.Result{}, err
	}
	return permissionRequirement{
		required: true,
		request: codingagent.PermissionRequest{
			ToolName: "edit_file", Action: codingagent.PermissionActionModify, Paths: files,
		},
		automatic:       t.patch.validateAutomaticPatch(edit.diff, files) == nil,
		readOnlyMessage: "Workspace edits are disabled for this session.",
		approval:        approval,
	}, tool.Result{}, nil
}

func (t *editFileTool) Resume(ctx context.Context, call tool.Call, interrupt tool.Interrupt, resolution tool.Result, _ tool.ProgressSink) (tool.Result, error) {
	if resolution.Status != tool.ResultCompleted {
		return resolution, nil
	}
	if interrupt.Kind != "approval" {
		return failedResult("The pending edit has an unsupported interrupt type."), nil
	}
	var payload patchApprovalPayload
	if err := json.Unmarshal(interrupt.Payload, &payload); err != nil || payload.Kind != "coding_patch_approval_v1" || payload.Version != 1 {
		return failedResult("The saved edit approval request is invalid."), nil
	}
	if payload.Digest == "" || payload.Digest != approvalDigest(payload) || interrupt.ID != approvalID(call, payload.Digest) {
		return failedResult("The saved edit approval request failed its integrity check."), nil
	}
	current, err := t.patch.fileState(payload.Files)
	if err != nil {
		return failedResult(err.Error()), nil
	}
	if !equalFileState(current, payload.BeforeState) {
		return failedResult("The worktree changed after approval was requested; the edit was not applied."), nil
	}
	edit, result, err := t.prepare(call.Arguments)
	if err != nil {
		return tool.Result{}, err
	}
	if result.Status != "" {
		return failedResult("The original exact edit is no longer valid."), nil
	}
	if len(payload.Files) != 1 || payload.Files[0] != edit.relative || payload.Patch != edit.diff || payload.Intent != edit.arguments.Intent {
		return failedResult("The approved edit does not match the original tool call."), nil
	}
	return t.apply(ctx, edit, payload.BeforeState)
}

func (t *editFileTool) prepare(raw json.RawMessage) (preparedFileEdit, tool.Result, error) {
	if t.patch == nil || t.patch.security == nil {
		return preparedFileEdit{}, tool.Result{}, errors.New("edit_file is not configured")
	}
	var arguments editFileArguments
	if err := decodeArguments(raw, &arguments); err != nil {
		return preparedFileEdit{}, invalidResult(err.Error()), nil
	}
	arguments.Path = strings.TrimSpace(strings.ReplaceAll(arguments.Path, "\\", "/"))
	arguments.Intent = strings.TrimSpace(arguments.Intent)
	if arguments.Path == "" || arguments.OldText == "" {
		return preparedFileEdit{}, invalidResult("edit_file requires a path and one non-empty exact old_text value."), nil
	}
	if !utf8.ValidString(arguments.OldText) || !utf8.ValidString(arguments.NewText) || strings.ContainsRune(arguments.OldText, 0) || strings.ContainsRune(arguments.NewText, 0) {
		return preparedFileEdit{}, invalidResult("edit_file accepts only valid UTF-8 text without NUL bytes."), nil
	}
	if t.patch.security.IsSensitivePath(arguments.Path) {
		return preparedFileEdit{}, deniedResult("Sensitive file " + arguments.Path + " cannot be modified by the Agent."), nil
	}
	absolute, relative, err := secureExistingPath(t.root, arguments.Path)
	if err != nil {
		return preparedFileEdit{}, invalidResult(err.Error()), nil
	}
	relative = filepath.ToSlash(relative)
	if err := validatePatchPath(t.root, relative); err != nil {
		return preparedFileEdit{}, invalidResult(err.Error()), nil
	}
	info, err := os.Stat(absolute)
	if err != nil || !info.Mode().IsRegular() {
		return preparedFileEdit{}, invalidResult("The edit target is not an available regular file."), nil
	}
	if info.Size() > t.maxTargetSize {
		return preparedFileEdit{}, invalidResult(fmt.Sprintf("The edit target exceeds the %d-byte limit.", t.maxTargetSize)), nil
	}
	content, err := os.ReadFile(absolute)
	if err != nil {
		return preparedFileEdit{}, tool.Result{}, fmt.Errorf("read edit target %s: %w", relative, err)
	}
	if !utf8.Valid(content) || strings.IndexByte(string(content), 0) >= 0 {
		return preparedFileEdit{}, invalidResult("edit_file can modify only UTF-8 text files."), nil
	}
	before := string(content)
	after, matches := replaceExactText(before, arguments.OldText, arguments.NewText)
	if matches == 0 {
		return preparedFileEdit{}, invalidResult("old_text was not found exactly once, including after line-ending normalization. Read the current file and retry with an exact match."), nil
	}
	if matches > 1 {
		return preparedFileEdit{}, invalidResult(fmt.Sprintf("old_text matched %d locations. Include more surrounding text so exactly one location matches.", matches)), nil
	}
	if after == before {
		return preparedFileEdit{}, invalidResult("The requested edit does not change the file."), nil
	}
	edits := myers.ComputeEdits(span.URIFromPath(relative), before, after)
	diff := normalizeGeneratedDiff(fmt.Sprint(gotextdiff.ToUnified("a/"+relative, "b/"+relative, before, edits)))
	if strings.TrimSpace(diff) == "" {
		return preparedFileEdit{}, invalidResult("The requested edit did not produce a valid text change."), nil
	}
	if len(diff) > t.patch.maxBytes {
		return preparedFileEdit{}, invalidResult(fmt.Sprintf("The generated edit preview exceeds the %d-byte limit.", t.patch.maxBytes)), nil
	}
	if t.patch.security.ContainsSecret(diff) {
		return preparedFileEdit{}, deniedResult("Edits containing recognized secret values are blocked; edit the secret locally outside CodePilot."), nil
	}
	state, err := t.patch.fileState([]string{relative})
	if err != nil {
		return preparedFileEdit{}, tool.Result{}, err
	}
	return preparedFileEdit{
		arguments: arguments, absolute: absolute, relative: relative, before: before, after: after,
		diff: diff, mode: info.Mode().Perm(), state: state,
	}, tool.Result{}, nil
}

// normalizeGeneratedDiff keeps terminal previews independent of the edited
// file's line endings. A raw carriage return is a terminal control character,
// so preserve an unusual standalone CR as visible text instead of emitting it.
func normalizeGeneratedDiff(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	return strings.ReplaceAll(value, "\r", `\r`)
}

func (t *editFileTool) apply(ctx context.Context, edit preparedFileEdit, expected map[string]string) (tool.Result, error) {
	if err := ctx.Err(); err != nil {
		return tool.Result{}, err
	}
	current, err := t.patch.fileState([]string{edit.relative})
	if err != nil {
		return failedResult(err.Error()), nil
	}
	if !equalFileState(current, expected) {
		return failedResult("The worktree changed while the edit was being prepared; the file was not modified."), nil
	}
	content, err := os.ReadFile(edit.absolute)
	if err != nil || string(content) != edit.before {
		return failedResult("The edit target changed before it could be written; the file was not modified."), nil
	}
	if err := replaceFile(edit.absolute, []byte(edit.after), edit.mode); err != nil {
		return failedResult("The exact edit could not be written: " + boundedText(err.Error(), 4096)), nil
	}
	details, err := json.Marshal(patchToolDetails{
		Kind: patchDetailKind, Detail: "Edited 1 file.",
		Diff: &patchDiffDetail{Text: edit.diff, Files: []string{edit.relative}},
	})
	if err != nil {
		return tool.Result{}, err
	}
	return completedResult("Edited "+edit.relative, details), nil
}

func replaceFile(path string, content []byte, mode os.FileMode) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".codepilot-edit-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func replaceExactText(before, oldText, newText string) (string, int) {
	replacement := convertLogicalLineEndings(newText, preferredLineEnding(before))
	if matches := strings.Count(before, oldText); matches != 0 {
		if matches != 1 {
			return "", matches
		}
		start := strings.Index(before, oldText)
		return before[:start] + replacement + before[start+len(oldText):], 1
	}
	normalizedBefore, offsets := normalizedNewlineView(before)
	normalizedOld := strings.ReplaceAll(oldText, "\r\n", "\n")
	matches := strings.Count(normalizedBefore, normalizedOld)
	if matches != 1 {
		return "", matches
	}
	start := strings.Index(normalizedBefore, normalizedOld)
	end := start + len(normalizedOld)
	return before[:offsets[start]] + replacement + before[offsets[end]:], 1
}

func normalizedNewlineView(value string) (string, []int) {
	var normalized strings.Builder
	normalized.Grow(len(value))
	offsets := make([]int, 1, len(value)+1)
	for index := 0; index < len(value); {
		if value[index] == '\r' && index+1 < len(value) && value[index+1] == '\n' {
			normalized.WriteByte('\n')
			index += 2
			offsets = append(offsets, index)
			continue
		}
		normalized.WriteByte(value[index])
		index++
		offsets = append(offsets, index)
	}
	return normalized.String(), offsets
}

func preferredLineEnding(value string) string {
	crlf := strings.Count(value, "\r\n")
	lf := strings.Count(value, "\n") - crlf
	if crlf > lf {
		return "\r\n"
	}
	return "\n"
}

func convertLogicalLineEndings(value, lineEnding string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	if lineEnding == "\r\n" {
		return strings.ReplaceAll(value, "\n", "\r\n")
	}
	return value
}

func lineEndingLabel(value string) string {
	crlf := strings.Count(value, "\r\n")
	lf := strings.Count(value, "\n") - crlf
	switch {
	case crlf > 0 && lf > 0:
		return "mixed"
	case crlf > 0:
		return "crlf"
	case lf > 0:
		return "lf"
	default:
		return "none"
	}
}

var _ tool.ResumableTool = (*editFileTool)(nil)
