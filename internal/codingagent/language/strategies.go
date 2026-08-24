package language

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	workspacefiles "github.com/eaglc/codepilot/internal/codingagent/workspace"
)

const maxRootEntries = 4096

// GoStrategy detects Go workspaces without invoking the Go toolchain.
type GoStrategy struct{}

func (GoStrategy) ID() ID { return Go }
func (GoStrategy) Detect(ctx context.Context, root string) (Detection, error) {
	return detectMarkersAndSources(ctx, root, []marker{{"go.work", 110}, {"go.mod", 100}}, []string{".go"})
}
func (GoStrategy) Profile() Profile {
	return Profile{ID: Go, Extensions: []string{".go"}, Server: Server{Program: "gopls", Args: []string{"serve"}}, PromptHint: "Use idiomatic Go, preserve package boundaries, format changed Go files, and validate with trusted Go check plans."}
}

// PythonStrategy detects Python projects without starting Python.
type PythonStrategy struct{}

func (PythonStrategy) ID() ID { return Python }
func (PythonStrategy) Detect(ctx context.Context, root string) (Detection, error) {
	return detectMarkersAndSources(ctx, root, []marker{{"pyproject.toml", 110}, {"pytest.ini", 100}, {"setup.cfg", 90}, {"setup.py", 90}, {"requirements.txt", 80}, {"Pipfile", 80}}, []string{".py"})
}
func (PythonStrategy) Profile() Profile {
	return Profile{ID: Python, Extensions: []string{".py"}, Server: Server{Program: "pyright-langserver", Args: []string{"--stdio"}}, PromptHint: "Use idiomatic Python, preserve the existing environment and style, and do not install dependencies implicitly."}
}

// NodeStrategy detects Node.js, JavaScript and TypeScript projects without running scripts.
type NodeStrategy struct{}

func (NodeStrategy) ID() ID { return Node }
func (NodeStrategy) Detect(ctx context.Context, root string) (Detection, error) {
	return detectMarkersAndSources(ctx, root, []marker{{"package.json", 110}, {"tsconfig.json", 105}, {"jsconfig.json", 100}, {"pnpm-lock.yaml", 90}, {"yarn.lock", 90}, {"package-lock.json", 90}}, []string{".js", ".jsx", ".mjs", ".cjs", ".ts", ".tsx"})
}
func (NodeStrategy) Profile() Profile {
	return Profile{ID: Node, Extensions: []string{".js", ".jsx", ".mjs", ".cjs", ".ts", ".tsx"}, Server: Server{Program: "typescript-language-server", Args: []string{"--stdio"}}, PromptHint: "Preserve the repository's package manager, module format and TypeScript/JavaScript conventions; never run install scripts implicitly."}
}

type marker struct {
	name  string
	score int
}

func detectMarkersAndSources(ctx context.Context, root string, markers []marker, extensions []string) (Detection, error) {
	if err := ctx.Err(); err != nil {
		return Detection{}, err
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return Detection{}, errors.New("language root is unavailable")
	}
	detection := Detection{}
	for _, candidate := range markers {
		if regularMarker(filepath.Join(root, candidate.name)) {
			if candidate.score > detection.Score {
				detection = Detection{Score: candidate.score}
			}
			if candidate.score == detection.Score {
				detection.Evidence = append(detection.Evidence, candidate.name)
			}
		}
	}
	if detection.Score > 0 {
		sort.Strings(detection.Evidence)
		return detection, nil
	}
	files, _, indexErr := workspacefiles.IndexFiles(ctx, root, ".", workspacefiles.FileIndexOptions{MaxFiles: maxRootEntries, Include: func(relative string) bool {
		for _, expected := range extensions {
			if strings.EqualFold(filepath.Ext(relative), expected) {
				return true
			}
		}
		return false
	}})
	if indexErr == nil && len(files) != 0 {
		return Detection{Score: 60, Evidence: []string{files[0]}}, nil
	}
	// Strategies remain usable in isolated unit/embedding contexts that are not
	// Git worktrees, but that fallback is deliberately root-only.
	entries, err := os.ReadDir(root)
	if err != nil {
		return Detection{}, errors.New("language root cannot be listed")
	}
	if len(entries) > maxRootEntries {
		entries = entries[:maxRootEntries]
	}
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		for _, expected := range extensions {
			if strings.EqualFold(filepath.Ext(entry.Name()), expected) {
				return Detection{Score: 60, Evidence: []string{entry.Name()}}, nil
			}
		}
	}
	return Detection{}, nil
}

func regularMarker(candidate string) bool {
	info, err := os.Lstat(candidate)
	return err == nil && info.Mode().IsRegular()
}
