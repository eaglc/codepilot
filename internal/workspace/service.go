package workspace

import (
	"context"
	"errors"
	"sync"

	"github.com/eaglc/codepilot/internal/agent"
	"github.com/eaglc/codepilot/internal/session"
)

var (
	_ session.WorkspaceReader = (*Service)(nil)
	_ agent.WorkspaceTools    = (*Service)(nil)
)

// ActionAuthorizer evaluates already hard-validated workspace side effects.
type ActionAuthorizer interface {
	Authorize(ctx context.Context, mode session.PermissionMode, action session.Action) (session.Authorization, error)
}

// CommandExecutor runs checks and starts allowlisted protocol processes without a shell.
type CommandExecutor interface {
	Run(ctx context.Context, spec CommandSpec) (CommandResult, error)
	Start(ctx context.Context, spec ProcessSpec) (CommandProcess, error)
}

// Limits bounds filesystem and Git data exposed outside this package.
type Limits struct {
	MaxFiles           int
	MaxSearchResults   int
	MaxScannedFiles    int
	MaxSearchFileBytes int64
	MaxReadBytes       int64
	MaxReadLines       int
	MaxDiffBytes       int
	MaxGitOutputBytes  int
}

// DefaultLimits returns conservative MVP read limits.
func DefaultLimits() Limits {
	return Limits{
		MaxFiles:           500,
		MaxSearchResults:   200,
		MaxScannedFiles:    10_000,
		MaxSearchFileBytes: 2 << 20,
		MaxReadBytes:       512 << 10,
		MaxReadLines:       1_000,
		MaxDiffBytes:       1 << 20,
		MaxGitOutputBytes:  2 << 20,
	}
}

// Dependencies contains explicit workspace capabilities and limits.
type Dependencies struct {
	Authorizer ActionAuthorizer
	Executor   CommandExecutor
	Limits     Limits
}

// Service implements the safe workspace capabilities used by sessions and agents.
type Service struct {
	authorizer ActionAuthorizer
	executor   CommandExecutor
	limits     Limits
	patchMu    sync.Mutex
	proposals  map[string]patchProposal
}

// NewService creates a workspace service with explicit side-effect boundaries.
func NewService(deps Dependencies) (*Service, error) {
	limits := deps.Limits
	if limits == (Limits{}) {
		limits = DefaultLimits()
	}
	if err := validateLimits(limits); err != nil {
		return nil, err
	}
	return &Service{
		authorizer: deps.Authorizer,
		executor:   deps.Executor,
		limits:     limits,
		proposals:  make(map[string]patchProposal),
	}, nil
}

func validateLimits(value Limits) error {
	if value.MaxFiles <= 0 || value.MaxSearchResults <= 0 || value.MaxScannedFiles <= 0 ||
		value.MaxSearchFileBytes <= 0 || value.MaxReadBytes <= 0 || value.MaxReadLines <= 0 ||
		value.MaxDiffBytes <= 0 || value.MaxGitOutputBytes <= 0 {
		return errors.New("create workspace service: all limits must be positive")
	}
	if value.MaxFiles > 10_000 || value.MaxSearchResults > 2_000 || value.MaxScannedFiles > 100_000 ||
		value.MaxSearchFileBytes > 32<<20 || value.MaxReadBytes > 8<<20 || value.MaxReadLines > 10_000 ||
		value.MaxDiffBytes > 16<<20 || value.MaxGitOutputBytes > 32<<20 {
		return errors.New("create workspace service: one or more limits exceed hard safety bounds")
	}
	return nil
}
