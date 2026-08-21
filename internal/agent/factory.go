package agent

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/eaglc/codepilot/internal/contextmanager"
	"github.com/eaglc/codepilot/internal/session"
)

var _ session.CodingAgentFactory = (*Factory)(nil)

// Dependencies contains the explicit product capabilities used by CodingAgent.
// CodeIntel is optional; a nil value keeps the seven text/workspace MVP tools.
type Dependencies struct {
	Workspaces WorkspaceTools
	Languages  LanguageResolver
	Authorizer session.Authorizer
	Invokers   AgentInvokerFactory
	CodeIntel  CodeNavigator
	Contexts   *contextmanager.Manager
}

// Factory creates CodingAgent instances while sharing only stateless or
// independently synchronized dependencies.
type Factory struct {
	deps Dependencies
}

// NewFactory validates and captures CodingAgent dependencies.
func NewFactory(deps Dependencies) (*Factory, error) {
	if isNilDependency(deps.Workspaces) || isNilDependency(deps.Languages) || isNilDependency(deps.Authorizer) || isNilDependency(deps.Invokers) || deps.Contexts == nil {
		return nil, errors.New("create coding agent factory: dependencies are incomplete")
	}
	return &Factory{deps: deps}, nil
}

// CreateCodingAgent creates one agent bound to an immutable worktree, session,
// provider profile, model, and set of run limits.
func (f *Factory) CreateCodingAgent(ctx context.Context, config session.CodingAgentConfig) (session.CodingAgent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f == nil {
		return nil, errors.New("create coding agent: factory is nil")
	}
	if err := validateCodingAgentConfig(config); err != nil {
		return nil, err
	}
	invoker, err := f.deps.Invokers.CreateInvoker(ctx)
	if err != nil {
		return nil, fmt.Errorf("create coding agent: create invoker: %w", err)
	}
	if isNilDependency(invoker) {
		return nil, errors.New("create coding agent: invoker factory returned nil")
	}
	return &CodingAgent{
		config:     config,
		workspaces: f.deps.Workspaces,
		languages:  f.deps.Languages,
		authorizer: f.deps.Authorizer,
		invoker:    invoker,
		codeIntel:  f.deps.CodeIntel,
		contexts:   f.deps.Contexts,
	}, nil
}

func validateCodingAgentConfig(config session.CodingAgentConfig) error {
	if config.SessionID == "" || config.WorkspaceID == "" || config.WorktreeID == "" || config.ProviderProfileID == "" || !validInvocationIdentifier(config.ModelID) {
		return errors.New("create coding agent: immutable identifiers are required")
	}
	if strings.TrimSpace(config.WorktreeRoot) == "" || !filepath.IsAbs(config.WorktreeRoot) || filepath.Clean(config.WorktreeRoot) != config.WorktreeRoot {
		return errors.New("create coding agent: worktree root must be an absolute clean path")
	}
	limits := config.Limits
	if limits.MaxSteps <= 0 || limits.MaxSteps > maxInvocationSteps || limits.MaxTurnDuration <= 0 || limits.MaxTurnDuration > maxInvocationDuration || limits.CommandTimeout <= 0 || limits.ToolResultMaxBytes <= 0 || limits.CommandOutputMaxBytes <= 0 {
		return errors.New("create coding agent: run limits are invalid")
	}
	return nil
}
