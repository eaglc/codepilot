package sessionstore

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/eaglc/codepilot/internal/session"
)

var (
	_ session.SessionStore      = (*MemoryStore)(nil)
	_ session.WorkspaceRegistry = (*MemoryStore)(nil)
)

// MemoryStore provides isolated in-memory session and workspace persistence.
type MemoryStore struct {
	mu sync.RWMutex

	sessions   map[session.SessionID]session.Session
	messages   map[session.SessionID][]session.Message
	turns      map[session.SessionID][]session.TurnRecord
	patches    map[session.SessionID][]session.PatchRecord
	workspaces map[session.WorkspaceID]session.WorkspaceRecord
	worktrees  map[session.WorktreeID]session.WorktreeRecord

	lastActiveSessionID session.SessionID
}

// NewMemoryStore creates an empty store for tests and local composition.
func NewMemoryStore() *MemoryStore {
	store := &MemoryStore{}
	store.resetLocked()
	return store
}

// Reset removes all stored values while keeping the store reusable.
func (s *MemoryStore) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.resetLocked()
}

// CreateSession inserts session metadata and initializes its record streams.
func (s *MemoryStore) CreateSession(ctx context.Context, value session.Session) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.sessions[value.ID]; exists {
		return storeError(session.ErrConflict, "sessionstore.create_session", "Session already exists.")
	}

	s.sessions[value.ID] = value
	s.messages[value.ID] = nil
	s.turns[value.ID] = nil
	s.patches[value.ID] = nil

	return nil
}

// LoadSession returns an isolated snapshot of one stored session.
func (s *MemoryStore) LoadSession(ctx context.Context, id session.SessionID) (session.SessionSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return session.SessionSnapshot{}, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	value, exists := s.sessions[id]
	if !exists {
		return session.SessionSnapshot{}, storeError(session.ErrNotFound, "sessionstore.load_session", "Session not found.")
	}

	return session.SessionSnapshot{
		Session:      value,
		RuntimeState: session.RuntimeIdle,
		Messages:     cloneMessages(s.messages[id]),
		Turns:        cloneTurns(s.turns[id]),
		Patches:      clonePatches(s.patches[id]),
	}, nil
}

// ListSessions returns filtered metadata ordered by recent use and stable ID.
func (s *MemoryStore) ListSessions(ctx context.Context, filter session.SessionFilter) ([]session.SessionSummary, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	values := make([]session.SessionSummary, 0, len(s.sessions))
	for _, stored := range s.sessions {
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

	sort.Slice(values, func(left, right int) bool {
		if values[left].UpdatedAt.Equal(values[right].UpdatedAt) {
			return values[left].ID < values[right].ID
		}
		return values[left].UpdatedAt.After(values[right].UpdatedAt)
	})

	return values, nil
}

// SaveSession replaces mutable session metadata for an existing session.
func (s *MemoryStore) SaveSession(ctx context.Context, value session.Session) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.sessions[value.ID]; !exists {
		return storeError(session.ErrNotFound, "sessionstore.save_session", "Session not found.")
	}

	s.sessions[value.ID] = value
	return nil
}

// AppendMessage appends a neutral message once for its unique ID.
func (s *MemoryStore) AppendMessage(ctx context.Context, value session.Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.sessions[value.SessionID]; !exists {
		return storeError(session.ErrNotFound, "sessionstore.append_message", "Session not found.")
	}
	if containsMessage(s.messages[value.SessionID], value.ID) {
		return nil
	}

	s.messages[value.SessionID] = append(s.messages[value.SessionID], value)
	return nil
}

// AppendPatch appends an applied patch once for its unique ID.
func (s *MemoryStore) AppendPatch(ctx context.Context, value session.PatchRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.sessions[value.SessionID]; !exists {
		return storeError(session.ErrNotFound, "sessionstore.append_patch", "Session not found.")
	}
	if containsPatch(s.patches[value.SessionID], value.ID) {
		return nil
	}

	s.patches[value.SessionID] = append(s.patches[value.SessionID], clonePatch(value))
	return nil
}

// CommitTurn appends final turn records and updates session metadata atomically.
func (s *MemoryStore) CommitTurn(ctx context.Context, commit session.TurnCommit) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.sessions[commit.Session.ID]; !exists {
		return storeError(session.ErrNotFound, "sessionstore.commit_turn", "Session not found.")
	}
	if commit.AssistantMessage.SessionID != commit.Session.ID || commit.Turn.SessionID != commit.Session.ID {
		return storeError(session.ErrInvalidInput, "sessionstore.commit_turn", "Turn records belong to another session.")
	}

	if !containsMessage(s.messages[commit.Session.ID], commit.AssistantMessage.ID) {
		s.messages[commit.Session.ID] = append(s.messages[commit.Session.ID], commit.AssistantMessage)
	}
	if !containsTurn(s.turns[commit.Session.ID], commit.Turn.ID) {
		s.turns[commit.Session.ID] = append(s.turns[commit.Session.ID], commit.Turn)
	}
	s.sessions[commit.Session.ID] = commit.Session

	return nil
}

// ArchiveSession marks a session archived without deleting its records.
func (s *MemoryStore) ArchiveSession(ctx context.Context, id session.SessionID, archivedAt time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	value, exists := s.sessions[id]
	if !exists {
		return storeError(session.ErrNotFound, "sessionstore.archive_session", "Session not found.")
	}

	value.Archived = true
	value.UpdatedAt = archivedAt
	s.sessions[id] = value

	return nil
}

// SaveWorkspace inserts or replaces workspace registration metadata.
func (s *MemoryStore) SaveWorkspace(ctx context.Context, value session.WorkspaceRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.workspaces[value.ID] = value
	return nil
}

// SaveWorktree inserts or replaces worktree registration metadata.
func (s *MemoryStore) SaveWorktree(ctx context.Context, value session.WorktreeRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.workspaces[value.WorkspaceID]; !exists {
		return storeError(session.ErrNotFound, "sessionstore.save_worktree", "Workspace not found.")
	}

	s.worktrees[value.ID] = value
	return nil
}

// LoadWorkspace returns one registered workspace.
func (s *MemoryStore) LoadWorkspace(ctx context.Context, id session.WorkspaceID) (session.WorkspaceRecord, error) {
	if err := ctx.Err(); err != nil {
		return session.WorkspaceRecord{}, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	value, exists := s.workspaces[id]
	if !exists {
		return session.WorkspaceRecord{}, storeError(session.ErrNotFound, "sessionstore.load_workspace", "Workspace not found.")
	}

	return value, nil
}

// LoadWorktree returns one registered worktree.
func (s *MemoryStore) LoadWorktree(ctx context.Context, id session.WorktreeID) (session.WorktreeRecord, error) {
	if err := ctx.Err(); err != nil {
		return session.WorktreeRecord{}, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	value, exists := s.worktrees[id]
	if !exists {
		return session.WorktreeRecord{}, storeError(session.ErrNotFound, "sessionstore.load_worktree", "Worktree not found.")
	}

	return value, nil
}

// FindWorktreeByRoot finds a worktree by its normalized absolute root.
func (s *MemoryStore) FindWorktreeByRoot(ctx context.Context, normalizedRoot string) (session.WorktreeRecord, bool, error) {
	if err := ctx.Err(); err != nil {
		return session.WorktreeRecord{}, false, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, value := range s.worktrees {
		if value.Root == normalizedRoot {
			return value, true, nil
		}
	}

	return session.WorktreeRecord{}, false, nil
}

// ListWorktrees returns registered worktrees ordered by recent use and stable ID.
func (s *MemoryStore) ListWorktrees(ctx context.Context) ([]session.WorktreeSummary, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	values := make([]session.WorktreeSummary, 0, len(s.worktrees))
	for _, worktree := range s.worktrees {
		workspace := s.workspaces[worktree.WorkspaceID]
		values = append(values, session.WorktreeSummary{
			ID:            worktree.ID,
			WorkspaceID:   worktree.WorkspaceID,
			DisplayName:   workspace.DisplayName,
			Root:          worktree.Root,
			LastSessionID: worktree.LastSessionID,
			Available:     worktree.Root != "",
			LastUsedAt:    worktree.LastUsedAt,
		})
	}

	sort.Slice(values, func(left, right int) bool {
		if values[left].LastUsedAt.Equal(values[right].LastUsedAt) {
			return values[left].ID < values[right].ID
		}
		return values[left].LastUsedAt.After(values[right].LastUsedAt)
	})

	return values, nil
}

// SaveLastActiveSession records the process-wide session resume target.
func (s *MemoryStore) SaveLastActiveSession(ctx context.Context, id session.SessionID) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.sessions[id]; !exists {
		return storeError(session.ErrNotFound, "sessionstore.save_last_active_session", "Session not found.")
	}

	s.lastActiveSessionID = id
	return nil
}

func (s *MemoryStore) resetLocked() {
	s.sessions = make(map[session.SessionID]session.Session)
	s.messages = make(map[session.SessionID][]session.Message)
	s.turns = make(map[session.SessionID][]session.TurnRecord)
	s.patches = make(map[session.SessionID][]session.PatchRecord)
	s.workspaces = make(map[session.WorkspaceID]session.WorkspaceRecord)
	s.worktrees = make(map[session.WorktreeID]session.WorktreeRecord)
	s.lastActiveSessionID = ""
}

func sessionSummary(value session.Session) session.SessionSummary {
	return session.SessionSummary{
		ID:                value.ID,
		WorkspaceID:       value.WorkspaceID,
		WorktreeID:        value.WorktreeID,
		Title:             value.Title,
		ProviderProfileID: value.ProviderProfileID,
		ModelID:           value.ModelID,
		PermissionMode:    value.PermissionMode,
		LastTurnStatus:    value.LastTurnStatus,
		Archived:          value.Archived,
		UpdatedAt:         value.UpdatedAt,
	}
}

func cloneMessages(values []session.Message) []session.Message {
	return append([]session.Message(nil), values...)
}

func cloneTurns(values []session.TurnRecord) []session.TurnRecord {
	return append([]session.TurnRecord(nil), values...)
}

func clonePatches(values []session.PatchRecord) []session.PatchRecord {
	cloned := make([]session.PatchRecord, len(values))
	for index, value := range values {
		cloned[index] = clonePatch(value)
	}
	return cloned
}

func clonePatch(value session.PatchRecord) session.PatchRecord {
	value.Files = append([]session.PatchedFile(nil), value.Files...)
	return value
}

func containsMessage(values []session.Message, id session.MessageID) bool {
	for _, value := range values {
		if value.ID == id {
			return true
		}
	}
	return false
}

func containsTurn(values []session.TurnRecord, id session.TurnID) bool {
	for _, value := range values {
		if value.ID == id {
			return true
		}
	}
	return false
}

func containsPatch(values []session.PatchRecord, id session.PatchID) bool {
	for _, value := range values {
		if value.ID == id {
			return true
		}
	}
	return false
}

func storeError(code session.ErrorCode, operation string, message string) error {
	return &session.AppError{
		Code:        code,
		Operation:   operation,
		UserMessage: message,
	}
}
