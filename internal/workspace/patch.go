package workspace

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/eaglc/codepilot/internal/agent"
	"github.com/eaglc/codepilot/internal/session"
)

// patchProposal retains the original file set and hashes while a prompt is
// pending, preventing approval from being replayed after worktree drift.
type patchProposal struct {
	root         string
	files        []string
	beforeHashes map[string]string
}

// ApplyPatch validates, authorizes, and atomically applies one unified diff.
func (s *Service) ApplyPatch(ctx context.Context, request agent.ApplyPatchRequest) (agent.ApplyPatchResult, error) {
	if err := ctx.Err(); err != nil {
		return agent.ApplyPatchResult{}, err
	}
	if err := validateApplyPatchRequest(request, s.limits.MaxDiffBytes); err != nil {
		return agent.ApplyPatchResult{}, err
	}
	if s.authorizer == nil {
		return agent.ApplyPatchResult{}, workspaceAppError(session.ErrInvalidState, "workspace.apply_patch", "Patch authorization is unavailable.", nil)
	}

	s.patchMu.Lock()
	defer s.patchMu.Unlock()

	root, err := s.verifiedWorktreeRoot(ctx, request.WorktreeRoot)
	if err != nil {
		return agent.ApplyPatchResult{}, err
	}
	if err := validateUnifiedDiff(request.Patch); err != nil {
		return agent.ApplyPatchResult{}, err
	}
	files, diffFiles, err := s.inspectPatch(ctx, root, []byte(request.Patch))
	if err != nil {
		return agent.ApplyPatchResult{}, err
	}
	beforeHashes, err := hashPatchFiles(root, files)
	if err != nil {
		return agent.ApplyPatchResult{}, err
	}
	setProposedStatuses(diffFiles, beforeHashes)
	proposed := session.DiffResult{Kind: session.DiffProposed, Text: request.Patch, Files: diffFiles}
	fingerprint := patchFingerprint(request, root)
	proposalKey := string(request.SessionID) + "\x00" + string(request.TurnID) + "\x00" + fingerprint
	previousProposal, hasPreviousProposal := s.proposals[proposalKey]
	if hasPreviousProposal && (!samePath(previousProposal.root, root) || !sameStrings(previousProposal.files, files)) {
		delete(s.proposals, proposalKey)
		return agent.ApplyPatchResult{ProposedDiff: proposed}, workspaceAppError(session.ErrConflict, "workspace.apply_patch", "The patch proposal no longer matches its original files.", nil)
	}
	if !hasPreviousProposal {
		if err := s.checkPatch(ctx, root, []byte(request.Patch)); err != nil {
			return agent.ApplyPatchResult{ProposedDiff: proposed}, err
		}
	}

	action := session.Action{
		ID:           "action_patch_" + fingerprint[:16],
		SessionID:    request.SessionID,
		TurnID:       request.TurnID,
		Kind:         session.ActionApplyPatch,
		WorktreeRoot: root,
		Summary:      boundedSummary(request.Intent, 500),
		Fingerprint:  fingerprint,
		Patch:        &session.PatchAction{Patch: request.Patch, Files: append([]string(nil), files...)},
	}
	authorization, err := s.authorizer.Authorize(ctx, request.PermissionMode, action)
	if err != nil {
		return agent.ApplyPatchResult{ProposedDiff: proposed}, err
	}
	switch authorization.Outcome {
	case session.AuthorizationDeny:
		delete(s.proposals, proposalKey)
		return agent.ApplyPatchResult{Denied: true, Reason: authorization.Reason, ProposedDiff: proposed}, nil
	case session.AuthorizationPrompt:
		if authorization.Request == nil {
			return agent.ApplyPatchResult{ProposedDiff: proposed}, workspaceAppError(session.ErrInternal, "workspace.apply_patch", "Patch approval could not be requested.", nil)
		}
		if !hasPreviousProposal {
			s.proposals[proposalKey] = patchProposal{
				root:         root,
				files:        append([]string(nil), files...),
				beforeHashes: cloneHashes(beforeHashes),
			}
		}
		return agent.ApplyPatchResult{ProposedDiff: proposed}, &session.ApprovalRequiredError{Request: *authorization.Request}
	case session.AuthorizationAllow:
	default:
		return agent.ApplyPatchResult{ProposedDiff: proposed}, workspaceAppError(session.ErrInternal, "workspace.apply_patch", "Patch authorization returned an invalid outcome.", nil)
	}

	expectedHashes := beforeHashes
	if hasPreviousProposal {
		expectedHashes = previousProposal.beforeHashes
	}
	delete(s.proposals, proposalKey)
	currentHashes, err := hashPatchFiles(root, files)
	if err != nil {
		return agent.ApplyPatchResult{ProposedDiff: proposed}, err
	}
	if !sameHashes(expectedHashes, currentHashes) {
		return agent.ApplyPatchResult{ProposedDiff: proposed}, workspaceAppError(session.ErrConflict, "workspace.apply_patch", "The worktree changed while the patch was awaiting approval.", nil)
	}
	if err := s.checkPatch(ctx, root, []byte(request.Patch)); err != nil {
		return agent.ApplyPatchResult{ProposedDiff: proposed}, err
	}
	if err := s.applyCheckedPatch(ctx, root, []byte(request.Patch), currentHashes); err != nil {
		return agent.ApplyPatchResult{ProposedDiff: proposed}, err
	}
	afterHashes, err := hashPatchFiles(root, files)
	if err != nil {
		return agent.ApplyPatchResult{ProposedDiff: proposed}, err
	}
	record, err := newPatchRecord(request, files, currentHashes, afterHashes)
	if err != nil {
		return agent.ApplyPatchResult{ProposedDiff: proposed}, err
	}
	return agent.ApplyPatchResult{Applied: true, ProposedDiff: proposed, PatchRecord: record}, nil
}

func validateApplyPatchRequest(request agent.ApplyPatchRequest, maxPatchBytes int) error {
	if strings.TrimSpace(request.WorktreeRoot) == "" || request.SessionID == "" || request.TurnID == "" {
		return workspaceAppError(session.ErrInvalidInput, "workspace.apply_patch", "Worktree, session, and turn identifiers are required.", nil)
	}
	switch request.PermissionMode {
	case session.PermissionReadOnly, session.PermissionAsk, session.PermissionAutoEdit:
	default:
		return workspaceAppError(session.ErrInvalidInput, "workspace.apply_patch", "The patch permission mode is invalid.", nil)
	}
	if strings.TrimSpace(request.Patch) == "" || len(request.Patch) > maxPatchBytes {
		return workspaceAppError(session.ErrInvalidInput, "workspace.apply_patch", "The unified diff is empty or exceeds its size limit.", nil)
	}
	if strings.IndexByte(request.Patch, 0) >= 0 {
		return workspaceAppError(session.ErrInvalidInput, "workspace.apply_patch", "The unified diff contains invalid binary data.", nil)
	}
	return nil
}

func validateUnifiedDiff(patch string) error {
	hasOldHeader := false
	hasNewHeader := false
	hasHunk := false
	for _, line := range strings.Split(strings.ReplaceAll(patch, "\r\n", "\n"), "\n") {
		switch {
		case strings.HasPrefix(line, "--- "):
			hasOldHeader = true
		case strings.HasPrefix(line, "+++ "):
			hasNewHeader = true
		case strings.HasPrefix(line, "@@ ") || line == "@@":
			hasHunk = true
		case strings.HasPrefix(line, "GIT binary patch"), strings.HasPrefix(line, "Binary files "):
			return invalidPatch("Binary patches are not allowed.")
		case strings.HasPrefix(line, "rename from "), strings.HasPrefix(line, "rename to "),
			strings.HasPrefix(line, "copy from "), strings.HasPrefix(line, "copy to "),
			strings.HasPrefix(line, "old mode "), strings.HasPrefix(line, "new mode "):
			return invalidPatch("Renames, copies, and file mode changes are not allowed.")
		case strings.HasPrefix(line, "new file mode "):
			if line != "new file mode 100644" && line != "new file mode 100755" {
				return invalidPatch("Only regular files can be created.")
			}
		case strings.HasPrefix(line, "deleted file mode "):
			if line != "deleted file mode 100644" && line != "deleted file mode 100755" {
				return invalidPatch("Only regular files can be deleted.")
			}
		case strings.HasPrefix(line, "index ") && (strings.HasSuffix(line, " 160000") || strings.HasSuffix(line, " 120000")):
			return invalidPatch("Submodule and symbolic-link patches are not allowed.")
		}
	}
	if !hasOldHeader || !hasNewHeader || !hasHunk {
		return invalidPatch("Only unified diffs with file headers and hunks are allowed.")
	}
	return nil
}

// inspectPatch asks Git to parse paths and statistics without modifying files,
// then applies the workspace path and sensitive-file rules to every target.
func (s *Service) inspectPatch(ctx context.Context, root string, patch []byte) ([]string, []session.DiffFile, error) {
	result, err := runGitInput(ctx, root, s.limits.MaxGitOutputBytes, []int{0, 1, 128}, patch, "apply", "--numstat", "-z")
	if err != nil {
		return nil, nil, wrapGitError("workspace.inspect_patch", err)
	}
	if result.exitCode != 0 || result.truncated {
		return nil, nil, invalidPatch("The unified diff could not be parsed safely.")
	}
	records := bytes.Split(result.stdout, []byte{0})
	metadata := make(map[string]session.DiffFile)
	for _, record := range records {
		if len(record) == 0 {
			continue
		}
		parts := bytes.SplitN(record, []byte{'\t'}, 3)
		if len(parts) != 3 || len(parts[2]) == 0 {
			return nil, nil, invalidPatch("Renames and malformed patch paths are not allowed.")
		}
		pathValue := string(parts[2])
		absolute, relative, pathErr := securePath(root, pathValue, true)
		if pathErr != nil {
			return nil, nil, pathErr
		}
		if info, statErr := os.Stat(absolute); statErr == nil && info.IsDir() {
			return nil, nil, invalidPatch("Patch targets must be regular files.")
		} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return nil, nil, workspaceAppError(session.ErrWorkspaceUnavailable, "workspace.inspect_patch", "A patch target could not be inspected.", statErr)
		}
		value := metadata[relative]
		value.Path = relative
		value.Additions += parsePatchCount(parts[0])
		value.Deletions += parsePatchCount(parts[1])
		metadata[relative] = value
	}
	if len(metadata) == 0 || len(metadata) > s.limits.MaxFiles {
		return nil, nil, invalidPatch("The patch is empty or changes too many files.")
	}
	files := make([]string, 0, len(metadata))
	for pathValue := range metadata {
		files = append(files, pathValue)
	}
	sort.Strings(files)
	diffFiles := make([]session.DiffFile, 0, len(files))
	for _, pathValue := range files {
		diffFiles = append(diffFiles, metadata[pathValue])
	}
	return files, diffFiles, nil
}

func (s *Service) checkPatch(ctx context.Context, root string, patch []byte) error {
	result, err := runGitInput(ctx, root, s.limits.MaxGitOutputBytes, []int{0, 1, 128}, patch, "apply", "--check", "--binary")
	if err != nil {
		return wrapGitError("workspace.check_patch", err)
	}
	if result.exitCode != 0 || result.truncated {
		return invalidPatch("The patch does not apply cleanly to the current worktree.")
	}
	return nil
}

// applyCheckedPatch reports possible partial mutation instead of resetting the
// worktree, because an automatic reset could destroy unrelated user changes.
func (s *Service) applyCheckedPatch(ctx context.Context, root string, patch []byte, beforeHashes map[string]string) error {
	result, err := runGitInput(ctx, root, s.limits.MaxGitOutputBytes, []int{0, 1, 128}, patch, "apply", "--binary", "--whitespace=nowarn")
	if err == nil && result.exitCode == 0 && !result.truncated {
		return nil
	}
	afterFailure, hashErr := hashPatchFiles(root, sortedHashPaths(beforeHashes))
	if hashErr == nil && !sameHashes(beforeHashes, afterFailure) {
		return workspaceAppError(session.ErrConflict, "workspace.apply_patch", "Patch application failed after the worktree changed; no automatic reset was attempted.", err)
	}
	if err != nil {
		return wrapGitError("workspace.apply_patch", err)
	}
	return workspaceAppError(session.ErrConflict, "workspace.apply_patch", "The patch could not be applied; no automatic reset was attempted.", nil)
}

func hashPatchFiles(root string, files []string) (map[string]string, error) {
	hashes := make(map[string]string, len(files))
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		return nil, workspaceAppError(session.ErrWorkspaceUnavailable, "workspace.hash_patch", "The worktree could not be opened safely.", err)
	}
	defer rootHandle.Close()
	for _, relative := range files {
		file, openErr := rootHandle.Open(filepath.FromSlash(relative))
		if errors.Is(openErr, os.ErrNotExist) {
			hashes[relative] = ""
			continue
		}
		if openErr != nil {
			return nil, workspaceAppError(session.ErrWorkspaceUnavailable, "workspace.hash_patch", "A patch target could not be read safely.", openErr)
		}
		info, statErr := file.Stat()
		if statErr != nil || !info.Mode().IsRegular() {
			_ = file.Close()
			return nil, workspaceAppError(session.ErrPermissionDenied, "workspace.hash_patch", "Patch targets must be regular files.", statErr)
		}
		digest := sha256.New()
		_, copyErr := io.Copy(digest, file)
		closeErr := file.Close()
		if copyErr != nil || closeErr != nil {
			return nil, workspaceAppError(session.ErrWorkspaceUnavailable, "workspace.hash_patch", "A patch target could not be hashed.", errors.Join(copyErr, closeErr))
		}
		hashes[relative] = hex.EncodeToString(digest.Sum(nil))
	}
	return hashes, nil
}

func newPatchRecord(request agent.ApplyPatchRequest, files []string, beforeHashes map[string]string, afterHashes map[string]string) (session.PatchRecord, error) {
	identifier, err := newPatchID()
	if err != nil {
		return session.PatchRecord{}, workspaceAppError(session.ErrInternal, "workspace.apply_patch", "The applied patch could not be identified.", err)
	}
	patchedFiles := make([]session.PatchedFile, 0, len(files))
	for _, pathValue := range files {
		patchedFiles = append(patchedFiles, session.PatchedFile{Path: pathValue, BeforeHash: beforeHashes[pathValue], AfterHash: afterHashes[pathValue]})
	}
	return session.PatchRecord{
		ID:        identifier,
		SessionID: request.SessionID,
		TurnID:    request.TurnID,
		Patch:     request.Patch,
		Files:     patchedFiles,
		AppliedAt: time.Now().UTC(),
	}, nil
}

func newPatchID() (session.PatchID, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(value)
	return session.PatchID("patch_" + strings.ToLower(encoded)), nil
}

func patchFingerprint(request agent.ApplyPatchRequest, root string) string {
	digest := sha256.New()
	for _, value := range []string{"apply-patch", string(request.SessionID), string(request.TurnID), filepath.Clean(root), request.Patch} {
		_, _ = io.WriteString(digest, value)
		_, _ = digest.Write([]byte{0})
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func setProposedStatuses(files []session.DiffFile, beforeHashes map[string]string) {
	for index := range files {
		files[index].Status = "M"
		if beforeHashes[files[index].Path] == "" {
			files[index].Status = "A"
		}
	}
}

func parsePatchCount(value []byte) int {
	count, err := strconv.Atoi(string(value))
	if err != nil || count < 0 {
		return 0
	}
	return count
}

func boundedSummary(value string, limit int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

func cloneHashes(values map[string]string) map[string]string {
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func sameHashes(left map[string]string, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if !strings.EqualFold(value, right[key]) {
			return false
		}
	}
	return true
}

func sameStrings(left []string, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func sortedHashPaths(values map[string]string) []string {
	paths := make([]string, 0, len(values))
	for pathValue := range values {
		paths = append(paths, pathValue)
	}
	sort.Strings(paths)
	return paths
}

func invalidPatch(message string) error {
	return workspaceAppError(session.ErrInvalidInput, "workspace.apply_patch", message, nil)
}
