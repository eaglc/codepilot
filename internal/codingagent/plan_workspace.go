package codingagent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	workspaceRevisionVersion  = 2
	maxWorkspaceStatusBytes   = 4 << 20
	maxWorkspaceDiffBytes     = 64 << 20
	maxWorkspaceRelevantPaths = 128
	maxWorkspaceRelevantFiles = 4096
	maxWorkspaceRelevantBytes = 128 << 20
	maxWorkspaceChangedPaths  = 256
)

// WorkspaceDriftSeverity classifies whether a changed worktree can still use
// the exact approved Plan revision.
type WorkspaceDriftSeverity string

const (
	WorkspaceDriftNone          WorkspaceDriftSeverity = "none"
	WorkspaceDriftInformational WorkspaceDriftSeverity = "informational"
	WorkspaceDriftMaterial      WorkspaceDriftSeverity = "material"
)

// WorkspaceDriftSource identifies the lifecycle checkpoint that observed drift.
type WorkspaceDriftSource string

const (
	WorkspaceDriftAtApproval      WorkspaceDriftSource = "approval"
	WorkspaceDriftAtExecution     WorkspaceDriftSource = "execution_start"
	WorkspaceDriftDuringExecution WorkspaceDriftSource = "execution_agent"
)

// WorkspaceDrift is a bounded durable assessment. It records digests and safe
// relative paths, never file contents or raw Git output.
type WorkspaceDrift struct {
	Severity       WorkspaceDriftSeverity `json:"severity"`
	Source         WorkspaceDriftSource   `json:"source"`
	Reason         string                 `json:"reason"`
	Summary        string                 `json:"summary"`
	Paths          []string               `json:"paths,omitempty"`
	PlanVersion    uint64                 `json:"plan_version"`
	PlanDigest     string                 `json:"plan_digest"`
	BaselineDigest string                 `json:"baseline_digest"`
	CurrentDigest  string                 `json:"current_digest"`
	DetectedAt     time.Time              `json:"detected_at"`
}

func workspaceRevisionEmpty(value WorkspaceRevision) bool {
	return value.Version == 0 && value.WorktreeID == "" && value.IdentityDigest == "" && value.GitHead == "" && value.StatusDigest == "" && value.DiffDigest == "" && len(value.ChangedPaths) == 0 && len(value.RelevantPaths) == 0 && value.RecordedAt.IsZero()
}

func validateWorkspaceRevision(value WorkspaceRevision) error {
	if value.WorktreeID == "" || value.RecordedAt.IsZero() || !isHexDigest(value.StatusDigest, 64, 64) {
		return errors.New("Workspace revision requires worktree identity, status digest, and timestamp")
	}
	if value.GitHead != "" && !isHexDigest(value.GitHead, 40, 40) && !isHexDigest(value.GitHead, 64, 64) {
		return errors.New("Workspace revision Git HEAD is invalid")
	}
	if value.Version != workspaceRevisionVersion || !isHexDigest(value.IdentityDigest, 64, 64) || !isHexDigest(value.DiffDigest, 64, 64) {
		return errors.New("Workspace revision version or identity/diff digest is invalid")
	}
	if len(value.ChangedPaths) > maxWorkspaceChangedPaths || len(value.RelevantPaths) > maxWorkspaceRelevantPaths {
		return errors.New("Workspace revision exceeds its path limits")
	}
	previous := ""
	for _, path := range value.ChangedPaths {
		normalized, err := NormalizePlanPath(path)
		if err != nil || normalized != path || path <= previous {
			return errors.New("Workspace revision changed paths are not canonical and ordered")
		}
		previous = path
	}
	previous = ""
	for _, path := range value.RelevantPaths {
		normalized, err := NormalizePlanPath(path.Path)
		if err != nil || normalized != path.Path || path.Path <= previous || !isHexDigest(path.Digest, 64, 64) || path.Files < 0 || path.Files > maxWorkspaceRelevantFiles {
			return errors.New("Workspace revision relevant path is invalid or unordered")
		}
		switch path.Kind {
		case "missing", "file", "directory", "symlink":
		default:
			return fmt.Errorf("Workspace revision path %q has unsupported kind %q", path.Path, path.Kind)
		}
		previous = path.Path
	}
	return nil
}

func planRelevantPaths(value PlanSubmission) []string {
	set := make(map[string]struct{})
	for _, step := range value.Steps {
		for _, path := range step.Files {
			set[path] = struct{}{}
		}
	}
	paths := make([]string, 0, len(set))
	for path := range set {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

func capturePlanWorkspaceRevision(ctx context.Context, worktreeID WorktreeID, root string, relevantPaths []string) (WorkspaceRevision, error) {
	if err := ctx.Err(); err != nil {
		return WorkspaceRevision{}, err
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return WorkspaceRevision{}, fmt.Errorf("capture Coding plan workspace revision: resolve worktree: %w", err)
	}
	absolute = filepath.Clean(absolute)
	if resolved, resolveErr := filepath.EvalSymlinks(absolute); resolveErr == nil {
		absolute = filepath.Clean(resolved)
	}
	identityDigest, err := captureWorktreeIdentity(ctx, worktreeID, absolute)
	if err != nil {
		return WorkspaceRevision{}, fmt.Errorf("capture Coding plan workspace revision: %w", err)
	}
	head, _ := runReadOnlyGit(ctx, absolute, maxWorkspaceStatusBytes, "rev-parse", "HEAD")
	status, err := runReadOnlyGit(ctx, absolute, maxWorkspaceStatusBytes, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return WorkspaceRevision{}, fmt.Errorf("capture Coding plan workspace revision: %w", err)
	}
	unstagedDigest, err := runReadOnlyGitDigest(ctx, absolute, maxWorkspaceDiffBytes, "diff", "--binary", "--no-ext-diff", "--no-textconv")
	if err != nil {
		return WorkspaceRevision{}, fmt.Errorf("capture Coding plan workspace revision: %w", err)
	}
	stagedDigest, err := runReadOnlyGitDigest(ctx, absolute, maxWorkspaceDiffBytes, "diff", "--cached", "--binary", "--no-ext-diff", "--no-textconv")
	if err != nil {
		return WorkspaceRevision{}, fmt.Errorf("capture Coding plan workspace revision: %w", err)
	}
	diffDigest := sha256.Sum256([]byte(unstagedDigest + "\x00" + stagedDigest))
	paths := append([]string(nil), relevantPaths...)
	sort.Strings(paths)
	paths = compactStrings(paths)
	if len(paths) > maxWorkspaceRelevantPaths {
		return WorkspaceRevision{}, fmt.Errorf("capture Coding plan workspace revision: relevant path count exceeds %d", maxWorkspaceRelevantPaths)
	}
	budget := workspaceFingerprintBudget{remainingBytes: maxWorkspaceRelevantBytes, remainingFiles: maxWorkspaceRelevantFiles}
	relevant := make([]WorkspacePathRevision, 0, len(paths))
	for _, path := range paths {
		normalized, normalizeErr := NormalizePlanPath(path)
		if normalizeErr != nil || normalized != path {
			return WorkspaceRevision{}, fmt.Errorf("capture Coding plan workspace revision: relevant path %q is invalid", path)
		}
		fingerprint, fingerprintErr := captureWorkspacePath(ctx, absolute, path, &budget)
		if fingerprintErr != nil {
			return WorkspaceRevision{}, fmt.Errorf("capture Coding plan workspace revision: fingerprint %q: %w", path, fingerprintErr)
		}
		relevant = append(relevant, fingerprint)
	}
	statusDigest := sha256.Sum256([]byte(status))
	return WorkspaceRevision{
		Version: workspaceRevisionVersion, WorktreeID: worktreeID, IdentityDigest: identityDigest,
		GitHead: strings.TrimSpace(head), StatusDigest: hex.EncodeToString(statusDigest[:]), DiffDigest: hex.EncodeToString(diffDigest[:]),
		ChangedPaths: parseGitStatusPaths(status), RelevantPaths: relevant, RecordedAt: time.Now().UTC(),
	}, nil
}

func captureWorktreeIdentity(ctx context.Context, worktreeID WorktreeID, root string) (string, error) {
	gitDir, err := runReadOnlyGit(ctx, root, maxWorkspaceStatusBytes, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", err
	}
	commonDir, err := runReadOnlyGit(ctx, root, maxWorkspaceStatusBytes, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", err
	}
	commonDir = strings.TrimSpace(commonDir)
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(root, commonDir)
	}
	parts := []string{string(worktreeID), canonicalIdentityPath(root), canonicalIdentityPath(strings.TrimSpace(gitDir)), canonicalIdentityPath(commonDir)}
	digest := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(digest[:]), nil
}

func canonicalIdentityPath(value string) string {
	value = filepath.Clean(value)
	if resolved, err := filepath.EvalSymlinks(value); err == nil {
		value = filepath.Clean(resolved)
	}
	value = filepath.ToSlash(value)
	if filepath.Separator == '\\' {
		value = strings.ToLower(value)
	}
	return value
}

type workspaceFingerprintBudget struct {
	remainingBytes int64
	remainingFiles int
}

func captureWorkspacePath(ctx context.Context, root, relative string, budget *workspaceFingerprintBudget) (WorkspacePathRevision, error) {
	target := filepath.Clean(filepath.Join(root, filepath.FromSlash(relative)))
	resolvedRelative, err := filepath.Rel(root, target)
	if err != nil || resolvedRelative == ".." || strings.HasPrefix(resolvedRelative, ".."+string(filepath.Separator)) {
		return WorkspacePathRevision{}, errors.New("path escapes the worktree")
	}
	info, err := os.Lstat(target)
	if errors.Is(err, os.ErrNotExist) {
		return WorkspacePathRevision{Path: relative, Kind: "missing", Digest: digestText("missing\x00" + relative)}, nil
	}
	if err != nil {
		return WorkspacePathRevision{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		destination, readErr := os.Readlink(target)
		if readErr != nil {
			return WorkspacePathRevision{}, readErr
		}
		if err := consumeWorkspaceFingerprintEntry(budget, int64(len(destination))); err != nil {
			return WorkspacePathRevision{}, err
		}
		return WorkspacePathRevision{Path: relative, Kind: "symlink", Digest: digestText("symlink\x00" + destination), Files: 1}, nil
	}
	if info.Mode().IsRegular() {
		if err := consumeWorkspaceFingerprintEntry(budget, 0); err != nil {
			return WorkspacePathRevision{}, err
		}
		digest, readErr := digestWorkspaceFile(ctx, target, info, budget)
		if readErr != nil {
			return WorkspacePathRevision{}, readErr
		}
		return WorkspacePathRevision{Path: relative, Kind: "file", Digest: digest, Files: 1}, nil
	}
	if !info.IsDir() {
		return WorkspacePathRevision{}, errors.New("path is not a regular file, directory, or symlink")
	}
	hasher := sha256.New()
	files := 0
	err = filepath.WalkDir(target, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path == target {
			return nil
		}
		if err := consumeWorkspaceFingerprintEntry(budget, 0); err != nil {
			return err
		}
		nested, relErr := filepath.Rel(root, path)
		if relErr != nil || nested == ".." || strings.HasPrefix(nested, ".."+string(filepath.Separator)) {
			return errors.New("nested path escapes the worktree")
		}
		nested = filepath.ToSlash(nested)
		entryInfo, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}
		kind := "directory"
		digest := ""
		switch {
		case entryInfo.Mode()&os.ModeSymlink != 0:
			destination, readErr := os.Readlink(path)
			if readErr != nil {
				return readErr
			}
			if err := consumeWorkspaceFingerprintBytes(budget, int64(len(destination))); err != nil {
				return err
			}
			kind, digest = "symlink", digestText("symlink\x00"+destination)
			files++
		case entryInfo.Mode().IsRegular():
			kind = "file"
			digest, infoErr = digestWorkspaceFile(ctx, path, entryInfo, budget)
			if infoErr != nil {
				return infoErr
			}
			files++
		case entryInfo.IsDir():
		default:
			return fmt.Errorf("nested path %q has unsupported file type", nested)
		}
		writeHashField(hasher, nested)
		writeHashField(hasher, kind)
		writeHashField(hasher, digest)
		return nil
	})
	if err != nil {
		return WorkspacePathRevision{}, err
	}
	return WorkspacePathRevision{Path: relative, Kind: "directory", Digest: hex.EncodeToString(hasher.Sum(nil)), Files: files}, nil
}

func digestWorkspaceFile(ctx context.Context, path string, before os.FileInfo, budget *workspaceFingerprintBudget) (string, error) {
	if before.Size() < 0 {
		return "", errors.New("relevant file size is invalid")
	}
	if err := consumeWorkspaceFingerprintBytes(budget, before.Size()); err != nil {
		return "", err
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	hasher := sha256.New()
	_, copyErr := io.Copy(hasher, &contextReader{ctx: ctx, reader: file})
	closeErr := file.Close()
	if copyErr != nil {
		return "", copyErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	after, err := os.Stat(path)
	if err != nil || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		return "", errors.New("file changed while its workspace fingerprint was captured")
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func consumeWorkspaceFingerprintEntry(budget *workspaceFingerprintBudget, size int64) error {
	if budget == nil || budget.remainingFiles <= 0 {
		return errors.New("relevant files exceed the workspace fingerprint budget")
	}
	budget.remainingFiles--
	return consumeWorkspaceFingerprintBytes(budget, size)
}

func consumeWorkspaceFingerprintBytes(budget *workspaceFingerprintBudget, size int64) error {
	if budget == nil || size < 0 || size > budget.remainingBytes {
		return errors.New("relevant files exceed the workspace fingerprint budget")
	}
	budget.remainingBytes -= size
	return nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(value []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(value)
}

func writeHashField(target hash.Hash, value string) {
	_, _ = target.Write([]byte(value))
	_, _ = target.Write([]byte{0})
}

func digestText(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func parseGitStatusPaths(status string) []string {
	tokens := bytes.Split([]byte(status), []byte{0})
	set := make(map[string]struct{})
	for index := 0; index < len(tokens); index++ {
		token := tokens[index]
		if len(token) < 4 {
			continue
		}
		code := string(token[:2])
		addStatusPath(set, string(token[3:]))
		if strings.ContainsAny(code, "RC") && index+1 < len(tokens) {
			index++
			addStatusPath(set, string(tokens[index]))
		}
	}
	values := make([]string, 0, min(len(set), maxWorkspaceChangedPaths))
	for value := range set {
		values = append(values, value)
	}
	sort.Strings(values)
	if len(values) > maxWorkspaceChangedPaths {
		values = values[:maxWorkspaceChangedPaths]
	}
	return values
}

func addStatusPath(target map[string]struct{}, value string) {
	if len(target) >= maxWorkspaceChangedPaths || !utf8.ValidString(value) {
		return
	}
	value = filepath.ToSlash(value)
	normalized, err := NormalizePlanPath(value)
	if err == nil && normalized == value {
		target[value] = struct{}{}
	}
}

func compactStrings(values []string) []string {
	if len(values) < 2 {
		return values
	}
	result := values[:1]
	for _, value := range values[1:] {
		if value != result[len(result)-1] {
			result = append(result, value)
		}
	}
	return result
}

func runReadOnlyGit(ctx context.Context, root string, limit int, arguments ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", append([]string{"-C", root, "--no-optional-locks"}, arguments...)...)
	command.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	var output bytes.Buffer
	writer := &limitedWriter{target: &output, limit: int64(limit)}
	command.Stdout = writer
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", errors.New("Git could not read the current worktree revision")
	}
	if writer.exceeded {
		return "", errors.New("Git revision output exceeded its size limit")
	}
	return output.String(), nil
}

func runReadOnlyGitDigest(ctx context.Context, root string, limit int64, arguments ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", append([]string{"-C", root, "--no-optional-locks"}, arguments...)...)
	command.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	hasher := sha256.New()
	writer := &limitedWriter{target: hasher, limit: limit}
	command.Stdout = writer
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", errors.New("Git could not fingerprint the current worktree diff")
	}
	if writer.exceeded {
		return "", errors.New("Git diff exceeded the workspace fingerprint limit")
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

type limitedWriter struct {
	target   io.Writer
	limit    int64
	written  int64
	exceeded bool
}

func (w *limitedWriter) Write(value []byte) (int, error) {
	original := len(value)
	remaining := w.limit - w.written
	if remaining <= 0 {
		w.exceeded = true
		return original, nil
	}
	if int64(len(value)) > remaining {
		value = value[:remaining]
		w.exceeded = true
	}
	written, err := w.target.Write(value)
	w.written += int64(written)
	if err != nil {
		return written, err
	}
	return original, nil
}

// ComputeWorkspaceRevisionDigest returns a stable digest that excludes capture time.
func ComputeWorkspaceRevisionDigest(value WorkspaceRevision) (string, error) {
	value.RecordedAt = time.Time{}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("compute workspace revision digest: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func classifyWorkspaceDrift(plan Plan, current WorkspaceRevision, source WorkspaceDriftSource, now time.Time) (WorkspaceDrift, error) {
	baseline := plan.WorkspaceRevision
	baselineDigest, err := ComputeWorkspaceRevisionDigest(baseline)
	if err != nil {
		return WorkspaceDrift{}, err
	}
	currentDigest, err := ComputeWorkspaceRevisionDigest(current)
	if err != nil {
		return WorkspaceDrift{}, err
	}
	value := WorkspaceDrift{
		Severity: WorkspaceDriftNone, Source: source, Reason: "unchanged", Summary: "The workspace still matches the reviewed Plan baseline.",
		PlanVersion: plan.Version, PlanDigest: plan.Digest, BaselineDigest: baselineDigest, CurrentDigest: currentDigest, DetectedAt: now,
	}
	material := func(reason, summary string, paths []string) (WorkspaceDrift, error) {
		value.Severity, value.Reason, value.Summary = WorkspaceDriftMaterial, reason, summary
		value.Paths = append([]string(nil), paths...)
		return value, nil
	}
	if baseline.WorktreeID != current.WorktreeID || baseline.Version >= workspaceRevisionVersion && baseline.IdentityDigest != current.IdentityDigest {
		return material("worktree_identity_changed", "The Plan is bound to a different worktree identity and must be revised.", nil)
	}
	if baseline.GitHead != current.GitHead {
		return material("git_head_changed", "Git HEAD changed after this Plan version was created.", nil)
	}
	changedRelevant := changedWorkspacePaths(baseline.RelevantPaths, current.RelevantPaths)
	if len(changedRelevant) != 0 {
		return material("relevant_path_changed", "Plan-relevant files changed after this version was created.", changedRelevant)
	}
	if baseline.StatusDigest != current.StatusDigest || baseline.DiffDigest != current.DiffDigest {
		value.Severity = WorkspaceDriftInformational
		value.Reason = "unrelated_workspace_changed"
		value.Summary = "Workspace changes were detected outside the Plan-relevant paths; execution may continue."
		value.Paths = unrelatedChangedPaths(baseline.ChangedPaths, current.ChangedPaths, baseline.RelevantPaths)
	}
	return value, nil
}

func changedWorkspacePaths(previous, current []WorkspacePathRevision) []string {
	currentByPath := make(map[string]WorkspacePathRevision, len(current))
	for _, value := range current {
		currentByPath[value.Path] = value
	}
	var changed []string
	for _, value := range previous {
		other, found := currentByPath[value.Path]
		if !found || other.Kind != value.Kind || other.Digest != value.Digest || other.Files != value.Files {
			changed = append(changed, value.Path)
		}
		delete(currentByPath, value.Path)
	}
	for path := range currentByPath {
		changed = append(changed, path)
	}
	sort.Strings(changed)
	return changed
}

func unrelatedChangedPaths(previous, current []string, relevant []WorkspacePathRevision) []string {
	set := make(map[string]struct{}, len(previous)+len(current))
	for _, path := range previous {
		set[path] = struct{}{}
	}
	for _, path := range current {
		set[path] = struct{}{}
	}
	values := make([]string, 0, len(set))
	for path := range set {
		if !pathCoveredByRelevantScope(path, relevant) {
			values = append(values, path)
		}
	}
	sort.Strings(values)
	if len(values) > 32 {
		values = values[:32]
	}
	return values
}

func pathCoveredByRelevantScope(path string, relevant []WorkspacePathRevision) bool {
	for _, value := range relevant {
		if path == value.Path || strings.HasPrefix(path, strings.TrimSuffix(value.Path, "/")+"/") {
			return true
		}
	}
	return false
}
