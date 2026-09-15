package codingagent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/eaglc/codepilot/internal/workflow"
)

const maxManagedPatchBytes = 64 << 20

type ManagedWorktreeStatus string

const (
	ManagedWorktreePending        ManagedWorktreeStatus = "pending"
	ManagedWorktreeReady          ManagedWorktreeStatus = "ready"
	ManagedWorktreeCaptured       ManagedWorktreeStatus = "captured"
	ManagedWorktreeRetained       ManagedWorktreeStatus = "retained"
	ManagedWorktreeCleanupPending ManagedWorktreeStatus = "cleanup_pending"
	ManagedWorktreeCleaned        ManagedWorktreeStatus = "cleaned"
)

type ChangeFile struct {
	Path         string `json:"path"`
	BeforeSHA256 string `json:"before_sha256"`
	AfterSHA256  string `json:"after_sha256"`
}

// ChangeSet is the stable, content-addressed result of one isolated Implement
// node. Patch bytes live in ArtifactStore; this metadata is sufficient to
// detect target drift and exactly-once reapplication after a restart.
type ChangeSet struct {
	Version          int          `json:"version"`
	ID               string       `json:"id"`
	ChildAgentID     ChildAgentID `json:"child_agent_id"`
	WorkflowID       string       `json:"workflow_id"`
	NodeID           NodeID       `json:"node_id"`
	BaselineCommit   string       `json:"baseline_commit"`
	BaseChangeSetIDs []string     `json:"base_change_set_ids,omitempty"`
	Patch            ArtifactRef  `json:"patch"`
	PatchSHA256      string       `json:"patch_sha256"`
	Files            []ChangeFile `json:"files"`
	Validation       []string     `json:"validation,omitempty"`
	CreatedAt        time.Time    `json:"created_at"`
	IntegratedAt     time.Time    `json:"integrated_at,omitempty"`
}

type ManagedWorktree struct {
	ID               string                `json:"id"`
	Root             string                `json:"root,omitempty"`
	GitCommonDir     string                `json:"git_common_dir,omitempty"`
	BaselineCommit   string                `json:"baseline_commit"`
	BaseChangeSetIDs []string              `json:"base_change_set_ids,omitempty"`
	Status           ManagedWorktreeStatus `json:"status"`
	ChangeSet        *ChangeSet            `json:"change_set,omitempty"`
	PreparedAt       time.Time             `json:"prepared_at,omitempty"`
	CapturedAt       time.Time             `json:"captured_at,omitempty"`
	CleanedAt        time.Time             `json:"cleaned_at,omitempty"`
}

type IntegrationPreview struct {
	ChangeSetID    string
	Patch          string
	Files          []string
	BeforeState    map[string]string
	PatchSHA256    string
	AlreadyApplied bool
}

type ManagedWorktreeManager interface {
	Prepare(ctx context.Context, activeRoot string, intent ManagedWorktree, bases []ChangeSet) (ManagedWorktree, error)
	Capture(ctx context.Context, value ManagedWorktree, child ChildAgent, validation []string) (ManagedWorktree, error)
	PreviewIntegration(ctx context.Context, activeRoot string, change ChangeSet) (IntegrationPreview, error)
	ApplyIntegration(ctx context.Context, activeRoot string, change ChangeSet) (IntegrationPreview, error)
	Cleanup(ctx context.Context, activeRoot string, value ManagedWorktree) error
}

type GitManagedWorktreeManager struct {
	root      string
	artifacts interface {
		ArtifactStore
		ArtifactReader
	}
}

func NewGitManagedWorktreeManager(root string, artifacts interface {
	ArtifactStore
	ArtifactReader
}) (*GitManagedWorktreeManager, error) {
	if strings.TrimSpace(root) == "" || artifacts == nil {
		return nil, errors.New("create managed worktree manager: root and artifact store are required")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("create managed worktree manager: %w", err)
	}
	return &GitManagedWorktreeManager{root: filepath.Clean(absolute), artifacts: artifacts}, nil
}

func (m *GitManagedWorktreeManager) Prepare(ctx context.Context, activeRoot string, intent ManagedWorktree, bases []ChangeSet) (ManagedWorktree, error) {
	if err := validateManagedWorktree(intent); err != nil || intent.Status != ManagedWorktreePending {
		return intent, fmt.Errorf("prepare managed worktree: invalid intent: %w", err)
	}
	activeRoot, err := filepath.Abs(activeRoot)
	if err != nil {
		return intent, err
	}
	target := filepath.Join(m.root, intent.ID)
	if err := m.validateTarget(target); err != nil {
		return intent, err
	}
	if err := os.MkdirAll(m.root, 0o700); err != nil {
		return intent, fmt.Errorf("prepare managed worktree root: %w", err)
	}
	if _, statErr := os.Stat(target); statErr == nil {
		if _, removeErr := runManagedGit(ctx, nil, activeRoot, "worktree", "remove", "--force", target); removeErr != nil {
			return intent, fmt.Errorf("reset incomplete managed worktree: %w", removeErr)
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return intent, fmt.Errorf("inspect managed worktree target: %w", statErr)
	}
	if _, err := runManagedGit(ctx, nil, activeRoot, "worktree", "add", "--detach", target, intent.BaselineCommit); err != nil {
		return intent, fmt.Errorf("create managed worktree: %w", err)
	}
	head, err := managedGitText(ctx, target, "rev-parse", "HEAD")
	if err != nil || head != intent.BaselineCommit {
		return intent, errors.New("managed worktree did not resolve to the exact Plan baseline")
	}
	common, err := managedCommonDir(ctx, target)
	if err != nil {
		return intent, err
	}
	activeCommon, err := managedCommonDir(ctx, activeRoot)
	if err != nil || !samePath(common, activeCommon) {
		return intent, errors.New("managed worktree does not share the active repository identity")
	}
	baseIDs := make([]string, 0, len(bases))
	for _, base := range bases {
		if err := validateChangeSet(base); err != nil {
			return intent, fmt.Errorf("prepare managed worktree base: %w", err)
		}
		artifact, loadErr := m.artifacts.LoadArtifact(ctx, base.Patch)
		if loadErr != nil || digestBytes(artifact.Data) != base.PatchSHA256 {
			return intent, errors.New("managed worktree base artifact failed integrity validation")
		}
		if _, applyErr := runManagedGit(ctx, artifact.Data, target, "apply", "--check", "--whitespace=nowarn", "-"); applyErr != nil {
			return intent, fmt.Errorf("managed worktree base does not apply: %w", applyErr)
		}
		if _, applyErr := runManagedGit(ctx, artifact.Data, target, "apply", "--whitespace=nowarn", "-"); applyErr != nil {
			return intent, fmt.Errorf("apply managed worktree base: %w", applyErr)
		}
		paths := changeSetPaths(base)
		arguments := append([]string{"add", "-A", "--"}, paths...)
		if _, addErr := runManagedGit(ctx, nil, target, arguments...); addErr != nil {
			return intent, fmt.Errorf("stage managed worktree base: %w", addErr)
		}
		baseIDs = append(baseIDs, base.ID)
	}
	now := time.Now().UTC()
	intent.Root, intent.GitCommonDir, intent.BaseChangeSetIDs = target, common, baseIDs
	intent.Status, intent.PreparedAt = ManagedWorktreeReady, now
	return intent, validateManagedWorktree(intent)
}

func (m *GitManagedWorktreeManager) Capture(ctx context.Context, value ManagedWorktree, child ChildAgent, validation []string) (ManagedWorktree, error) {
	if err := validateManagedWorktree(value); err != nil || value.Status != ManagedWorktreeReady {
		return value, fmt.Errorf("capture managed worktree: invalid state: %w", err)
	}
	changed, err := managedChangedPaths(ctx, value.Root)
	if err != nil {
		return value, err
	}
	if len(changed) == 0 {
		return value, errors.New("isolated Implement node produced no workspace changes")
	}
	for _, name := range changed {
		if !pathWithinScopes(name, child.Task.WritePaths) {
			return value, fmt.Errorf("isolated Implement changed %q outside its declared write scope", name)
		}
	}
	untracked, err := managedUntrackedPaths(ctx, value.Root)
	if err != nil {
		return value, err
	}
	if len(untracked) != 0 {
		arguments := append([]string{"add", "-N", "--"}, untracked...)
		if _, err := runManagedGit(ctx, nil, value.Root, arguments...); err != nil {
			return value, fmt.Errorf("prepare untracked files for change artifact: %w", err)
		}
	}
	patch, err := runManagedGit(ctx, nil, value.Root, "diff", "--binary", "--full-index", "--no-ext-diff", "--no-textconv", "--")
	if err != nil || len(patch) == 0 || !utf8.Valid(patch) {
		return value, errors.New("isolated change patch is empty, invalid, or too large")
	}
	patchText := string(patch)
	for _, marker := range []string{"GIT binary patch", "Binary files ", "old mode ", "new mode ", "rename from ", "rename to ", "copy from ", "copy to "} {
		if strings.Contains(patchText, marker) {
			return value, errors.New("isolated change contains unsupported binary, mode, rename, or copy metadata")
		}
	}
	untrackedSet := make(map[string]bool, len(untracked))
	for _, name := range untracked {
		untrackedSet[name] = true
	}
	files := make([]ChangeFile, 0, len(changed))
	for _, name := range changed {
		before := "missing"
		if !untrackedSet[name] {
			content, showErr := runManagedGit(ctx, nil, value.Root, "show", ":"+name)
			if showErr == nil {
				before = digestBytes(content)
			}
		}
		after, stateErr := managedFileDigest(value.Root, name)
		if stateErr != nil {
			return value, stateErr
		}
		files = append(files, ChangeFile{Path: name, BeforeSHA256: before, AfterSHA256: after})
	}
	ref, err := m.artifacts.SaveArtifact(ctx, Artifact{MediaType: "text/x-diff", Data: patch})
	if err != nil {
		return value, fmt.Errorf("save isolated change artifact: %w", err)
	}
	patchDigest := digestBytes(patch)
	idDigest := sha256.Sum256([]byte(string(child.ID) + "\x00" + patchDigest))
	change := ChangeSet{
		Version: 1, ID: "changeset_" + hex.EncodeToString(idDigest[:16]), ChildAgentID: child.ID,
		WorkflowID: child.WorkflowID, NodeID: child.NodeID, BaselineCommit: value.BaselineCommit,
		BaseChangeSetIDs: append([]string(nil), value.BaseChangeSetIDs...), Patch: ref, PatchSHA256: patchDigest,
		Files: files, Validation: append([]string(nil), validation...), CreatedAt: time.Now().UTC(),
	}
	if err := validateChangeSet(change); err != nil {
		return value, err
	}
	value.Status, value.ChangeSet, value.CapturedAt = ManagedWorktreeCaptured, &change, change.CreatedAt
	return value, validateManagedWorktree(value)
}

func (m *GitManagedWorktreeManager) PreviewIntegration(ctx context.Context, activeRoot string, change ChangeSet) (IntegrationPreview, error) {
	if err := validateChangeSet(change); err != nil {
		return IntegrationPreview{}, err
	}
	head, err := managedGitText(ctx, activeRoot, "rev-parse", "HEAD")
	if err != nil || head != change.BaselineCommit {
		return IntegrationPreview{}, errors.New("active worktree HEAD drifted from the change-set baseline")
	}
	artifact, err := m.artifacts.LoadArtifact(ctx, change.Patch)
	if err != nil || int64(len(artifact.Data)) != change.Patch.Size || artifact.MediaType != change.Patch.MediaType || digestBytes(artifact.Data) != change.PatchSHA256 || !utf8.Valid(artifact.Data) {
		return IntegrationPreview{}, errors.New("change-set patch artifact failed integrity validation")
	}
	preview := IntegrationPreview{ChangeSetID: change.ID, Patch: string(artifact.Data), Files: changeSetPaths(change), BeforeState: make(map[string]string, len(change.Files)), PatchSHA256: change.PatchSHA256}
	beforeMatches, afterMatches := 0, 0
	for _, file := range change.Files {
		current, stateErr := managedFileDigest(activeRoot, file.Path)
		if stateErr != nil {
			return IntegrationPreview{}, stateErr
		}
		preview.BeforeState[file.Path] = current
		if current == file.BeforeSHA256 {
			beforeMatches++
		}
		if current == file.AfterSHA256 {
			afterMatches++
		}
	}
	if afterMatches == len(change.Files) {
		preview.AlreadyApplied = true
		return preview, nil
	}
	if beforeMatches != len(change.Files) {
		return IntegrationPreview{}, errors.New("one or more change-set target files drifted; integration was blocked")
	}
	if _, err := runManagedGit(ctx, artifact.Data, activeRoot, "apply", "--check", "--whitespace=nowarn", "-"); err != nil {
		return IntegrationPreview{}, fmt.Errorf("change set conflicts with the active worktree: %w", err)
	}
	return preview, nil
}

func (m *GitManagedWorktreeManager) ApplyIntegration(ctx context.Context, activeRoot string, change ChangeSet) (IntegrationPreview, error) {
	preview, err := m.PreviewIntegration(ctx, activeRoot, change)
	if err != nil || preview.AlreadyApplied {
		return preview, err
	}
	if _, err := runManagedGit(ctx, []byte(preview.Patch), activeRoot, "apply", "--whitespace=nowarn", "-"); err != nil {
		return IntegrationPreview{}, fmt.Errorf("apply verified change set: %w", err)
	}
	for _, file := range change.Files {
		current, stateErr := managedFileDigest(activeRoot, file.Path)
		if stateErr != nil || current != file.AfterSHA256 {
			return IntegrationPreview{}, errors.New("integrated change set did not produce its recorded after-state")
		}
	}
	return preview, nil
}

func (m *GitManagedWorktreeManager) Cleanup(ctx context.Context, activeRoot string, value ManagedWorktree) error {
	if err := validateManagedWorktree(value); err != nil || value.Status != ManagedWorktreeCleanupPending {
		return fmt.Errorf("cleanup managed worktree: invalid state: %w", err)
	}
	if err := m.validateTarget(value.Root); err != nil {
		return err
	}
	if _, err := os.Stat(value.Root); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if _, err := runManagedGit(ctx, nil, activeRoot, "worktree", "remove", "--force", value.Root); err != nil {
		return fmt.Errorf("remove managed worktree: %w", err)
	}
	return nil
}

func (m *GitManagedWorktreeManager) validateTarget(target string) error {
	relative, err := filepath.Rel(m.root, filepath.Clean(target))
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return errors.New("managed worktree target is outside its trusted root")
	}
	return nil
}

func validateManagedWorktree(value ManagedWorktree) error {
	if !safeManagedID(value.ID) || !isHexDigest(value.BaselineCommit, 40, 40) && !isHexDigest(value.BaselineCommit, 64, 64) {
		return errors.New("managed worktree identity and baseline are invalid")
	}
	if len(value.BaseChangeSetIDs) > maxChildTaskItems {
		return errors.New("managed worktree base set exceeds its bound")
	}
	for _, id := range value.BaseChangeSetIDs {
		if !safeManagedID(id) {
			return errors.New("managed worktree base identity is invalid")
		}
	}
	switch value.Status {
	case ManagedWorktreePending:
		if value.Root != "" || value.GitCommonDir != "" || !value.PreparedAt.IsZero() || value.ChangeSet != nil {
			return errors.New("pending managed worktree contains prepared state")
		}
	case ManagedWorktreeReady, ManagedWorktreeRetained, ManagedWorktreeCaptured, ManagedWorktreeCleanupPending, ManagedWorktreeCleaned:
		if !filepath.IsAbs(value.Root) || !filepath.IsAbs(value.GitCommonDir) || value.PreparedAt.IsZero() {
			return errors.New("prepared managed worktree requires absolute trusted locations")
		}
	default:
		return errors.New("managed worktree status is unsupported")
	}
	if value.Status == ManagedWorktreeCaptured || value.Status == ManagedWorktreeCleanupPending || value.Status == ManagedWorktreeCleaned {
		if value.ChangeSet == nil || validateChangeSet(*value.ChangeSet) != nil || value.CapturedAt.IsZero() {
			return errors.New("captured managed worktree requires a valid change set")
		}
	} else if value.ChangeSet != nil || !value.CapturedAt.IsZero() {
		return errors.New("uncaptured managed worktree cannot carry a change set")
	}
	if value.Status == ManagedWorktreeCleaned && value.CleanedAt.IsZero() || value.Status != ManagedWorktreeCleaned && !value.CleanedAt.IsZero() {
		return errors.New("managed worktree cleanup timestamp is inconsistent")
	}
	return nil
}

func validateChangeSet(value ChangeSet) error {
	if value.Version != 1 || !safeManagedID(value.ID) || value.ChildAgentID == "" || value.WorkflowID == "" || value.NodeID == "" || !isHexDigest(value.BaselineCommit, 40, 40) && !isHexDigest(value.BaselineCommit, 64, 64) || !isHexDigest(value.PatchSHA256, 64, 64) || value.Patch.ID == "" || value.Patch.MediaType != "text/x-diff" || value.Patch.Size <= 0 || len(value.Files) == 0 || len(value.Files) > maxChildTaskItems || value.CreatedAt.IsZero() {
		return errors.New("change set identity, baseline, artifact, files, and timestamp are required")
	}
	if len(value.BaseChangeSetIDs) > maxChildTaskItems {
		return errors.New("change set exceeds its base dependency limit")
	}
	seenBases := make(map[string]struct{}, len(value.BaseChangeSetIDs))
	for _, baseID := range value.BaseChangeSetIDs {
		if !safeManagedID(baseID) {
			return errors.New("change set contains an invalid base dependency")
		}
		if _, exists := seenBases[baseID]; exists {
			return errors.New("change set repeats a base dependency")
		}
		seenBases[baseID] = struct{}{}
	}
	previous := ""
	for _, file := range value.Files {
		if normalized, err := workflow.NormalizePath(file.Path); err != nil || normalized != file.Path || file.Path <= previous || !validFileStateDigest(file.BeforeSHA256) || !validFileStateDigest(file.AfterSHA256) || file.BeforeSHA256 == file.AfterSHA256 {
			return errors.New("change-set files are not canonical, sorted, unique changes")
		}
		previous = file.Path
	}
	if !value.IntegratedAt.IsZero() && value.IntegratedAt.Before(value.CreatedAt) {
		return errors.New("change-set integration precedes creation")
	}
	return nil
}

func managedChangedPaths(ctx context.Context, root string) ([]string, error) {
	tracked, err := runManagedGit(ctx, nil, root, "diff", "--name-only", "-z", "--")
	if err != nil {
		return nil, err
	}
	trackedPaths, err := canonicalNULPaths(tracked)
	if err != nil {
		return nil, err
	}
	untracked, err := managedUntrackedPaths(ctx, root)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(trackedPaths)+len(untracked))
	for _, name := range append(trackedPaths, untracked...) {
		seen[name] = struct{}{}
	}
	result := make([]string, 0, len(seen))
	for name := range seen {
		result = append(result, name)
	}
	sort.Strings(result)
	return result, nil
}

func managedUntrackedPaths(ctx context.Context, root string) ([]string, error) {
	output, err := runManagedGit(ctx, nil, root, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, err
	}
	return canonicalNULPaths(output)
}

func canonicalNULPaths(output []byte) ([]string, error) {
	values := make(map[string]struct{})
	for _, raw := range bytes.Split(output, []byte{0}) {
		if len(raw) == 0 {
			continue
		}
		value := filepath.ToSlash(string(raw))
		normalized, err := workflow.NormalizePath(value)
		if err != nil || normalized != value {
			return nil, errors.New("Git returned a non-canonical managed-worktree path")
		}
		values[value] = struct{}{}
	}
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result, nil
}

func pathWithinScopes(name string, scopes []string) bool {
	for _, scope := range scopes {
		candidate, boundary := name, scope
		if runtime.GOOS == "windows" {
			candidate, boundary = strings.ToLower(candidate), strings.ToLower(boundary)
		}
		if candidate == boundary || strings.HasPrefix(candidate, boundary+"/") {
			return true
		}
	}
	return false
}

func managedFileDigest(root, name string) (string, error) {
	absolute := filepath.Join(root, filepath.FromSlash(name))
	info, err := os.Lstat(absolute)
	if errors.Is(err, os.ErrNotExist) {
		return "missing", nil
	}
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("change-set target %q is not a regular file", name)
	}
	content, err := os.ReadFile(absolute)
	if err != nil {
		return "", fmt.Errorf("read change-set target %q: %w", name, err)
	}
	return digestBytes(content), nil
}

func managedCommonDir(ctx context.Context, root string) (string, error) {
	value, err := managedGitText(ctx, root, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(value) {
		value = filepath.Join(root, value)
	}
	return filepath.Abs(value)
}

func managedGitText(ctx context.Context, root string, arguments ...string) (string, error) {
	output, err := runManagedGit(ctx, nil, root, arguments...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func runManagedGit(ctx context.Context, input []byte, root string, arguments ...string) ([]byte, error) {
	args := append([]string{"-c", "core.autocrlf=false", "-C", root, "--no-optional-locks"}, arguments...)
	command := exec.CommandContext(ctx, "git", args...)
	if input != nil {
		command.Stdin = bytes.NewReader(input)
	}
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	err := command.Run()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if output.Len() > maxManagedPatchBytes {
		return nil, errors.New("managed Git output exceeded its bound")
	}
	if err != nil {
		message := strings.TrimSpace(output.String())
		if message == "" {
			message = err.Error()
		}
		return nil, errors.New(boundedUTF8(message, 4096))
	}
	return append([]byte(nil), output.Bytes()...), nil
}

func changeSetPaths(value ChangeSet) []string {
	paths := make([]string, len(value.Files))
	for index := range value.Files {
		paths[index] = value.Files[index].Path
	}
	return paths
}

func digestBytes(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func validFileStateDigest(value string) bool {
	return value == "missing" || isHexDigest(value, 64, 64)
}

func safeManagedID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '-' || character == '_' || character == '.' {
			continue
		}
		return false
	}
	return true
}

var _ ManagedWorktreeManager = (*GitManagedWorktreeManager)(nil)
