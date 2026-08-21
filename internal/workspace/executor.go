package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/eaglc/codepilot/internal/session"
)

const (
	maxCommandTimeout     = 30 * time.Minute
	maxCommandOutputBytes = 16 << 20
	maxCommandArguments   = 256
	maxCommandArgument    = 4 << 10
)

// ProcessSpec describes one allowlisted long-lived process without a shell.
type ProcessSpec struct {
	ID           string
	Program      string
	Args         []string
	Dir          string
	EnvAllowlist []string
}

// CommandProcess exposes only the standard streams and lifecycle operations
// needed by a protocol adapter such as an LSP client. Wait is called once;
// Kill implementations must close the process so Wait and pipe I/O unblock.
type CommandProcess interface {
	Stdin() io.WriteCloser
	Stdout() io.ReadCloser
	Stderr() io.ReadCloser
	Wait() error
	Kill() error
}

// inheritedEnvironmentAllowlist names the only opt-in variables a trusted
// check plan may copy from the host process.
var inheritedEnvironmentAllowlist = map[string]struct{}{
	"CGO_ENABLED":             {},
	"GOCACHE":                 {},
	"GOENV":                   {},
	"GOMODCACHE":              {},
	"GONOPROXY":               {},
	"GONOSUMDB":               {},
	"GOPATH":                  {},
	"GOPRIVATE":               {},
	"GOPROXY":                 {},
	"GOROOT":                  {},
	"GOSUMDB":                 {},
	"GOTMPDIR":                {},
	"PYTHONDONTWRITEBYTECODE": {},
	"PYTHONUTF8":              {},
	"VIRTUAL_ENV":             {},
}

// baseEnvironment contains the minimum platform variables needed to locate and
// start approved executables. Arbitrary parent environment is never inherited.
var baseEnvironment = []string{
	"HOME",
	"LANG",
	"LC_ALL",
	// The Go toolchain uses LOCALAPPDATA for its default build cache on Windows.
	"LOCALAPPDATA",
	"PATH",
	"PATHEXT",
	"SYSTEMROOT",
	"TEMP",
	"TMP",
	"TMPDIR",
	"USERPROFILE",
	"WINDIR",
}

// LocalCommandExecutor runs allowlisted checks and language servers without a shell.
type LocalCommandExecutor struct{}

// NewLocalCommandExecutor creates a stateless local command executor.
func NewLocalCommandExecutor() *LocalCommandExecutor {
	return &LocalCommandExecutor{}
}

// Run validates and runs one structured check command with bounded output.
func (e *LocalCommandExecutor) Run(ctx context.Context, spec CommandSpec) (CommandResult, error) {
	if err := ctx.Err(); err != nil {
		return CommandResult{}, err
	}
	directory, err := validateCommandSpec(ctx, spec)
	if err != nil {
		return CommandResult{}, err
	}
	environment, err := buildCommandEnvironment(spec.EnvAllowlist)
	if err != nil {
		return CommandResult{}, err
	}

	commandContext, cancel := context.WithTimeout(ctx, spec.Timeout)
	defer cancel()
	command := exec.CommandContext(commandContext, spec.Program, spec.Args...)
	command.Dir = directory
	command.Env = environment
	output := newCommandOutput(spec.MaxOutputBytes)
	command.Stdout = output.writer(commandStdout)
	command.Stderr = output.writer(commandStderr)
	startedAt := time.Now()
	runErr := command.Run()
	result := output.result(time.Since(startedAt))
	if command.ProcessState != nil {
		result.ExitCode = command.ProcessState.ExitCode()
	}
	if errors.Is(commandContext.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
		result.TimedOut = true
		return result, nil
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if runErr == nil {
		return result, nil
	}
	var exitError *exec.ExitError
	if errors.As(runErr, &exitError) {
		return result, nil
	}
	return result, workspaceAppError(session.ErrWorkspaceUnavailable, "workspace.run_command", "The approved check command could not be started.", fmt.Errorf("start %s: %w", spec.Program, runErr))
}

// Start validates and starts one allowlisted language-server process. The
// context gates startup but does not own the long-lived process; the caller
// must terminate it and call Wait.
func (e *LocalCommandExecutor) Start(ctx context.Context, spec ProcessSpec) (CommandProcess, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	directory, err := validateProcessSpec(ctx, spec)
	if err != nil {
		return nil, err
	}
	environment, err := buildCommandEnvironment(spec.EnvAllowlist)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	command := exec.Command(spec.Program, spec.Args...)
	command.Dir = directory
	command.Env = environment
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, workspaceAppError(session.ErrWorkspaceUnavailable, "workspace.start_process", "The language server could not be prepared.", err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, workspaceAppError(session.ErrWorkspaceUnavailable, "workspace.start_process", "The language server could not be prepared.", err)
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, workspaceAppError(session.ErrWorkspaceUnavailable, "workspace.start_process", "The language server could not be prepared.", err)
	}
	if err := command.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		return nil, workspaceAppError(session.ErrWorkspaceUnavailable, "workspace.start_process", "The approved language server could not be started.", fmt.Errorf("start %s: %w", spec.Program, err))
	}
	return &localCommandProcess{command: command, stdin: stdin, stdout: stdout, stderr: stderr}, nil
}

type localCommandProcess struct {
	command *exec.Cmd
	stdin   io.WriteCloser
	stdout  io.ReadCloser
	stderr  io.ReadCloser
}

func (p *localCommandProcess) Stdin() io.WriteCloser { return p.stdin }
func (p *localCommandProcess) Stdout() io.ReadCloser { return p.stdout }
func (p *localCommandProcess) Stderr() io.ReadCloser { return p.stderr }

func (p *localCommandProcess) Wait() error {
	return p.command.Wait()
}

func (p *localCommandProcess) Kill() error {
	if p == nil || p.command == nil || p.command.Process == nil || p.command.ProcessState != nil {
		return nil
	}
	err := p.command.Process.Kill()
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}

func validateProcessSpec(ctx context.Context, spec ProcessSpec) (string, error) {
	if strings.TrimSpace(spec.ID) == "" || len(spec.ID) > 128 || containsControl(spec.ID) {
		return "", invalidCommand("A valid language-server command ID is required.")
	}
	if len(spec.Args) > maxCommandArguments {
		return "", invalidCommand("The language-server command has too many arguments.")
	}
	for _, argument := range spec.Args {
		if len(argument) > maxCommandArgument || containsControl(argument) || unsafeCommandPathArgument(argument) {
			return "", invalidCommand("A language-server argument is invalid.")
		}
	}
	if err := validateLanguageServerCommand(spec.Program, spec.Args); err != nil {
		return "", err
	}
	if _, err := buildCommandEnvironment(spec.EnvAllowlist); err != nil {
		return "", err
	}
	directory, err := validateCommandDirectory(ctx, spec.Dir)
	if err != nil {
		return "", err
	}
	rootValue, _, err := gitLine(ctx, directory, 64<<10, []int{0}, "rev-parse", "--path-format=absolute", "--show-toplevel")
	if err != nil {
		return "", workspaceAppError(session.ErrWorkspaceUnavailable, "workspace.start_process", "The language server directory is unavailable.", err)
	}
	root, err := canonicalExistingDirectory(rootValue)
	if err != nil {
		return "", err
	}
	if !sameFilesystemPath(root, directory) {
		return "", invalidCommand("Language servers can start only at the Git worktree root.")
	}
	return directory, nil
}

func validateLanguageServerCommand(program string, arguments []string) error {
	if strings.TrimSpace(program) != program || program == "" || len(program) > 128 || filepath.Base(program) != program || filepath.VolumeName(program) != "" || strings.ContainsAny(program, `/\\`) || containsControl(program) {
		return invalidCommand("Only approved language-server executable names without paths are allowed.")
	}
	name := strings.TrimSuffix(strings.ToLower(program), ".exe")
	valid := name == "gopls" && slices.Equal(arguments, []string{"serve"})
	valid = valid || (name == "pyright-langserver" || name == "basedpyright-langserver") && slices.Equal(arguments, []string{"--stdio"})
	if !valid {
		return invalidCommand("The executable or arguments are not in the language-server allowlist.")
	}
	return nil
}

func sameFilesystemPath(left string, right string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
	}
	return filepath.Clean(left) == filepath.Clean(right)
}

// validateCommandSpec checks every side-effect-bearing command fact before a
// child process is created and returns a canonical directory inside a worktree.
func validateCommandSpec(ctx context.Context, spec CommandSpec) (string, error) {
	if strings.TrimSpace(spec.ID) == "" || len(spec.ID) > 128 || containsControl(spec.ID) {
		return "", invalidCommand("A valid command plan ID is required.")
	}
	if spec.Timeout <= 0 || spec.Timeout > maxCommandTimeout {
		return "", invalidCommand("The command timeout is outside the allowed range.")
	}
	if spec.MaxOutputBytes <= 0 || spec.MaxOutputBytes > maxCommandOutputBytes {
		return "", invalidCommand("The command output limit is outside the allowed range.")
	}
	if len(spec.Args) > maxCommandArguments {
		return "", invalidCommand("The command has too many arguments.")
	}
	for _, argument := range spec.Args {
		if len(argument) > maxCommandArgument || containsControl(argument) {
			return "", invalidCommand("A command argument is invalid or too long.")
		}
		if unsafeCommandPathArgument(argument) {
			return "", invalidCommand("Command arguments cannot address paths outside the worktree.")
		}
	}
	if err := validateAllowedCommand(spec.Program, spec.Args); err != nil {
		return "", err
	}
	if _, err := buildCommandEnvironment(spec.EnvAllowlist); err != nil {
		return "", err
	}
	directory, err := validateCommandDirectory(ctx, spec.Dir)
	if err != nil {
		return "", err
	}
	return directory, nil
}

// validateAllowedCommand limits execution to the MVP verification commands and
// rejects flags that could load code or write artifacts outside the worktree.
func validateAllowedCommand(program string, arguments []string) error {
	if strings.TrimSpace(program) != program || program == "" || len(program) > 128 || filepath.Base(program) != program || filepath.VolumeName(program) != "" || strings.ContainsAny(program, `/\\`) || containsControl(program) {
		return invalidCommand("Only approved executable names without paths are allowed.")
	}
	name := strings.TrimSuffix(strings.ToLower(program), ".exe")
	switch {
	case name == "go":
		if len(arguments) == 0 || (arguments[0] != "test" && arguments[0] != "vet") {
			return invalidCommand("Only go test and go vet checks are allowed.")
		}
		if err := validateGoArguments(arguments[1:]); err != nil {
			return err
		}
	case name == "python" || name == "python3" || name == "py":
		if len(arguments) < 2 || arguments[0] != "-m" || arguments[1] != "pytest" {
			return invalidCommand("Python checks must use python -m pytest.")
		}
		if err := validatePytestArguments(arguments[2:]); err != nil {
			return err
		}
	case name == "pytest":
		if err := validatePytestArguments(arguments); err != nil {
			return err
		}
	default:
		return invalidCommand("The executable is not in the project-check allowlist.")
	}
	return nil
}

func validateGoArguments(arguments []string) error {
	expectsFlagValue := false
	for _, argument := range arguments {
		if forbiddenGoArgument(argument) {
			return invalidCommand("A Go check argument can execute or write outside the approved check plan.")
		}
		if expectsFlagValue {
			expectsFlagValue = false
			continue
		}
		if strings.HasPrefix(argument, "-") {
			flag := strings.ToLower(argument)
			if index := strings.IndexByte(flag, '='); index >= 0 {
				flag = flag[:index]
			} else if goFlagConsumesNextArgument(flag) {
				expectsFlagValue = true
			}
			continue
		}
		if argument != "." && !strings.HasPrefix(strings.ReplaceAll(argument, "\\", "/"), "./") {
			return invalidCommand("Go checks can target only packages inside the current worktree.")
		}
	}
	if expectsFlagValue {
		return invalidCommand("A Go check flag is missing its value.")
	}
	return nil
}

func goFlagConsumesNextArgument(flag string) bool {
	switch flag {
	case "-bench", "-benchtime", "-count", "-cpu", "-list", "-p", "-parallel", "-printfuncs", "-run", "-shuffle", "-tags", "-timeout", "-vet":
		return true
	default:
		return false
	}
}

func forbiddenGoArgument(argument string) bool {
	lower := strings.ToLower(argument)
	for _, prefix := range []string{
		"-args", "-coverprofile", "-cpuprofile", "-exec", "-memprofile", "-modfile",
		"-o", "-outputdir", "-overlay", "-test.cpuprofile", "-test.memprofile",
		"-test.outputdir", "-test.trace", "-toolexec", "-trace", "-workfile",
	} {
		if lower == prefix || strings.HasPrefix(lower, prefix+"=") {
			return true
		}
	}
	return strings.HasPrefix(argument, "@")
}

func validatePytestArguments(arguments []string) error {
	for _, argument := range arguments {
		lower := strings.ToLower(argument)
		for _, prefix := range []string{"-o", "-p", "--basetemp", "--junit-xml", "--junitxml", "--override-ini"} {
			if lower == prefix || strings.HasPrefix(lower, prefix+"=") {
				return invalidCommand("A pytest argument can load code or write outside the approved check plan.")
			}
		}
		if strings.HasPrefix(argument, "@") {
			return invalidCommand("Pytest argument files are not allowed.")
		}
	}
	return nil
}

func validateCommandDirectory(ctx context.Context, value string) (string, error) {
	if !filepath.IsAbs(value) {
		return "", invalidCommand("The command directory must be an absolute worktree path.")
	}
	directory, err := canonicalExistingDirectory(value)
	if err != nil {
		return "", err
	}
	rootValue, _, err := gitLine(ctx, directory, 64<<10, []int{0}, "rev-parse", "--path-format=absolute", "--show-toplevel")
	if err != nil {
		return "", workspaceAppError(session.ErrWorkspaceUnavailable, "workspace.validate_command", "The command directory is not in an available Git worktree.", err)
	}
	root, err := canonicalExistingDirectory(rootValue)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(root, directory)
	if err != nil || pathLeavesRoot(relative) {
		return "", invalidCommand("The command directory leaves its Git worktree.")
	}
	if relative != "." {
		for _, component := range strings.FieldsFunc(relative, func(value rune) bool { return value == '/' || value == '\\' }) {
			if isSensitiveDirectory(component) {
				return "", workspaceAppError(session.ErrPermissionDenied, "workspace.validate_command", "Commands cannot run in a protected directory.", nil)
			}
		}
	}
	return directory, nil
}

func buildCommandEnvironment(requested []string) ([]string, error) {
	names := make(map[string]struct{}, len(baseEnvironment)+len(requested))
	for _, name := range baseEnvironment {
		names[name] = struct{}{}
	}
	for _, value := range requested {
		trimmed := strings.TrimSpace(value)
		name := strings.ToUpper(trimmed)
		if name == "" || trimmed != value || strings.Contains(name, "=") || (runtime.GOOS != "windows" && name != value) {
			return nil, invalidCommand("An environment allowlist entry is invalid.")
		}
		if _, allowed := inheritedEnvironmentAllowlist[name]; !allowed {
			return nil, invalidCommand("The command requested an environment variable outside the allowlist.")
		}
		names[name] = struct{}{}
	}
	ordered := make([]string, 0, len(names))
	for name := range names {
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)
	environment := make([]string, 0, len(ordered)+4)
	for _, name := range ordered {
		if value, exists := os.LookupEnv(name); exists {
			environment = append(environment, name+"="+value)
		}
	}
	environment = append(environment,
		"GCM_INTERACTIVE=never",
		"GIT_TERMINAL_PROMPT=0",
		"NO_COLOR=1",
		"PIP_NO_INPUT=1",
	)
	return environment, nil
}

func unsafeCommandPathArgument(argument string) bool {
	if filepath.IsAbs(argument) || filepath.VolumeName(argument) != "" {
		return true
	}
	normalized := strings.ReplaceAll(argument, "\\", "/")
	return normalized == ".." || strings.HasPrefix(normalized, "../") || strings.Contains(normalized, "/../")
}

func containsControl(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) {
			return true
		}
	}
	return false
}

func invalidCommand(message string) error {
	return workspaceAppError(session.ErrInvalidInput, "workspace.run_command", message, nil)
}

type commandStream uint8

const (
	commandStdout commandStream = iota
	commandStderr
)

// commandOutput gives stdout and stderr one shared, mutex-protected byte budget
// because exec may write both streams concurrently.
type commandOutput struct {
	mu        sync.Mutex
	remaining int
	stdout    bytes.Buffer
	stderr    bytes.Buffer
	truncated bool
}

func newCommandOutput(limit int) *commandOutput {
	return &commandOutput{remaining: limit}
}

func (o *commandOutput) writer(stream commandStream) io.Writer {
	return commandOutputWriter{output: o, stream: stream}
}

func (o *commandOutput) result(duration time.Duration) CommandResult {
	o.mu.Lock()
	defer o.mu.Unlock()
	return CommandResult{
		ExitCode:  -1,
		Stdout:    o.stdout.String(),
		Stderr:    o.stderr.String(),
		Duration:  duration,
		Truncated: o.truncated,
	}
}

type commandOutputWriter struct {
	output *commandOutput
	stream commandStream
}

func (w commandOutputWriter) Write(value []byte) (int, error) {
	originalLength := len(value)
	w.output.mu.Lock()
	defer w.output.mu.Unlock()
	if len(value) > w.output.remaining {
		value = value[:max(w.output.remaining, 0)]
		w.output.truncated = originalLength > 0
	}
	if w.output.remaining > 0 {
		var err error
		if w.stream == commandStdout {
			_, err = w.output.stdout.Write(value)
		} else {
			_, err = w.output.stderr.Write(value)
		}
		w.output.remaining -= len(value)
		if err != nil {
			return 0, err
		}
	}
	return originalLength, nil
}
