package workspace

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/eaglc/codepilot/internal/agent"
	"github.com/eaglc/codepilot/internal/session"
)

// SearchCode performs a bounded in-process search over safe Git-visible files.
func (s *Service) SearchCode(ctx context.Context, request agent.SearchCodeRequest) (agent.SearchCodeResult, error) {
	root, err := s.verifiedWorktreeRoot(ctx, request.WorktreeRoot)
	if err != nil {
		return agent.SearchCodeResult{}, err
	}
	query := request.Query
	if strings.TrimSpace(query) == "" || len(query) > 1024 {
		return agent.SearchCodeResult{}, workspaceAppError(session.ErrInvalidInput, "workspace.search_code", "The search query is empty or too large.", nil)
	}
	limit, err := boundedLimit(request.Limit, s.limits.MaxSearchResults, "search result")
	if err != nil {
		return agent.SearchCodeResult{}, err
	}
	if err := validateGlob(request.Glob); err != nil {
		return agent.SearchCodeResult{}, err
	}
	var expression *regexp.Regexp
	if request.Regex {
		expression, err = regexp.Compile(query)
		if err != nil {
			return agent.SearchCodeResult{}, workspaceAppError(session.ErrInvalidInput, "workspace.search_code", "The regular expression is invalid.", err)
		}
	}
	paths, sourceTruncated, err := s.gitVisibleFiles(ctx, root)
	if err != nil {
		return agent.SearchCodeResult{}, err
	}
	result := agent.SearchCodeResult{Matches: make([]agent.SearchMatch, 0, limit), Truncated: sourceTruncated}
	for _, candidate := range paths {
		if err := ctx.Err(); err != nil {
			return agent.SearchCodeResult{}, err
		}
		if !matchesGlob(candidate, request.Glob) {
			continue
		}
		_, relative, pathErr := secureExistingFile(root, candidate)
		if pathErr != nil {
			continue
		}
		content, skipped, readErr := s.readSearchFile(root, relative)
		if readErr != nil {
			return agent.SearchCodeResult{}, readErr
		}
		if skipped {
			result.Truncated = true
			continue
		}
		lines := strings.Split(strings.ReplaceAll(strings.ReplaceAll(string(content), "\r\n", "\n"), "\r", "\n"), "\n")
		for lineIndex, line := range lines {
			column := searchColumn(line, query, expression)
			if column == 0 {
				continue
			}
			if len(result.Matches) == limit {
				result.Truncated = true
				return result, nil
			}
			result.Matches = append(result.Matches, agent.SearchMatch{
				Path:   relative,
				Line:   lineIndex + 1,
				Column: column,
				Text:   truncateUTF8(line, 4096),
			})
		}
	}
	return result, nil
}

func (s *Service) readSearchFile(root string, relative string) ([]byte, bool, error) {
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		return nil, false, workspaceAppError(session.ErrWorkspaceUnavailable, "workspace.search_code", "The worktree could not be opened safely.", err)
	}
	defer rootHandle.Close()
	info, err := rootHandle.Stat(filepath.FromSlash(relative))
	if err != nil {
		return nil, false, workspaceAppError(session.ErrWorkspaceUnavailable, "workspace.search_code", "A source file became unavailable during search.", err)
	}
	if !info.Mode().IsRegular() || info.Size() > s.limits.MaxSearchFileBytes {
		return nil, true, nil
	}
	file, err := rootHandle.Open(filepath.FromSlash(relative))
	if err != nil {
		return nil, false, workspaceAppError(session.ErrWorkspaceUnavailable, "workspace.search_code", "A source file could not be opened during search.", err)
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, s.limits.MaxSearchFileBytes+1))
	if err != nil {
		return nil, false, workspaceAppError(session.ErrWorkspaceUnavailable, "workspace.search_code", "A source file could not be read during search.", err)
	}
	if int64(len(content)) > s.limits.MaxSearchFileBytes {
		return nil, true, nil
	}
	if bytes.IndexByte(content, 0) >= 0 || !utf8.Valid(content) {
		return nil, false, nil
	}
	return content, false, nil
}

func searchColumn(line string, query string, expression *regexp.Regexp) int {
	byteIndex := -1
	if expression != nil {
		location := expression.FindStringIndex(line)
		if location != nil {
			byteIndex = location[0]
		}
	} else {
		byteIndex = strings.Index(line, query)
	}
	if byteIndex < 0 {
		return 0
	}
	return utf8.RuneCountInString(line[:byteIndex]) + 1
}

func truncateUTF8(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	value = value[:maxBytes]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}
