package app

import (
	"context"
	"errors"
	"strings"
	"sync"

	tea "charm.land/bubbletea/v2"
	"github.com/eaglc/codepilot/internal/session"
	"github.com/eaglc/codepilot/internal/ui"
)

// App owns the top-level UI and every process-scoped resource created for it.
type App struct {
	mu        sync.Mutex
	closeOnce sync.Once

	options  Options
	base     foundation
	caps     capabilities
	runtime  runtime
	model    *ui.Model
	running  bool
	closed   bool
	closeErr error
}

// TrustRequiredError indicates that a normalized Git worktree needs explicit
// user confirmation before CodePilot may persist state or inspect its files.
type TrustRequiredError struct {
	Path string
}

// Error returns a safe prompt description containing only the normalized path.
func (e *TrustRequiredError) Error() string {
	if e == nil || strings.TrimSpace(e.Path) == "" {
		return "Workspace trust confirmation is required."
	}
	return "Workspace trust confirmation is required for " + e.Path + "."
}

// New validates options and builds the complete application in dependency order.
func New(ctx context.Context, options Options) (*App, error) {
	if ctx == nil {
		return nil, startupError("app.new", "CodePilot could not start because its context is missing.", nil)
	}
	if strings.TrimSpace(options.WorkingDirectory) == "" {
		return nil, startupError("app.new", "A working directory is required.", nil)
	}
	base, err := buildFoundation(ctx, options)
	if err != nil {
		return nil, startupError("app.build_foundation", "CodePilot could not load its configuration or local state.", err)
	}
	caps, err := buildCapabilities(base)
	if err != nil {
		return nil, errors.Join(startupError("app.build_capabilities", "CodePilot could not initialize its local capabilities.", err), closeFoundation(base))
	}
	run, err := buildRuntime(ctx, base, caps)
	if err != nil {
		return nil, errors.Join(preserveApplicationError(err, "CodePilot could not activate this Git worktree."), closeCapabilities(caps), closeFoundation(base))
	}
	model, err := buildPresentation(base, run)
	if err != nil {
		return nil, errors.Join(startupError("app.build_presentation", "CodePilot could not initialize its terminal interface.", err), closeRuntime(run), closeCapabilities(caps), closeFoundation(base))
	}
	return &App{options: options, base: base, caps: caps, runtime: run, model: model}, nil
}

// Run starts the full-screen terminal program and blocks until the user exits
// or the process context is cancelled. An App can be run only once.
func (a *App) Run(ctx context.Context) error {
	if a == nil {
		return startupError("app.run", "CodePilot is unavailable.", nil)
	}
	if ctx == nil {
		return startupError("app.run", "CodePilot could not run because its context is missing.", nil)
	}
	a.mu.Lock()
	if a.closed || a.running {
		a.mu.Unlock()
		return startupError("app.run", "CodePilot has already been run or closed.", nil)
	}
	a.running = true
	a.mu.Unlock()

	programOptions := []tea.ProgramOption{tea.WithContext(ctx), tea.WithoutSignalHandler()}
	if a.options.DisableInput {
		programOptions = append(programOptions, tea.WithInput(nil))
	} else if a.options.Input != nil {
		programOptions = append(programOptions, tea.WithInput(a.options.Input))
	}
	if a.options.Output != nil {
		programOptions = append(programOptions, tea.WithOutput(a.options.Output))
	}
	_, err := tea.NewProgram(a.model, programOptions...).Run()
	if errors.Is(err, tea.ErrProgramKilled) && ctx.Err() != nil {
		return nil
	}
	if err != nil {
		return startupError("app.run", "The terminal interface stopped unexpectedly.", err)
	}
	return nil
}

// Close releases resources in reverse dependency order and is idempotent.
func (a *App) Close() error {
	if a == nil {
		return nil
	}
	a.closeOnce.Do(func() {
		a.mu.Lock()
		a.closed = true
		a.mu.Unlock()
		a.closeErr = errors.Join(closeRuntime(a.runtime), closeCapabilities(a.caps), closeFoundation(a.base))
	})
	return a.closeErr
}

// UserMessage returns safe terminal copy for a startup or runtime error.
func UserMessage(err error) string {
	if err == nil {
		return ""
	}
	var appError *session.AppError
	if errors.As(err, &appError) && strings.TrimSpace(appError.UserMessage) != "" {
		return appError.UserMessage
	}
	if errors.Is(err, context.Canceled) {
		return "CodePilot was cancelled."
	}
	return "CodePilot could not complete the requested operation."
}

// WorkspaceTrustRequired returns the normalized path that must be confirmed.
func WorkspaceTrustRequired(err error) (string, bool) {
	var trustError *TrustRequiredError
	if !errors.As(err, &trustError) || strings.TrimSpace(trustError.Path) == "" {
		return "", false
	}
	return trustError.Path, true
}

func preserveApplicationError(err error, fallback string) error {
	var appError *session.AppError
	if errors.As(err, &appError) {
		return err
	}
	return startupError("app.start", fallback, err)
}

func startupError(operation string, message string, cause error) error {
	return &session.AppError{
		Code:        session.ErrInternal,
		Operation:   operation,
		UserMessage: message,
		Cause:       cause,
	}
}
