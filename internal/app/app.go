// Package app is the CodePilot composition root.
package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/eaglc/codepilot/internal/agent"
	"github.com/eaglc/codepilot/internal/codingagent"
	"github.com/eaglc/codepilot/internal/codingagent/language"
	"github.com/eaglc/codepilot/internal/codingagent/lsp"
	"github.com/eaglc/codepilot/internal/codingagent/prompt"
	codingtools "github.com/eaglc/codepilot/internal/codingagent/tools"
	"github.com/eaglc/codepilot/internal/codingagent/workspace"
	codingfile "github.com/eaglc/codepilot/internal/codingstore/file"
	"github.com/eaglc/codepilot/internal/contextmanager"
	"github.com/eaglc/codepilot/internal/llm"
	"github.com/eaglc/codepilot/internal/provider"
	providercredential "github.com/eaglc/codepilot/internal/provider/credential"
	"github.com/eaglc/codepilot/internal/provider/deepseek"
	providerfile "github.com/eaglc/codepilot/internal/provider/file"
	"github.com/eaglc/codepilot/internal/provider/ollama"
	"github.com/eaglc/codepilot/internal/provider/openai"
	sessionfile "github.com/eaglc/codepilot/internal/sessionstore/file"
	"github.com/eaglc/codepilot/internal/ui"
)

// Options contains process-owned CodePilot paths and terminal streams.
type Options struct {
	WorkingDirectory string
	ConfigDir        string
	StateDir         string
	ProviderProfile  string
	Model            string
	Permission       string
	SensitivePaths   []string
	TrustWorkspace   bool
	RelocateWorktree codingagent.WorktreeID
	SkipRelocation   bool
	Input            io.Reader
	Output           io.Writer
}

// Application owns the composed product lifecycle.
type Application struct {
	mu       sync.Mutex
	worktree workspace.ResolvedWorktree
	input    io.Reader
	output   io.Writer
	bridge   *ui.EventBridge
	model    *ui.Model
	lease    *sessionfile.StateLease
	lsp      *lsp.Manager
	closed   bool
	running  bool
}

// TrustRequiredError asks the CLI to confirm a resolved worktree before a
// worktree or session binding is persisted.
type TrustRequiredError struct{ Path string }

func (e *TrustRequiredError) Error() string { return "workspace trust is required: " + e.Path }

// RelocationRequiredError asks the CLI to confirm mapping one unavailable
// durable worktree to the newly selected path.
type RelocationRequiredError struct {
	WorktreeID   codingagent.WorktreeID
	PreviousPath string
	NewPath      string
}

func (e *RelocationRequiredError) Error() string {
	return "worktree relocation confirmation is required"
}

// New resolves product paths, restores durable workspace/session state and
// composes the provider-neutral Agent runtime with the terminal UI.
func New(ctx context.Context, options Options) (*Application, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	resolved, err := workspace.ResolveWorktree(ctx, options.WorkingDirectory)
	if err != nil {
		return nil, err
	}
	configDir, stateDir, err := resolveDirectories(options.ConfigDir, options.StateDir)
	if err != nil {
		return nil, err
	}
	configDir, err = prepareDirectory(configDir)
	if err != nil {
		return nil, fmt.Errorf("create application config directory: %w", err)
	}
	stateDir, err = prepareDirectory(stateDir)
	if err != nil {
		return nil, fmt.Errorf("create application state directory: %w", err)
	}
	stateLease, err := sessionfile.AcquireStateLease(stateDir)
	if err != nil {
		return nil, err
	}
	keepLease := false
	defer func() {
		if !keepLease {
			_ = stateLease.Close()
		}
	}()
	productStore, err := codingfile.NewRepository(stateDir)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	binding, err := prepareWorkspaceBinding(ctx, productStore, resolved, options, now)
	if err != nil {
		return nil, err
	}
	workspaceID, worktreeID := binding.workspace.ID, binding.worktree.ID

	agentSessions, err := sessionfile.NewRepository(stateDir)
	if err != nil {
		return nil, err
	}
	profiles, err := providerfile.NewRepository(configDir)
	if err != nil {
		return nil, err
	}
	modelService, selection, selectionForced, err := buildProviders(ctx, profiles, options.ProviderProfile, options.Model)
	if err != nil {
		return nil, err
	}
	providerManager := &productProviderManager{profiles: profiles, service: modelService}
	securityPolicy, err := codingagent.NewSecurityPolicy(nil)
	if err != nil {
		return nil, err
	}
	summarizer, err := contextmanager.NewModelSummarizer(modelService, nil)
	if err != nil {
		return nil, err
	}
	compaction, err := contextmanager.NewCompactionStrategy(contextmanager.Policy{
		RecentTurns: 4, SummarizeThreshold: 60_000, HardLimit: 80_000,
	}, contextmanager.ByteTokenizer{}, summarizer, agentSessions, securityPolicy)
	if err != nil {
		return nil, err
	}
	contexts, err := contextmanager.NewManager(compaction)
	if err != nil {
		return nil, err
	}
	agentRuntime, err := agent.NewRuntime(agent.Dependencies{Models: modelService, Contexts: contexts, Sessions: agentSessions, DataPolicy: securityPolicy})
	if err != nil {
		return nil, err
	}
	bridge, err := ui.NewEventBridge(512)
	if err != nil {
		return nil, err
	}
	languageServers, err := lsp.NewManager(lsp.Options{})
	if err != nil {
		_ = bridge.Close()
		return nil, err
	}
	keepLanguageServers := false
	defer func() {
		if !keepLanguageServers {
			_ = languageServers.Close()
		}
	}()
	workspaceManager, err := codingagent.NewWorkspaceManager(productStore, languageServers)
	if err != nil {
		_ = bridge.Close()
		return nil, err
	}
	service, err := codingagent.NewService(codingagent.Dependencies{
		Sessions: productStore, AgentSessions: agentSessions, Worktrees: workspaceManager, Workspaces: workspaceManager,
		Agent: agentRuntime, Tools: codingtools.NewFactory(codingtools.Options{Artifacts: productStore, Security: securityPolicy, Languages: language.NewDefaultRegistry(), Navigator: languageServers}), Prompts: prompt.NewBuilder(), Events: bridge,
		Providers: providerManager, Limits: agent.RunLimits{MaxSteps: 32, MaxDuration: 30 * time.Minute},
	})
	if err != nil {
		_ = bridge.Close()
		return nil, err
	}
	session, found, err := activeSession(ctx, productStore, worktreeID)
	if err != nil {
		_ = bridge.Close()
		return nil, err
	}
	permission, permissionForced, err := selectedPermission(options.Permission)
	if err != nil {
		_ = bridge.Close()
		return nil, err
	}
	sensitivePaths, err := codingagent.NormalizeSensitivePaths(options.SensitivePaths)
	if err != nil {
		_ = bridge.Close()
		return nil, fmt.Errorf("configure sensitive paths: %w", err)
	}
	sensitivePathsForced := len(options.SensitivePaths) != 0
	if !found {
		session, err = service.CreateSession(ctx, codingagent.Session{
			WorkspaceID: workspaceID, WorktreeID: worktreeID, Title: filepath.Base(resolved.Root),
			ProviderProfileID: string(selection.profile), ModelID: selection.model,
			PermissionMode: permission,
			SensitivePaths: sensitivePaths,
		})
		if err != nil {
			_ = bridge.Close()
			return nil, err
		}
	} else if selectionForced || permissionForced || sensitivePathsForced {
		if selectionForced {
			session.ProviderProfileID = string(selection.profile)
			session.ModelID = selection.model
		}
		if permissionForced {
			session.PermissionMode = permission
		}
		if sensitivePathsForced {
			session.SensitivePaths = sensitivePaths
		}
		session.UpdatedAt = now
		if err := productStore.SaveSession(ctx, session); err != nil {
			_ = bridge.Close()
			return nil, fmt.Errorf("update active Coding session: %w", err)
		}
	}
	preflightContext, cancelPreflight := context.WithTimeout(ctx, 10*time.Second)
	_, preflightErr := modelService.Preflight(preflightContext, llm.ModelRef{Provider: session.ProviderProfileID, Model: session.ModelID})
	cancelPreflight()
	var providerIssue *codingagent.ProviderIssue
	if preflightErr != nil {
		code, message, retryable, classified := provider.ErrorInfo(preflightErr)
		if !classified {
			code, message = provider.ErrorConnectionFailed, "Provider validation failed. Review the profile and try again."
		}
		providerIssue = &codingagent.ProviderIssue{Code: string(code), Message: message, Retryable: retryable}
	}
	var recoveryWarning string
	if preflightErr == nil {
		recoveryContext, cancelRecovery := context.WithTimeout(ctx, 2*time.Minute)
		_, recoveryErr := service.RecoverAutomatically(recoveryContext, session.ID)
		cancelRecovery()
		if recoveryErr != nil {
			recoveryWarning = "Automatic crash recovery stopped safely. Review the pending recovery action before continuing."
		}
	}
	snapshot, err := service.SwitchSession(ctx, session.ID)
	if err != nil {
		_ = bridge.Close()
		return nil, err
	}
	if recoveryWarning != "" {
		snapshot.RecoveryWarnings = append(snapshot.RecoveryWarnings, recoveryWarning)
	}
	model, err := ui.NewModel(ctx, service, bridge, snapshot, ui.WithProviderIssue(providerIssue))
	if err != nil {
		_ = bridge.Close()
		return nil, err
	}
	input := options.Input
	if input == nil {
		input = os.Stdin
	}
	output := options.Output
	if output == nil {
		output = os.Stdout
	}
	keepLease = true
	keepLanguageServers = true
	return &Application{worktree: resolved, input: input, output: output, bridge: bridge, model: model, lease: stateLease, lsp: languageServers}, nil
}

// Run starts the composed terminal product.
func (a *Application) Run(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if a == nil {
		return errors.New("run application: application is nil")
	}
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return errors.New("run application: application is closed")
	}
	if a.running {
		a.mu.Unlock()
		return errors.New("run application: application is already running")
	}
	a.running = true
	model, input, output := a.model, a.input, a.output
	a.mu.Unlock()
	err := ui.Run(ctx, model, input, output)
	a.mu.Lock()
	a.running = false
	a.mu.Unlock()
	return err
}

// Close releases presentation resources exactly once.
func (a *Application) Close() error {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil
	}
	a.closed = true
	var bridgeErr, lspErr, leaseErr error
	if a.bridge != nil {
		bridgeErr = a.bridge.Close()
	}
	if a.lease != nil {
		leaseErr = a.lease.Close()
	}
	if a.lsp != nil {
		lspErr = a.lsp.Close()
	}
	return errors.Join(bridgeErr, lspErr, leaseErr)
}

// WorkspaceTrustRequired extracts a safe resolved path from a trust error.
func WorkspaceTrustRequired(err error) (string, bool) {
	var target *TrustRequiredError
	if !errors.As(err, &target) {
		return "", false
	}
	return target.Path, true
}

// WorktreeRelocationRequired extracts one safe, explicit startup repair prompt.
func WorktreeRelocationRequired(err error) (codingagent.WorktreeID, string, string, bool) {
	var target *RelocationRequiredError
	if !errors.As(err, &target) {
		return "", "", "", false
	}
	return target.WorktreeID, target.PreviousPath, target.NewPath, true
}

// UserMessage converts an internal error into bounded CLI-safe text.
func UserMessage(err error) string {
	if err == nil {
		return ""
	}
	var trust *TrustRequiredError
	if errors.As(err, &trust) {
		return "Workspace trust is required."
	}
	var relocation *RelocationRequiredError
	if errors.As(err, &relocation) {
		return "Worktree relocation confirmation is required."
	}
	message := strings.TrimSpace(codingagent.RedactSensitiveText(err.Error()))
	if len(message) > 512 {
		message = message[:512] + "…"
	}
	return message
}

type modelSelection struct {
	profile provider.ProfileID
	model   string
}

func buildProviders(ctx context.Context, profiles provider.ProfileRepository, requestedProvider string, requestedModel string) (*provider.Service, modelSelection, bool, error) {
	if profiles == nil {
		return nil, modelSelection{}, false, errors.New("configure providers: profile repository is required")
	}
	if err := seedBuiltinProfiles(ctx, profiles); err != nil {
		return nil, modelSelection{}, false, err
	}
	keyringCredentials, err := providercredential.NewKeyringStore("CodePilot")
	if err != nil {
		return nil, modelSelection{}, false, err
	}
	environmentCredentials, err := providercredential.NewEnvironmentStore(map[string]string{
		"openai": "OPENAI_API_KEY", "env.OPENAI_API_KEY": "OPENAI_API_KEY",
		"deepseek": "DEEPSEEK_API_KEY", "env.DEEPSEEK_API_KEY": "DEEPSEEK_API_KEY",
	})
	if err != nil {
		return nil, modelSelection{}, false, err
	}
	credentials, err := providercredential.NewChainStore(keyringCredentials, environmentCredentials)
	if err != nil {
		return nil, modelSelection{}, false, err
	}
	service, err := provider.NewService(profiles, credentials, openai.New(http.DefaultClient), deepseek.New(http.DefaultClient), ollama.New(http.DefaultClient))
	if err != nil {
		return nil, modelSelection{}, false, err
	}
	forcedProvider := strings.TrimSpace(requestedProvider)
	if forcedProvider == "" {
		forcedProvider = strings.TrimSpace(os.Getenv("CODEPILOT_PROVIDER"))
	}
	forcedModel := strings.TrimSpace(requestedModel)
	if forcedModel == "" {
		forcedModel = strings.TrimSpace(os.Getenv("CODEPILOT_MODEL"))
	}
	selected := forcedProvider
	if selected == "" {
		selected = automaticallySelectedProfile(ctx, profiles, credentials)
	}
	profile, err := profiles.LoadProfile(ctx, provider.ProfileID(selected))
	if err != nil {
		return nil, modelSelection{}, false, fmt.Errorf("configure provider profile %q: %w", selected, err)
	}
	model := profile.DefaultModel
	if forcedModel != "" {
		model = forcedModel
	}
	return service, modelSelection{profile: profile.ID, model: model}, forcedProvider != "" || forcedModel != "", nil
}

func seedBuiltinProfiles(ctx context.Context, profiles provider.ProfileRepository) error {
	existing, err := profiles.ListProfiles(ctx)
	if err != nil {
		return fmt.Errorf("load Provider profiles: %w", err)
	}
	present := make(map[provider.ProfileID]struct{}, len(existing))
	for _, profile := range existing {
		present[profile.ID] = struct{}{}
	}
	builtins := []provider.Profile{
		{ID: "openai", Kind: provider.KindOpenAI, DisplayName: "OpenAI", BaseURL: strings.TrimSpace(os.Getenv("OPENAI_BASE_URL")), DefaultModel: "gpt-5.6-sol", CredentialRef: "openai"},
		{ID: "deepseek", Kind: provider.KindDeepSeek, DisplayName: "DeepSeek", BaseURL: strings.TrimSpace(os.Getenv("DEEPSEEK_BASE_URL")), DefaultModel: "deepseek-v4-flash", CredentialRef: "deepseek"},
		{ID: "ollama", Kind: provider.KindOllama, DisplayName: "Ollama", BaseURL: ollamaBaseURL(os.Getenv("OLLAMA_HOST")), DefaultModel: "qwen-coder"},
	}
	for _, profile := range builtins {
		if _, found := present[profile.ID]; found {
			continue
		}
		if err := profiles.SaveProfile(ctx, profile); err != nil {
			return fmt.Errorf("initialize Provider profile %q: %w", profile.ID, err)
		}
	}
	return nil
}

func automaticallySelectedProfile(ctx context.Context, profiles provider.ProfileRepository, credentials provider.CredentialStore) string {
	for _, id := range []provider.ProfileID{"openai", "deepseek"} {
		profile, err := profiles.LoadProfile(ctx, id)
		if err != nil || profile.CredentialRef == "" {
			continue
		}
		value, found, err := credentials.LoadCredential(ctx, profile.CredentialRef)
		for index := range value {
			value[index] = 0
		}
		if err == nil && found {
			return string(id)
		}
	}
	return "ollama"
}

func ollamaBaseURL(value string) string {
	value = strings.TrimSpace(value)
	if value != "" && !strings.Contains(value, "://") {
		return "http://" + value
	}
	return value
}

func selectedPermission(requested string) (codingagent.PermissionMode, bool, error) {
	value := strings.TrimSpace(requested)
	if value == "" {
		value = strings.TrimSpace(os.Getenv("CODEPILOT_PERMISSION"))
	}
	if value == "" {
		return codingagent.PermissionAsk, false, nil
	}
	mode := codingagent.PermissionMode(strings.ReplaceAll(strings.ToLower(value), "-", "_"))
	switch mode {
	case codingagent.PermissionReadOnly, codingagent.PermissionAsk, codingagent.PermissionAutoEdit:
		return mode, true, nil
	default:
		return "", false, fmt.Errorf("configure permissions: value %q is unsupported", value)
	}
}

func activeSession(ctx context.Context, repository codingagent.SessionRepository, worktreeID codingagent.WorktreeID) (codingagent.Session, bool, error) {
	sessions, err := repository.ListSessions(ctx)
	if err != nil {
		return codingagent.Session{}, false, fmt.Errorf("list Coding sessions: %w", err)
	}
	for _, session := range sessions {
		if session.WorktreeID == worktreeID && !session.Archived {
			return session, true, nil
		}
	}
	return codingagent.Session{}, false, nil
}

func findWorkspace(ctx context.Context, repository codingagent.WorkspaceRepository, id codingagent.WorkspaceID) (codingagent.Workspace, bool, error) {
	values, err := repository.ListWorkspaces(ctx)
	if err != nil {
		return codingagent.Workspace{}, false, fmt.Errorf("list Coding workspaces: %w", err)
	}
	for _, value := range values {
		if value.ID == id {
			return value, true, nil
		}
	}
	return codingagent.Workspace{}, false, nil
}

func findWorktree(ctx context.Context, repository codingagent.WorkspaceRepository, workspaceID codingagent.WorkspaceID, id codingagent.WorktreeID) (codingagent.Worktree, bool, error) {
	values, err := repository.ListWorktrees(ctx, workspaceID)
	if err != nil {
		return codingagent.Worktree{}, false, fmt.Errorf("list Coding worktrees: %w", err)
	}
	for _, value := range values {
		if value.ID == id {
			return value, true, nil
		}
	}
	return codingagent.Worktree{}, false, nil
}

type preparedWorkspaceBinding struct {
	workspace codingagent.Workspace
	worktree  codingagent.Worktree
}

func prepareWorkspaceBinding(ctx context.Context, repository codingagent.WorkspaceRepository, resolved workspace.ResolvedWorktree, options Options, now time.Time) (preparedWorkspaceBinding, error) {
	workspaces, err := repository.ListWorkspaces(ctx)
	if err != nil {
		return preparedWorkspaceBinding{}, fmt.Errorf("list Coding workspaces: %w", err)
	}
	var storedWorkspace codingagent.Workspace
	workspaceFound := false
	defaultID := codingagent.WorkspaceID(stableID("workspace", resolved.GitCommonDir))
	for _, value := range workspaces {
		if value.ID == defaultID || sameLocalPath(value.GitCommonDir, resolved.GitCommonDir) {
			if value.RepositoryFingerprint != "" && workspace.VerifyRepositoryFingerprint(ctx, resolved.Root, value.RepositoryFingerprint) != nil {
				continue
			}
			storedWorkspace, workspaceFound = value, true
			break
		}
	}
	if !workspaceFound && resolved.RepositoryFingerprint != "" {
		var matches []codingagent.Workspace
		for _, value := range workspaces {
			if value.RepositoryFingerprint != "" && workspace.VerifyRepositoryFingerprint(ctx, resolved.Root, value.RepositoryFingerprint) == nil {
				matches = append(matches, value)
			}
		}
		if len(matches) == 1 {
			candidate := matches[0]
			worktrees, listErr := repository.ListWorktrees(ctx, candidate.ID)
			if listErr != nil {
				return preparedWorkspaceBinding{}, fmt.Errorf("list relocation candidates: %w", listErr)
			}
			unavailable := make([]codingagent.Worktree, 0, len(worktrees))
			for _, value := range worktrees {
				live, resolveErr := workspace.ResolveWorktree(ctx, value.Root)
				if resolveErr != nil || !sameLocalPath(live.Root, value.Root) || !sameLocalPath(live.GitDir, value.GitDir) || !sameLocalPath(live.GitCommonDir, candidate.GitCommonDir) {
					unavailable = append(unavailable, value)
				}
			}
			selected, selectedFound := relocationCandidate(unavailable, options.RelocateWorktree)
			if options.RelocateWorktree != "" && !selectedFound {
				return preparedWorkspaceBinding{}, errors.New("the requested relocation target is not an unavailable worktree for this repository")
			}
			if options.RelocateWorktree == "" && len(unavailable) == 1 && !options.SkipRelocation {
				return preparedWorkspaceBinding{}, &RelocationRequiredError{WorktreeID: unavailable[0].ID, PreviousPath: unavailable[0].Root, NewPath: resolved.Root}
			}
			if selectedFound {
				manager, managerErr := codingagent.NewWorkspaceManager(repository, nil)
				if managerErr != nil {
					return preparedWorkspaceBinding{}, managerErr
				}
				relocated, relocateErr := manager.RelocateWorktree(ctx, codingagent.RelocateWorktreeRequest{WorktreeID: selected.ID, NewPath: resolved.Root})
				if relocateErr != nil {
					return preparedWorkspaceBinding{}, fmt.Errorf("repair relocated worktree: %w", relocateErr)
				}
				candidate, err = repository.LoadWorkspace(ctx, candidate.ID)
				if err != nil {
					return preparedWorkspaceBinding{}, err
				}
				candidate.Trusted, candidate.UpdatedAt = true, now
				if err := repository.SaveWorkspace(ctx, candidate); err != nil {
					return preparedWorkspaceBinding{}, fmt.Errorf("activate relocated workspace: %w", err)
				}
				return preparedWorkspaceBinding{workspace: candidate, worktree: relocated}, nil
			}
		}
	}

	if !workspaceFound {
		id := defaultID
		for _, value := range workspaces {
			if value.ID == id {
				id = codingagent.WorkspaceID(stableID("workspace", resolved.GitCommonDir+"\x00"+resolved.RepositoryFingerprint))
				break
			}
		}
		storedWorkspace = codingagent.Workspace{ID: id, DisplayName: filepath.Base(resolved.Root), GitCommonDir: resolved.GitCommonDir, RepositoryFingerprint: resolved.RepositoryFingerprint, CreatedAt: now, UpdatedAt: now}
	}
	worktrees, err := repository.ListWorktrees(ctx, storedWorkspace.ID)
	if err != nil {
		return preparedWorkspaceBinding{}, fmt.Errorf("list Coding worktrees: %w", err)
	}
	var storedWorktree codingagent.Worktree
	worktreeFound := false
	for _, value := range worktrees {
		if sameLocalPath(value.Root, resolved.Root) {
			storedWorktree, worktreeFound = value, true
			break
		}
	}
	if worktreeFound && (!sameLocalPath(storedWorktree.GitDir, resolved.GitDir) || (storedWorkspace.RepositoryFingerprint != "" && workspace.VerifyRepositoryFingerprint(ctx, resolved.Root, storedWorkspace.RepositoryFingerprint) != nil)) {
		if options.RelocateWorktree == storedWorktree.ID {
			manager, managerErr := codingagent.NewWorkspaceManager(repository, nil)
			if managerErr != nil {
				return preparedWorkspaceBinding{}, managerErr
			}
			relocated, relocateErr := manager.RelocateWorktree(ctx, codingagent.RelocateWorktreeRequest{WorktreeID: storedWorktree.ID, NewPath: resolved.Root})
			if relocateErr != nil {
				return preparedWorkspaceBinding{}, fmt.Errorf("repair changed worktree binding: %w", relocateErr)
			}
			storedWorktree = relocated
		} else if options.SkipRelocation {
			storedWorkspace = codingagent.Workspace{
				ID:          codingagent.WorkspaceID(stableID("workspace", resolved.GitCommonDir+"\x00"+resolved.RepositoryFingerprint)),
				DisplayName: filepath.Base(resolved.Root), GitCommonDir: resolved.GitCommonDir, RepositoryFingerprint: resolved.RepositoryFingerprint,
				CreatedAt: now, UpdatedAt: now,
			}
			workspaceFound, worktreeFound = false, false
			storedWorktree = codingagent.Worktree{}
		} else if !options.SkipRelocation {
			return preparedWorkspaceBinding{}, &RelocationRequiredError{WorktreeID: storedWorktree.ID, PreviousPath: storedWorktree.Root, NewPath: resolved.Root}
		}
	}
	if !options.TrustWorkspace && (!workspaceFound || !storedWorkspace.Trusted || !worktreeFound) {
		return preparedWorkspaceBinding{}, &TrustRequiredError{Path: resolved.Root}
	}
	storedWorkspace.Trusted = true
	storedWorkspace.GitCommonDir = resolved.GitCommonDir
	if storedWorkspace.RepositoryFingerprint == "" {
		storedWorkspace.RepositoryFingerprint = resolved.RepositoryFingerprint
	}
	storedWorkspace.UpdatedAt = now
	if err := repository.SaveWorkspace(ctx, storedWorkspace); err != nil {
		return preparedWorkspaceBinding{}, fmt.Errorf("activate workspace: %w", err)
	}
	if !worktreeFound {
		storedWorktree = codingagent.Worktree{
			ID: codingagent.WorktreeID(stableID("worktree", string(storedWorkspace.ID)+"\x00"+resolved.Root)), WorkspaceID: storedWorkspace.ID,
			Root: resolved.Root, GitDir: resolved.GitDir, CreatedAt: now, LastUsedAt: now,
		}
	} else {
		storedWorktree.LastUsedAt = now
	}
	if err := repository.SaveWorktree(ctx, storedWorktree); err != nil {
		return preparedWorkspaceBinding{}, fmt.Errorf("activate worktree: %w", err)
	}
	return preparedWorkspaceBinding{workspace: storedWorkspace, worktree: storedWorktree}, nil
}

func relocationCandidate(values []codingagent.Worktree, requested codingagent.WorktreeID) (codingagent.Worktree, bool) {
	if requested == "" {
		return codingagent.Worktree{}, false
	}
	for _, value := range values {
		if value.ID == requested {
			return value, true
		}
	}
	return codingagent.Worktree{}, false
}

func sameLocalPath(left, right string) bool {
	left, right = filepath.Clean(left), filepath.Clean(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func stableID(prefix, value string) string {
	digest := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(value))))
	return prefix + "_" + hex.EncodeToString(digest[:16])
}

func resolveDirectories(configDir, stateDir string) (string, string, error) {
	if strings.TrimSpace(configDir) == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return "", "", fmt.Errorf("resolve config directory: %w", err)
		}
		configDir = filepath.Join(base, "CodePilot")
	}
	if strings.TrimSpace(stateDir) == "" {
		if runtime.GOOS == "windows" && strings.TrimSpace(os.Getenv("LOCALAPPDATA")) != "" {
			stateDir = filepath.Join(os.Getenv("LOCALAPPDATA"), "CodePilot", "State")
		} else {
			base, err := os.UserCacheDir()
			if err != nil {
				return "", "", fmt.Errorf("resolve state directory: %w", err)
			}
			stateDir = filepath.Join(base, "codepilot", "state")
		}
	}
	return configDir, stateDir, nil
}

func prepareDirectory(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("directory path is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	absolute = filepath.Clean(absolute)
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return "", err
	}
	return absolute, nil
}
