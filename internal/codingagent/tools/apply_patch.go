package codingtools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/eaglc/codepilot/internal/codingagent"
	"github.com/eaglc/codepilot/internal/llm"
	"github.com/eaglc/codepilot/internal/tool"
)

const patchDetailKind = "coding_patch_v1"

type applyPatchTool struct {
	name              string
	root              string
	security          *codingagent.SecurityPolicy
	maxBytes          int
	maxFiles          int
	maxOutput         int
	maxAutoBytes      int
	maxAutoFiles      int
	maxAutoLines      int
	maxAutoTargetSize int64
}

type patchArguments struct {
	Patch  string `json:"patch"`
	Intent string `json:"intent"`
}

type patchApprovalPayload struct {
	Kind        string            `json:"kind"`
	Version     int               `json:"version"`
	Summary     string            `json:"summary"`
	Patch       string            `json:"patch"`
	Intent      string            `json:"intent,omitempty"`
	Files       []string          `json:"files"`
	BeforeState map[string]string `json:"before_state"`
	Digest      string            `json:"digest"`
}

type patchToolDetails struct {
	Kind   string           `json:"kind"`
	Detail string           `json:"detail,omitempty"`
	Diff   *patchDiffDetail `json:"diff,omitempty"`
}

type patchDiffDetail struct {
	Text  string   `json:"text"`
	Files []string `json:"files"`
}

func (t *applyPatchTool) Definition() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        t.toolName(),
		Description: "Apply an advanced bounded unified diff, primarily for atomic multi-file changes to existing files. Use create_file for new text files and edit_file for ordinary single-file replacements. Mode, rename, and copy metadata are not supported. The product permission policy may require user approval before any file is changed.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"patch":{"type":"string","minLength":1},"intent":{"type":"string"}},"required":["patch"],"additionalProperties":false}`),
	}
}

func (t *applyPatchTool) toolName() string {
	if strings.TrimSpace(t.name) == "" {
		return "apply_patch"
	}
	return t.name
}

func (*applyPatchTool) ReplayPolicy() tool.ReplayPolicy { return tool.ReplayNever }

func (t *applyPatchTool) Execute(ctx context.Context, call tool.Call, _ tool.ProgressSink) (tool.Result, error) {
	arguments, files, before, err := t.prepare(ctx, call.Arguments)
	if err != nil {
		if errors.Is(err, errSensitiveMutation) {
			return deniedResult(err.Error()), nil
		}
		return invalidResult(err.Error()), nil
	}
	return t.apply(ctx, arguments, files, before)
}

func (t *applyPatchTool) PermissionRequirement(ctx context.Context, call tool.Call) (permissionRequirement, tool.Result, error) {
	arguments, files, before, err := t.prepare(ctx, call.Arguments)
	if err != nil {
		if errors.Is(err, errSensitiveMutation) {
			return permissionRequirement{}, deniedResult(err.Error()), nil
		}
		return permissionRequirement{}, invalidResult(err.Error()), nil
	}
	approval, err := t.approval(call, arguments, files, before)
	if err != nil {
		return permissionRequirement{}, tool.Result{}, err
	}
	return permissionRequirement{
		required: true,
		request: codingagent.PermissionRequest{
			ToolName: t.toolName(), Action: codingagent.PermissionActionModify, Paths: files,
		},
		automatic:       t.validateAutomaticPatch(arguments.Patch, files) == nil,
		readOnlyMessage: "Workspace edits are disabled for this session.",
		approval:        approval,
	}, tool.Result{}, nil
}

func (t *applyPatchTool) approval(call tool.Call, arguments patchArguments, files []string, before map[string]string) (tool.Result, error) {
	payload := patchApprovalPayload{
		Kind: "coding_patch_approval_v1", Version: 1, Summary: patchSummary(arguments.Intent, files), Patch: arguments.Patch,
		Intent: arguments.Intent, Files: files, BeforeState: before,
	}
	payload.Digest = approvalDigest(payload)
	encoded, err := json.Marshal(payload)
	if err != nil {
		return tool.Result{}, err
	}
	return tool.Result{
		Status:    tool.ResultInterrupted,
		Content:   []llm.Content{{Type: llm.ContentText, Text: "Approval is required before applying this patch."}},
		Interrupt: &tool.Interrupt{ID: approvalID(call, payload.Digest), Kind: "approval", Payload: encoded},
	}, nil
}

func (t *applyPatchTool) validateAutomaticPatch(patch string, files []string) error {
	if len(patch) > t.maxAutoBytes || len(files) > t.maxAutoFiles {
		return errors.New("the patch exceeds the automatic edit size or file limit")
	}
	changedLines := 0
	for _, line := range strings.Split(patch, "\n") {
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
			changedLines++
		}
	}
	if changedLines > t.maxAutoLines || strings.ContainsRune(patch, 0) || !utf8.ValidString(patch) {
		return errors.New("the patch exceeds the automatic edit change or text limit")
	}
	for _, name := range files {
		lower := strings.ToLower(filepath.ToSlash(name))
		components := strings.Split(lower, "/")
		for _, component := range components[:max(0, len(components)-1)] {
			switch component {
			case ".git", ".codepilot", ".codex", ".husky", "node_modules", "vendor", ".venv", "venv", "dist", "build":
				return errors.New("the patch targets a directory excluded from automatic edits")
			}
		}
		if lower == ".gitmodules" || strings.HasPrefix(lower, ".github/workflows/") || automaticBinaryExtension(filepath.Ext(lower)) {
			return errors.New("the patch targets a file type excluded from automatic edits")
		}
		absolute := filepath.Join(t.root, filepath.FromSlash(name))
		info, err := os.Stat(absolute)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !info.Mode().IsRegular() || info.Size() > t.maxAutoTargetSize {
			return errors.New("the patch target exceeds the automatic edit file limit")
		}
		content, err := os.ReadFile(absolute)
		if err != nil || !utf8.Valid(content) || strings.IndexByte(string(content), 0) >= 0 {
			return errors.New("the patch target is not an automatic-edit text file")
		}
	}
	return nil
}

func automaticBinaryExtension(extension string) bool {
	switch strings.ToLower(extension) {
	case ".exe", ".dll", ".so", ".dylib", ".class", ".jar", ".zip", ".tar", ".gz", ".7z", ".pdf", ".png", ".jpg", ".jpeg", ".gif", ".webp", ".ico", ".woff", ".woff2", ".ttf", ".mp3", ".mp4", ".mov", ".avi":
		return true
	default:
		return false
	}
}

func (t *applyPatchTool) Resume(ctx context.Context, call tool.Call, interrupt tool.Interrupt, resolution tool.Result, _ tool.ProgressSink) (tool.Result, error) {
	if resolution.Status != tool.ResultCompleted {
		return resolution, nil
	}
	if interrupt.Kind != "approval" {
		return failedResult("The pending patch has an unsupported interrupt type."), nil
	}
	var payload patchApprovalPayload
	if err := json.Unmarshal(interrupt.Payload, &payload); err != nil || payload.Kind != "coding_patch_approval_v1" || payload.Version != 1 {
		return failedResult("The saved patch approval request is invalid."), nil
	}
	if payload.Digest == "" || payload.Digest != approvalDigest(payload) || interrupt.ID != approvalID(call, payload.Digest) {
		return failedResult("The saved patch approval request failed its integrity check."), nil
	}
	var arguments patchArguments
	if err := decodeArguments(call.Arguments, &arguments); err != nil {
		return failedResult("The original patch arguments are no longer available."), nil
	}
	arguments.Patch = normalizePatch(arguments.Patch)
	if arguments.Patch != payload.Patch || strings.TrimSpace(arguments.Intent) != payload.Intent {
		return failedResult("The approved patch does not match the original tool call."), nil
	}
	current, err := t.fileState(payload.Files)
	if err != nil {
		return failedResult(err.Error()), nil
	}
	if !equalFileState(current, payload.BeforeState) {
		return failedResult("The worktree changed after approval was requested; the patch was not applied."), nil
	}
	return t.apply(ctx, arguments, payload.Files, payload.BeforeState)
}

func (t *applyPatchTool) prepare(ctx context.Context, raw json.RawMessage) (patchArguments, []string, map[string]string, error) {
	var arguments patchArguments
	if err := decodeArguments(raw, &arguments); err != nil {
		return patchArguments{}, nil, nil, err
	}
	arguments.Patch = normalizePatch(arguments.Patch)
	arguments.Intent = strings.TrimSpace(arguments.Intent)
	if strings.TrimSpace(arguments.Patch) == "" {
		return patchArguments{}, nil, nil, errors.New("The patch is empty.")
	}
	if len(arguments.Patch) > t.maxBytes {
		return patchArguments{}, nil, nil, fmt.Errorf("The patch exceeds the %d-byte limit.", t.maxBytes)
	}
	if t.security.ContainsSecret(arguments.Patch) {
		return patchArguments{}, nil, nil, fmt.Errorf("%w: patches containing recognized secret values are blocked; edit the secret locally outside CodePilot", errSensitiveMutation)
	}
	files, err := patchFiles(arguments.Patch)
	if err != nil {
		return patchArguments{}, nil, nil, err
	}
	if len(files) > t.maxFiles {
		return patchArguments{}, nil, nil, fmt.Errorf("The patch changes more than %d files.", t.maxFiles)
	}
	for _, name := range files {
		if t.security.IsSensitivePath(name) {
			return patchArguments{}, nil, nil, fmt.Errorf("%w: sensitive file %s cannot be modified by the Agent", errSensitiveMutation, name)
		}
		if err := validatePatchPath(t.root, name); err != nil {
			return patchArguments{}, nil, nil, err
		}
	}
	if _, err := t.gitApply(ctx, true, arguments.Patch); err != nil {
		return patchArguments{}, nil, nil, fmt.Errorf("Git rejected the patch: %s", boundedText(err.Error(), 4096))
	}
	before, err := t.fileState(files)
	if err != nil {
		return patchArguments{}, nil, nil, err
	}
	return arguments, files, before, nil
}

func (t *applyPatchTool) apply(ctx context.Context, arguments patchArguments, files []string, expected map[string]string) (tool.Result, error) {
	current, err := t.fileState(files)
	if err != nil {
		return failedResult(err.Error()), nil
	}
	if !equalFileState(current, expected) {
		return failedResult("The worktree changed while the patch was being prepared; the patch was not applied."), nil
	}
	if _, err := t.gitApply(ctx, true, arguments.Patch); err != nil {
		return failedResult("The patch no longer applies cleanly: " + boundedText(err.Error(), 4096)), nil
	}
	if _, err := t.gitApply(ctx, false, arguments.Patch); err != nil {
		return failedResult("Git could not apply the validated patch: " + boundedText(err.Error(), 4096)), nil
	}
	details, err := json.Marshal(patchToolDetails{
		Kind:   patchDetailKind,
		Detail: fmt.Sprintf("Applied %d file(s).", len(files)),
		Diff:   &patchDiffDetail{Text: arguments.Patch, Files: append([]string(nil), files...)},
	})
	if err != nil {
		return tool.Result{}, err
	}
	return completedResult(fmt.Sprintf("Applied patch to %d file(s): %s", len(files), strings.Join(files, ", ")), details), nil
}

func (t *applyPatchTool) gitApply(ctx context.Context, check bool, patch string) (string, error) {
	arguments := []string{"-c", "core.autocrlf=false", "-C", t.root, "apply", "--whitespace=nowarn"}
	if check {
		arguments = append(arguments, "--check")
	}
	arguments = append(arguments, "-")
	command := exec.CommandContext(ctx, "git", arguments...)
	command.Stdin = strings.NewReader(patch)
	output := &boundedBuffer{limit: t.maxOutput}
	command.Stdout = output
	command.Stderr = output
	err := command.Run()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if output.truncated {
		return "", fmt.Errorf("Git output exceeded the %d-byte limit", t.maxOutput)
	}
	if err != nil {
		message := strings.TrimSpace(output.String())
		if message == "" {
			message = err.Error()
		}
		return "", errors.New(message)
	}
	return output.String(), nil
}

func (t *applyPatchTool) fileState(files []string) (map[string]string, error) {
	state := make(map[string]string, len(files))
	for _, name := range files {
		path := filepath.Join(t.root, filepath.FromSlash(name))
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			state[name] = "missing"
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("The current state of %s could not be inspected.", name)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("The patch target %s is not a regular file.", name)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("The current state of %s could not be read.", name)
		}
		digest := sha256.Sum256(content)
		state[name] = hex.EncodeToString(digest[:])
	}
	return state, nil
}

func patchFiles(patch string) ([]string, error) {
	if strings.Contains(patch, "GIT binary patch") || strings.Contains(patch, "Binary files ") {
		return nil, errors.New("Binary patches are not supported.")
	}
	lines := strings.Split(patch, "\n")
	files := make(map[string]struct{})
	var oldPath string
	for _, line := range lines {
		for _, prefix := range []string{"old mode ", "new mode ", "new file mode ", "deleted file mode ", "rename from ", "rename to ", "copy from ", "copy to ", "similarity index "} {
			if strings.HasPrefix(line, prefix) {
				return nil, errors.New("Mode, rename, and copy metadata are not supported in patches.")
			}
		}
		if strings.HasPrefix(line, "--- ") {
			oldPath = patchHeaderPath(strings.TrimPrefix(line, "--- "))
			continue
		}
		if !strings.HasPrefix(line, "+++ ") {
			continue
		}
		newPath := patchHeaderPath(strings.TrimPrefix(line, "+++ "))
		selected := newPath
		if selected == "/dev/null" {
			selected = oldPath
		}
		if selected == "" || selected == "/dev/null" {
			return nil, errors.New("The unified diff contains an invalid file header.")
		}
		selected = strings.TrimPrefix(selected, "a/")
		selected = strings.TrimPrefix(selected, "b/")
		selected = filepath.ToSlash(filepath.Clean(filepath.FromSlash(selected)))
		files[selected] = struct{}{}
		oldPath = ""
	}
	if len(files) == 0 {
		return nil, errors.New("The patch does not contain unified diff file headers.")
	}
	values := make([]string, 0, len(files))
	for name := range files {
		values = append(values, name)
	}
	sort.Strings(values)
	return values, nil
}

func patchHeaderPath(value string) string {
	value = strings.TrimSpace(value)
	if index := strings.IndexByte(value, '\t'); index >= 0 {
		value = value[:index]
	}
	return strings.TrimSpace(value)
}

func validatePatchPath(root, name string) error {
	if name == "" || name == "." || filepath.IsAbs(name) || strings.ContainsRune(name, 0) {
		return errors.New("The patch contains an invalid worktree path.")
	}
	firstComponent := strings.SplitN(filepath.ToSlash(name), "/", 2)[0]
	if strings.EqualFold(firstComponent, ".git") {
		return errors.New("Patches cannot modify Git administrative files.")
	}
	joined := filepath.Clean(filepath.Join(root, filepath.FromSlash(name)))
	relative, err := filepath.Rel(root, joined)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("The patch contains a path outside the worktree.")
	}
	parent := filepath.Dir(joined)
	for {
		info, statErr := os.Lstat(parent)
		if statErr == nil {
			if !info.IsDir() {
				return errors.New("A patch target parent is not a directory.")
			}
			resolved, evalErr := filepath.EvalSymlinks(parent)
			if evalErr != nil {
				return errors.New("A patch target parent could not be resolved.")
			}
			resolvedRelative, relErr := filepath.Rel(root, resolved)
			if relErr != nil || resolvedRelative == ".." || strings.HasPrefix(resolvedRelative, ".."+string(filepath.Separator)) {
				return errors.New("A patch target resolves outside the worktree.")
			}
			break
		}
		if !errors.Is(statErr, os.ErrNotExist) || parent == root {
			return errors.New("A patch target parent is unavailable.")
		}
		parent = filepath.Dir(parent)
	}
	return nil
}

func normalizePatch(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	if !strings.HasSuffix(value, "\n") {
		value += "\n"
	}
	return value
}

func approvalDigest(payload patchApprovalPayload) string {
	copy := payload
	copy.Digest = ""
	encoded, _ := json.Marshal(copy)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func approvalID(call tool.Call, digest string) string {
	seed := call.IdempotencyKey + "\x00" + call.ID + "\x00" + digest
	digestValue := sha256.Sum256([]byte(seed))
	return "approval_" + hex.EncodeToString(digestValue[:16])
}

func patchSummary(intent string, files []string) string {
	if intent != "" {
		return boundedText(intent+" ("+strings.Join(files, ", ")+")", 4096)
	}
	return boundedText("Apply changes to "+strings.Join(files, ", "), 4096)
}

func equalFileState(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}

var _ tool.ResumableTool = (*applyPatchTool)(nil)

var errSensitiveMutation = errors.New("sensitive workspace mutation denied")
