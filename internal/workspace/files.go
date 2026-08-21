package workspace

import (
	"bytes"
	"context"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/eaglc/codepilot/internal/agent"
	"github.com/eaglc/codepilot/internal/session"
)

// ListFiles returns Git-visible, safe regular files in stable order.
func (s *Service) ListFiles(ctx context.Context, request agent.ListFilesRequest) (agent.ListFilesResult, error) {
	root, err := s.verifiedWorktreeRoot(ctx, request.WorktreeRoot)
	if err != nil {
		return agent.ListFilesResult{}, err
	}
	limit, err := boundedLimit(request.Limit, s.limits.MaxFiles, "file")
	if err != nil {
		return agent.ListFilesResult{}, err
	}
	if err := validateGlob(request.Pattern); err != nil {
		return agent.ListFilesResult{}, err
	}
	paths, sourceTruncated, err := s.gitVisibleFiles(ctx, root)
	if err != nil {
		return agent.ListFilesResult{}, err
	}
	result := agent.ListFilesResult{Files: make([]agent.FileInfo, 0, min(limit, len(paths))), Truncated: sourceTruncated}
	for _, candidate := range paths {
		if err := ctx.Err(); err != nil {
			return agent.ListFilesResult{}, err
		}
		if !matchesGlob(candidate, request.Pattern) {
			continue
		}
		_, relative, pathErr := secureExistingFile(root, candidate)
		if pathErr != nil {
			continue
		}
		rootHandle, openErr := os.OpenRoot(root)
		if openErr != nil {
			return agent.ListFilesResult{}, workspaceAppError(session.ErrWorkspaceUnavailable, "workspace.list_files", "The worktree could not be opened safely.", openErr)
		}
		info, statErr := rootHandle.Stat(filepath.FromSlash(relative))
		closeErr := rootHandle.Close()
		if closeErr != nil {
			return agent.ListFilesResult{}, workspaceAppError(session.ErrWorkspaceUnavailable, "workspace.list_files", "The worktree file handle could not be closed.", closeErr)
		}
		if statErr != nil || !info.Mode().IsRegular() {
			continue
		}
		if len(result.Files) == limit {
			result.Truncated = true
			break
		}
		result.Files = append(result.Files, agent.FileInfo{Path: relative, Size: info.Size()})
	}
	return result, nil
}

// ListWorkspaceFiles exposes safe Git-visible files and their inferred parent
// directories to session UI features without walking ignored paths.
func (s *Service) ListWorkspaceFiles(ctx context.Context, root string, limit int) (session.WorkspaceFileList, error) {
	entryLimit, err := boundedLimit(limit, s.limits.MaxFiles, "workspace path")
	if err != nil {
		return session.WorkspaceFileList{}, err
	}
	result, err := s.ListFiles(ctx, agent.ListFilesRequest{WorktreeRoot: root, Limit: entryLimit})
	if err != nil {
		return session.WorkspaceFileList{}, err
	}
	directorySet := make(map[string]struct{})
	for _, file := range result.Files {
		for directory := path.Dir(file.Path); directory != "." && directory != "/"; directory = path.Dir(directory) {
			directorySet[directory+"/"] = struct{}{}
		}
	}
	directories := make([]string, 0, len(directorySet))
	for directory := range directorySet {
		directories = append(directories, directory)
	}
	sort.Strings(directories)
	entries := make([]session.WorkspaceFile, 0, len(directories)+len(result.Files))
	for _, directory := range directories {
		entries = append(entries, session.WorkspaceFile{Path: directory, Directory: true})
	}
	for _, file := range result.Files {
		entries = append(entries, session.WorkspaceFile{Path: file.Path, Size: file.Size})
	}
	truncated := result.Truncated || len(entries) > entryLimit
	if len(entries) > entryLimit {
		entries = entries[:entryLimit]
	}
	return session.WorkspaceFileList{Files: entries, Truncated: truncated}, nil
}

// ReadFile returns a one-based line range from a safe text file.
func (s *Service) ReadFile(ctx context.Context, request agent.ReadFileRequest) (agent.ReadFileResult, error) {
	root, err := s.verifiedWorktreeRoot(ctx, request.WorktreeRoot)
	if err != nil {
		return agent.ReadFileResult{}, err
	}
	startLine := request.StartLine
	if startLine == 0 {
		startLine = 1
	}
	lineCount := request.LineCount
	if lineCount == 0 {
		lineCount = min(200, s.limits.MaxReadLines)
	}
	if startLine < 1 || lineCount < 1 || lineCount > s.limits.MaxReadLines {
		return agent.ReadFileResult{}, workspaceAppError(session.ErrInvalidInput, "workspace.read_file", "The requested line range is invalid or too large.", nil)
	}
	_, relative, err := secureExistingFile(root, request.Path)
	if err != nil {
		return agent.ReadFileResult{}, err
	}
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		return agent.ReadFileResult{}, workspaceAppError(session.ErrWorkspaceUnavailable, "workspace.read_file", "The worktree could not be opened safely.", err)
	}
	defer rootHandle.Close()
	file, err := rootHandle.Open(filepath.FromSlash(relative))
	if err != nil {
		return agent.ReadFileResult{}, workspaceAppError(session.ErrWorkspaceUnavailable, "workspace.read_file", "The requested file could not be opened safely.", err)
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, s.limits.MaxReadBytes+1))
	if err != nil {
		return agent.ReadFileResult{}, workspaceAppError(session.ErrWorkspaceUnavailable, "workspace.read_file", "The requested file could not be read.", err)
	}
	truncatedByBytes := int64(len(content)) > s.limits.MaxReadBytes
	if truncatedByBytes {
		content = content[:s.limits.MaxReadBytes]
		content = trimIncompleteUTF8(content)
	}
	if bytes.IndexByte(content, 0) >= 0 || !utf8.Valid(content) {
		return agent.ReadFileResult{}, workspaceAppError(session.ErrInvalidInput, "workspace.read_file", "Binary files cannot be read as source text.", nil)
	}
	normalized := strings.ReplaceAll(string(content), "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	lines := strings.Split(normalized, "\n")
	if !truncatedByBytes && len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	totalLines := len(lines)
	startIndex := min(startLine-1, totalLines)
	endIndex := min(startIndex+lineCount, totalLines)
	selected := lines[startIndex:endIndex]
	endLine := startIndex
	if len(selected) > 0 {
		endLine = startIndex + len(selected)
	}
	return agent.ReadFileResult{
		Path:            relative,
		Content:         strings.Join(selected, "\n"),
		StartLine:       startLine,
		EndLine:         endLine,
		TotalLines:      totalLines,
		TotalLinesKnown: !truncatedByBytes,
		Truncated:       truncatedByBytes || endIndex < totalLines,
	}, nil
}

func (s *Service) gitVisibleFiles(ctx context.Context, root string) ([]string, bool, error) {
	result, err := runGit(ctx, root, s.limits.MaxGitOutputBytes, []int{0}, "ls-files", "--cached", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, false, wrapGitError("workspace.list_files", err)
	}
	items := splitNULRecords(result.stdout, result.stdoutTruncated)
	paths := make([]string, 0, min(len(items), s.limits.MaxScannedFiles))
	seen := make(map[string]struct{}, min(len(items), s.limits.MaxScannedFiles))
	truncated := result.truncated
	for _, item := range items {
		if len(item) == 0 {
			continue
		}
		candidate := strings.ReplaceAll(string(item), "\\", "/")
		if _, exists := seen[candidate]; exists {
			continue
		}
		if len(paths) == s.limits.MaxScannedFiles {
			truncated = true
			break
		}
		seen[candidate] = struct{}{}
		paths = append(paths, candidate)
	}
	sort.Strings(paths)
	return paths, truncated, nil
}

func matchesGlob(relativePath string, pattern string) bool {
	normalized := strings.TrimSpace(strings.ReplaceAll(pattern, "\\", "/"))
	if normalized == "" || normalized == "*" || normalized == "**" || normalized == "**/*" {
		return true
	}
	matched, err := path.Match(normalized, relativePath)
	if err == nil && matched {
		return true
	}
	if strings.HasPrefix(normalized, "**/") {
		normalized = strings.TrimPrefix(normalized, "**/")
		matched, _ = path.Match(normalized, relativePath)
		if matched {
			return true
		}
	}
	if !strings.Contains(normalized, "/") {
		matched, _ = path.Match(normalized, path.Base(relativePath))
		return matched
	}
	return false
}

func validateGlob(pattern string) error {
	normalized := strings.TrimSpace(strings.ReplaceAll(pattern, "\\", "/"))
	if normalized == "" || normalized == "*" || normalized == "**" || normalized == "**/*" {
		return nil
	}
	if strings.HasPrefix(normalized, "**/") {
		normalized = strings.TrimPrefix(normalized, "**/")
	}
	if _, err := path.Match(normalized, "validation-path"); err != nil {
		return workspaceAppError(session.ErrInvalidInput, "workspace.validate_glob", "The file pattern is invalid.", err)
	}
	return nil
}

func boundedLimit(requested int, maximum int, noun string) (int, error) {
	if requested == 0 {
		return maximum, nil
	}
	if requested < 0 || requested > maximum {
		return 0, workspaceAppError(session.ErrInvalidInput, "workspace.validate_limit", "The requested "+noun+" limit is invalid or too large.", nil)
	}
	return requested, nil
}

func trimIncompleteUTF8(value []byte) []byte {
	for removed := 0; removed < utf8.UTFMax && !utf8.Valid(value) && len(value) > 0; removed++ {
		value = value[:len(value)-1]
	}
	return value
}
