package workspace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/eaglc/codepilot/internal/session"
)

// ReadDiff returns a bounded workspace or session-scoped Git diff.
func (s *Service) ReadDiff(ctx context.Context, request session.DiffRequest) (session.DiffResult, error) {
	if request.Kind != session.DiffProposed && request.Kind != session.DiffSession && request.Kind != session.DiffWorkspace {
		return session.DiffResult{}, workspaceAppError(session.ErrInvalidInput, "workspace.read_diff", "The requested diff kind is invalid.", nil)
	}
	if request.Kind == session.DiffProposed {
		return session.DiffResult{Kind: request.Kind}, nil
	}
	root, err := s.verifiedWorktreeRoot(ctx, request.WorktreeRoot)
	if err != nil {
		return session.DiffResult{}, err
	}
	files, err := secureDiffFiles(root, request.Files)
	if err != nil {
		return session.DiffResult{}, err
	}
	if request.Kind == session.DiffSession && len(files) == 0 {
		return session.DiffResult{Kind: request.Kind}, nil
	}

	hasHead, err := s.hasGitHead(ctx, root)
	if err != nil {
		return session.DiffResult{}, err
	}
	output := newBoundedBuffer(s.limits.MaxDiffBytes)
	metadata := make(map[string]session.DiffFile)
	truncated := false
	if hasHead {
		safeTrackedFiles, trackedTruncated, trackedErr := s.safeTrackedDiffFiles(ctx, root)
		if trackedErr != nil {
			return session.DiffResult{}, trackedErr
		}
		truncated = truncated || trackedTruncated
		trackedFiles := safeTrackedFiles
		if len(files) > 0 {
			trackedFiles = intersectPaths(files, safeTrackedFiles)
		}
		if len(trackedFiles) > 0 {
			arguments := []string{"diff", "--no-ext-diff", "--binary", "HEAD", "--"}
			arguments = append(arguments, trackedFiles...)
			tracked, gitErr := runGit(ctx, root, s.limits.MaxDiffBytes, []int{0}, arguments...)
			if gitErr != nil {
				return session.DiffResult{}, wrapGitError("workspace.read_diff", gitErr)
			}
			_, _ = output.Write(tracked.stdout)
			truncated = truncated || tracked.truncated || output.truncated
			trackedMetadata, metadataErr := s.trackedDiffMetadata(ctx, root, trackedFiles)
			if metadataErr != nil {
				return session.DiffResult{}, metadataErr
			}
			for _, value := range trackedMetadata {
				metadata[value.Path] = value
			}
		}
	}

	untracked, sourceTruncated, err := s.untrackedDiffFiles(ctx, root, hasHead, files)
	if err != nil {
		return session.DiffResult{}, err
	}
	truncated = truncated || sourceTruncated
	for _, relative := range untracked {
		if err := ctx.Err(); err != nil {
			return session.DiffResult{}, err
		}
		arguments := []string{"diff", "--no-index", "--binary", "--", os.DevNull, relative}
		value, gitErr := runGit(ctx, root, s.limits.MaxDiffBytes, []int{0, 1}, arguments...)
		if gitErr != nil {
			return session.DiffResult{}, wrapGitError("workspace.read_untracked_diff", gitErr)
		}
		_, _ = output.Write(value.stdout)
		truncated = truncated || value.truncated || output.truncated
		additions := countTextLines(root, relative, s.limits.MaxReadBytes)
		metadata[relative] = session.DiffFile{Path: relative, Status: "??", Additions: additions}
	}

	values := make([]session.DiffFile, 0, len(metadata))
	for _, value := range metadata {
		if len(values) == s.limits.MaxFiles {
			truncated = true
			break
		}
		values = append(values, value)
	}
	sort.Slice(values, func(left int, right int) bool { return values[left].Path < values[right].Path })
	drifted, err := sessionDiffDrifted(root, request.ExpectedHashes)
	if err != nil {
		return session.DiffResult{}, err
	}
	return session.DiffResult{
		Kind:      request.Kind,
		Text:      string(output.Bytes()),
		Files:     values,
		Truncated: truncated,
		Drifted:   drifted,
	}, nil
}

func (s *Service) hasGitHead(ctx context.Context, root string) (bool, error) {
	_, exitCode, err := gitLine(ctx, root, s.limits.MaxGitOutputBytes, []int{0, 128}, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return false, wrapGitError("workspace.git_head", err)
	}
	return exitCode == 0, nil
}

func secureDiffFiles(root string, requested []string) ([]string, error) {
	files := make([]string, 0, len(requested))
	seen := make(map[string]struct{}, len(requested))
	for _, value := range requested {
		absolute, relative, err := securePath(root, value, true)
		if err != nil {
			return nil, err
		}
		if info, statErr := os.Stat(absolute); statErr == nil && info.IsDir() {
			return nil, workspaceAppError(session.ErrInvalidInput, "workspace.read_diff", "Diff filters must identify files, not directories.", nil)
		}
		if _, exists := seen[relative]; exists {
			continue
		}
		seen[relative] = struct{}{}
		files = append(files, relative)
	}
	sort.Strings(files)
	return files, nil
}

func (s *Service) safeTrackedDiffFiles(ctx context.Context, root string) ([]string, bool, error) {
	result, err := runGit(ctx, root, s.limits.MaxGitOutputBytes, []int{0}, "diff", "--name-status", "-z", "HEAD", "--")
	if err != nil {
		return nil, false, wrapGitError("workspace.list_changed_files", err)
	}
	items := splitNULRecords(result.stdout, result.stdoutTruncated)
	values := make([]string, 0)
	for index := 0; index < len(items); {
		status := string(items[index])
		index++
		if status == "" || index >= len(items) {
			break
		}
		pathValue := string(items[index])
		index++
		pairedPath := ""
		if strings.HasPrefix(status, "R") || strings.HasPrefix(status, "C") {
			if index >= len(items) {
				break
			}
			pairedPath = string(items[index])
			index++
		}
		_, relative, pathErr := securePath(root, pathValue, true)
		if pathErr != nil {
			continue
		}
		if pairedPath != "" {
			_, pairedRelative, pairedErr := securePath(root, pairedPath, true)
			if pairedErr != nil {
				continue
			}
			relative = pairedRelative
		}
		if len(values) == s.limits.MaxFiles {
			return values, true, nil
		}
		values = append(values, relative)
	}
	sort.Strings(values)
	return values, result.truncated, nil
}

func intersectPaths(requested []string, available []string) []string {
	availableSet := make(map[string]struct{}, len(available))
	for _, value := range available {
		availableSet[value] = struct{}{}
	}
	result := make([]string, 0, min(len(requested), len(available)))
	for _, value := range requested {
		if _, exists := availableSet[value]; exists {
			result = append(result, value)
		}
	}
	return result
}

func (s *Service) untrackedDiffFiles(ctx context.Context, root string, hasHead bool, filters []string) ([]string, bool, error) {
	arguments := []string{"ls-files"}
	if !hasHead {
		arguments = append(arguments, "--cached")
	}
	arguments = append(arguments, "--others", "--exclude-standard", "-z")
	result, err := runGit(ctx, root, s.limits.MaxGitOutputBytes, []int{0}, arguments...)
	if err != nil {
		return nil, false, wrapGitError("workspace.list_untracked", err)
	}
	filterSet := make(map[string]struct{}, len(filters))
	for _, value := range filters {
		filterSet[value] = struct{}{}
	}
	items := splitNULRecords(result.stdout, result.stdoutTruncated)
	values := make([]string, 0, len(items))
	for _, item := range items {
		if len(item) == 0 {
			continue
		}
		_, relative, pathErr := securePath(root, string(item), false)
		if pathErr != nil {
			continue
		}
		if len(filterSet) > 0 {
			if _, selected := filterSet[relative]; !selected {
				continue
			}
		}
		values = append(values, relative)
		if len(values) == s.limits.MaxFiles {
			return values, true, nil
		}
	}
	sort.Strings(values)
	return values, result.truncated, nil
}

func (s *Service) trackedDiffMetadata(ctx context.Context, root string, files []string) ([]session.DiffFile, error) {
	statuses, err := s.trackedDiffStatuses(ctx, root, files)
	if err != nil {
		return nil, err
	}
	arguments := []string{"diff", "--numstat", "-z", "HEAD", "--"}
	arguments = append(arguments, files...)
	result, err := runGit(ctx, root, s.limits.MaxGitOutputBytes, []int{0}, arguments...)
	if err != nil {
		return nil, wrapGitError("workspace.diff_metadata", err)
	}
	values := make(map[string]session.DiffFile, len(statuses))
	for pathValue, status := range statuses {
		values[pathValue] = session.DiffFile{Path: pathValue, Status: status}
	}
	for _, item := range splitNULRecords(result.stdout, result.stdoutTruncated) {
		parts := strings.Split(string(item), "\t")
		if len(parts) != 3 || parts[2] == "" {
			continue
		}
		_, relative, pathErr := securePath(root, parts[2], true)
		if pathErr != nil {
			continue
		}
		value := values[relative]
		if value.Path == "" {
			value = session.DiffFile{Path: relative, Status: "M"}
		}
		if parts[0] == "-" || parts[1] == "-" {
			value.Status = "B"
		}
		value.Additions = parseCount(parts[0])
		value.Deletions = parseCount(parts[1])
		values[relative] = value
	}
	ordered := make([]session.DiffFile, 0, len(values))
	for _, value := range values {
		ordered = append(ordered, value)
	}
	sort.Slice(ordered, func(left int, right int) bool { return ordered[left].Path < ordered[right].Path })
	return ordered, nil
}

func (s *Service) trackedDiffStatuses(ctx context.Context, root string, files []string) (map[string]string, error) {
	arguments := []string{"diff", "--name-status", "-z", "HEAD", "--"}
	arguments = append(arguments, files...)
	result, err := runGit(ctx, root, s.limits.MaxGitOutputBytes, []int{0}, arguments...)
	if err != nil {
		return nil, wrapGitError("workspace.diff_status", err)
	}
	items := splitNULRecords(result.stdout, result.stdoutTruncated)
	statuses := make(map[string]string)
	for index := 0; index < len(items); {
		status := string(items[index])
		index++
		if status == "" || index >= len(items) {
			break
		}
		pathValue := string(items[index])
		index++
		if strings.HasPrefix(status, "R") || strings.HasPrefix(status, "C") {
			if index >= len(items) {
				break
			}
			pathValue = string(items[index])
			index++
		}
		_, relative, pathErr := securePath(root, pathValue, true)
		if pathErr != nil {
			continue
		}
		statuses[relative] = status
	}
	return statuses, nil
}

func sessionDiffDrifted(root string, expected map[string]string) (bool, error) {
	for pathValue, expectedHash := range expected {
		_, relative, err := securePath(root, pathValue, true)
		if err != nil {
			return false, err
		}
		rootHandle, openErr := os.OpenRoot(root)
		if openErr != nil {
			return false, workspaceAppError(session.ErrWorkspaceUnavailable, "workspace.diff_drift", "The worktree could not be opened safely.", openErr)
		}
		file, readErr := rootHandle.Open(filepath.FromSlash(relative))
		actualHash := ""
		if readErr == nil {
			digest := sha256.New()
			_, readErr = io.Copy(digest, file)
			closeFileErr := file.Close()
			if readErr == nil {
				readErr = closeFileErr
			}
			actualHash = hex.EncodeToString(digest.Sum(nil))
		}
		closeErr := rootHandle.Close()
		if closeErr != nil {
			return false, workspaceAppError(session.ErrWorkspaceUnavailable, "workspace.diff_drift", "The worktree file handle could not be closed.", closeErr)
		}
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			return false, workspaceAppError(session.ErrWorkspaceUnavailable, "workspace.diff_drift", "A session file could not be checked for drift.", readErr)
		}
		if !strings.EqualFold(strings.TrimSpace(expectedHash), actualHash) {
			return true, nil
		}
	}
	return false, nil
}

func countTextLines(root string, relative string, maxBytes int64) int {
	_, safeRelative, err := secureExistingFile(root, relative)
	if err != nil {
		return 0
	}
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		return 0
	}
	defer rootHandle.Close()
	file, err := rootHandle.Open(filepath.FromSlash(safeRelative))
	if err != nil {
		return 0
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil || int64(len(content)) > maxBytes || bytes.IndexByte(content, 0) >= 0 {
		return 0
	}
	if len(content) == 0 {
		return 0
	}
	lines := bytes.Count(content, []byte{'\n'})
	if content[len(content)-1] != '\n' {
		lines++
	}
	return lines
}
