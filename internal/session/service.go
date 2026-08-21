package session

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Dependencies contains the concrete capabilities required by Service.
type Dependencies struct {
	CodingAgents      CodingAgentFactory
	SessionStore      SessionStore
	WorkspaceRegistry WorkspaceRegistry
	WorkspaceReader   WorkspaceReader
	ModelCatalog      ModelCatalog
	Authorizer        Authorizer
	Events            EventSink
	Limits            RunLimits
}

// Service coordinates the active session and its single running turn.
type Service struct {
	operations sync.Mutex
	mu         sync.Mutex
	closeOnce  sync.Once

	deps Dependencies

	active           SessionSnapshot
	activeWorktree   WorktreeRecord
	agent            CodingAgent
	runtimeState     RuntimeState
	activeTurnID     TurnID
	activeTurnCancel context.CancelFunc
	activeTurnDone   chan struct{}
	closed           bool
	closeErr         error
}

// NewService validates dependencies and creates an inactive session service.
func NewService(deps Dependencies) (*Service, error) {
	if deps.CodingAgents == nil || deps.SessionStore == nil || deps.WorkspaceRegistry == nil || deps.WorkspaceReader == nil || deps.ModelCatalog == nil || deps.Authorizer == nil || deps.Events == nil {
		return nil, applicationError(ErrInvalidInput, "session.new_service", "Session dependencies are incomplete.", nil)
	}
	if err := validateRunLimits(deps.Limits); err != nil {
		return nil, err
	}

	return &Service{
		deps:         deps,
		runtimeState: RuntimeIdle,
	}, nil
}

// Activate resolves a worktree and restores or creates its active session.
func (s *Service) Activate(ctx context.Context, workingDirectory string) (SessionSnapshot, error) {
	s.operations.Lock()
	defer s.operations.Unlock()

	if err := ctx.Err(); err != nil {
		return SessionSnapshot{}, err
	}
	if strings.TrimSpace(workingDirectory) == "" {
		return SessionSnapshot{}, applicationError(ErrInvalidInput, "session.activate", "Working directory is required.", nil)
	}
	if err := s.requireIdle(); err != nil {
		return SessionSnapshot{}, err
	}

	resolved, err := s.deps.WorkspaceReader.ResolveWorktree(ctx, workingDirectory)
	if err != nil {
		return SessionSnapshot{}, err
	}
	worktreeState, err := s.deps.WorkspaceReader.ReadWorktreeState(ctx, resolved.Root)
	if err != nil {
		return SessionSnapshot{}, err
	}
	if !worktreeState.Available {
		return SessionSnapshot{}, applicationError(ErrWorkspaceUnavailable, "session.activate", "The target worktree is unavailable.", nil)
	}

	worktree, found, err := s.deps.WorkspaceRegistry.FindWorktreeByRoot(ctx, resolved.Root)
	if err != nil {
		return SessionSnapshot{}, err
	}
	if !found {
		worktree, err = s.registerResolvedWorktree(ctx, resolved, worktreeState)
		if err != nil {
			return SessionSnapshot{}, err
		}
	}

	value, err := s.restoreOrCreateSession(ctx, worktree, worktreeState)
	if err != nil {
		return SessionSnapshot{}, err
	}
	if err := s.activateSnapshot(ctx, value, worktree, worktreeState); err != nil {
		return SessionSnapshot{}, err
	}

	if err := s.publishSessionEvent(ctx, EventSessionActivated, value.Session.ID); err != nil {
		return SessionSnapshot{}, err
	}

	return s.CurrentSession(ctx)
}

// CurrentSession returns an isolated snapshot of the active session.
func (s *Service) CurrentSession(ctx context.Context) (SessionSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return SessionSnapshot{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.active.Session.ID == "" {
		return SessionSnapshot{}, applicationError(ErrInvalidState, "session.current_session", "No session is active.", nil)
	}

	value := cloneSessionSnapshot(s.active)
	value.RuntimeState = s.runtimeState
	return value, nil
}

// StartTurn persists the user message and starts one asynchronous coding turn.
func (s *Service) StartTurn(ctx context.Context, text string) (TurnID, error) {
	s.operations.Lock()
	defer s.operations.Unlock()

	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return "", applicationError(ErrInvalidInput, "session.start_turn", "Message cannot be empty.", nil)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return "", applicationError(ErrInvalidState, "session.start_turn", "Session service is closed.", nil)
	}
	if s.active.Session.ID == "" {
		s.mu.Unlock()
		return "", applicationError(ErrInvalidState, "session.start_turn", "No session is active.", nil)
	}
	if s.runtimeState != RuntimeIdle {
		s.mu.Unlock()
		return "", applicationError(ErrInvalidState, "session.start_turn", "Another turn is still active.", nil)
	}
	if s.agent == nil || s.active.Session.ProviderProfileID == "" || s.active.Session.ModelID == "" {
		s.mu.Unlock()
		return "", applicationError(ErrProviderUnavailable, "session.start_turn", "Configure a provider and model before starting a turn.", nil)
	}

	activeSession := s.active.Session
	worktree := s.activeWorktree
	history := cloneMessages(s.active.Messages)
	agent := s.agent
	s.mu.Unlock()

	worktreeState, err := s.deps.WorkspaceReader.ReadWorktreeState(ctx, worktree.Root)
	if err != nil {
		return "", err
	}
	if !worktreeState.Available {
		return "", applicationError(ErrWorkspaceUnavailable, "session.start_turn", "The active worktree is unavailable.", nil)
	}

	turnIDValue, err := newEntityID("turn")
	if err != nil {
		return "", err
	}
	messageIDValue, err := newEntityID("msg")
	if err != nil {
		return "", err
	}
	turnID := TurnID(turnIDValue)
	now := time.Now().UTC()
	userMessage := Message{
		ID:        MessageID(messageIDValue),
		SessionID: activeSession.ID,
		TurnID:    turnID,
		Role:      RoleUser,
		Content:   trimmed,
		CreatedAt: now,
	}

	if activeSession.Title == "" {
		activeSession.Title = titleFromMessage(trimmed)
	}
	activeSession.UpdatedAt = now
	if err := s.deps.SessionStore.AppendMessage(ctx, userMessage); err != nil {
		return "", err
	}
	if err := s.deps.SessionStore.SaveSession(ctx, activeSession); err != nil {
		return "", err
	}

	turnCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	scope := TurnScope{
		TurnID:            turnID,
		SessionID:         activeSession.ID,
		WorkspaceID:       activeSession.WorkspaceID,
		WorktreeID:        activeSession.WorktreeID,
		WorktreeRoot:      worktree.Root,
		ProviderProfileID: activeSession.ProviderProfileID,
		ModelID:           activeSession.ModelID,
		PermissionMode:    activeSession.PermissionMode,
		Limits:            s.deps.Limits,
	}
	request := TurnRequest{
		Scope:       scope,
		History:     history,
		UserMessage: userMessage,
	}
	events := &turnEventSink{
		service:   s,
		sessionID: activeSession.ID,
		turnID:    turnID,
	}

	s.mu.Lock()
	if s.closed || s.runtimeState != RuntimeIdle || s.active.Session.ID != activeSession.ID {
		s.mu.Unlock()
		cancel()
		return "", applicationError(ErrInvalidState, "session.start_turn", "Active session changed before the turn started.", nil)
	}
	s.active.Session = activeSession
	s.active.Messages = append(s.active.Messages, userMessage)
	s.active.WorktreeState = worktreeState
	s.runtimeState = RuntimeRunning
	s.activeTurnID = turnID
	s.activeTurnCancel = cancel
	s.activeTurnDone = done
	s.mu.Unlock()

	if err := events.publish(ctx, Event{Kind: EventTurnStarted}); err != nil {
		s.finishTurnState(turnID, done)
		cancel()
		return "", err
	}

	go s.runTurn(turnCtx, request, userMessage, now, agent, events, done)
	return turnID, nil
}

// CancelTurn cancels the active turn and waits for its goroutine to finish.
func (s *Service) CancelTurn(ctx context.Context) error {
	s.mu.Lock()
	if s.runtimeState == RuntimeIdle || s.activeTurnID == "" {
		s.mu.Unlock()
		return applicationError(ErrInvalidState, "session.cancel_turn", "No turn is active.", nil)
	}
	done := s.activeTurnDone
	cancel := s.activeTurnCancel
	if s.runtimeState != RuntimeCancelling {
		s.runtimeState = RuntimeCancelling
	}
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ResolveApproval validates and forwards a decision for the active turn.
func (s *Service) ResolveApproval(ctx context.Context, resolution ApprovalResolution) error {
	s.mu.Lock()
	if s.runtimeState != RuntimeAwaitingApproval || resolution.SessionID != s.active.Session.ID || resolution.TurnID != s.activeTurnID {
		s.mu.Unlock()
		return applicationError(ErrInvalidState, "session.resolve_approval", "Approval does not belong to the active turn.", nil)
	}
	s.mu.Unlock()

	if err := s.deps.Authorizer.Resolve(ctx, resolution); err != nil {
		return err
	}

	s.mu.Lock()
	if s.runtimeState == RuntimeAwaitingApproval && s.activeTurnID == resolution.TurnID {
		s.runtimeState = RuntimeRunning
	}
	s.mu.Unlock()

	return nil
}

// CreateSession creates and activates a session for the current worktree.
func (s *Service) CreateSession(ctx context.Context, request CreateSessionRequest) (SessionSummary, error) {
	s.operations.Lock()
	defer s.operations.Unlock()

	if err := s.requireIdle(); err != nil {
		return SessionSummary{}, err
	}

	s.mu.Lock()
	if s.active.Session.ID == "" {
		s.mu.Unlock()
		return SessionSummary{}, applicationError(ErrInvalidState, "session.create_session", "No worktree is active.", nil)
	}
	current := s.active.Session
	worktree := s.activeWorktree
	worktreeState := s.active.WorktreeState
	s.mu.Unlock()

	value, err := s.newSession(worktree, worktreeState, ModelSelection{
		ProviderProfileID: current.ProviderProfileID,
		ModelID:           current.ModelID,
	}, strings.TrimSpace(request.Title))
	if err != nil {
		return SessionSummary{}, err
	}
	if err := s.deps.SessionStore.CreateSession(ctx, value); err != nil {
		return SessionSummary{}, err
	}

	snapshot, err := s.deps.SessionStore.LoadSession(ctx, value.ID)
	if err != nil {
		return SessionSummary{}, err
	}
	if err := s.activateSnapshot(ctx, snapshot, worktree, worktreeState); err != nil {
		return SessionSummary{}, err
	}
	if err := s.saveWorktreeSession(ctx, worktree, value.ID); err != nil {
		return SessionSummary{}, err
	}

	return sessionSummaryFromSession(value), nil
}

// ListSessions returns lightweight session metadata from the store.
func (s *Service) ListSessions(ctx context.Context, filter SessionFilter) ([]SessionSummary, error) {
	return s.deps.SessionStore.ListSessions(ctx, filter)
}

// SwitchSession activates an idle, available session and rebuilds its agent.
func (s *Service) SwitchSession(ctx context.Context, id SessionID) error {
	s.operations.Lock()
	defer s.operations.Unlock()

	if err := s.requireIdle(); err != nil {
		return err
	}

	snapshot, err := s.deps.SessionStore.LoadSession(ctx, id)
	if err != nil {
		return err
	}
	if snapshot.Session.Archived {
		return applicationError(ErrInvalidState, "session.switch_session", "Archived sessions cannot be activated.", nil)
	}
	worktree, err := s.deps.WorkspaceRegistry.LoadWorktree(ctx, snapshot.Session.WorktreeID)
	if err != nil {
		return err
	}
	worktreeState, err := s.deps.WorkspaceReader.ReadWorktreeState(ctx, worktree.Root)
	if err != nil {
		return err
	}
	if !worktreeState.Available {
		return applicationError(ErrWorkspaceUnavailable, "session.switch_session", "The target worktree is unavailable.", nil)
	}

	if err := s.activateSnapshot(ctx, snapshot, worktree, worktreeState); err != nil {
		return err
	}
	return s.saveWorktreeSession(ctx, worktree, id)
}

// RenameSession updates the durable title of an idle session.
func (s *Service) RenameSession(ctx context.Context, id SessionID, title string) error {
	s.operations.Lock()
	defer s.operations.Unlock()

	if err := s.requireIdle(); err != nil {
		return err
	}
	trimmed := strings.TrimSpace(title)
	if trimmed == "" {
		return applicationError(ErrInvalidInput, "session.rename_session", "Session title cannot be empty.", nil)
	}

	snapshot, err := s.deps.SessionStore.LoadSession(ctx, id)
	if err != nil {
		return err
	}
	snapshot.Session.Title = trimmed
	snapshot.Session.UpdatedAt = time.Now().UTC()
	if err := s.deps.SessionStore.SaveSession(ctx, snapshot.Session); err != nil {
		return err
	}

	s.mu.Lock()
	if s.active.Session.ID == id {
		s.active.Session = snapshot.Session
	}
	s.mu.Unlock()
	return nil
}

// ArchiveSession archives an idle session without deleting its history.
func (s *Service) ArchiveSession(ctx context.Context, id SessionID) error {
	s.operations.Lock()
	defer s.operations.Unlock()

	if err := s.requireIdle(); err != nil {
		return err
	}
	archivedAt := time.Now().UTC()
	if err := s.deps.SessionStore.ArchiveSession(ctx, id, archivedAt); err != nil {
		return err
	}

	s.mu.Lock()
	if s.active.Session.ID == id {
		s.active.Session.Archived = true
		s.active.Session.UpdatedAt = archivedAt
	}
	s.mu.Unlock()
	return nil
}

// OpenWorkspace activates a session for another trusted Git worktree.
func (s *Service) OpenWorkspace(ctx context.Context, path string) (WorktreeSummary, error) {
	if _, err := s.Activate(ctx, path); err != nil {
		return WorktreeSummary{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	return worktreeSummaryFromRecord(s.activeWorktree, s.active.Session.ID, s.active.WorktreeState.Available), nil
}

// ListWorkspaces returns registered worktrees for the workspace command UI.
func (s *Service) ListWorkspaces(ctx context.Context) ([]WorktreeSummary, error) {
	return s.deps.WorkspaceRegistry.ListWorktrees(ctx)
}

// ReadDiff reads a bounded diff for the active worktree and session.
func (s *Service) ReadDiff(ctx context.Context, kind DiffKind) (DiffResult, error) {
	s.mu.Lock()
	if s.active.Session.ID == "" {
		s.mu.Unlock()
		return DiffResult{}, applicationError(ErrInvalidState, "session.read_diff", "No session is active.", nil)
	}
	request := DiffRequest{
		WorktreeRoot: s.activeWorktree.Root,
		SessionID:    s.active.Session.ID,
		Kind:         kind,
	}
	if kind == DiffSession {
		request.ExpectedHashes = make(map[string]string)
		seen := make(map[string]struct{})
		for _, patch := range s.active.Patches {
			for _, file := range patch.Files {
				if _, exists := seen[file.Path]; !exists {
					seen[file.Path] = struct{}{}
					request.Files = append(request.Files, file.Path)
				}
				request.ExpectedHashes[file.Path] = file.AfterHash
			}
		}
	}
	s.mu.Unlock()

	return s.deps.WorkspaceReader.ReadDiff(ctx, request)
}

// ListWorkspaceFiles returns bounded safe file and directory paths for the
// active worktree's UI mention picker.
func (s *Service) ListWorkspaceFiles(ctx context.Context, limit int) (WorkspaceFileList, error) {
	s.mu.Lock()
	if s.active.Session.ID == "" {
		s.mu.Unlock()
		return WorkspaceFileList{}, applicationError(ErrInvalidState, "session.list_workspace_files", "No session is active.", nil)
	}
	root := s.activeWorktree.Root
	s.mu.Unlock()
	return s.deps.WorkspaceReader.ListWorkspaceFiles(ctx, root, limit)
}

// ListProviderProfiles returns configured secret-free provider profiles.
func (s *Service) ListProviderProfiles(ctx context.Context) ([]ProviderProfile, error) {
	return s.deps.ModelCatalog.ListProviderProfiles(ctx)
}

// ConfigureProvider validates and stores a provider profile.
func (s *Service) ConfigureProvider(ctx context.Context, request ConfigureProviderRequest) (ProviderProfile, error) {
	return s.deps.ModelCatalog.ConfigureProvider(ctx, request)
}

// ListModels returns models available to a configured provider profile.
func (s *Service) ListModels(ctx context.Context, profileID ProviderProfileID) ([]ModelOption, error) {
	return s.deps.ModelCatalog.ListModels(ctx, profileID)
}

// SwitchModel validates a selection and rebuilds the active coding agent.
func (s *Service) SwitchModel(ctx context.Context, selection ModelSelection) error {
	s.operations.Lock()
	defer s.operations.Unlock()

	if err := s.requireIdle(); err != nil {
		return err
	}
	validation, err := s.deps.ModelCatalog.ValidateSelection(ctx, selection)
	if err != nil {
		return err
	}
	if !validation.Valid {
		return applicationError(ErrProviderUnavailable, "session.switch_model", validation.UserMessage, nil)
	}

	s.mu.Lock()
	if s.active.Session.ID == "" {
		s.mu.Unlock()
		return applicationError(ErrInvalidState, "session.switch_model", "No session is active.", nil)
	}
	value := s.active.Session
	worktree := s.activeWorktree
	s.mu.Unlock()

	value.ProviderProfileID = selection.ProviderProfileID
	value.ModelID = selection.ModelID
	value.UpdatedAt = time.Now().UTC()
	newAgent, err := s.deps.CodingAgents.CreateCodingAgent(ctx, codingAgentConfig(value, worktree.Root, s.deps.Limits))
	if err != nil {
		return err
	}
	if err := s.deps.SessionStore.SaveSession(ctx, value); err != nil {
		closeErr := newAgent.Close()
		return errors.Join(err, closeErr)
	}

	oldAgent := s.swapAgentAndSession(value, newAgent)
	if oldAgent != nil && oldAgent != newAgent {
		return oldAgent.Close()
	}
	return nil
}

// SetPermissionMode updates the idle session and clears exact temporary grants.
func (s *Service) SetPermissionMode(ctx context.Context, mode PermissionMode) error {
	s.operations.Lock()
	defer s.operations.Unlock()

	if err := s.requireIdle(); err != nil {
		return err
	}
	if !validPermissionMode(mode) {
		return applicationError(ErrInvalidInput, "session.set_permission_mode", "Permission mode is invalid.", nil)
	}

	s.mu.Lock()
	if s.active.Session.ID == "" {
		s.mu.Unlock()
		return applicationError(ErrInvalidState, "session.set_permission_mode", "No session is active.", nil)
	}
	value := s.active.Session
	s.mu.Unlock()

	if value.PermissionMode == mode {
		return nil
	}
	value.PermissionMode = mode
	value.UpdatedAt = time.Now().UTC()
	if err := s.deps.SessionStore.SaveSession(ctx, value); err != nil {
		return err
	}
	if err := s.deps.Authorizer.ClearSession(ctx, value.ID); err != nil {
		return err
	}

	s.mu.Lock()
	if s.active.Session.ID == value.ID {
		s.active.Session = value
	}
	s.mu.Unlock()
	return nil
}

// Close cancels the active turn and releases the active agent exactly once.
func (s *Service) Close() error {
	s.closeOnce.Do(func() {
		s.operations.Lock()
		defer s.operations.Unlock()

		s.mu.Lock()
		s.closed = true
		cancel := s.activeTurnCancel
		done := s.activeTurnDone
		if s.activeTurnID != "" {
			s.runtimeState = RuntimeCancelling
		}
		s.mu.Unlock()

		if cancel != nil {
			cancel()
		}
		if done != nil {
			<-done
		}

		s.mu.Lock()
		agent := s.agent
		activeSession := s.active.Session
		s.mu.Unlock()

		var closeErrors []error
		if activeSession.ID != "" {
			if err := s.deps.SessionStore.SaveSession(context.Background(), activeSession); err != nil {
				closeErrors = append(closeErrors, err)
			}
			if err := s.deps.Authorizer.ClearSession(context.Background(), activeSession.ID); err != nil {
				closeErrors = append(closeErrors, err)
			}
		}
		if agent != nil {
			if err := agent.Close(); err != nil {
				closeErrors = append(closeErrors, err)
			}
		}
		s.closeErr = errors.Join(closeErrors...)
	})

	return s.closeErr
}

func (s *Service) runTurn(ctx context.Context, request TurnRequest, userMessage Message, startedAt time.Time, agent CodingAgent, events *turnEventSink, done chan struct{}) {
	result, runErr := agent.RunTurn(ctx, request, events)
	cancelled := errors.Is(ctx.Err(), context.Canceled) || errors.Is(runErr, context.Canceled)

	for _, patch := range result.AppliedPatches {
		if patch.SessionID == "" {
			patch.SessionID = request.Scope.SessionID
		}
		if patch.TurnID == "" {
			patch.TurnID = request.Scope.TurnID
		}
		if err := s.recordPatch(context.Background(), request.Scope, patch); err != nil && runErr == nil {
			runErr = err
		}
	}

	appliedPatches := s.patchesForTurn(request.Scope.TurnID)
	if result.CheckSummary.Outcome == "" {
		result.CheckSummary.Outcome = CheckNotRun
	}
	status := classifyTurnStatus(cancelled, runErr != nil, len(appliedPatches) > 0, result.CheckSummary.Outcome)
	terminationReason := result.TerminationReason
	if cancelled {
		terminationReason = "cancelled"
	} else if runErr != nil {
		terminationReason = "error"
	} else if terminationReason == "" {
		terminationReason = "final"
	}

	completedAt := time.Now().UTC()
	assistantMessageID, idErr := newEntityID("msg")
	if idErr != nil && runErr == nil {
		runErr = idErr
	}
	finalText := result.FinalText
	if finalText == "" {
		finalText = finalTextForStatus(status)
	}
	assistantMessage := Message{
		ID:        MessageID(assistantMessageID),
		SessionID: request.Scope.SessionID,
		TurnID:    request.Scope.TurnID,
		Role:      RoleAssistant,
		Content:   finalText,
		CreatedAt: completedAt,
	}
	turnRecord := TurnRecord{
		ID:                request.Scope.TurnID,
		SessionID:         request.Scope.SessionID,
		UserMessageID:     userMessage.ID,
		Status:            status,
		TerminationReason: terminationReason,
		ProviderProfileID: request.Scope.ProviderProfileID,
		ModelID:           request.Scope.ModelID,
		Steps:             result.Steps,
		CheckSummary:      result.CheckSummary,
		StartedAt:         startedAt,
		CompletedAt:       completedAt,
	}

	s.mu.Lock()
	activeSession := s.active.Session
	if s.activeTurnID == request.Scope.TurnID && activeSession.ID == request.Scope.SessionID {
		activeSession.LastTurnStatus = status
		activeSession.UpdatedAt = completedAt
	}
	s.mu.Unlock()

	commitErr := s.deps.SessionStore.CommitTurn(context.Background(), TurnCommit{
		Session:          activeSession,
		AssistantMessage: assistantMessage,
		Turn:             turnRecord,
	})
	if commitErr != nil {
		if err := events.publish(context.Background(), Event{
			Kind: EventSessionSaveFailed,
			Payload: EventPayload{Error: &ErrorEventPayload{
				Code:        ErrPersistence,
				Operation:   "session.commit_turn",
				UserMessage: "The turn finished, but session history could not be saved.",
				Retryable:   true,
			}},
		}); err != nil {
			s.recordRecoveryWarning(request.Scope.TurnID, "The turn finished, but session history could not be saved.")
		}
	}

	s.mu.Lock()
	if s.activeTurnID == request.Scope.TurnID && s.active.Session.ID == request.Scope.SessionID {
		s.active.Session = activeSession
		s.active.Messages = append(s.active.Messages, assistantMessage)
		s.active.Turns = append(s.active.Turns, turnRecord)
	}
	s.mu.Unlock()

	if err := events.publish(context.Background(), Event{
		Kind: finalEventKind(status),
		Payload: EventPayload{Turn: &TurnEventPayload{
			Status:            status,
			TerminationReason: terminationReason,
			CheckSummary:      result.CheckSummary,
		}},
	}); err != nil {
		s.recordRecoveryWarning(request.Scope.TurnID, "The turn finished, but its result could not be delivered to the UI.")
	}

	s.finishTurnState(request.Scope.TurnID, done)
}

// recordRecoveryWarning preserves a non-fatal final-turn failure so it remains
// visible in the session snapshot even when the event sink cannot deliver it.
func (s *Service) recordRecoveryWarning(turnID TurnID, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active.RecoveryWarnings = append(s.active.RecoveryWarnings, RecoveryWarning{
		Code:        RecoveryTurnUnrecorded,
		Stream:      string(turnID),
		UserMessage: message,
	})
}

func (s *Service) registerResolvedWorktree(ctx context.Context, resolved ResolvedWorktree, state WorktreeState) (WorktreeRecord, error) {
	workspaceIDValue, err := newEntityID("ws")
	if err != nil {
		return WorktreeRecord{}, err
	}
	worktreeIDValue, err := newEntityID("wt")
	if err != nil {
		return WorktreeRecord{}, err
	}
	now := time.Now().UTC()
	workspace := WorkspaceRecord{
		ID:           WorkspaceID(workspaceIDValue),
		DisplayName:  resolved.DisplayName,
		GitCommonDir: resolved.GitCommonDir,
		Trusted:      true,
		CreatedAt:    now,
		LastUsedAt:   now,
	}
	worktree := WorktreeRecord{
		ID:          WorktreeID(worktreeIDValue),
		WorkspaceID: workspace.ID,
		Root:        resolved.Root,
		GitDir:      resolved.GitDir,
		CreatedAt:   now,
		LastUsedAt:  now,
	}

	if !state.Available {
		return WorktreeRecord{}, applicationError(ErrWorkspaceUnavailable, "session.register_worktree", "The worktree is unavailable.", nil)
	}
	if err := s.deps.WorkspaceRegistry.SaveWorkspace(ctx, workspace); err != nil {
		return WorktreeRecord{}, err
	}
	if err := s.deps.WorkspaceRegistry.SaveWorktree(ctx, worktree); err != nil {
		return WorktreeRecord{}, err
	}

	return worktree, nil
}

func (s *Service) restoreOrCreateSession(ctx context.Context, worktree WorktreeRecord, state WorktreeState) (SessionSnapshot, error) {
	if worktree.LastSessionID != "" {
		value, err := s.deps.SessionStore.LoadSession(ctx, worktree.LastSessionID)
		if err == nil {
			if !value.Session.Archived {
				return value, nil
			}
		} else if !hasApplicationErrorCode(err, ErrNotFound) {
			return SessionSnapshot{}, err
		}
	}

	values, err := s.deps.SessionStore.ListSessions(ctx, SessionFilter{WorktreeID: worktree.ID})
	if err != nil {
		return SessionSnapshot{}, err
	}
	if len(values) > 0 {
		return s.deps.SessionStore.LoadSession(ctx, values[0].ID)
	}

	selection, err := s.lastConfiguredModel(ctx)
	if err != nil {
		return SessionSnapshot{}, err
	}
	value, err := s.newSession(worktree, state, selection, "")
	if err != nil {
		return SessionSnapshot{}, err
	}
	if err := s.deps.SessionStore.CreateSession(ctx, value); err != nil {
		return SessionSnapshot{}, err
	}
	if err := s.saveWorktreeSession(ctx, worktree, value.ID); err != nil {
		return SessionSnapshot{}, err
	}

	return s.deps.SessionStore.LoadSession(ctx, value.ID)
}

// lastConfiguredModel carries the most recently validated global profile into
// a brand-new worktree session. Profiles whose credentials are no longer
// available fall back to the normal picker instead of breaking startup.
func (s *Service) lastConfiguredModel(ctx context.Context) (ModelSelection, error) {
	profiles, err := s.deps.ModelCatalog.ListProviderProfiles(ctx)
	if err != nil {
		return ModelSelection{}, err
	}
	var latest ProviderProfile
	for _, profile := range profiles {
		if profile.ID == "" || strings.TrimSpace(profile.ModelID) == "" || strings.EqualFold(profile.CredentialLocation, "missing") {
			continue
		}
		if latest.ID == "" || profile.ValidatedAt.After(latest.ValidatedAt) {
			latest = profile
		}
	}
	return ModelSelection{ProviderProfileID: latest.ID, ModelID: latest.ModelID}, nil
}

func (s *Service) newSession(worktree WorktreeRecord, state WorktreeState, selection ModelSelection, title string) (Session, error) {
	sessionIDValue, err := newEntityID("ses")
	if err != nil {
		return Session{}, err
	}
	now := time.Now().UTC()
	return Session{
		ID:                SessionID(sessionIDValue),
		WorkspaceID:       worktree.WorkspaceID,
		WorktreeID:        worktree.ID,
		Title:             title,
		ProviderProfileID: selection.ProviderProfileID,
		ModelID:           selection.ModelID,
		PermissionMode:    PermissionAsk,
		BaseCommit:        state.HeadCommit,
		CreatedAt:         now,
		UpdatedAt:         now,
	}, nil
}

func (s *Service) activateSnapshot(ctx context.Context, value SessionSnapshot, worktree WorktreeRecord, state WorktreeState) error {
	newAgent, err := s.createAgent(ctx, value.Session, worktree.Root)
	if err != nil {
		return err
	}

	s.mu.Lock()
	oldAgent := s.agent
	oldSessionID := s.active.Session.ID
	s.mu.Unlock()

	if oldSessionID != "" && oldSessionID != value.Session.ID {
		if err := s.deps.Authorizer.ClearSession(ctx, oldSessionID); err != nil {
			if newAgent != nil {
				_ = newAgent.Close()
			}
			return err
		}
	}
	if oldAgent != nil && oldAgent != newAgent {
		if err := oldAgent.Close(); err != nil {
			if newAgent != nil {
				_ = newAgent.Close()
			}
			return err
		}
	}

	value = cloneSessionSnapshot(value)
	value.RuntimeState = RuntimeIdle
	value.WorktreeState = state
	s.mu.Lock()
	s.active = value
	s.activeWorktree = worktree
	s.agent = newAgent
	s.runtimeState = RuntimeIdle
	s.activeTurnID = ""
	s.activeTurnCancel = nil
	s.activeTurnDone = nil
	s.mu.Unlock()

	return s.deps.WorkspaceRegistry.SaveLastActiveSession(ctx, value.Session.ID)
}

func (s *Service) createAgent(ctx context.Context, value Session, worktreeRoot string) (CodingAgent, error) {
	if value.ProviderProfileID == "" || value.ModelID == "" {
		return nil, nil
	}

	selection := ModelSelection{
		ProviderProfileID: value.ProviderProfileID,
		ModelID:           value.ModelID,
	}
	validation, err := s.deps.ModelCatalog.ValidateSelection(ctx, selection)
	if err != nil {
		return nil, err
	}
	if !validation.Valid {
		return nil, applicationError(ErrProviderUnavailable, "session.create_agent", validation.UserMessage, nil)
	}

	return s.deps.CodingAgents.CreateCodingAgent(ctx, codingAgentConfig(value, worktreeRoot, s.deps.Limits))
}

func (s *Service) saveWorktreeSession(ctx context.Context, worktree WorktreeRecord, sessionID SessionID) error {
	worktree.LastSessionID = sessionID
	worktree.LastUsedAt = time.Now().UTC()
	return s.deps.WorkspaceRegistry.SaveWorktree(ctx, worktree)
}

func (s *Service) requireIdle() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return applicationError(ErrInvalidState, "session.require_idle", "Session service is closed.", nil)
	}
	if s.runtimeState != RuntimeIdle {
		return applicationError(ErrInvalidState, "session.require_idle", "The active turn must finish first.", nil)
	}
	return nil
}

func (s *Service) recordPatch(ctx context.Context, scope TurnScope, value PatchRecord) error {
	if value.SessionID != scope.SessionID || value.TurnID != scope.TurnID {
		return applicationError(ErrInvalidInput, "session.record_patch", "Patch record belongs to another turn.", nil)
	}
	if err := s.deps.SessionStore.AppendPatch(ctx, value); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active.Session.ID == scope.SessionID && s.activeTurnID == scope.TurnID && !containsPatchRecord(s.active.Patches, value.ID) {
		s.active.Patches = append(s.active.Patches, clonePatchRecord(value))
	}
	return nil
}

func (s *Service) patchesForTurn(turnID TurnID) []PatchRecord {
	s.mu.Lock()
	defer s.mu.Unlock()

	values := make([]PatchRecord, 0)
	for _, value := range s.active.Patches {
		if value.TurnID == turnID {
			values = append(values, clonePatchRecord(value))
		}
	}
	return values
}

func (s *Service) finishTurnState(turnID TurnID, done chan struct{}) {
	s.mu.Lock()
	if s.activeTurnID == turnID {
		s.runtimeState = RuntimeIdle
		s.activeTurnID = ""
		s.activeTurnCancel = nil
		s.activeTurnDone = nil
	}
	s.mu.Unlock()
	close(done)
}

func (s *Service) publishSessionEvent(ctx context.Context, kind EventKind, sessionID SessionID) error {
	return s.deps.Events.Publish(ctx, Event{
		ID:        fmt.Sprintf("%s:%s:%d", sessionID, kind, time.Now().UTC().UnixNano()),
		SessionID: sessionID,
		Kind:      kind,
		Time:      time.Now().UTC(),
	})
}

func (s *Service) swapAgentAndSession(value Session, newAgent CodingAgent) CodingAgent {
	s.mu.Lock()
	defer s.mu.Unlock()

	oldAgent := s.agent
	s.agent = newAgent
	s.active.Session = value
	return oldAgent
}

type turnEventSink struct {
	mu sync.Mutex

	service   *Service
	sessionID SessionID
	turnID    TurnID
	sequence  uint64
}

func (s *turnEventSink) Publish(ctx context.Context, event Event) error {
	if event.SessionID != s.sessionID || event.TurnID != s.turnID {
		return applicationError(ErrInvalidInput, "session.publish_turn_event", "Event belongs to another turn.", nil)
	}
	return s.publish(ctx, event)
}

func (s *turnEventSink) publish(ctx context.Context, event Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	event.SessionID = s.sessionID
	event.TurnID = s.turnID
	s.sequence++
	event.Sequence = s.sequence
	if event.ID == "" {
		event.ID = fmt.Sprintf("%s:%d", s.turnID, event.Sequence)
	}
	if event.Time.IsZero() {
		event.Time = time.Now().UTC()
	}

	if err := s.handle(event); err != nil {
		return err
	}
	return s.service.deps.Events.Publish(ctx, event)
}

func (s *turnEventSink) handle(event Event) error {
	switch event.Kind {
	case EventPatchApplied:
		if event.Payload.Patch == nil {
			return applicationError(ErrInvalidInput, "session.handle_turn_event", "Patch event has no patch record.", nil)
		}
		return s.service.recordPatch(context.Background(), TurnScope{
			SessionID: s.sessionID,
			TurnID:    s.turnID,
		}, event.Payload.Patch.Record)
	case EventApprovalRequested:
		s.service.mu.Lock()
		if s.service.activeTurnID == s.turnID && s.service.runtimeState == RuntimeRunning {
			s.service.runtimeState = RuntimeAwaitingApproval
		}
		s.service.mu.Unlock()
	case EventApprovalResolved:
		s.service.mu.Lock()
		if s.service.activeTurnID == s.turnID && s.service.runtimeState == RuntimeAwaitingApproval {
			s.service.runtimeState = RuntimeRunning
		}
		s.service.mu.Unlock()
	}
	return nil
}

func newEntityID(prefix string) (string, error) {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", applicationError(ErrInternal, "session.new_id", "Could not create an identifier.", err)
	}
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(random)
	return prefix + "_" + strings.ToLower(encoded), nil
}

func applicationError(code ErrorCode, operation string, message string, cause error) error {
	return &AppError{
		Code:        code,
		Operation:   operation,
		UserMessage: message,
		Cause:       cause,
	}
}

func hasApplicationErrorCode(err error, code ErrorCode) bool {
	var appError *AppError
	return errors.As(err, &appError) && appError.Code == code
}
