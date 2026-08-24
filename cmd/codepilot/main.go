package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"

	"github.com/eaglc/codepilot/internal/app"
	"github.com/eaglc/codepilot/internal/buildinfo"
	"github.com/eaglc/codepilot/internal/codingagent"
)

var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(ctx context.Context, arguments []string, stdin io.Reader, stdout io.Writer, stderr io.Writer) int {
	if len(arguments) != 0 && (arguments[0] == "doctor" || arguments[0] == "repair") {
		return runMaintenance(ctx, arguments[0], arguments[1:], stdout, stderr)
	}
	flags := flag.NewFlagSet("codepilot", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "Usage of codepilot:")
		_, _ = fmt.Fprintln(stderr, "       codepilot [flags]")
		_, _ = fmt.Fprintln(stderr, "       codepilot doctor [--state-dir DIR] [--json]")
		_, _ = fmt.Fprintln(stderr, "       codepilot repair [--state-dir DIR] [--json]")
		_, _ = fmt.Fprintln(stderr, "\nCommands:")
		_, _ = fmt.Fprintln(stderr, "  doctor  inspect cross-repository consistency without changing session data")
		_, _ = fmt.Fprintln(stderr, "  repair  explicitly reconcile or reversibly archive inconsistent bindings")
		_, _ = fmt.Fprintln(stderr, "\nFlags:")
		flags.PrintDefaults()
	}
	workspacePath := flags.String("workspace", "", "Git worktree to open (defaults to the current directory)")
	configDir := flags.String("config-dir", "", "override the CodePilot configuration directory")
	stateDir := flags.String("state-dir", "", "override the CodePilot state directory")
	providerProfile := flags.String("provider", "", "provider profile: openai, deepseek, or ollama")
	modelID := flags.String("model", "", "model ID (uses the provider default when omitted)")
	permission := flags.String("permission", "", "workspace permission: ask, read-only/read_only, or auto-edit/auto_edit")
	var sensitivePaths stringListFlag
	flags.Var(&sensitivePaths, "sensitive-path", "additional worktree-relative sensitive file or directory (repeatable)")
	showVersion := flags.Bool("version", false, "print the CodePilot version")
	trustWorkspace := flags.Bool("trust-workspace", false, "trust a new worktree without an interactive confirmation")
	relocateWorktree := flags.String("relocate-worktree", "", "explicitly relocate an unavailable stored worktree ID to --workspace")
	skipRelocation := flags.Bool("skip-relocation", false, "open --workspace as a new binding instead of a detected relocation")
	if err := flags.Parse(arguments); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *showVersion {
		_, _ = fmt.Fprintln(stdout, buildVersionString())
		return 0
	}
	if flags.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, "codepilot: unexpected positional arguments")
		return 2
	}
	workingDirectory := strings.TrimSpace(*workspacePath)
	if workingDirectory == "" {
		var err error
		workingDirectory, err = os.Getwd()
		if err != nil {
			_, _ = fmt.Fprintln(stderr, "codepilot: current directory is unavailable")
			return 1
		}
	}

	// Preserve the original reader so Bubble Tea can recognize an *os.File and
	// use native Windows console input for arrows and other special keys.
	options := app.Options{
		WorkingDirectory: workingDirectory,
		ConfigDir:        strings.TrimSpace(*configDir),
		StateDir:         strings.TrimSpace(*stateDir),
		ProviderProfile:  strings.TrimSpace(*providerProfile),
		Model:            strings.TrimSpace(*modelID),
		Permission:       strings.TrimSpace(*permission),
		SensitivePaths:   append([]string(nil), sensitivePaths...),
		TrustWorkspace:   *trustWorkspace,
		RelocateWorktree: codingagent.WorktreeID(strings.TrimSpace(*relocateWorktree)),
		SkipRelocation:   *skipRelocation,
		Input:            stdin,
		Output:           stdout,
	}
	application, err := app.New(ctx, options)
	input := bufio.NewReader(stdin)
	for attempts := 0; err != nil && attempts < 3; attempts++ {
		if worktreeID, previousPath, newPath, required := app.WorktreeRelocationRequired(err); required {
			_, _ = fmt.Fprintf(stdout, "A saved worktree is unavailable:\n%s\nRelocate it to this matching Git worktree and restore its sessions?\n%s\n[y/N]: ", previousPath, newPath)
			answer, readErr := input.ReadString('\n')
			if readErr != nil && strings.TrimSpace(answer) == "" {
				_, _ = fmt.Fprintln(stderr, "codepilot: worktree relocation was not confirmed")
				return 1
			}
			answer = strings.ToLower(strings.TrimSpace(answer))
			if answer == "y" || answer == "yes" {
				options.RelocateWorktree = worktreeID
			} else {
				options.SkipRelocation = true
			}
			application, err = app.New(ctx, options)
			continue
		}
		if trustPath, required := app.WorkspaceTrustRequired(err); required {
			_, _ = fmt.Fprintf(stdout, "CodePilot will read files and may run explicitly approved actions in:\n%s\nTrust this Git worktree? [y/N]: ", trustPath)
			answer, readErr := input.ReadString('\n')
			if readErr != nil && strings.TrimSpace(answer) == "" {
				_, _ = fmt.Fprintln(stderr, "codepilot: workspace trust was not confirmed")
				return 1
			}
			answer = strings.ToLower(strings.TrimSpace(answer))
			if answer != "y" && answer != "yes" {
				_, _ = fmt.Fprintln(stdout, "Workspace was not trusted; no session was created.")
				return 0
			}
			options.TrustWorkspace = true
			application, err = app.New(ctx, options)
			continue
		}
		break
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "codepilot: "+app.UserMessage(err))
		return 1
	}
	runErr := application.Run(ctx)
	closeErr := application.Close()
	if runErr != nil {
		_, _ = fmt.Fprintln(stderr, "codepilot: "+app.UserMessage(runErr))
		return 1
	}
	if closeErr != nil {
		_, _ = fmt.Fprintln(stderr, "codepilot: some local state could not be closed cleanly")
		return 1
	}
	return 0
}

func buildVersionString() string {
	return buildinfo.Format(version, commit, buildDate)
}

type stringListFlag []string

func (f *stringListFlag) String() string { return strings.Join(*f, ",") }
func (f *stringListFlag) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return errors.New("value cannot be empty")
	}
	*f = append(*f, value)
	return nil
}

func runMaintenance(ctx context.Context, command string, arguments []string, stdout io.Writer, stderr io.Writer) int {
	flags := flag.NewFlagSet("codepilot "+command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	stateDir := flags.String("state-dir", "", "override the CodePilot state directory")
	jsonOutput := flags.Bool("json", false, "write a machine-readable JSON report")
	if err := flags.Parse(arguments); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		_, _ = fmt.Fprintf(stderr, "codepilot %s: unexpected positional arguments\n", command)
		return 2
	}
	options := app.MaintenanceOptions{StateDir: strings.TrimSpace(*stateDir)}
	if command == "doctor" {
		report, err := app.DiagnoseState(ctx, options)
		if err != nil {
			_, _ = fmt.Fprintln(stderr, "codepilot doctor: "+app.UserMessage(err))
			return 1
		}
		if *jsonOutput {
			return writeJSONReport(stdout, stderr, report)
		}
		if len(report.Issues) == 0 {
			_, _ = fmt.Fprintln(stdout, "CodePilot state is consistent.")
			return 0
		}
		_, _ = fmt.Fprintf(stdout, "Found %d consistency issue(s):\n", len(report.Issues))
		for _, issue := range report.Issues {
			_, _ = fmt.Fprintf(stdout, "- %s [%s]: %s repair=%s\n", issue.Kind, issue.ID, issue.Message, issue.RepairAction)
		}
		return 0
	}
	report, err := app.RepairState(ctx, options)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "codepilot repair: "+app.UserMessage(err))
		return 1
	}
	if *jsonOutput {
		code := writeJSONReport(stdout, stderr, report)
		if code != 0 || len(report.After.Issues) != 0 {
			return 1
		}
		return 0
	}
	for _, action := range report.Actions {
		status := "completed"
		if !action.Completed {
			status = "unresolved"
		}
		_, _ = fmt.Fprintf(stdout, "- %s: %s (%s)\n", action.Action, action.Message, status)
	}
	if len(report.After.Issues) != 0 {
		_, _ = fmt.Fprintf(stdout, "Repair finished with %d unresolved issue(s). No durable session data was deleted.\n", len(report.After.Issues))
		return 1
	}
	_, _ = fmt.Fprintln(stdout, "Repair complete. No durable session data was deleted.")
	return 0
}

func writeJSONReport(stdout io.Writer, stderr io.Writer, value any) int {
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		_, _ = fmt.Fprintln(stderr, "codepilot: write report: "+err.Error())
		return 1
	}
	return 0
}
