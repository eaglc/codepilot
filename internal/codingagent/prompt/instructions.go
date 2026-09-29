package prompt

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/eaglc/codepilot/internal/codingagent"
	workspacefiles "github.com/eaglc/codepilot/internal/codingagent/workspace"
)

const (
	projectInstructionFile        = "AGENTS.md"
	maxInstructionFiles           = 32
	maxInstructionFileSize        = 16 << 10
	maxInstructionBytes           = 64 << 10
	maxInstructionEntries         = 50_000
	maxInstructionSources         = 64
	maxInstructionDiagnosticBytes = 512
)

type projectGuidanceDocument struct {
	Source  string `json:"source"`
	Scope   string `json:"scope"`
	Digest  string `json:"sha256"`
	Content string `json:"content"`
}

func encodeProjectGuidance(documents []projectGuidanceDocument) ([]byte, error) {
	encoded, err := json.Marshal(documents)
	if err != nil {
		return nil, errors.New("build Coding prompt: project guidance could not be encoded")
	}
	return encoded, nil
}

func discoverProjectGuidance(ctx context.Context, root string, security *codingagent.SecurityPolicy) ([]projectGuidanceDocument, codingagent.InstructionReport, error) {
	report := codingagent.InstructionReport{}
	resolvedRoot, err := resolveInstructionRoot(root)
	if err != nil {
		report.Sources = append(report.Sources, failedInstructionSource(".", ".", err))
		return nil, report, err
	}
	documents := make([]projectGuidanceDocument, 0)
	totalBytes := 0
	files, truncated, err := workspacefiles.IndexFiles(ctx, resolvedRoot, ".", workspacefiles.FileIndexOptions{MaxFiles: maxInstructionEntries, Include: func(relative string) bool {
		base := path.Base(relative)
		return base == projectInstructionFile || commonInstructionMisspelling(base)
	}})
	if err != nil {
		err = errors.New("build Coding prompt: project instruction discovery could not index the Git worktree")
		report.Sources = append(report.Sources, failedInstructionSource(".", ".", err))
		return nil, report, err
	}
	if truncated {
		err = errors.New("build Coding prompt: project instruction discovery exceeded its entry limit")
		report.Sources = append(report.Sources, failedInstructionSource(".", ".", err))
		return nil, report, err
	}
	rootFound := false
	for _, relative := range files {
		rootFound = rootFound || relative == projectInstructionFile
	}
	if !rootFound {
		report.Sources = append(report.Sources, codingagent.InstructionSource{
			Source: projectInstructionFile, Scope: ".", Status: codingagent.InstructionNotFound,
			Diagnostic: "No root AGENTS.md was found.",
		})
	}
	var discoveryErr error
	for index, relative := range files {
		if err := ctx.Err(); err != nil {
			return nil, report, err
		}
		if len(report.Sources) == maxInstructionSources-1 && index < len(files) {
			limitErr := errors.New("build Coding prompt: project instruction discovery report exceeded its source limit")
			report.Sources = append(report.Sources, failedInstructionSource(".", ".", limitErr))
			if discoveryErr == nil {
				discoveryErr = limitErr
			}
			break
		}
		scope := instructionScope(relative)
		if commonInstructionMisspelling(path.Base(relative)) {
			report.Sources = append(report.Sources, codingagent.InstructionSource{
				Source: relative, Scope: scope, Status: codingagent.InstructionIgnored,
				Diagnostic: "Use the exact filename AGENTS.md; this non-standard file is not loaded.",
			})
			continue
		}
		if security.IsSensitivePath(relative) {
			report.Sources = append(report.Sources, codingagent.InstructionSource{
				Source: relative, Scope: scope, Status: codingagent.InstructionIgnored,
				Diagnostic: "The instruction source is inside a sensitive path and is not loaded.",
			})
			continue
		}
		if len(documents) == maxInstructionFiles {
			readErr := errors.New("build Coding prompt: project instruction file count exceeded its limit")
			report.Sources = append(report.Sources, failedInstructionSource(relative, scope, readErr))
			if discoveryErr == nil {
				discoveryErr = readErr
			}
			continue
		}
		candidate := filepath.Join(resolvedRoot, filepath.FromSlash(relative))
		document, readErr := readProjectGuidance(resolvedRoot, candidate, relative, security)
		if readErr != nil {
			report.Sources = append(report.Sources, failedInstructionSource(relative, scope, readErr))
			if discoveryErr == nil {
				discoveryErr = readErr
			}
			continue
		}
		if document.Content == "" {
			report.Sources = append(report.Sources, codingagent.InstructionSource{
				Source: relative, Scope: scope, Status: codingagent.InstructionIgnored,
				Diagnostic: "The instruction source is empty and is not loaded.",
			})
			continue
		}
		totalBytes += len(document.Content)
		if totalBytes > maxInstructionBytes {
			readErr := errors.New("build Coding prompt: project instruction content exceeded its total size limit")
			report.Sources = append(report.Sources, failedInstructionSource(relative, scope, readErr))
			if discoveryErr == nil {
				discoveryErr = readErr
			}
			continue
		}
		documents = append(documents, document)
		report.Sources = append(report.Sources, codingagent.InstructionSource{
			Source: document.Source, Scope: document.Scope, SHA256: document.Digest, Status: codingagent.InstructionLoaded,
		})
	}
	sort.Slice(documents, func(left, right int) bool {
		leftDepth := instructionScopeDepth(documents[left].Scope)
		rightDepth := instructionScopeDepth(documents[right].Scope)
		if leftDepth != rightDepth {
			return leftDepth < rightDepth
		}
		return documents[left].Source < documents[right].Source
	})
	sort.SliceStable(report.Sources, func(left, right int) bool {
		leftDepth := instructionScopeDepth(report.Sources[left].Scope)
		rightDepth := instructionScopeDepth(report.Sources[right].Scope)
		if leftDepth != rightDepth {
			return leftDepth < rightDepth
		}
		return report.Sources[left].Source < report.Sources[right].Source
	})
	return documents, report, discoveryErr
}

func instructionScope(relative string) string {
	scope := path.Dir(relative)
	if scope == "" {
		return "."
	}
	return scope
}

func commonInstructionMisspelling(base string) bool {
	return base != projectInstructionFile && (strings.EqualFold(base, projectInstructionFile) || strings.EqualFold(base, "AGENT.md"))
}

func failedInstructionSource(source, scope string, err error) codingagent.InstructionSource {
	diagnostic := codingagent.RedactSensitiveText(err.Error())
	if len(diagnostic) > maxInstructionDiagnosticBytes {
		diagnostic = diagnostic[:maxInstructionDiagnosticBytes] + "…"
	}
	return codingagent.InstructionSource{Source: source, Scope: scope, Status: codingagent.InstructionFailed, Diagnostic: diagnostic}
}

func instructionScopeDepth(scope string) int {
	if scope == "." {
		return 0
	}
	return strings.Count(scope, "/") + 1
}

func resolveInstructionRoot(root string) (string, error) {
	absolute, err := filepath.Abs(strings.TrimSpace(root))
	if err != nil {
		return "", errors.New("build Coding prompt: worktree root is invalid")
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", errors.New("build Coding prompt: worktree root is unavailable")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", errors.New("build Coding prompt: worktree root is unavailable")
	}
	return filepath.Clean(resolved), nil
}

func readProjectGuidance(root, candidate, relative string, security *codingagent.SecurityPolicy) (projectGuidanceDocument, error) {
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil || !sameInstructionPath(resolved, candidate) || !withinInstructionRoot(root, resolved) {
		return projectGuidanceDocument{}, fmt.Errorf("build Coding prompt: project instruction %q is not a regular in-worktree file", relative)
	}
	file, err := os.Open(resolved)
	if err != nil {
		return projectGuidanceDocument{}, fmt.Errorf("build Coding prompt: project instruction %q is unreadable", relative)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxInstructionFileSize {
		return projectGuidanceDocument{}, fmt.Errorf("build Coding prompt: project instruction %q is not a regular file within the size limit", relative)
	}
	content, err := io.ReadAll(io.LimitReader(file, maxInstructionFileSize+1))
	if err != nil || len(content) > maxInstructionFileSize {
		return projectGuidanceDocument{}, fmt.Errorf("build Coding prompt: project instruction %q could not be read within the size limit", relative)
	}
	if !utf8.Valid(content) {
		return projectGuidanceDocument{}, fmt.Errorf("build Coding prompt: project instruction %q is not valid UTF-8", relative)
	}
	text := strings.TrimSpace(strings.ReplaceAll(string(content), "\r\n", "\n"))
	text = security.RedactText(text)
	digest := sha256.Sum256([]byte(text))
	scope := instructionScope(relative)
	return projectGuidanceDocument{Source: relative, Scope: scope, Digest: hex.EncodeToString(digest[:]), Content: text}, nil
}

func sameInstructionPath(left, right string) bool {
	return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
}

func withinInstructionRoot(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
