package workspace

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const maxIndexedPathBytes = 4096

// FileIndexOptions bounds Git-aware discovery. Include runs only after path,
// symlink and dependency-directory validation.
type FileIndexOptions struct {
	MaxFiles int
	Include  func(relative string) bool
}

// IndexFiles returns tracked and untracked, non-ignored regular files using
// Git's own exclude engine. It never follows symlinks or scans dependency and
// build directories.
func IndexFiles(ctx context.Context, root, start string, options FileIndexOptions) ([]string, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if options.MaxFiles <= 0 || options.MaxFiles > 100_000 {
		return nil, false, errors.New("index worktree files: file limit is invalid")
	}
	root = filepath.Clean(root)
	start = filepath.ToSlash(filepath.Clean(strings.TrimSpace(start)))
	if start == "" || start == "./" {
		start = "."
	}
	if filepath.IsAbs(start) || start == ".." || strings.HasPrefix(start, "../") || strings.ContainsRune(start, '\x00') {
		return nil, false, errors.New("index worktree files: start path is invalid")
	}
	indexCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	arguments := []string{"-C", root, "ls-files", "--cached", "--others", "--exclude-standard", "-z"}
	if start != "." {
		arguments = append(arguments, "--", ":(top,literal)"+start)
	}
	command := exec.CommandContext(indexCtx, "git", arguments...)
	command.Stderr = io.Discard
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, false, errors.New("index worktree files: Git output is unavailable")
	}
	if err := command.Start(); err != nil {
		return nil, false, errors.New("index worktree files: Git is unavailable")
	}
	reader := bufio.NewReaderSize(stdout, maxIndexedPathBytes+1)
	files := make([]string, 0, min(options.MaxFiles, 1024))
	seen := make(map[string]struct{})
	truncated := false
	for {
		value, readErr := reader.ReadSlice(0)
		if errors.Is(readErr, bufio.ErrBufferFull) {
			cancel()
			_ = command.Wait()
			return nil, false, errors.New("index worktree files: Git returned an overlong path")
		}
		if len(value) != 0 {
			pathValue := strings.TrimSuffix(string(value), "\x00")
			if !utf8.ValidString(pathValue) || len(pathValue) > maxIndexedPathBytes {
				cancel()
				_ = command.Wait()
				return nil, false, errors.New("index worktree files: Git returned an invalid path")
			}
			relative := filepath.ToSlash(filepath.Clean(filepath.FromSlash(pathValue)))
			if safeIndexedPath(relative) && !excludedIndexedPath(relative) {
				candidate := filepath.Join(root, filepath.FromSlash(relative))
				info, statErr := os.Lstat(candidate)
				if statErr == nil && info.Mode().IsRegular() && (options.Include == nil || options.Include(relative)) {
					if _, exists := seen[relative]; !exists {
						seen[relative] = struct{}{}
						if len(files) == options.MaxFiles {
							truncated = true
							cancel()
							break
						}
						files = append(files, relative)
					}
				}
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			cancel()
			_ = command.Wait()
			return nil, false, errors.New("index worktree files: Git output could not be read")
		}
	}
	waitErr := command.Wait()
	if !truncated && waitErr != nil {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		return nil, false, errors.New("index worktree files: Git could not list the worktree")
	}
	sort.Strings(files)
	return files, truncated, nil
}

func safeIndexedPath(relative string) bool {
	return relative != "." && relative != ".." && !filepath.IsAbs(relative) && !strings.HasPrefix(relative, "../") && !strings.ContainsRune(relative, '\x00')
}

func excludedIndexedPath(relative string) bool {
	for _, component := range strings.Split(strings.ToLower(filepath.ToSlash(relative)), "/") {
		switch component {
		case ".git", ".codepilot", ".codex", ".claude", ".idea", ".vscode", "node_modules", "vendor", ".venv", "venv", "__pycache__", ".tox", ".mypy_cache", ".pytest_cache", ".next", "dist", "build", "target", "coverage":
			return true
		}
	}
	return false
}
