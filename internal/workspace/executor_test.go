package workspace

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eaglc/codepilot/internal/session"
)

func TestLocalCommandExecutorRunsAllowedGoCheck(t *testing.T) {
	root := newCommandFixture(t, "package fixture\n\nimport \"testing\"\n\nfunc TestPass(t *testing.T) {}\n")
	executor := NewLocalCommandExecutor()

	result, err := executor.Run(context.Background(), commandSpec(root, "go", "test", "./..."))
	if err != nil {
		t.Fatalf("run go test: %v", err)
	}
	if result.ExitCode != 0 || result.TimedOut || result.Truncated || !strings.Contains(result.Stdout, "ok") {
		t.Fatalf("unexpected command result: %#v", result)
	}
}

func TestLocalCommandExecutorReturnsBoundedFailedCheckOutput(t *testing.T) {
	source := "package fixture\n\nimport (\n\t\"strings\"\n\t\"testing\"\n)\n\nfunc TestFail(t *testing.T) {\n\tt.Log(strings.Repeat(\"x\", 4096))\n\tt.Fail()\n}\n"
	root := newCommandFixture(t, source)
	executor := NewLocalCommandExecutor()
	spec := commandSpec(root, "go", "test", "-v", "./...")
	spec.MaxOutputBytes = 256

	result, err := executor.Run(context.Background(), spec)
	if err != nil {
		t.Fatalf("run failing go test: %v", err)
	}
	if result.ExitCode == 0 || !result.Truncated || len(result.Stdout)+len(result.Stderr) > spec.MaxOutputBytes {
		t.Fatalf("unexpected failed command result: %#v", result)
	}
}

func TestLocalCommandExecutorEnforcesTimeout(t *testing.T) {
	root := newCommandFixture(t, "package fixture\n\nimport (\n\t\"testing\"\n\t\"time\"\n)\n\nfunc TestSlow(t *testing.T) { time.Sleep(30 * time.Second) }\n")
	executor := NewLocalCommandExecutor()
	spec := commandSpec(root, "go", "test", "-run", "TestSlow", "./...")
	spec.Timeout = 250 * time.Millisecond

	result, err := executor.Run(context.Background(), spec)
	if err != nil {
		t.Fatalf("run timed command: %v", err)
	}
	if !result.TimedOut || result.ExitCode == 0 || result.Duration > 5*time.Second {
		t.Fatalf("timeout was not enforced: %#v", result)
	}
}

func TestLocalCommandExecutorRejectsCommandsOutsideAllowlist(t *testing.T) {
	tests := []struct {
		name    string
		program string
		args    []string
	}{
		{name: "shell", program: "powershell", args: []string{"-Command", "go test ./..."}},
		{name: "program path", program: filepath.Join("bin", "go"), args: []string{"test", "./..."}},
		{name: "go run", program: "go", args: []string{"run", "."}},
		{name: "go exec flag", program: "go", args: []string{"test", "-exec=helper", "./..."}},
		{name: "remote Go package", program: "go", args: []string{"test", "example.com/untrusted/package"}},
		{name: "python code", program: "python", args: []string{"-c", "print('unsafe')"}},
		{name: "pytest plugin", program: "pytest", args: []string{"-p", "plugin"}},
		{name: "response file", program: "pytest", args: []string{"@arguments.txt"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateAllowedCommand(test.program, test.args); errorCode(err) != session.ErrInvalidInput {
				t.Fatalf("allowlist error = %v", err)
			}
		})
	}
}

func TestLocalCommandExecutorRejectsUnsafeDirectoryAndArguments(t *testing.T) {
	executor := NewLocalCommandExecutor()
	nonWorktree := t.TempDir()
	runTestGit(t, nonWorktree, "init", "--bare")
	spec := commandSpec(nonWorktree, "go", "test", "./...")
	if _, err := executor.Run(context.Background(), spec); errorCode(err) != session.ErrWorkspaceUnavailable {
		t.Fatalf("non-worktree directory error = %v", err)
	}

	root := newCommandFixture(t, "package fixture\n")
	spec = commandSpec(root, "go", "test", "../outside")
	if _, err := executor.Run(context.Background(), spec); errorCode(err) != session.ErrInvalidInput {
		t.Fatalf("escaping argument error = %v", err)
	}
}

func TestValidateProcessSpecAllowsOnlyLanguageServersAtWorktreeRoot(t *testing.T) {
	root := newCommandFixture(t, "package fixture\n")
	for _, spec := range []ProcessSpec{
		{ID: "lsp-go", Program: "gopls", Args: []string{"serve"}, Dir: root},
		{ID: "lsp-python", Program: "pyright-langserver", Args: []string{"--stdio"}, Dir: root},
		{ID: "lsp-basedpyright", Program: "basedpyright-langserver", Args: []string{"--stdio"}, Dir: root},
	} {
		if directory, err := validateProcessSpec(context.Background(), spec); err != nil || !sameFilesystemPath(directory, root) {
			t.Fatalf("valid process spec %#v: directory=%q err=%v", spec, directory, err)
		}
	}

	nested := filepath.Join(root, "nested")
	if err := os.Mkdir(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	tests := []ProcessSpec{
		{ID: "shell", Program: "powershell", Args: []string{"-Command", "gopls serve"}, Dir: root},
		{ID: "extra", Program: "gopls", Args: []string{"serve", "-remote=auto"}, Dir: root},
		{ID: "nested", Program: "gopls", Args: []string{"serve"}, Dir: nested},
		{ID: "secret-env", Program: "gopls", Args: []string{"serve"}, Dir: root, EnvAllowlist: []string{"API_TOKEN"}},
	}
	for _, spec := range tests {
		if _, err := validateProcessSpec(context.Background(), spec); errorCode(err) != session.ErrInvalidInput {
			t.Fatalf("unsafe process spec %#v error = %v", spec, err)
		}
	}
}

func TestBuildCommandEnvironmentUsesExplicitAllowlist(t *testing.T) {
	t.Setenv("CODEPILOT_TEST_SECRET", "must-not-leak")
	t.Setenv("GOCACHE", filepath.Join(t.TempDir(), "cache"))
	environment, err := buildCommandEnvironment([]string{"GOCACHE"})
	if err != nil {
		t.Fatalf("build environment: %v", err)
	}
	joined := strings.Join(environment, "\n")
	if strings.Contains(joined, "CODEPILOT_TEST_SECRET") || !strings.Contains(joined, "GOCACHE=") {
		t.Fatalf("unexpected command environment: %q", joined)
	}
	if _, err := buildCommandEnvironment([]string{"API_TOKEN"}); errorCode(err) != session.ErrInvalidInput {
		t.Fatalf("secret environment request error = %v", err)
	}
}

func TestBuildCommandEnvironmentPreservesWindowsBuildCacheLocation(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("LOCALAPPDATA is a Windows process requirement")
	}
	location := filepath.Join(t.TempDir(), "local-app-data")
	t.Setenv("LOCALAPPDATA", location)

	environment, err := buildCommandEnvironment(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(environment, "\n"), "LOCALAPPDATA="+location) {
		t.Fatalf("environment does not preserve LOCALAPPDATA: %q", environment)
	}
}

func TestCommandOutputSharesOneLimitAcrossStreams(t *testing.T) {
	output := newCommandOutput(128)
	stdout := output.writer(commandStdout)
	stderr := output.writer(commandStderr)
	var waitGroup sync.WaitGroup
	waitGroup.Add(2)
	go func() {
		defer waitGroup.Done()
		_, _ = stdout.Write([]byte(strings.Repeat("a", 100)))
	}()
	go func() {
		defer waitGroup.Done()
		_, _ = stderr.Write([]byte(strings.Repeat("b", 100)))
	}()
	waitGroup.Wait()
	result := output.result(time.Second)
	if len(result.Stdout)+len(result.Stderr) != 128 || !result.Truncated {
		t.Fatalf("unexpected bounded output: %#v", result)
	}
}

func newCommandFixture(t *testing.T, testSource string) string {
	t.Helper()
	root := t.TempDir()
	runTestGit(t, root, "init", "-b", "main")
	writeTestFile(t, root, "go.mod", "module example.invalid/commandfixture\n\ngo 1.26\n")
	writeTestFile(t, root, "fixture_test.go", testSource)
	return root
}

func commandSpec(root string, program string, arguments ...string) CommandSpec {
	return CommandSpec{
		ID:             "check_test",
		Program:        program,
		Args:           arguments,
		Dir:            root,
		EnvAllowlist:   []string{"GOCACHE", "GOMODCACHE", "GOTMPDIR"},
		Timeout:        time.Minute,
		MaxOutputBytes: 1 << 20,
	}
}
