package sessionstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/eaglc/codepilot/internal/session"
)

// ErrStoreClosed indicates that a FileStore method was called after Close.
var ErrStoreClosed = errors.New("session store is closed")

var (
	_ session.SessionStore      = (*FileStore)(nil)
	_ session.WorkspaceRegistry = (*FileStore)(nil)
)

// FileStore persists versioned workspace and session state below one StateDir.
type FileStore struct {
	layout storeLayout

	mu             sync.RWMutex
	closed         bool
	registry       registryDTO
	workspacePaths map[session.WorkspaceID]string
	worktreePaths  map[session.WorktreeID]string
	sessionPaths   map[session.SessionID]string
	sessionLocks   map[session.SessionID]*sync.Mutex

	metadataMu sync.Mutex
}

type registryDTO struct {
	Version             int                   `json:"version"`
	LastActiveSessionID session.SessionID     `json:"last_active_session_id,omitempty"`
	WorkspaceIDs        []session.WorkspaceID `json:"workspace_ids"`
}

type workspaceDTO struct {
	Version      int                 `json:"version"`
	ID           session.WorkspaceID `json:"id"`
	DisplayName  string              `json:"display_name"`
	GitCommonDir string              `json:"git_common_dir"`
	Trusted      bool                `json:"trusted"`
	CreatedAt    time.Time           `json:"created_at"`
	LastUsedAt   time.Time           `json:"last_used_at"`
}

type worktreeDTO struct {
	Version       int                 `json:"version"`
	ID            session.WorktreeID  `json:"id"`
	WorkspaceID   session.WorkspaceID `json:"workspace_id"`
	Root          string              `json:"root"`
	GitDir        string              `json:"git_dir"`
	LastSessionID session.SessionID   `json:"last_session_id,omitempty"`
	CreatedAt     time.Time           `json:"created_at"`
	LastUsedAt    time.Time           `json:"last_used_at"`
}

type sessionDTO struct {
	Version           int                       `json:"version"`
	ID                session.SessionID         `json:"id"`
	WorkspaceID       session.WorkspaceID       `json:"workspace_id"`
	WorktreeID        session.WorktreeID        `json:"worktree_id"`
	Title             string                    `json:"title"`
	ProviderProfileID session.ProviderProfileID `json:"provider_profile_id,omitempty"`
	ModelID           string                    `json:"model_id,omitempty"`
	PermissionMode    session.PermissionMode    `json:"permission_mode"`
	BaseCommit        string                    `json:"base_commit,omitempty"`
	LastTurnStatus    session.TurnStatus        `json:"last_turn_status,omitempty"`
	Archived          bool                      `json:"archived"`
	CreatedAt         time.Time                 `json:"created_at"`
	UpdatedAt         time.Time                 `json:"updated_at"`
}

type messageDTO struct {
	ID        session.MessageID   `json:"id"`
	SessionID session.SessionID   `json:"session_id"`
	TurnID    session.TurnID      `json:"turn_id"`
	Role      session.MessageRole `json:"role"`
	Content   string              `json:"content"`
	CreatedAt time.Time           `json:"created_at"`
}

type turnDTO struct {
	ID                session.TurnID            `json:"id"`
	SessionID         session.SessionID         `json:"session_id"`
	UserMessageID     session.MessageID         `json:"user_message_id"`
	Status            session.TurnStatus        `json:"status"`
	TerminationReason string                    `json:"termination_reason"`
	ProviderProfileID session.ProviderProfileID `json:"provider_profile_id,omitempty"`
	ModelID           string                    `json:"model_id,omitempty"`
	Steps             int                       `json:"steps"`
	CheckSummary      checkSummaryDTO           `json:"check_summary"`
	StartedAt         time.Time                 `json:"started_at"`
	CompletedAt       time.Time                 `json:"completed_at"`
}

type checkSummaryDTO struct {
	Outcome   session.CheckOutcome `json:"outcome"`
	Summary   string               `json:"summary"`
	Truncated bool                 `json:"truncated"`
}

type patchDTO struct {
	ID        session.PatchID   `json:"id"`
	SessionID session.SessionID `json:"session_id"`
	TurnID    session.TurnID    `json:"turn_id"`
	Patch     string            `json:"patch"`
	Files     []patchedFileDTO  `json:"files"`
	AppliedAt time.Time         `json:"applied_at"`
}

type patchedFileDTO struct {
	Path       string `json:"path"`
	BeforeHash string `json:"before_hash"`
	AfterHash  string `json:"after_hash"`
}

// NewFileStore opens or creates StateDir and validates all indexed metadata.
func NewFileStore(stateDir string) (*FileStore, error) {
	if strings.TrimSpace(stateDir) == "" {
		return nil, errors.New("create file store: state directory is empty")
	}
	absolute, err := filepath.Abs(stateDir)
	if err != nil {
		return nil, fmt.Errorf("create file store: resolve state directory: %w", err)
	}
	absolute = filepath.Clean(absolute)
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return nil, fmt.Errorf("create file store directory: %w", err)
	}

	store := &FileStore{
		layout:         newStoreLayout(absolute),
		registry:       registryDTO{Version: currentStoreVersion},
		workspacePaths: make(map[session.WorkspaceID]string),
		worktreePaths:  make(map[session.WorktreeID]string),
		sessionPaths:   make(map[session.SessionID]string),
		sessionLocks:   make(map[session.SessionID]*sync.Mutex),
	}
	if err := store.loadIndex(); err != nil {
		return nil, err
	}

	return store, nil
}

// Close makes all subsequent operations fail with ErrStoreClosed.
func (s *FileStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil
	}
	s.closed = true

	return nil
}

// CreateSession creates versioned metadata for a session bound to a registered worktree.
func (s *FileStore) CreateSession(ctx context.Context, value session.Session) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if err := validateSession(value); err != nil {
		return storeAppError(session.ErrInvalidInput, "sessionstore.create_session", "Session data is invalid.", err)
	}

	s.metadataMu.Lock()
	defer s.metadataMu.Unlock()

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return closedStoreError("sessionstore.create_session")
	}
	if _, exists := s.sessionPaths[value.ID]; exists {
		s.mu.Unlock()
		return storeAppError(session.ErrConflict, "sessionstore.create_session", "Session already exists.", nil)
	}
	worktreePath, exists := s.worktreePaths[value.WorktreeID]
	s.mu.Unlock()
	if !exists {
		return storeAppError(session.ErrNotFound, "sessionstore.create_session", "Worktree not found.", nil)
	}

	worktree, err := loadWorktreeMetadata(worktreePath)
	if err != nil {
		return err
	}
	if worktree.WorkspaceID != value.WorkspaceID {
		return storeAppError(session.ErrInvalidInput, "sessionstore.create_session", "Session belongs to another workspace.", nil)
	}

	path := s.layout.sessionPath(string(value.WorkspaceID), string(value.WorktreeID), string(value.ID))
	if _, err := os.Stat(path); err == nil {
		return storeAppError(session.ErrConflict, "sessionstore.create_session", "Session already exists.", nil)
	} else if !errors.Is(err, os.ErrNotExist) {
		return storeAppError(session.ErrPersistence, "sessionstore.create_session", "Session metadata could not be inspected.", err)
	}
	if err := writeJSONAtomic(path, newSessionDTO(value)); err != nil {
		return storeAppError(session.ErrPersistence, "sessionstore.create_session", "Session could not be saved.", err)
	}

	s.mu.Lock()
	s.sessionPaths[value.ID] = path
	s.sessionLocks[value.ID] = &sync.Mutex{}
	s.mu.Unlock()

	return nil
}

// LoadSession returns metadata and validated append-only records.
func (s *FileStore) LoadSession(ctx context.Context, id session.SessionID) (session.SessionSnapshot, error) {
	if err := contextError(ctx); err != nil {
		return session.SessionSnapshot{}, err
	}
	path, lock, err := s.sessionLocation(id, "sessionstore.load_session")
	if err != nil {
		return session.SessionSnapshot{}, err
	}

	lock.Lock()
	defer lock.Unlock()

	value, err := loadSessionMetadata(path)
	if err != nil {
		return session.SessionSnapshot{}, err
	}
	directory := filepath.Dir(path)
	messagePath := filepath.Join(directory, "messages.jsonl")
	turnPath := filepath.Join(directory, "turns.jsonl")
	patchPath := filepath.Join(directory, "patches.jsonl")
	warnings, err := recoveryWarnings(map[string]string{
		"messages": messagePath,
		"turns":    turnPath,
		"patches":  patchPath,
	})
	if err != nil {
		return session.SessionSnapshot{}, storeAppError(session.ErrPersistence, "sessionstore.load_session", "Session recovery state could not be inspected.", err)
	}
	messages, err := loadMessages(messagePath, id)
	if err != nil {
		return session.SessionSnapshot{}, err
	}
	turns, err := loadTurns(turnPath, id)
	if err != nil {
		return session.SessionSnapshot{}, err
	}
	patches, err := loadPatches(patchPath, id)
	if err != nil {
		return session.SessionSnapshot{}, err
	}

	return session.SessionSnapshot{
		Session:          value,
		RuntimeState:     session.RuntimeIdle,
		Messages:         messages,
		Turns:            turns,
		Patches:          patches,
		RecoveryWarnings: warnings,
	}, nil
}

func recoveryWarnings(streams map[string]string) ([]session.RecoveryWarning, error) {
	names := make([]string, 0, len(streams))
	for name := range streams {
		names = append(names, name)
	}
	sort.Strings(names)
	warnings := make([]session.RecoveryWarning, 0)
	for _, name := range names {
		truncated, err := hasTruncatedJSONLine(streams[name])
		if err != nil {
			return nil, err
		}
		if truncated {
			warnings = append(warnings, session.RecoveryWarning{
				Code:        session.RecoveryTruncatedLog,
				Stream:      name,
				UserMessage: "An incomplete final " + name + " record was ignored while restoring this session.",
			})
		}
	}
	return warnings, nil
}

// ListSessions reads metadata only and returns a stable recent-first order.
func (s *FileStore) ListSessions(ctx context.Context, filter session.SessionFilter) ([]session.SessionSummary, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}

	locations, err := s.allSessionLocations("sessionstore.list_sessions")
	if err != nil {
		return nil, err
	}
	values := make([]session.SessionSummary, 0, len(locations))
	for id, path := range locations {
		if err := contextError(ctx); err != nil {
			return nil, err
		}
		lock, err := s.sessionLock(id, "sessionstore.list_sessions")
		if err != nil {
			return nil, err
		}
		lock.Lock()
		stored, loadErr := loadSessionMetadata(path)
		lock.Unlock()
		if loadErr != nil {
			return nil, loadErr
		}
		if filter.WorkspaceID != "" && stored.WorkspaceID != filter.WorkspaceID {
			continue
		}
		if filter.WorktreeID != "" && stored.WorktreeID != filter.WorktreeID {
			continue
		}
		if !filter.IncludeArchived && stored.Archived {
			continue
		}
		values = append(values, sessionSummary(stored))
	}

	sort.Slice(values, func(left int, right int) bool {
		if values[left].UpdatedAt.Equal(values[right].UpdatedAt) {
			return values[left].ID < values[right].ID
		}
		return values[left].UpdatedAt.After(values[right].UpdatedAt)
	})

	return values, nil
}

// SaveSession atomically replaces mutable metadata without allowing rebinding.
func (s *FileStore) SaveSession(ctx context.Context, value session.Session) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if err := validateSession(value); err != nil {
		return storeAppError(session.ErrInvalidInput, "sessionstore.save_session", "Session data is invalid.", err)
	}
	path, lock, err := s.sessionLocation(value.ID, "sessionstore.save_session")
	if err != nil {
		return err
	}

	lock.Lock()
	defer lock.Unlock()

	previous, err := loadSessionMetadata(path)
	if err != nil {
		return err
	}
	if previous.WorkspaceID != value.WorkspaceID || previous.WorktreeID != value.WorktreeID || !previous.CreatedAt.Equal(value.CreatedAt) {
		return storeAppError(session.ErrInvalidInput, "sessionstore.save_session", "Session binding cannot be changed.", nil)
	}
	if err := writeJSONAtomic(path, newSessionDTO(value)); err != nil {
		return storeAppError(session.ErrPersistence, "sessionstore.save_session", "Session could not be saved.", err)
	}

	return nil
}

// AppendMessage appends a neutral message once for its unique ID.
func (s *FileStore) AppendMessage(ctx context.Context, value session.Message) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if err := validateMessage(value); err != nil {
		return storeAppError(session.ErrInvalidInput, "sessionstore.append_message", "Message data is invalid.", err)
	}
	path, lock, err := s.sessionLocation(value.SessionID, "sessionstore.append_message")
	if err != nil {
		return err
	}

	lock.Lock()
	defer lock.Unlock()

	streamPath := filepath.Join(filepath.Dir(path), "messages.jsonl")
	values, err := readJSONLines[messageDTO](streamPath)
	if err != nil {
		return corruptedStoreError("sessionstore.append_message", err)
	}
	for _, stored := range values {
		if stored.ID == value.ID {
			return nil
		}
	}
	if err := prepareJSONLinesForAppend(streamPath); err != nil {
		return storeAppError(session.ErrPersistence, "sessionstore.append_message", "Message log could not be recovered.", err)
	}
	if err := appendJSONLine(streamPath, newMessageDTO(value)); err != nil {
		return storeAppError(session.ErrPersistence, "sessionstore.append_message", "Message could not be saved.", err)
	}

	return nil
}

// AppendPatch appends a successful patch record once for its unique ID.
func (s *FileStore) AppendPatch(ctx context.Context, value session.PatchRecord) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if err := validatePatch(value); err != nil {
		return storeAppError(session.ErrInvalidInput, "sessionstore.append_patch", "Patch record is invalid.", err)
	}
	path, lock, err := s.sessionLocation(value.SessionID, "sessionstore.append_patch")
	if err != nil {
		return err
	}

	lock.Lock()
	defer lock.Unlock()

	streamPath := filepath.Join(filepath.Dir(path), "patches.jsonl")
	values, err := readJSONLines[patchDTO](streamPath)
	if err != nil {
		return corruptedStoreError("sessionstore.append_patch", err)
	}
	for _, stored := range values {
		if stored.ID == value.ID {
			return nil
		}
	}
	if err := prepareJSONLinesForAppend(streamPath); err != nil {
		return storeAppError(session.ErrPersistence, "sessionstore.append_patch", "Patch log could not be recovered.", err)
	}
	if err := appendJSONLine(streamPath, newPatchDTO(value)); err != nil {
		return storeAppError(session.ErrPersistence, "sessionstore.append_patch", "Patch record could not be saved.", err)
	}

	return nil
}

// CommitTurn appends final records idempotently and then replaces metadata.
func (s *FileStore) CommitTurn(ctx context.Context, commit session.TurnCommit) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if err := validateTurnCommit(commit); err != nil {
		return storeAppError(session.ErrInvalidInput, "sessionstore.commit_turn", "Turn commit is invalid.", err)
	}
	path, lock, err := s.sessionLocation(commit.Session.ID, "sessionstore.commit_turn")
	if err != nil {
		return err
	}

	lock.Lock()
	defer lock.Unlock()

	previous, err := loadSessionMetadata(path)
	if err != nil {
		return err
	}
	if previous.WorkspaceID != commit.Session.WorkspaceID || previous.WorktreeID != commit.Session.WorktreeID || !previous.CreatedAt.Equal(commit.Session.CreatedAt) {
		return storeAppError(session.ErrInvalidInput, "sessionstore.commit_turn", "Session binding cannot be changed.", nil)
	}

	directory := filepath.Dir(path)
	messagePath := filepath.Join(directory, "messages.jsonl")
	messages, err := readJSONLines[messageDTO](messagePath)
	if err != nil {
		return corruptedStoreError("sessionstore.commit_turn", err)
	}
	if !containsMessageDTO(messages, commit.AssistantMessage.ID) {
		if err := prepareJSONLinesForAppend(messagePath); err != nil {
			return storeAppError(session.ErrPersistence, "sessionstore.commit_turn", "Message log could not be recovered.", err)
		}
		if err := appendJSONLine(messagePath, newMessageDTO(commit.AssistantMessage)); err != nil {
			return storeAppError(session.ErrPersistence, "sessionstore.commit_turn", "Assistant message could not be saved.", err)
		}
	}

	turnPath := filepath.Join(directory, "turns.jsonl")
	turns, err := readJSONLines[turnDTO](turnPath)
	if err != nil {
		return corruptedStoreError("sessionstore.commit_turn", err)
	}
	if !containsTurnDTO(turns, commit.Turn.ID) {
		if err := prepareJSONLinesForAppend(turnPath); err != nil {
			return storeAppError(session.ErrPersistence, "sessionstore.commit_turn", "Turn log could not be recovered.", err)
		}
		if err := appendJSONLine(turnPath, newTurnDTO(commit.Turn)); err != nil {
			return storeAppError(session.ErrPersistence, "sessionstore.commit_turn", "Turn record could not be saved.", err)
		}
	}

	if err := writeJSONAtomic(path, newSessionDTO(commit.Session)); err != nil {
		return storeAppError(session.ErrPersistence, "sessionstore.commit_turn", "Session could not be saved.", err)
	}

	return nil
}

// ArchiveSession marks metadata archived without deleting history.
func (s *FileStore) ArchiveSession(ctx context.Context, id session.SessionID, archivedAt time.Time) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	path, lock, err := s.sessionLocation(id, "sessionstore.archive_session")
	if err != nil {
		return err
	}

	lock.Lock()
	defer lock.Unlock()

	value, err := loadSessionMetadata(path)
	if err != nil {
		return err
	}
	value.Archived = true
	value.UpdatedAt = archivedAt.UTC()
	if err := writeJSONAtomic(path, newSessionDTO(value)); err != nil {
		return storeAppError(session.ErrPersistence, "sessionstore.archive_session", "Session could not be archived.", err)
	}

	return nil
}

// SaveWorkspace inserts or replaces workspace registration metadata.
func (s *FileStore) SaveWorkspace(ctx context.Context, value session.WorkspaceRecord) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if err := validateWorkspace(value); err != nil {
		return storeAppError(session.ErrInvalidInput, "sessionstore.save_workspace", "Workspace data is invalid.", err)
	}

	s.metadataMu.Lock()
	defer s.metadataMu.Unlock()

	s.mu.RLock()
	if s.closed {
		s.mu.RUnlock()
		return closedStoreError("sessionstore.save_workspace")
	}
	_, exists := s.workspacePaths[value.ID]
	registry := cloneRegistry(s.registry)
	s.mu.RUnlock()

	path := s.layout.workspacePath(string(value.ID))
	if err := writeJSONAtomic(path, newWorkspaceDTO(value)); err != nil {
		return storeAppError(session.ErrPersistence, "sessionstore.save_workspace", "Workspace could not be saved.", err)
	}
	if !exists {
		registry.WorkspaceIDs = append(registry.WorkspaceIDs, value.ID)
		sort.Slice(registry.WorkspaceIDs, func(left int, right int) bool {
			return registry.WorkspaceIDs[left] < registry.WorkspaceIDs[right]
		})
		if err := writeJSONAtomic(s.layout.registryPath(), registry); err != nil {
			return storeAppError(session.ErrPersistence, "sessionstore.save_workspace", "Workspace registry could not be saved.", err)
		}
	}

	s.mu.Lock()
	s.workspacePaths[value.ID] = path
	s.registry = registry
	s.mu.Unlock()

	return nil
}

// SaveWorktree inserts or replaces worktree metadata below its workspace.
func (s *FileStore) SaveWorktree(ctx context.Context, value session.WorktreeRecord) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if err := validateWorktree(value); err != nil {
		return storeAppError(session.ErrInvalidInput, "sessionstore.save_worktree", "Worktree data is invalid.", err)
	}

	s.metadataMu.Lock()
	defer s.metadataMu.Unlock()

	s.mu.RLock()
	if s.closed {
		s.mu.RUnlock()
		return closedStoreError("sessionstore.save_worktree")
	}
	_, workspaceExists := s.workspacePaths[value.WorkspaceID]
	existingPath, worktreeExists := s.worktreePaths[value.ID]
	s.mu.RUnlock()
	if !workspaceExists {
		return storeAppError(session.ErrNotFound, "sessionstore.save_worktree", "Workspace not found.", nil)
	}
	if worktreeExists {
		previous, err := loadWorktreeMetadata(existingPath)
		if err != nil {
			return err
		}
		if previous.WorkspaceID != value.WorkspaceID || !previous.CreatedAt.Equal(value.CreatedAt) {
			return storeAppError(session.ErrInvalidInput, "sessionstore.save_worktree", "Worktree binding cannot be changed.", nil)
		}
	}

	path := s.layout.worktreePath(string(value.WorkspaceID), string(value.ID))
	if err := writeJSONAtomic(path, newWorktreeDTO(value)); err != nil {
		return storeAppError(session.ErrPersistence, "sessionstore.save_worktree", "Worktree could not be saved.", err)
	}
	if err := os.MkdirAll(s.layout.sessionsDir(string(value.WorkspaceID), string(value.ID)), 0o700); err != nil {
		return storeAppError(session.ErrPersistence, "sessionstore.save_worktree", "Worktree session directory could not be created.", err)
	}

	s.mu.Lock()
	s.worktreePaths[value.ID] = path
	s.mu.Unlock()

	return nil
}

// LoadWorkspace returns one registered workspace.
func (s *FileStore) LoadWorkspace(ctx context.Context, id session.WorkspaceID) (session.WorkspaceRecord, error) {
	if err := contextError(ctx); err != nil {
		return session.WorkspaceRecord{}, err
	}
	s.mu.RLock()
	if s.closed {
		s.mu.RUnlock()
		return session.WorkspaceRecord{}, closedStoreError("sessionstore.load_workspace")
	}
	path, exists := s.workspacePaths[id]
	s.mu.RUnlock()
	if !exists {
		return session.WorkspaceRecord{}, storeAppError(session.ErrNotFound, "sessionstore.load_workspace", "Workspace not found.", nil)
	}

	return loadWorkspaceMetadata(path)
}

// LoadWorktree returns one registered worktree.
func (s *FileStore) LoadWorktree(ctx context.Context, id session.WorktreeID) (session.WorktreeRecord, error) {
	if err := contextError(ctx); err != nil {
		return session.WorktreeRecord{}, err
	}
	s.mu.RLock()
	if s.closed {
		s.mu.RUnlock()
		return session.WorktreeRecord{}, closedStoreError("sessionstore.load_worktree")
	}
	path, exists := s.worktreePaths[id]
	s.mu.RUnlock()
	if !exists {
		return session.WorktreeRecord{}, storeAppError(session.ErrNotFound, "sessionstore.load_worktree", "Worktree not found.", nil)
	}

	return loadWorktreeMetadata(path)
}

// FindWorktreeByRoot finds a registered worktree by normalized absolute root.
func (s *FileStore) FindWorktreeByRoot(ctx context.Context, normalizedRoot string) (session.WorktreeRecord, bool, error) {
	if err := contextError(ctx); err != nil {
		return session.WorktreeRecord{}, false, err
	}
	paths, err := s.allWorktreePaths("sessionstore.find_worktree_by_root")
	if err != nil {
		return session.WorktreeRecord{}, false, err
	}
	for _, path := range paths {
		if err := contextError(ctx); err != nil {
			return session.WorktreeRecord{}, false, err
		}
		value, err := loadWorktreeMetadata(path)
		if err != nil {
			return session.WorktreeRecord{}, false, err
		}
		if value.Root == normalizedRoot {
			return value, true, nil
		}
	}

	return session.WorktreeRecord{}, false, nil
}

// ListWorktrees returns registered worktrees ordered by recent use.
func (s *FileStore) ListWorktrees(ctx context.Context) ([]session.WorktreeSummary, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	paths, err := s.allWorktreePaths("sessionstore.list_worktrees")
	if err != nil {
		return nil, err
	}
	values := make([]session.WorktreeSummary, 0, len(paths))
	for _, path := range paths {
		if err := contextError(ctx); err != nil {
			return nil, err
		}
		worktree, err := loadWorktreeMetadata(path)
		if err != nil {
			return nil, err
		}
		workspace, err := s.LoadWorkspace(ctx, worktree.WorkspaceID)
		if err != nil {
			return nil, err
		}
		rootInfo, statErr := os.Stat(worktree.Root)
		available := statErr == nil && rootInfo.IsDir()
		values = append(values, session.WorktreeSummary{
			ID:            worktree.ID,
			WorkspaceID:   worktree.WorkspaceID,
			DisplayName:   workspace.DisplayName,
			Root:          worktree.Root,
			LastSessionID: worktree.LastSessionID,
			Available:     available,
			LastUsedAt:    worktree.LastUsedAt,
		})
	}
	sort.Slice(values, func(left int, right int) bool {
		if values[left].LastUsedAt.Equal(values[right].LastUsedAt) {
			return values[left].ID < values[right].ID
		}
		return values[left].LastUsedAt.After(values[right].LastUsedAt)
	})

	return values, nil
}

// SaveLastActiveSession atomically records the process resume target.
func (s *FileStore) SaveLastActiveSession(ctx context.Context, id session.SessionID) error {
	if err := contextError(ctx); err != nil {
		return err
	}

	s.metadataMu.Lock()
	defer s.metadataMu.Unlock()

	s.mu.RLock()
	if s.closed {
		s.mu.RUnlock()
		return closedStoreError("sessionstore.save_last_active_session")
	}
	_, exists := s.sessionPaths[id]
	registry := cloneRegistry(s.registry)
	s.mu.RUnlock()
	if !exists {
		return storeAppError(session.ErrNotFound, "sessionstore.save_last_active_session", "Session not found.", nil)
	}

	registry.LastActiveSessionID = id
	if err := writeJSONAtomic(s.layout.registryPath(), registry); err != nil {
		return storeAppError(session.ErrPersistence, "sessionstore.save_last_active_session", "Workspace registry could not be saved.", err)
	}
	s.mu.Lock()
	s.registry = registry
	s.mu.Unlock()

	return nil
}

func (s *FileStore) loadIndex() error {
	var registry registryDTO
	found, err := readJSONFile(s.layout.registryPath(), &registry)
	if err != nil {
		return corruptedStoreError("sessionstore.open", err)
	}
	if !found {
		return nil
	}
	if registry.Version != currentStoreVersion {
		return corruptedStoreError("sessionstore.open", fmt.Errorf("unsupported registry version %d", registry.Version))
	}

	seenWorkspaces := make(map[session.WorkspaceID]struct{}, len(registry.WorkspaceIDs))
	for _, workspaceID := range registry.WorkspaceIDs {
		if err := validateStoreID(string(workspaceID)); err != nil {
			return corruptedStoreError("sessionstore.open", fmt.Errorf("invalid workspace ID %q: %w", workspaceID, err))
		}
		if _, exists := seenWorkspaces[workspaceID]; exists {
			return corruptedStoreError("sessionstore.open", fmt.Errorf("duplicate workspace ID %q", workspaceID))
		}
		seenWorkspaces[workspaceID] = struct{}{}

		workspacePath := s.layout.workspacePath(string(workspaceID))
		workspace, err := loadWorkspaceMetadata(workspacePath)
		if err != nil {
			return err
		}
		if workspace.ID != workspaceID {
			return corruptedStoreError("sessionstore.open", fmt.Errorf("workspace path ID %q does not match record %q", workspaceID, workspace.ID))
		}
		s.workspacePaths[workspaceID] = workspacePath

		if err := s.loadWorktreeIndex(workspaceID); err != nil {
			return err
		}
	}
	if registry.LastActiveSessionID != "" {
		if _, exists := s.sessionPaths[registry.LastActiveSessionID]; !exists {
			return corruptedStoreError("sessionstore.open", fmt.Errorf("last active session %q is not indexed", registry.LastActiveSessionID))
		}
	}
	s.registry = registry

	return nil
}

func (s *FileStore) loadWorktreeIndex(workspaceID session.WorkspaceID) error {
	directory := s.layout.worktreesDir(string(workspaceID))
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return storeAppError(session.ErrPersistence, "sessionstore.open", "Worktree index could not be read.", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if err := validateStoreID(entry.Name()); err != nil {
			return corruptedStoreError("sessionstore.open", fmt.Errorf("invalid worktree directory %q: %w", entry.Name(), err))
		}
		path := s.layout.worktreePath(string(workspaceID), entry.Name())
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			continue
		}
		worktree, err := loadWorktreeMetadata(path)
		if err != nil {
			return err
		}
		if string(worktree.ID) != entry.Name() || worktree.WorkspaceID != workspaceID {
			return corruptedStoreError("sessionstore.open", fmt.Errorf("worktree directory %q does not match record", entry.Name()))
		}
		if _, exists := s.worktreePaths[worktree.ID]; exists {
			return corruptedStoreError("sessionstore.open", fmt.Errorf("duplicate worktree ID %q", worktree.ID))
		}
		s.worktreePaths[worktree.ID] = path
		if err := s.loadSessionIndex(workspaceID, worktree.ID); err != nil {
			return err
		}
	}

	return nil
}

func (s *FileStore) loadSessionIndex(workspaceID session.WorkspaceID, worktreeID session.WorktreeID) error {
	directory := s.layout.sessionsDir(string(workspaceID), string(worktreeID))
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return storeAppError(session.ErrPersistence, "sessionstore.open", "Session index could not be read.", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if err := validateStoreID(entry.Name()); err != nil {
			return corruptedStoreError("sessionstore.open", fmt.Errorf("invalid session directory %q: %w", entry.Name(), err))
		}
		path := s.layout.sessionPath(string(workspaceID), string(worktreeID), entry.Name())
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			continue
		}
		value, err := loadSessionMetadata(path)
		if err != nil {
			return err
		}
		if string(value.ID) != entry.Name() || value.WorkspaceID != workspaceID || value.WorktreeID != worktreeID {
			return corruptedStoreError("sessionstore.open", fmt.Errorf("session directory %q does not match record", entry.Name()))
		}
		if _, exists := s.sessionPaths[value.ID]; exists {
			return corruptedStoreError("sessionstore.open", fmt.Errorf("duplicate session ID %q", value.ID))
		}
		s.sessionPaths[value.ID] = path
		s.sessionLocks[value.ID] = &sync.Mutex{}
	}

	return nil
}

func (s *FileStore) sessionLocation(id session.SessionID, operation string) (string, *sync.Mutex, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.closed {
		return "", nil, closedStoreError(operation)
	}
	path, exists := s.sessionPaths[id]
	if !exists {
		return "", nil, storeAppError(session.ErrNotFound, operation, "Session not found.", nil)
	}

	return path, s.sessionLocks[id], nil
}

func (s *FileStore) sessionLock(id session.SessionID, operation string) (*sync.Mutex, error) {
	_, lock, err := s.sessionLocation(id, operation)
	return lock, err
}

func (s *FileStore) allSessionLocations(operation string) (map[session.SessionID]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.closed {
		return nil, closedStoreError(operation)
	}
	locations := make(map[session.SessionID]string, len(s.sessionPaths))
	for id, path := range s.sessionPaths {
		locations[id] = path
	}

	return locations, nil
}

func (s *FileStore) allWorktreePaths(operation string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.closed {
		return nil, closedStoreError(operation)
	}
	paths := make([]string, 0, len(s.worktreePaths))
	for _, path := range s.worktreePaths {
		paths = append(paths, path)
	}

	return paths, nil
}

func loadWorkspaceMetadata(path string) (session.WorkspaceRecord, error) {
	var stored workspaceDTO
	found, err := readJSONFile(path, &stored)
	if err != nil {
		return session.WorkspaceRecord{}, corruptedStoreError("sessionstore.load_workspace", err)
	}
	if !found {
		return session.WorkspaceRecord{}, corruptedStoreError("sessionstore.load_workspace", os.ErrNotExist)
	}
	if stored.Version != currentStoreVersion {
		return session.WorkspaceRecord{}, corruptedStoreError("sessionstore.load_workspace", fmt.Errorf("unsupported workspace version %d", stored.Version))
	}
	value := stored.workspace()
	if err := validateWorkspace(value); err != nil {
		return session.WorkspaceRecord{}, corruptedStoreError("sessionstore.load_workspace", err)
	}

	return value, nil
}

func loadWorktreeMetadata(path string) (session.WorktreeRecord, error) {
	var stored worktreeDTO
	found, err := readJSONFile(path, &stored)
	if err != nil {
		return session.WorktreeRecord{}, corruptedStoreError("sessionstore.load_worktree", err)
	}
	if !found {
		return session.WorktreeRecord{}, corruptedStoreError("sessionstore.load_worktree", os.ErrNotExist)
	}
	if stored.Version != currentStoreVersion {
		return session.WorktreeRecord{}, corruptedStoreError("sessionstore.load_worktree", fmt.Errorf("unsupported worktree version %d", stored.Version))
	}
	value := stored.worktree()
	if err := validateWorktree(value); err != nil {
		return session.WorktreeRecord{}, corruptedStoreError("sessionstore.load_worktree", err)
	}

	return value, nil
}

func loadSessionMetadata(path string) (session.Session, error) {
	var stored sessionDTO
	found, err := readJSONFile(path, &stored)
	if err != nil {
		return session.Session{}, corruptedStoreError("sessionstore.load_session", err)
	}
	if !found {
		return session.Session{}, corruptedStoreError("sessionstore.load_session", os.ErrNotExist)
	}
	if stored.Version != currentStoreVersion {
		return session.Session{}, corruptedStoreError("sessionstore.load_session", fmt.Errorf("unsupported session version %d", stored.Version))
	}
	value := stored.session()
	if err := validateSession(value); err != nil {
		return session.Session{}, corruptedStoreError("sessionstore.load_session", err)
	}

	return value, nil
}

func loadMessages(path string, sessionID session.SessionID) ([]session.Message, error) {
	stored, err := readJSONLines[messageDTO](path)
	if err != nil {
		return nil, corruptedStoreError("sessionstore.load_session", err)
	}
	values := make([]session.Message, 0, len(stored))
	seen := make(map[session.MessageID]struct{}, len(stored))
	for _, record := range stored {
		value := record.message()
		if err := validateMessage(value); err != nil || value.SessionID != sessionID {
			return nil, corruptedStoreError("sessionstore.load_session", errors.New("invalid message record"))
		}
		if _, exists := seen[value.ID]; exists {
			return nil, corruptedStoreError("sessionstore.load_session", fmt.Errorf("duplicate message ID %q", value.ID))
		}
		seen[value.ID] = struct{}{}
		values = append(values, value)
	}

	return values, nil
}

func loadTurns(path string, sessionID session.SessionID) ([]session.TurnRecord, error) {
	stored, err := readJSONLines[turnDTO](path)
	if err != nil {
		return nil, corruptedStoreError("sessionstore.load_session", err)
	}
	values := make([]session.TurnRecord, 0, len(stored))
	seen := make(map[session.TurnID]struct{}, len(stored))
	for _, record := range stored {
		value := record.turn()
		if err := validateTurn(value); err != nil || value.SessionID != sessionID {
			return nil, corruptedStoreError("sessionstore.load_session", errors.New("invalid turn record"))
		}
		if _, exists := seen[value.ID]; exists {
			return nil, corruptedStoreError("sessionstore.load_session", fmt.Errorf("duplicate turn ID %q", value.ID))
		}
		seen[value.ID] = struct{}{}
		values = append(values, value)
	}

	return values, nil
}

func loadPatches(path string, sessionID session.SessionID) ([]session.PatchRecord, error) {
	stored, err := readJSONLines[patchDTO](path)
	if err != nil {
		return nil, corruptedStoreError("sessionstore.load_session", err)
	}
	values := make([]session.PatchRecord, 0, len(stored))
	seen := make(map[session.PatchID]struct{}, len(stored))
	for _, record := range stored {
		value := record.patchRecord()
		if err := validatePatch(value); err != nil || value.SessionID != sessionID {
			return nil, corruptedStoreError("sessionstore.load_session", errors.New("invalid patch record"))
		}
		if _, exists := seen[value.ID]; exists {
			return nil, corruptedStoreError("sessionstore.load_session", fmt.Errorf("duplicate patch ID %q", value.ID))
		}
		seen[value.ID] = struct{}{}
		values = append(values, value)
	}

	return values, nil
}

func validateWorkspace(value session.WorkspaceRecord) error {
	if err := validateStoreID(string(value.ID)); err != nil {
		return err
	}
	if strings.TrimSpace(value.DisplayName) == "" || strings.TrimSpace(value.GitCommonDir) == "" {
		return errors.New("workspace name or Git common directory is empty")
	}
	if !filepath.IsAbs(value.GitCommonDir) {
		return errors.New("Git common directory is not absolute")
	}
	return nil
}

func validateWorktree(value session.WorktreeRecord) error {
	if err := validateStoreID(string(value.ID)); err != nil {
		return err
	}
	if err := validateStoreID(string(value.WorkspaceID)); err != nil {
		return err
	}
	if strings.TrimSpace(value.Root) == "" || strings.TrimSpace(value.GitDir) == "" {
		return errors.New("worktree root or Git directory is empty")
	}
	if !filepath.IsAbs(value.Root) || !filepath.IsAbs(value.GitDir) {
		return errors.New("worktree root or Git directory is not absolute")
	}
	if value.LastSessionID != "" {
		return validateStoreID(string(value.LastSessionID))
	}
	return nil
}

func validateSession(value session.Session) error {
	if err := validateStoreID(string(value.ID)); err != nil {
		return err
	}
	if err := validateStoreID(string(value.WorkspaceID)); err != nil {
		return err
	}
	if err := validateStoreID(string(value.WorktreeID)); err != nil {
		return err
	}
	switch value.PermissionMode {
	case session.PermissionReadOnly, session.PermissionAsk, session.PermissionAutoEdit:
	default:
		return errors.New("invalid permission mode")
	}
	return nil
}

func validateMessage(value session.Message) error {
	if err := validateStoreID(string(value.ID)); err != nil {
		return err
	}
	if err := validateStoreID(string(value.SessionID)); err != nil {
		return err
	}
	if err := validateStoreID(string(value.TurnID)); err != nil {
		return err
	}
	if value.Role != session.RoleUser && value.Role != session.RoleAssistant {
		return errors.New("invalid message role")
	}
	return nil
}

func validateTurn(value session.TurnRecord) error {
	if err := validateStoreID(string(value.ID)); err != nil {
		return err
	}
	if err := validateStoreID(string(value.SessionID)); err != nil {
		return err
	}
	if err := validateStoreID(string(value.UserMessageID)); err != nil {
		return err
	}
	return nil
}

func validatePatch(value session.PatchRecord) error {
	if err := validateStoreID(string(value.ID)); err != nil {
		return err
	}
	if err := validateStoreID(string(value.SessionID)); err != nil {
		return err
	}
	if err := validateStoreID(string(value.TurnID)); err != nil {
		return err
	}
	return nil
}

func validateTurnCommit(commit session.TurnCommit) error {
	if err := validateSession(commit.Session); err != nil {
		return err
	}
	if err := validateMessage(commit.AssistantMessage); err != nil {
		return err
	}
	if err := validateTurn(commit.Turn); err != nil {
		return err
	}
	if commit.AssistantMessage.SessionID != commit.Session.ID || commit.Turn.SessionID != commit.Session.ID || commit.AssistantMessage.TurnID != commit.Turn.ID {
		return errors.New("turn records belong to another session or turn")
	}
	return nil
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return errors.New("context is nil")
	}
	return ctx.Err()
}

func closedStoreError(operation string) error {
	return storeAppError(session.ErrInvalidState, operation, "Session store is closed.", ErrStoreClosed)
}

func corruptedStoreError(operation string, cause error) error {
	return storeAppError(session.ErrCorruptedState, operation, "Stored session data is corrupted.", cause)
}

func storeAppError(code session.ErrorCode, operation string, message string, cause error) error {
	return &session.AppError{
		Code:        code,
		Operation:   operation,
		UserMessage: message,
		Cause:       cause,
	}
}

func cloneRegistry(value registryDTO) registryDTO {
	value.WorkspaceIDs = append([]session.WorkspaceID(nil), value.WorkspaceIDs...)
	return value
}

func newWorkspaceDTO(value session.WorkspaceRecord) workspaceDTO {
	return workspaceDTO{
		Version:      currentStoreVersion,
		ID:           value.ID,
		DisplayName:  value.DisplayName,
		GitCommonDir: value.GitCommonDir,
		Trusted:      value.Trusted,
		CreatedAt:    value.CreatedAt.UTC(),
		LastUsedAt:   value.LastUsedAt.UTC(),
	}
}

func (d workspaceDTO) workspace() session.WorkspaceRecord {
	return session.WorkspaceRecord{
		ID:           d.ID,
		DisplayName:  d.DisplayName,
		GitCommonDir: d.GitCommonDir,
		Trusted:      d.Trusted,
		CreatedAt:    d.CreatedAt,
		LastUsedAt:   d.LastUsedAt,
	}
}

func newWorktreeDTO(value session.WorktreeRecord) worktreeDTO {
	return worktreeDTO{
		Version:       currentStoreVersion,
		ID:            value.ID,
		WorkspaceID:   value.WorkspaceID,
		Root:          value.Root,
		GitDir:        value.GitDir,
		LastSessionID: value.LastSessionID,
		CreatedAt:     value.CreatedAt.UTC(),
		LastUsedAt:    value.LastUsedAt.UTC(),
	}
}

func (d worktreeDTO) worktree() session.WorktreeRecord {
	return session.WorktreeRecord{
		ID:            d.ID,
		WorkspaceID:   d.WorkspaceID,
		Root:          d.Root,
		GitDir:        d.GitDir,
		LastSessionID: d.LastSessionID,
		CreatedAt:     d.CreatedAt,
		LastUsedAt:    d.LastUsedAt,
	}
}

func newSessionDTO(value session.Session) sessionDTO {
	return sessionDTO{
		Version:           currentStoreVersion,
		ID:                value.ID,
		WorkspaceID:       value.WorkspaceID,
		WorktreeID:        value.WorktreeID,
		Title:             value.Title,
		ProviderProfileID: value.ProviderProfileID,
		ModelID:           value.ModelID,
		PermissionMode:    value.PermissionMode,
		BaseCommit:        value.BaseCommit,
		LastTurnStatus:    value.LastTurnStatus,
		Archived:          value.Archived,
		CreatedAt:         value.CreatedAt.UTC(),
		UpdatedAt:         value.UpdatedAt.UTC(),
	}
}

func (d sessionDTO) session() session.Session {
	return session.Session{
		ID:                d.ID,
		WorkspaceID:       d.WorkspaceID,
		WorktreeID:        d.WorktreeID,
		Title:             d.Title,
		ProviderProfileID: d.ProviderProfileID,
		ModelID:           d.ModelID,
		PermissionMode:    d.PermissionMode,
		BaseCommit:        d.BaseCommit,
		LastTurnStatus:    d.LastTurnStatus,
		Archived:          d.Archived,
		CreatedAt:         d.CreatedAt,
		UpdatedAt:         d.UpdatedAt,
	}
}

func newMessageDTO(value session.Message) messageDTO {
	return messageDTO{
		ID:        value.ID,
		SessionID: value.SessionID,
		TurnID:    value.TurnID,
		Role:      value.Role,
		Content:   value.Content,
		CreatedAt: value.CreatedAt.UTC(),
	}
}

func (d messageDTO) message() session.Message {
	return session.Message{ID: d.ID, SessionID: d.SessionID, TurnID: d.TurnID, Role: d.Role, Content: d.Content, CreatedAt: d.CreatedAt}
}

func newTurnDTO(value session.TurnRecord) turnDTO {
	return turnDTO{
		ID:                value.ID,
		SessionID:         value.SessionID,
		UserMessageID:     value.UserMessageID,
		Status:            value.Status,
		TerminationReason: value.TerminationReason,
		ProviderProfileID: value.ProviderProfileID,
		ModelID:           value.ModelID,
		Steps:             value.Steps,
		CheckSummary:      newCheckSummaryDTO(value.CheckSummary),
		StartedAt:         value.StartedAt.UTC(),
		CompletedAt:       value.CompletedAt.UTC(),
	}
}

func (d turnDTO) turn() session.TurnRecord {
	return session.TurnRecord{ID: d.ID, SessionID: d.SessionID, UserMessageID: d.UserMessageID, Status: d.Status, TerminationReason: d.TerminationReason, ProviderProfileID: d.ProviderProfileID, ModelID: d.ModelID, Steps: d.Steps, CheckSummary: d.CheckSummary.checkSummary(), StartedAt: d.StartedAt, CompletedAt: d.CompletedAt}
}

func newCheckSummaryDTO(value session.CheckSummary) checkSummaryDTO {
	return checkSummaryDTO{
		Outcome:   value.Outcome,
		Summary:   value.Summary,
		Truncated: value.Truncated,
	}
}

func (d checkSummaryDTO) checkSummary() session.CheckSummary {
	return session.CheckSummary{Outcome: d.Outcome, Summary: d.Summary, Truncated: d.Truncated}
}

func newPatchDTO(value session.PatchRecord) patchDTO {
	files := make([]patchedFileDTO, 0, len(value.Files))
	for _, file := range value.Files {
		files = append(files, patchedFileDTO{
			Path:       file.Path,
			BeforeHash: file.BeforeHash,
			AfterHash:  file.AfterHash,
		})
	}
	return patchDTO{
		ID:        value.ID,
		SessionID: value.SessionID,
		TurnID:    value.TurnID,
		Patch:     value.Patch,
		Files:     files,
		AppliedAt: value.AppliedAt.UTC(),
	}
}

func (d patchDTO) patchRecord() session.PatchRecord {
	files := make([]session.PatchedFile, 0, len(d.Files))
	for _, file := range d.Files {
		files = append(files, session.PatchedFile{Path: file.Path, BeforeHash: file.BeforeHash, AfterHash: file.AfterHash})
	}
	return session.PatchRecord{ID: d.ID, SessionID: d.SessionID, TurnID: d.TurnID, Patch: d.Patch, Files: files, AppliedAt: d.AppliedAt}
}

func containsMessageDTO(values []messageDTO, id session.MessageID) bool {
	for _, value := range values {
		if value.ID == id {
			return true
		}
	}
	return false
}

func containsTurnDTO(values []turnDTO, id session.TurnID) bool {
	for _, value := range values {
		if value.ID == id {
			return true
		}
	}
	return false
}
