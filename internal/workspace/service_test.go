package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/eaglc/codepilot/internal/agent"
	"github.com/eaglc/codepilot/internal/session"
)

func TestServiceResolveWorktreeAndReadState(t *testing.T) {
	fixture := newGitFixture(t)
	nested := filepath.Join(fixture.root, "src")

	resolved, err := fixture.service.ResolveWorktree(context.Background(), nested)
	if err != nil {
		t.Fatalf("resolve worktree: %v", err)
	}
	if !samePath(resolved.Root, fixture.root) || resolved.DisplayName != filepath.Base(fixture.root) {
		t.Fatalf("unexpected resolved worktree: %#v", resolved)
	}
	if resolved.GitDir == "" || resolved.GitCommonDir == "" {
		t.Fatalf("Git paths were not resolved: %#v", resolved)
	}

	state, err := fixture.service.ReadWorktreeState(context.Background(), fixture.root)
	if err != nil {
		t.Fatalf("read clean state: %v", err)
	}
	if state.Branch != "main" || state.HeadCommit == "" || state.Dirty || !state.Available {
		t.Fatalf("unexpected clean state: %#v", state)
	}
	writeTestFile(t, fixture.root, "main.go", "package main\n\nfunc answer() int { return 42 }\n")
	state, err = fixture.service.ReadWorktreeState(context.Background(), fixture.root)
	if err != nil {
		t.Fatalf("read dirty state: %v", err)
	}
	if !state.Dirty {
		t.Fatalf("modified worktree reported clean: %#v", state)
	}
}

func TestServiceRejectsNonGitAndNonRootPaths(t *testing.T) {
	service := newTestService(t, DefaultLimits())
	directory := t.TempDir()
	runTestGit(t, directory, "init", "--bare")
	if _, err := service.ResolveWorktree(context.Background(), directory); errorCode(err) != session.ErrWorkspaceUnavailable {
		t.Fatalf("non-Git directory error = %v", err)
	}

	fixture := newGitFixture(t)
	_, err := fixture.service.ReadWorktreeState(context.Background(), filepath.Join(fixture.root, "src"))
	if errorCode(err) != session.ErrInvalidInput {
		t.Fatalf("nested root error = %v", err)
	}
}

func TestServiceListFilesFiltersIgnoredAndSensitivePaths(t *testing.T) {
	fixture := newGitFixture(t)
	writeTestFile(t, fixture.root, "new.go", "package main\n")
	writeTestFile(t, fixture.root, "ignored.log", "do not list\n")

	result, err := fixture.service.ListFiles(context.Background(), agent.ListFilesRequest{
		WorktreeRoot: fixture.root,
		Pattern:      "*.go",
	})
	if err != nil {
		t.Fatalf("list files: %v", err)
	}
	paths := filePaths(result.Files)
	if !reflect.DeepEqual(paths, []string{"main.go", "new.go", "src/util.go"}) {
		t.Fatalf("unexpected files: %#v", result)
	}
	for _, forbidden := range []string{".env", "server.pem", "ignored.log"} {
		if containsString(paths, forbidden) {
			t.Fatalf("unsafe or ignored path was listed: %s", forbidden)
		}
	}

	limited, err := fixture.service.ListFiles(context.Background(), agent.ListFilesRequest{WorktreeRoot: fixture.root, Limit: 1})
	if err != nil {
		t.Fatalf("list limited files: %v", err)
	}
	if len(limited.Files) != 1 || !limited.Truncated {
		t.Fatalf("file limit was not enforced: %#v", limited)
	}
}

func TestServiceListWorkspaceFilesIncludesInferredDirectories(t *testing.T) {
	fixture := newGitFixture(t)

	result, err := fixture.service.ListWorkspaceFiles(context.Background(), fixture.root, 20)
	if err != nil {
		t.Fatalf("list workspace paths: %v", err)
	}
	entries := make(map[string]bool, len(result.Files))
	for _, entry := range result.Files {
		entries[entry.Path] = entry.Directory
	}
	if directory, exists := entries["src/"]; !exists || !directory {
		t.Fatalf("src directory is missing: %#v", result.Files)
	}
	if directory, exists := entries["src/util.go"]; !exists || directory {
		t.Fatalf("src file is missing or marked as a directory: %#v", result.Files)
	}
}

func TestServiceReadFileRangeAndHardPathRules(t *testing.T) {
	fixture := newGitFixture(t)
	result, err := fixture.service.ReadFile(context.Background(), agent.ReadFileRequest{
		WorktreeRoot: fixture.root,
		Path:         "main.go",
		StartLine:    2,
		LineCount:    2,
	})
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if result.Content != "\nfunc answer() int {" || result.StartLine != 2 || result.EndLine != 3 || !result.Truncated {
		t.Fatalf("unexpected file range: %#v", result)
	}

	outside := filepath.Join(filepath.Dir(fixture.root), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	unsafePaths := []string{"../outside.txt", outside, ".git/config", ".GIT/config", ".env", "server.pem"}
	for _, pathValue := range unsafePaths {
		t.Run(strings.ReplaceAll(pathValue, string(filepath.Separator), "_"), func(t *testing.T) {
			_, readErr := fixture.service.ReadFile(context.Background(), agent.ReadFileRequest{WorktreeRoot: fixture.root, Path: pathValue})
			if errorCode(readErr) != session.ErrPermissionDenied {
				t.Fatalf("unsafe path %q error = %v", pathValue, readErr)
			}
		})
	}

	writeTestBytes(t, fixture.root, "binary.dat", []byte{0, 1, 2})
	_, err = fixture.service.ReadFile(context.Background(), agent.ReadFileRequest{WorktreeRoot: fixture.root, Path: "binary.dat"})
	if errorCode(err) != session.ErrInvalidInput {
		t.Fatalf("binary file error = %v", err)
	}
}

func TestServiceRejectsSymlinkEscape(t *testing.T) {
	fixture := newGitFixture(t)
	outside := filepath.Join(filepath.Dir(fixture.root), "outside-secret.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	link := filepath.Join(fixture.root, "link.txt")
	if err := os.Symlink(outside, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symbolic links are unavailable for this Windows user: %v", err)
		}
		t.Fatalf("create symlink: %v", err)
	}
	_, err := fixture.service.ReadFile(context.Background(), agent.ReadFileRequest{WorktreeRoot: fixture.root, Path: "link.txt"})
	if errorCode(err) != session.ErrPermissionDenied {
		t.Fatalf("escaping symlink error = %v", err)
	}
}

func TestServiceSearchCodeLiteralRegexAndLimits(t *testing.T) {
	fixture := newGitFixture(t)
	literal, err := fixture.service.SearchCode(context.Background(), agent.SearchCodeRequest{
		WorktreeRoot: fixture.root,
		Query:        "answer",
		Glob:         "*.go",
	})
	if err != nil {
		t.Fatalf("literal search: %v", err)
	}
	if len(literal.Matches) != 2 || literal.Matches[0].Path != "main.go" || literal.Matches[0].Line != 3 {
		t.Fatalf("unexpected literal matches: %#v", literal)
	}

	regular, err := fixture.service.SearchCode(context.Background(), agent.SearchCodeRequest{
		WorktreeRoot: fixture.root,
		Query:        `return\s+41`,
		Regex:        true,
		Glob:         "main.go",
	})
	if err != nil {
		t.Fatalf("regex search: %v", err)
	}
	if len(regular.Matches) != 1 || regular.Matches[0].Column != 2 {
		t.Fatalf("unexpected regex match: %#v", regular)
	}

	limited, err := fixture.service.SearchCode(context.Background(), agent.SearchCodeRequest{
		WorktreeRoot: fixture.root,
		Query:        "answer",
		Limit:        1,
	})
	if err != nil {
		t.Fatalf("limited search: %v", err)
	}
	if len(limited.Matches) != 1 || !limited.Truncated {
		t.Fatalf("search limit was not enforced: %#v", limited)
	}
	_, err = fixture.service.SearchCode(context.Background(), agent.SearchCodeRequest{WorktreeRoot: fixture.root, Query: "[", Regex: true})
	if errorCode(err) != session.ErrInvalidInput {
		t.Fatalf("invalid regex error = %v", err)
	}
	_, err = fixture.service.SearchCode(context.Background(), agent.SearchCodeRequest{WorktreeRoot: fixture.root, Query: "answer", Glob: "["})
	if errorCode(err) != session.ErrInvalidInput {
		t.Fatalf("invalid glob error = %v", err)
	}
}

func TestServiceGitStatusHidesSensitiveEntries(t *testing.T) {
	fixture := newGitFixture(t)
	writeTestFile(t, fixture.root, "main.go", "package main\n\nfunc answer() int { return 42 }\n")
	writeTestFile(t, fixture.root, ".env", "API_KEY=changed-secret\n")

	result, err := fixture.service.GitStatus(context.Background(), agent.GitStatusRequest{WorktreeRoot: fixture.root})
	if err != nil {
		t.Fatalf("git status: %v", err)
	}
	if !result.Dirty || result.HiddenEntries != 1 || len(result.Entries) != 1 || result.Entries[0].Path != "main.go" {
		t.Fatalf("unexpected safe status: %#v", result)
	}
}

func TestServiceReadWorkspaceAndSessionDiff(t *testing.T) {
	fixture := newGitFixture(t)
	mainContent := "package main\n\nfunc answer() int {\n\treturn 42\n}\n"
	writeTestFile(t, fixture.root, "main.go", mainContent)
	writeTestFile(t, fixture.root, "notes.txt", "new note\n")
	writeTestFile(t, fixture.root, ".env", "API_KEY=changed-secret-value\n")

	result, err := fixture.service.ReadDiff(context.Background(), session.DiffRequest{
		WorktreeRoot: fixture.root,
		Kind:         session.DiffWorkspace,
	})
	if err != nil {
		t.Fatalf("read workspace diff: %v", err)
	}
	if !strings.Contains(result.Text, "return 42") || !strings.Contains(result.Text, "new note") {
		t.Fatalf("workspace diff omitted safe changes: %s", result.Text)
	}
	if strings.Contains(result.Text, "changed-secret-value") || strings.Contains(result.Text, ".env") {
		t.Fatalf("workspace diff exposed sensitive content: %s", result.Text)
	}
	if !containsDiffFile(result.Files, "main.go") || !containsDiffFile(result.Files, "notes.txt") {
		t.Fatalf("workspace diff metadata is incomplete: %#v", result.Files)
	}

	filtered, err := fixture.service.ReadDiff(context.Background(), session.DiffRequest{
		WorktreeRoot: fixture.root,
		Kind:         session.DiffWorkspace,
		Files:        []string{"main.go"},
	})
	if err != nil {
		t.Fatalf("read filtered diff: %v", err)
	}
	if strings.Contains(filtered.Text, "new note") || containsDiffFile(filtered.Files, "notes.txt") {
		t.Fatalf("filtered diff included another file: %#v", filtered)
	}

	digest := sha256.Sum256([]byte(mainContent))
	expectedHash := hex.EncodeToString(digest[:])
	sessionResult, err := fixture.service.ReadDiff(context.Background(), session.DiffRequest{
		WorktreeRoot: fixture.root,
		Kind:         session.DiffSession,
		Files:        []string{"main.go"},
		ExpectedHashes: map[string]string{
			"main.go": expectedHash,
		},
	})
	if err != nil {
		t.Fatalf("read session diff: %v", err)
	}
	if sessionResult.Drifted {
		t.Fatal("matching post-patch hash was marked drifted")
	}
	sessionResult, err = fixture.service.ReadDiff(context.Background(), session.DiffRequest{
		WorktreeRoot: fixture.root,
		Kind:         session.DiffSession,
		Files:        []string{"main.go"},
		ExpectedHashes: map[string]string{
			"main.go": strings.Repeat("0", 64),
		},
	})
	if err != nil || !sessionResult.Drifted {
		t.Fatalf("external drift was not detected: result=%#v err=%v", sessionResult, err)
	}

	_, err = fixture.service.ReadDiff(context.Background(), session.DiffRequest{
		WorktreeRoot: fixture.root,
		Kind:         session.DiffWorkspace,
		Files:        []string{".env"},
	})
	if errorCode(err) != session.ErrPermissionDenied {
		t.Fatalf("sensitive diff filter error = %v", err)
	}
}

func TestServiceReadDiffEnforcesOutputLimit(t *testing.T) {
	fixture := newGitFixture(t)
	limits := DefaultLimits()
	limits.MaxDiffBytes = 80
	service := newTestService(t, limits)
	writeTestFile(t, fixture.root, "main.go", strings.Repeat("changed line\n", 100))

	result, err := service.ReadDiff(context.Background(), session.DiffRequest{WorktreeRoot: fixture.root, Kind: session.DiffWorkspace})
	if err != nil {
		t.Fatalf("read limited diff: %v", err)
	}
	if !result.Truncated || len(result.Text) > limits.MaxDiffBytes {
		t.Fatalf("diff limit was not enforced: bytes=%d result=%#v", len(result.Text), result)
	}
}

func TestServiceDiffHidesSensitiveRenameSource(t *testing.T) {
	fixture := newGitFixture(t)
	runTestGit(t, fixture.root, "mv", ".env", "apparently-safe.txt")

	result, err := fixture.service.ReadDiff(context.Background(), session.DiffRequest{WorktreeRoot: fixture.root, Kind: session.DiffWorkspace})
	if err != nil {
		t.Fatalf("read rename diff: %v", err)
	}
	if strings.Contains(result.Text, "initial-secret") || strings.Contains(result.Text, "apparently-safe.txt") {
		t.Fatalf("sensitive rename source leaked through diff: %s", result.Text)
	}
	status, err := fixture.service.GitStatus(context.Background(), agent.GitStatusRequest{WorktreeRoot: fixture.root})
	if err != nil {
		t.Fatalf("read rename status: %v", err)
	}
	if status.HiddenEntries != 1 || len(status.Entries) != 0 {
		t.Fatalf("sensitive rename was exposed in status: %#v", status)
	}
}

func TestServiceSupportsRepositoryWithoutHead(t *testing.T) {
	root := t.TempDir()
	runTestGit(t, root, "init", "-b", "main")
	writeTestFile(t, root, "start.go", "package start\n")
	service := newTestService(t, DefaultLimits())

	state, err := service.ReadWorktreeState(context.Background(), root)
	if err != nil {
		t.Fatalf("read unborn state: %v", err)
	}
	if state.HeadCommit != "" || state.Branch != "main" || !state.Dirty {
		t.Fatalf("unexpected unborn state: %#v", state)
	}
	diff, err := service.ReadDiff(context.Background(), session.DiffRequest{WorktreeRoot: root, Kind: session.DiffWorkspace})
	if err != nil {
		t.Fatalf("read unborn diff: %v", err)
	}
	if !strings.Contains(diff.Text, "package start") || !containsDiffFile(diff.Files, "start.go") {
		t.Fatalf("unborn diff omitted new file: %#v", diff)
	}
}

func TestNewServiceRejectsUnsafeLimits(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxDiffBytes = 17 << 20
	if _, err := NewService(Dependencies{Limits: limits}); err == nil {
		t.Fatal("expected hard limit validation error")
	}
}

type gitFixture struct {
	root    string
	service *Service
}

func newGitFixture(t *testing.T) gitFixture {
	t.Helper()
	parent := t.TempDir()
	root := filepath.Join(parent, "repo")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatalf("create repository directory: %v", err)
	}
	runTestGit(t, root, "init", "-b", "main")
	runTestGit(t, root, "config", "user.name", "CodePilot Tests")
	runTestGit(t, root, "config", "user.email", "codepilot@example.invalid")
	runTestGit(t, root, "config", "core.autocrlf", "false")
	writeTestFile(t, root, ".gitignore", "ignored.log\n")
	writeTestFile(t, root, "main.go", "package main\n\nfunc answer() int {\n\treturn 41\n}\n")
	writeTestFile(t, root, "src/util.go", "package src\n\nfunc answerText() string { return \"answer\" }\n")
	writeTestFile(t, root, ".env", "API_KEY=initial-secret\n")
	writeTestFile(t, root, "server.pem", "private certificate material\n")
	runTestGit(t, root, "add", "-A")
	runTestGit(t, root, "commit", "-m", "initial")
	return gitFixture{root: root, service: newTestService(t, DefaultLimits())}
}

func newTestService(t *testing.T, limits Limits) *Service {
	t.Helper()
	service, err := NewService(Dependencies{Limits: limits})
	if err != nil {
		t.Fatalf("create workspace service: %v", err)
	}
	return service
}

func runTestGit(t *testing.T, root string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
}

func writeTestFile(t *testing.T, root string, relative string, content string) {
	t.Helper()
	writeTestBytes(t, root, relative, []byte(content))
}

func writeTestBytes(t *testing.T, root string, relative string, content []byte) {
	t.Helper()
	pathValue := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(pathValue), 0o755); err != nil {
		t.Fatalf("create parent directory: %v", err)
	}
	if err := os.WriteFile(pathValue, content, 0o600); err != nil {
		t.Fatalf("write %s: %v", relative, err)
	}
}

func filePaths(files []agent.FileInfo) []string {
	values := make([]string, 0, len(files))
	for _, value := range files {
		values = append(values, value.Path)
	}
	return values
}

func containsDiffFile(files []session.DiffFile, pathValue string) bool {
	for _, value := range files {
		if value.Path == pathValue {
			return true
		}
	}
	return false
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func errorCode(err error) session.ErrorCode {
	var appError *session.AppError
	if errors.As(err, &appError) {
		return appError.Code
	}
	return ""
}
