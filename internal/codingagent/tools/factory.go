// Package codingtools implements workspace-scoped tools for the Coding Agent product.
package codingtools

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/eaglc/codepilot/internal/codingagent"
	"github.com/eaglc/codepilot/internal/codingagent/language"
	"github.com/eaglc/codepilot/internal/codingagent/lsp"
	workspacefiles "github.com/eaglc/codepilot/internal/codingagent/workspace"
	"github.com/eaglc/codepilot/internal/llm"
	"github.com/eaglc/codepilot/internal/tool"
)

const (
	defaultMaxFileBytes      = 1 << 20
	defaultMaxFiles          = 1000
	defaultMaxMatches        = 200
	defaultMaxOutput         = 1 << 20
	defaultMaxPatchBytes     = 2 << 20
	defaultMaxPatchFiles     = 100
	defaultMaxAutoEditBytes  = 256 << 10
	defaultMaxAutoEditFiles  = 20
	defaultMaxAutoEditLines  = 2000
	defaultMaxAutoTargetSize = 1 << 20
	defaultCheckDisplay      = 64 << 10
	defaultCheckOutput       = 8 << 20
	defaultCheckTimeout      = 5 * time.Minute
	defaultArtifactThreshold = 64 << 10
	defaultArtifactPreview   = 16 << 10
)

// Options bounds read-only Coding tool output.
type Options struct {
	MaxFileBytes      int64
	MaxFiles          int
	MaxMatches        int
	MaxOutput         int
	MaxPatchBytes     int
	MaxPatchFiles     int
	MaxAutoEditBytes  int
	MaxAutoEditFiles  int
	MaxAutoEditLines  int
	MaxAutoTargetSize int64
	MaxCheckDisplay   int
	MaxCheckOutput    int
	CheckTimeout      time.Duration
	Artifacts         codingagent.ArtifactStore
	ArtifactThreshold int
	ArtifactPreview   int
	Security          *codingagent.SecurityPolicy
	Languages         *language.Registry
	Navigator         lsp.Navigator
}

// Factory creates an isolated tool registry for one trusted worktree.
type Factory struct{ options Options }

// NewFactory creates a bounded read-only Coding tool factory.
func NewFactory(options Options) *Factory {
	if options.MaxFileBytes <= 0 {
		options.MaxFileBytes = defaultMaxFileBytes
	}
	if options.MaxFiles <= 0 {
		options.MaxFiles = defaultMaxFiles
	}
	if options.MaxMatches <= 0 {
		options.MaxMatches = defaultMaxMatches
	}
	if options.MaxOutput <= 0 {
		options.MaxOutput = defaultMaxOutput
	}
	if options.MaxPatchBytes <= 0 {
		options.MaxPatchBytes = defaultMaxPatchBytes
	}
	if options.MaxPatchFiles <= 0 {
		options.MaxPatchFiles = defaultMaxPatchFiles
	}
	if options.MaxAutoEditBytes <= 0 {
		options.MaxAutoEditBytes = defaultMaxAutoEditBytes
	}
	if options.MaxAutoEditFiles <= 0 {
		options.MaxAutoEditFiles = defaultMaxAutoEditFiles
	}
	if options.MaxAutoEditLines <= 0 {
		options.MaxAutoEditLines = defaultMaxAutoEditLines
	}
	if options.MaxAutoTargetSize <= 0 {
		options.MaxAutoTargetSize = defaultMaxAutoTargetSize
	}
	if options.MaxCheckDisplay <= 0 {
		options.MaxCheckDisplay = defaultCheckDisplay
	}
	if options.MaxCheckOutput <= 0 {
		options.MaxCheckOutput = defaultCheckOutput
	}
	if options.CheckTimeout <= 0 {
		options.CheckTimeout = defaultCheckTimeout
	}
	if options.ArtifactThreshold <= 0 {
		options.ArtifactThreshold = defaultArtifactThreshold
	}
	if options.ArtifactPreview <= 0 {
		options.ArtifactPreview = defaultArtifactPreview
	}
	return &Factory{options: options}
}

// CreateTools implements codingagent.ToolFactory.
func (f *Factory) CreateTools(ctx context.Context, scope codingagent.ToolScope) (*tool.Registry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if scope.SessionID == "" || scope.WorkspaceID == "" || scope.WorktreeID == "" || strings.TrimSpace(scope.WorktreeRoot) == "" {
		return nil, errors.New("create Coding tools: complete session and worktree scope is required")
	}
	root, err := filepath.Abs(scope.WorktreeRoot)
	if err != nil {
		return nil, fmt.Errorf("create Coding tools: resolve root: %w", err)
	}
	root = filepath.Clean(root)
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return nil, errors.New("create Coding tools: worktree root is unavailable")
	}
	options := NewFactory(f.options).options
	security, err := options.Security.WithSensitivePaths(scope.SensitivePaths)
	if err != nil {
		return nil, fmt.Errorf("create Coding tools: sensitive-path policy: %w", err)
	}
	plans := detectCheckPlans(root)
	if (options.Languages == nil) != (options.Navigator == nil) {
		return nil, errors.New("create Coding tools: language registry and navigator must be configured together")
	}
	patchOptions := func(name string) *applyPatchTool {
		return &applyPatchTool{
			name: name, root: root, security: security,
			maxBytes: options.MaxPatchBytes, maxFiles: options.MaxPatchFiles, maxOutput: options.MaxOutput,
			maxAutoBytes: options.MaxAutoEditBytes, maxAutoFiles: options.MaxAutoEditFiles,
			maxAutoLines: options.MaxAutoEditLines, maxAutoTargetSize: options.MaxAutoTargetSize,
		}
	}
	executables := []tool.Tool{
		&readFileTool{root: root, maxBytes: options.MaxFileBytes},
		&listFilesTool{root: root, maxFiles: options.MaxFiles, security: security},
		&searchCodeTool{root: root, maxFileBytes: options.MaxFileBytes, maxFiles: options.MaxFiles, maxMatches: options.MaxMatches, security: security},
		&gitStatusTool{root: root, maxOutput: options.MaxOutput},
		&gitDiffTool{root: root, maxOutput: options.MaxOutput},
		&gitLogTool{root: root, maxOutput: options.MaxOutput},
		&gitBranchesTool{root: root, maxOutput: options.MaxOutput},
		&gitShowCommitTool{root: root, maxOutput: options.MaxOutput},
		patchOptions("apply_patch"),
		&editFileTool{root: root, maxTargetSize: options.MaxAutoTargetSize, patch: patchOptions("edit_file")},
		&replaceFileTool{root: root, maxTargetSize: options.MaxAutoTargetSize, patch: patchOptions("replace_file")},
		&listCheckPlansTool{plans: plans},
		&runChecksTool{root: root, plans: plans, displayLimit: options.MaxCheckDisplay, outputLimit: options.MaxCheckOutput, timeout: options.CheckTimeout, artifacts: options.Artifacts, security: security},
	}
	if options.Languages != nil {
		profile, detectErr := options.Languages.Detect(ctx, root)
		if detectErr != nil {
			return nil, fmt.Errorf("create Coding tools: detect languages: %w", detectErr)
		}
		if len(profile.Languages) != 0 {
			navigation := navigationContext{
				root: root, worktreeID: string(scope.WorktreeID), security: security,
				profile: profile, navigator: options.Navigator,
			}
			executables = append(executables,
				&navigationTool{context: navigation, operation: navigationDefinition},
				&navigationTool{context: navigation, operation: navigationReferences},
				&navigationTool{context: navigation, operation: navigationDiagnostics},
				&navigationTool{context: navigation, operation: navigationDocumentSymbols},
			)
		}
	}
	for index := range executables {
		executables[index] = withPermissionBoundary(executables[index], scope.PermissionMode, scope.PermissionGrants)
		executables[index] = withSecurityBoundary(executables[index], security)
		executables[index] = withArtifactBoundary(executables[index], options.Artifacts, options.ArtifactThreshold, options.ArtifactPreview)
	}
	return tool.NewRegistry(executables...)
}

type readFileTool struct {
	root     string
	maxBytes int64
}

func (*readFileTool) Definition() llm.ToolDefinition {
	return llm.ToolDefinition{Name: "read_file", Description: "Read a UTF-8 text file inside the current worktree with optional inclusive line bounds. Displayed line separators are normalized to LF; result metadata records the original line-ending style and file SHA-256.", InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"start_line":{"type":"integer","minimum":1},"end_line":{"type":"integer","minimum":1}},"required":["path"],"additionalProperties":false}`)}
}

func (*readFileTool) ReplayPolicy() tool.ReplayPolicy { return tool.ReplaySafe }

func (t *readFileTool) Execute(ctx context.Context, call tool.Call, _ tool.ProgressSink) (tool.Result, error) {
	var arguments struct {
		Path      string `json:"path"`
		StartLine int    `json:"start_line"`
		EndLine   int    `json:"end_line"`
	}
	if err := decodeArguments(call.Arguments, &arguments); err != nil {
		return invalidResult(err.Error()), nil
	}
	path, relative, err := secureExistingPath(t.root, arguments.Path)
	if err != nil {
		return deniedResult(err.Error()), nil
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return failedResult("The requested path is not an available regular file."), nil
	}
	if info.Size() > t.maxBytes {
		return failedResult(fmt.Sprintf("The file exceeds the %d-byte read limit.", t.maxBytes)), nil
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return failedResult("The file could not be read."), nil
	}
	if err := ctx.Err(); err != nil {
		return tool.Result{}, err
	}
	if !utf8.Valid(content) || bytes.IndexByte(content, 0) >= 0 {
		return failedResult("The requested file is not UTF-8 text."), nil
	}
	displayContent := strings.ReplaceAll(string(content), "\r\n", "\n")
	lines := strings.Split(displayContent, "\n")
	start := arguments.StartLine
	if start <= 0 {
		start = 1
	}
	end := arguments.EndLine
	if end <= 0 || end > len(lines) {
		end = len(lines)
	}
	if start > end || start > len(lines) {
		return invalidResult("The requested line range is outside the file."), nil
	}
	var output strings.Builder
	for line := start; line <= end; line++ {
		fmt.Fprintf(&output, "%d: %s", line, lines[line-1])
		if line != end {
			output.WriteByte('\n')
		}
	}
	digest := sha256.Sum256(content)
	details, _ := json.Marshal(map[string]any{
		"path": relative, "start_line": start, "end_line": end, "bytes": len(content),
		"sha256": hex.EncodeToString(digest[:]), "line_ending": lineEndingLabel(string(content)),
		"ends_with_newline": strings.HasSuffix(string(content), "\n") || strings.HasSuffix(string(content), "\r"),
	})
	return completedResult(output.String(), details), nil
}

type listFilesTool struct {
	root     string
	maxFiles int
	security *codingagent.SecurityPolicy
}

func (*listFilesTool) Definition() llm.ToolDefinition {
	return llm.ToolDefinition{Name: "list_files", Description: "List files recursively inside a worktree directory without following symlinks.", InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"additionalProperties":false}`)}
}

func (*listFilesTool) ReplayPolicy() tool.ReplayPolicy { return tool.ReplaySafe }

func (t *listFilesTool) Execute(ctx context.Context, call tool.Call, _ tool.ProgressSink) (tool.Result, error) {
	var arguments struct {
		Path string `json:"path"`
	}
	if err := decodeArguments(call.Arguments, &arguments); err != nil {
		return invalidResult(err.Error()), nil
	}
	requested := arguments.Path
	if strings.TrimSpace(requested) == "" {
		requested = "."
	}
	if t.security.IsSensitivePath(requested) {
		return deniedResult("Sensitive paths are excluded from recursive file listing."), nil
	}
	_, startRelative, err := secureExistingPath(t.root, requested)
	if err != nil {
		return deniedResult(err.Error()), nil
	}
	files, truncated, err := workspacefiles.IndexFiles(ctx, t.root, startRelative, workspacefiles.FileIndexOptions{MaxFiles: t.maxFiles, Include: func(relative string) bool {
		return !t.security.IsSensitivePath(relative)
	}})
	if err != nil {
		return failedResult("The worktree files could not be listed."), nil
	}
	text := strings.Join(files, "\n")
	if text == "" {
		text = "No files were found."
	}
	details, _ := json.Marshal(map[string]any{"count": len(files), "truncated": truncated})
	return completedResult(text, details), nil
}

type searchCodeTool struct {
	root         string
	maxFileBytes int64
	maxFiles     int
	maxMatches   int
	security     *codingagent.SecurityPolicy
}

func (*searchCodeTool) Definition() llm.ToolDefinition {
	return llm.ToolDefinition{Name: "search_code", Description: "Search UTF-8 worktree files for a literal text query and return bounded line matches.", InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"},"path":{"type":"string"},"case_sensitive":{"type":"boolean"}},"required":["query"],"additionalProperties":false}`)}
}

func (*searchCodeTool) ReplayPolicy() tool.ReplayPolicy { return tool.ReplaySafe }

func (t *searchCodeTool) Execute(ctx context.Context, call tool.Call, _ tool.ProgressSink) (tool.Result, error) {
	var arguments struct {
		Query         string `json:"query"`
		Path          string `json:"path"`
		CaseSensitive bool   `json:"case_sensitive"`
	}
	if err := decodeArguments(call.Arguments, &arguments); err != nil {
		return invalidResult(err.Error()), nil
	}
	if arguments.Query == "" {
		return invalidResult("The search query cannot be empty."), nil
	}
	requested := arguments.Path
	if strings.TrimSpace(requested) == "" {
		requested = "."
	}
	if t.security.IsSensitivePath(requested) {
		return deniedResult("Sensitive paths are excluded from search_code. Use read_file to request one explicit, redacted read."), nil
	}
	_, startRelative, err := secureExistingPath(t.root, requested)
	if err != nil {
		return deniedResult(err.Error()), nil
	}
	needle := arguments.Query
	if !arguments.CaseSensitive {
		needle = strings.ToLower(needle)
	}
	var matches []string
	files, filesTruncated, err := workspacefiles.IndexFiles(ctx, t.root, startRelative, workspacefiles.FileIndexOptions{MaxFiles: t.maxFiles, Include: func(relative string) bool {
		return !t.security.IsSensitivePath(relative)
	}})
	if err != nil {
		return failedResult("The worktree files could not be indexed for search."), nil
	}
	truncated := filesTruncated
	for _, relative := range files {
		if err := ctx.Err(); err != nil {
			return tool.Result{}, err
		}
		path := filepath.Join(t.root, filepath.FromSlash(relative))
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() > t.maxFileBytes {
			continue
		}
		file, err := os.Open(path)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 64<<10), int(t.maxFileBytes))
		line := 0
		for scanner.Scan() {
			line++
			value := scanner.Text()
			candidate := value
			if !arguments.CaseSensitive {
				candidate = strings.ToLower(candidate)
			}
			if strings.Contains(candidate, needle) {
				matches = append(matches, relative+":"+strconv.Itoa(line)+": "+value)
				if len(matches) >= t.maxMatches {
					truncated = true
					_ = file.Close()
					break
				}
			}
		}
		_ = file.Close()
		if len(matches) >= t.maxMatches {
			break
		}
	}
	text := strings.Join(matches, "\n")
	if text == "" {
		text = "No matches were found."
	}
	details, _ := json.Marshal(map[string]any{"count": len(matches), "truncated": truncated})
	return completedResult(text, details), nil
}

type gitStatusTool struct {
	root      string
	maxOutput int
}

func (*gitStatusTool) Definition() llm.ToolDefinition {
	return llm.ToolDefinition{Name: "git_status", Description: "Show the current worktree branch and concise Git status.", InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)}
}

func (*gitStatusTool) ReplayPolicy() tool.ReplayPolicy { return tool.ReplaySafe }

func (t *gitStatusTool) Execute(ctx context.Context, call tool.Call, _ tool.ProgressSink) (tool.Result, error) {
	var arguments struct{}
	if err := decodeArguments(call.Arguments, &arguments); err != nil {
		return invalidResult(err.Error()), nil
	}
	return runGit(ctx, t.root, t.maxOutput, "status", "--short", "--branch")
}

type gitDiffTool struct {
	root      string
	maxOutput int
}

func (*gitDiffTool) Definition() llm.ToolDefinition {
	return llm.ToolDefinition{Name: "git_diff", Description: "Show a bounded Git diff for the current worktree or one worktree-relative path.", InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"staged":{"type":"boolean"}},"additionalProperties":false}`)}
}

func (*gitDiffTool) ReplayPolicy() tool.ReplayPolicy { return tool.ReplaySafe }

func (t *gitDiffTool) Execute(ctx context.Context, call tool.Call, _ tool.ProgressSink) (tool.Result, error) {
	var arguments struct {
		Path   string `json:"path"`
		Staged bool   `json:"staged"`
	}
	if err := decodeArguments(call.Arguments, &arguments); err != nil {
		return invalidResult(err.Error()), nil
	}
	command := []string{"diff", "--no-ext-diff", "--unified=3"}
	if arguments.Staged {
		command = append(command, "--cached")
	}
	if strings.TrimSpace(arguments.Path) != "" {
		_, relative, err := secureExistingPath(t.root, arguments.Path)
		if err != nil {
			return deniedResult(err.Error()), nil
		}
		command = append(command, "--", filepath.ToSlash(relative))
	}
	return runGit(ctx, t.root, t.maxOutput, command...)
}

func runGit(ctx context.Context, root string, limit int, arguments ...string) (tool.Result, error) {
	command := exec.CommandContext(ctx, "git", append([]string{"-C", root}, arguments...)...)
	output := &boundedBuffer{limit: limit}
	command.Stdout = output
	command.Stderr = output
	err := command.Run()
	if ctx.Err() != nil {
		return tool.Result{}, ctx.Err()
	}
	if output.Truncated() {
		return failedResult(fmt.Sprintf("Git output exceeded the %d-byte limit.", limit)), nil
	}
	if err != nil {
		return failedResult("Git could not complete the requested read-only operation: " + boundedText(output.String(), 4096)), nil
	}
	text := strings.TrimSpace(output.String())
	if text == "" {
		text = "No changes were found."
	}
	details, _ := json.Marshal(map[string]any{"bytes": len(output.Bytes())})
	return completedResult(text, details), nil
}

type boundedBuffer struct {
	mu        sync.Mutex
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

func (b *boundedBuffer) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	original := len(value)
	remaining := b.limit - b.buffer.Len()
	if remaining <= 0 {
		b.truncated = true
		return original, nil
	}
	if len(value) > remaining {
		value = value[:remaining]
		b.truncated = true
	}
	_, _ = b.buffer.Write(value)
	return original, nil
}

func (b *boundedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

func (b *boundedBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buffer.Bytes()...)
}

func (b *boundedBuffer) Truncated() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.truncated
}

func secureExistingPath(root, requested string) (string, string, error) {
	if strings.TrimSpace(requested) == "" || filepath.IsAbs(requested) {
		return "", "", errors.New("The path must be a non-empty worktree-relative path.")
	}
	joined := filepath.Clean(filepath.Join(root, filepath.FromSlash(requested)))
	relative, err := filepath.Rel(root, joined)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", "", errors.New("The requested path is outside the worktree.")
	}
	resolved, err := filepath.EvalSymlinks(joined)
	if err != nil {
		return "", "", errors.New("The requested worktree path is unavailable.")
	}
	resolvedRelative, err := filepath.Rel(root, resolved)
	if err != nil || resolvedRelative == ".." || strings.HasPrefix(resolvedRelative, ".."+string(filepath.Separator)) {
		return "", "", errors.New("The requested path resolves outside the worktree.")
	}
	return resolved, relative, nil
}

func decodeArguments(value json.RawMessage, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("Tool arguments are invalid: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("Tool arguments contain multiple JSON values.")
	}
	return nil
}

func completedResult(text string, details json.RawMessage) tool.Result {
	return tool.Result{Status: tool.ResultCompleted, Content: []llm.Content{{Type: llm.ContentText, Text: text}}, Details: details}
}

func invalidResult(text string) tool.Result {
	return tool.Result{Status: tool.ResultInvalid, Content: []llm.Content{{Type: llm.ContentText, Text: text}}}
}

func deniedResult(text string) tool.Result {
	return tool.Result{Status: tool.ResultDenied, Content: []llm.Content{{Type: llm.ContentText, Text: text}}}
}

func failedResult(text string) tool.Result {
	return tool.Result{Status: tool.ResultFailed, Content: []llm.Content{{Type: llm.ContentText, Text: text}}}
}

func boundedText(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for len(value) != 0 && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value + "...[truncated]"
}
