package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
)

import "github.com/eaglc/codepilot/internal/app"

var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(ctx context.Context, arguments []string, stdin io.Reader, stdout io.Writer, stderr io.Writer) int {
	flags := flag.NewFlagSet("codepilot", flag.ContinueOnError)
	flags.SetOutput(stderr)
	workspacePath := flags.String("workspace", "", "Git worktree to open (defaults to the current directory)")
	configDir := flags.String("config-dir", "", "override the CodePilot configuration directory")
	stateDir := flags.String("state-dir", "", "override the CodePilot state directory")
	showVersion := flags.Bool("version", false, "print the CodePilot version")
	trustWorkspace := flags.Bool("trust-workspace", false, "trust a new worktree without an interactive confirmation")
	if err := flags.Parse(arguments); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *showVersion {
		_, _ = fmt.Fprintln(stdout, "codepilot "+version)
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
		TrustWorkspace:   *trustWorkspace,
		Input:            stdin,
		Output:           stdout,
	}
	application, err := app.New(ctx, options)
	if trustPath, required := app.WorkspaceTrustRequired(err); required {
		_, _ = fmt.Fprintf(stdout, "CodePilot will read files and may run explicitly approved actions in:\n%s\nTrust this Git worktree? [y/N]: ", trustPath)
		input := bufio.NewReader(stdin)
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
