package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"time"

	"github.com/eaglc/codepilot/internal/agent"
	"github.com/eaglc/codepilot/internal/approval"
	"github.com/eaglc/codepilot/internal/config"
	"github.com/eaglc/codepilot/internal/contextmanager"
	"github.com/eaglc/codepilot/internal/credential"
	"github.com/eaglc/codepilot/internal/language"
	"github.com/eaglc/codepilot/internal/lsp"
	"github.com/eaglc/codepilot/internal/provider"
	"github.com/eaglc/codepilot/internal/session"
	"github.com/eaglc/codepilot/internal/sessionstore"
	"github.com/eaglc/codepilot/internal/ui"
	"github.com/eaglc/codepilot/internal/workspace"
)

const eventBridgeCapacity = 256

type foundation struct {
	workingDirectory string
	paths            config.Paths
	configuration    config.Config
	trustWorkspace   bool
	lock             *sessionstore.ProcessLock
	store            *sessionstore.FileStore
	profiles         *config.ProviderFileStore
	credentials      *credential.FallbackStore
	bridge           *ui.EventBridge
}

type capabilities struct {
	httpClient *http.Client
	providers  *provider.Service
	approvals  *approval.Service
	workspaces *workspace.Service
	languages  *language.Registry
	navigator  *lsp.Navigator
}

type runtime struct {
	checkpoints *agent.MemoryCheckpointStore
	sessions    *session.Service
	snapshot    session.SessionSnapshot
}

func buildFoundation(ctx context.Context, options Options) (foundation, error) {
	if err := ctx.Err(); err != nil {
		return foundation{}, err
	}
	workingDirectory, err := filepath.Abs(options.WorkingDirectory)
	if err != nil {
		return foundation{}, fmt.Errorf("resolve working directory: %w", err)
	}
	workingDirectory = filepath.Clean(workingDirectory)
	paths, err := resolvePaths(options)
	if err != nil {
		return foundation{}, err
	}
	configuration, err := config.Load(filepath.Join(paths.ConfigDir, "config.yaml"))
	if err != nil {
		return foundation{}, err
	}

	base := foundation{
		workingDirectory: workingDirectory,
		paths:            paths,
		configuration:    configuration,
		trustWorkspace:   options.TrustWorkspace,
	}
	base.lock, err = sessionstore.AcquireProcessLock(paths.StateDir)
	if err != nil {
		return foundation{}, err
	}
	base.store, err = sessionstore.NewFileStore(paths.StateDir)
	if err != nil {
		return foundation{}, errors.Join(err, base.lock.Close())
	}
	base.profiles = config.NewProviderFileStore(filepath.Join(paths.ConfigDir, "providers.yaml"))
	keyringStore, err := credential.NewKeyringStore()
	if err != nil {
		return foundation{}, errors.Join(err, closeFoundation(base))
	}
	base.credentials = credential.NewFallbackStore(keyringStore, credential.NewMemoryStore())
	base.bridge = ui.NewEventBridge(eventBridgeCapacity)
	return base, nil
}

func buildCapabilities(base foundation) (capabilities, error) {
	values := capabilities{
		httpClient: &http.Client{Timeout: 30 * time.Second},
		approvals:  approval.NewService(),
	}
	var err error
	values.providers, err = provider.NewService(base.profiles, base.credentials, provider.DefaultAdapters(values.httpClient))
	if err != nil {
		return capabilities{}, errors.Join(err, closeCapabilities(values))
	}
	executor := workspace.NewLocalCommandExecutor()
	values.workspaces, err = workspace.NewService(workspace.Dependencies{
		Authorizer: values.approvals,
		Executor:   executor,
		Limits:     workspace.DefaultLimits(),
	})
	if err != nil {
		return capabilities{}, errors.Join(err, closeCapabilities(values))
	}
	values.languages, err = language.NewRegistry(language.NewGoStrategy(), language.NewPythonStrategy())
	if err != nil {
		return capabilities{}, errors.Join(err, closeCapabilities(values))
	}
	values.navigator, err = lsp.NewNavigator(lsp.Options{Executor: executor, Authorizer: values.approvals})
	if err != nil {
		return capabilities{}, errors.Join(err, closeCapabilities(values))
	}
	return values, nil
}

func buildRuntime(ctx context.Context, base foundation, caps capabilities) (runtime, error) {
	values := runtime{checkpoints: agent.NewMemoryCheckpointStore()}
	resolved, err := caps.workspaces.ResolveWorktree(ctx, base.workingDirectory)
	if err != nil {
		return runtime{}, errors.Join(err, closeRuntime(values))
	}
	_, registered, err := base.store.FindWorktreeByRoot(ctx, resolved.Root)
	if err != nil {
		return runtime{}, errors.Join(err, closeRuntime(values))
	}
	if !registered && !base.trustWorkspace {
		return runtime{}, errors.Join(&TrustRequiredError{Path: resolved.Root}, closeRuntime(values))
	}
	invokers, err := agent.NewEinoInvokerFactory(agent.EinoInvokerDependencies{
		Models:      caps.providers,
		Checkpoints: values.checkpoints,
	})
	if err != nil {
		return runtime{}, errors.Join(err, closeRuntime(values))
	}
	contexts, err := contextmanager.NewManager(contextmanager.NopStrategy{})
	if err != nil {
		return runtime{}, errors.Join(err, closeRuntime(values))
	}
	agents, err := agent.NewFactory(agent.Dependencies{
		Workspaces: caps.workspaces,
		Languages:  caps.languages,
		Authorizer: caps.approvals,
		Invokers:   invokers,
		CodeIntel:  caps.navigator,
		Contexts:   contexts,
	})
	if err != nil {
		return runtime{}, errors.Join(err, closeRuntime(values))
	}
	values.sessions, err = session.NewService(session.Dependencies{
		CodingAgents:      agents,
		SessionStore:      base.store,
		WorkspaceRegistry: base.store,
		WorkspaceReader:   caps.workspaces,
		ModelCatalog:      caps.providers,
		Authorizer:        caps.approvals,
		Events:            base.bridge,
		Limits:            base.configuration.Agent.RunLimits(),
	})
	if err != nil {
		return runtime{}, errors.Join(err, closeRuntime(values))
	}
	values.snapshot, err = values.sessions.Activate(ctx, base.workingDirectory)
	if err != nil {
		return runtime{}, errors.Join(err, closeRuntime(values))
	}
	return values, nil
}

func buildPresentation(base foundation, run runtime) (*ui.Model, error) {
	if run.sessions == nil || base.bridge == nil || run.snapshot.Session.ID == "" {
		return nil, errors.New("build presentation: active session is unavailable")
	}
	return ui.NewModel(run.sessions, base.bridge, run.snapshot), nil
}

func resolvePaths(options Options) (config.Paths, error) {
	paths, err := config.ResolvePaths()
	if err != nil {
		return config.Paths{}, err
	}
	if options.ConfigDir != "" {
		paths.ConfigDir, err = filepath.Abs(options.ConfigDir)
		if err != nil {
			return config.Paths{}, fmt.Errorf("resolve config directory: %w", err)
		}
	}
	if options.StateDir != "" {
		paths.StateDir, err = filepath.Abs(options.StateDir)
		if err != nil {
			return config.Paths{}, fmt.Errorf("resolve state directory: %w", err)
		}
	}
	paths.ConfigDir = filepath.Clean(paths.ConfigDir)
	paths.StateDir = filepath.Clean(paths.StateDir)
	if err := config.ValidatePaths(paths); err != nil {
		return config.Paths{}, err
	}
	return paths, nil
}

func closeRuntime(values runtime) error {
	var closeErrors []error
	if values.sessions != nil {
		closeErrors = append(closeErrors, values.sessions.Close())
	}
	if values.checkpoints != nil {
		closeErrors = append(closeErrors, values.checkpoints.Close())
	}
	return errors.Join(closeErrors...)
}

func closeCapabilities(values capabilities) error {
	var closeErrors []error
	if values.navigator != nil {
		closeErrors = append(closeErrors, values.navigator.Close())
	}
	if values.approvals != nil {
		closeErrors = append(closeErrors, values.approvals.Close())
	}
	if values.httpClient != nil {
		values.httpClient.CloseIdleConnections()
	}
	return errors.Join(closeErrors...)
}

func closeFoundation(values foundation) error {
	var closeErrors []error
	if values.bridge != nil {
		closeErrors = append(closeErrors, values.bridge.Close())
	}
	if values.credentials != nil {
		closeErrors = append(closeErrors, values.credentials.Close())
	}
	if values.store != nil {
		closeErrors = append(closeErrors, values.store.Close())
	}
	if values.lock != nil {
		closeErrors = append(closeErrors, values.lock.Close())
	}
	return errors.Join(closeErrors...)
}
