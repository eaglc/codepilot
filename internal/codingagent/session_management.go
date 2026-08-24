package codingagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	agentsession "github.com/eaglc/codepilot/internal/agent/session"
)

// SessionListOptions scopes product session discovery without exposing store DTOs.
type SessionListOptions struct {
	WorkspaceID     WorkspaceID
	WorktreeID      WorktreeID
	IncludeArchived bool
}

// ListSessions returns stable product sessions matching the requested Coding scope.
func (s *Service) ListSessions(ctx context.Context, options SessionListOptions) ([]Session, error) {
	values, err := s.deps.Sessions.ListSessions(ctx)
	if err != nil {
		return nil, fmt.Errorf("list Coding Agent sessions: %w", err)
	}
	filtered := make([]Session, 0, len(values))
	for _, value := range values {
		if !options.IncludeArchived && value.Archived {
			continue
		}
		if options.WorkspaceID != "" && value.WorkspaceID != options.WorkspaceID {
			continue
		}
		if options.WorktreeID != "" && value.WorktreeID != options.WorktreeID {
			continue
		}
		value.ActiveLane = sessionLane(value)
		filtered = append(filtered, value)
	}
	sort.SliceStable(filtered, func(left, right int) bool {
		if filtered[left].UpdatedAt.Equal(filtered[right].UpdatedAt) {
			return filtered[left].ID < filtered[right].ID
		}
		return filtered[left].UpdatedAt.After(filtered[right].UpdatedAt)
	})
	return filtered, nil
}

// SwitchSession validates and activates one durable product session, returning
// a fresh authoritative snapshot rather than reusing presentation state.
func (s *Service) SwitchSession(ctx context.Context, id SessionID) (Snapshot, error) {
	if id == "" {
		return Snapshot{}, errors.New("switch Coding Agent session: id is required")
	}
	s.mu.RLock()
	active, activeState := s.active, s.states[s.active]
	s.mu.RUnlock()
	if active != "" && active != id && (activeState == RuntimeRunning || activeState == RuntimeCancelling) {
		return Snapshot{}, errors.New("switch Coding Agent session: the active session is still running")
	}
	product, err := s.deps.Sessions.LoadSession(ctx, id)
	if err != nil {
		return Snapshot{}, fmt.Errorf("switch Coding Agent session: %w", err)
	}
	if product.Archived {
		return Snapshot{}, errors.New("switch Coding Agent session: archived sessions cannot be activated")
	}
	if _, err := s.deps.Worktrees.LoadWorktree(ctx, product.WorktreeID); err != nil {
		return Snapshot{}, fmt.Errorf("switch Coding Agent session: load worktree: %w", err)
	}
	durable, err := s.deps.AgentSessions.Load(ctx, product.AgentSessionID)
	if err != nil {
		return Snapshot{}, fmt.Errorf("switch Coding Agent session: load Agent session: %w", err)
	}
	if durable.Metadata.Archived {
		return Snapshot{}, errors.New("switch Coding Agent session: its Agent session is archived")
	}
	if s.deps.Workspaces != nil {
		if err := s.deps.Workspaces.ActivateWorktree(ctx, product.WorktreeID); err != nil {
			return Snapshot{}, fmt.Errorf("switch Coding Agent session: activate worktree: %w", err)
		}
	}
	snapshot, err := s.Snapshot(ctx, id)
	if err != nil {
		return Snapshot{}, err
	}
	s.mu.Lock()
	s.active = id
	s.mu.Unlock()
	if err := s.publishSessionEvent(ctx, EventSessionActivated, snapshot.Session, snapshot.Revision); err != nil {
		return snapshot, fmt.Errorf("switch Coding Agent session: publish activation after state changed: %w", err)
	}
	return snapshot, nil
}

// RenameSession updates mutable presentation metadata only.
func (s *Service) RenameSession(ctx context.Context, id SessionID, title string) (Session, error) {
	title = strings.TrimSpace(title)
	if id == "" || title == "" {
		return Session{}, errors.New("rename Coding Agent session: id and title are required")
	}
	if utf8.RuneCountInString(title) > 256 || strings.ContainsRune(title, '\x00') {
		return Session{}, errors.New("rename Coding Agent session: title is invalid or too long")
	}
	operation := s.operationLock(id)
	operation.Lock()
	defer operation.Unlock()
	product, err := s.deps.Sessions.LoadSession(ctx, id)
	if err != nil {
		return Session{}, fmt.Errorf("rename Coding Agent session: %w", err)
	}
	if product.Archived {
		return Session{}, errors.New("rename Coding Agent session: archived sessions are immutable")
	}
	product.Title = title
	product.UpdatedAt = time.Now().UTC()
	if err := s.deps.Sessions.SaveSession(ctx, product); err != nil {
		return Session{}, fmt.Errorf("rename Coding Agent session: %w", err)
	}
	if err := s.publishSessionEvent(ctx, EventSessionUpdated, product, 0); err != nil {
		return product, fmt.Errorf("rename Coding Agent session: publish update after durable commit: %w", err)
	}
	return product, nil
}

// SetPermissionMode updates future Coding tool policy and revokes every active
// session grant so changing modes cannot accidentally reactivate old approval.
func (s *Service) SetPermissionMode(ctx context.Context, id SessionID, mode PermissionMode) (Session, error) {
	if id == "" {
		return Session{}, errors.New("set Coding Agent permissions: session id is required")
	}
	if mode != PermissionReadOnly && mode != PermissionAsk && mode != PermissionAutoEdit {
		return Session{}, fmt.Errorf("set Coding Agent permissions: unsupported mode %q", mode)
	}
	state := s.state(id)
	if state == RuntimeRunning || state == RuntimeCancelling {
		return Session{}, errors.New("set Coding Agent permissions: the session is still running")
	}
	operation := s.operationLock(id)
	operation.Lock()
	defer operation.Unlock()
	state = s.state(id)
	if state == RuntimeRunning || state == RuntimeCancelling {
		return Session{}, errors.New("set Coding Agent permissions: the session is still running")
	}
	product, err := s.deps.Sessions.LoadSession(ctx, id)
	if err != nil {
		return Session{}, fmt.Errorf("set Coding Agent permissions: %w", err)
	}
	if product.Archived {
		return Session{}, errors.New("set Coding Agent permissions: archived sessions are immutable")
	}
	if product.PermissionMode == mode {
		return product, nil
	}
	now := time.Now().UTC()
	for index := range product.PermissionGrants {
		if product.PermissionGrants[index].RevokedAt.IsZero() && now.Before(product.PermissionGrants[index].ExpiresAt) {
			product.PermissionGrants[index].RevokedAt = now
		}
	}
	product.PermissionMode = mode
	product.UpdatedAt = now
	if err := s.deps.Sessions.SaveSession(ctx, product); err != nil {
		return Session{}, fmt.Errorf("set Coding Agent permissions: %w", err)
	}
	if err := s.publishSessionEvent(ctx, EventSessionUpdated, product, 0); err != nil {
		return product, fmt.Errorf("set Coding Agent permissions: publish update after durable commit: %w", err)
	}
	return product, nil
}

// ArchiveSession hides an inactive session while preserving both repositories and journals.
func (s *Service) ArchiveSession(ctx context.Context, id SessionID) (Session, error) {
	if id == "" {
		return Session{}, errors.New("archive Coding Agent session: id is required")
	}
	s.mu.RLock()
	active := s.active
	s.mu.RUnlock()
	if active == id {
		return Session{}, errors.New("archive Coding Agent session: switch to another session first")
	}
	operation := s.operationLock(id)
	operation.Lock()
	defer operation.Unlock()
	product, err := s.deps.Sessions.LoadSession(ctx, id)
	if err != nil {
		return Session{}, fmt.Errorf("archive Coding Agent session: %w", err)
	}
	if product.Archived {
		return product, nil
	}
	state := s.state(id)
	if state == RuntimeRunning || state == RuntimeCancelling {
		return Session{}, errors.New("archive Coding Agent session: session is still running")
	}
	if archiver, ok := s.deps.AgentSessions.(agentsession.JournalArchiver); ok {
		if _, err := archiver.CreateJournalArchive(ctx, product.AgentSessionID); err != nil {
			return Session{}, fmt.Errorf("archive Coding Agent session: create journal cold copy: %w", err)
		}
	}
	if err := s.deps.AgentSessions.SetArchived(ctx, product.AgentSessionID, true); err != nil {
		return Session{}, fmt.Errorf("archive Coding Agent session: archive Agent session: %w", err)
	}
	product.Archived = true
	product.UpdatedAt = time.Now().UTC()
	if err := s.deps.Sessions.SaveSession(ctx, product); err != nil {
		rollbackErr := s.deps.AgentSessions.SetArchived(ctx, product.AgentSessionID, false)
		return Session{}, errors.Join(fmt.Errorf("archive Coding Agent session: %w", err), rollbackErr)
	}
	if err := s.publishSessionEvent(ctx, EventSessionUpdated, product, 0); err != nil {
		return product, fmt.Errorf("archive Coding Agent session: publish update after durable commit: %w", err)
	}
	return product, nil
}

// ForkLaneRequest selects a historical entry on the active branch.
type ForkLaneRequest struct {
	SessionID   SessionID
	FromEntryID string
}

// ForkLane creates or reuses a deterministic branch and makes it active.
func (s *Service) ForkLane(ctx context.Context, request ForkLaneRequest) (Snapshot, error) {
	from := agentsession.EntryID(strings.TrimSpace(request.FromEntryID))
	if request.SessionID == "" || from == "" {
		return Snapshot{}, errors.New("fork Coding Agent session lane: session and entry ids are required")
	}
	operation := s.operationLock(request.SessionID)
	operation.Lock()
	defer operation.Unlock()
	product, err := s.deps.Sessions.LoadSession(ctx, request.SessionID)
	if err != nil {
		return Snapshot{}, fmt.Errorf("fork Coding Agent session lane: %w", err)
	}
	if product.Archived {
		return Snapshot{}, errors.New("fork Coding Agent session lane: session is archived")
	}
	durable, err := s.deps.AgentSessions.Load(ctx, product.AgentSessionID)
	if err != nil {
		return Snapshot{}, fmt.Errorf("fork Coding Agent session lane: load Agent session: %w", err)
	}
	if recovery := agentsession.AnalyzeRecovery(durable); len(recovery.PendingRuns) != 0 || len(recovery.PendingTools) != 0 || len(recovery.PendingInterrupts) != 0 {
		return Snapshot{}, errors.New("fork Coding Agent session lane: unfinished work must be resolved first")
	}
	branch, err := agentsession.BranchEntries(durable, sessionLane(product))
	if err != nil {
		return Snapshot{}, fmt.Errorf("fork Coding Agent session lane: load active branch: %w", err)
	}
	if !branchContainsEntry(branch, from) {
		return Snapshot{}, errors.New("fork Coding Agent session lane: entry is not on the active branch")
	}
	lane := deterministicLane(request.SessionID, from)
	if err := ensureLaneFork(ctx, s.deps.AgentSessions, durable, product.AgentSessionID, lane, from); err != nil {
		return Snapshot{}, err
	}
	product.ActiveLane = lane
	product.UpdatedAt = time.Now().UTC()
	if err := s.deps.Sessions.SaveSession(ctx, product); err != nil {
		return Snapshot{}, fmt.Errorf("fork Coding Agent session lane: save active lane %q: %w", lane, err)
	}
	snapshot, err := s.Snapshot(ctx, product.ID)
	if err != nil {
		return Snapshot{}, err
	}
	if err := s.publishSessionEvent(ctx, EventSessionUpdated, snapshot.Session, snapshot.Revision); err != nil {
		return snapshot, fmt.Errorf("fork Coding Agent session lane: publish update after durable commit: %w", err)
	}
	return snapshot, nil
}

func branchContainsEntry(entries []agentsession.Entry, id agentsession.EntryID) bool {
	for _, entry := range entries {
		if entry.ID == id {
			return true
		}
	}
	return false
}

func deterministicLane(sessionID SessionID, from agentsession.EntryID) agentsession.Lane {
	digest := sha256.Sum256([]byte(string(sessionID) + ":" + string(from)))
	return agentsession.Lane("branch_" + hex.EncodeToString(digest[:8]))
}

func ensureLaneFork(ctx context.Context, repository agentsession.Repository, durable agentsession.Snapshot, sessionID agentsession.ID, lane agentsession.Lane, from agentsession.EntryID) error {
	for _, pointer := range durable.Lanes {
		if pointer.Lane != lane {
			continue
		}
		for _, record := range durable.Records {
			if record.Type == agentsession.RecordLaneForked && record.LaneFork != nil && record.LaneFork.Lane == lane && record.LaneFork.FromEntryID == from {
				return nil
			}
		}
		return fmt.Errorf("fork Coding Agent session lane: lane %q already has a different origin", lane)
	}
	if err := repository.ForkLane(ctx, sessionID, lane, from); err != nil {
		return fmt.Errorf("fork Coding Agent session lane: %w", err)
	}
	return nil
}

func (s *Service) publishSessionEvent(ctx context.Context, kind EventKind, product Session, revision uint64) error {
	id, err := newID("event")
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.eventSeq++
	sequence := s.eventSeq
	s.mu.Unlock()
	return s.deps.Events.PublishCodingEvent(ctx, Event{
		ID: id, Sequence: sequence, SnapshotRevision: revision, SessionID: product.ID, Timestamp: time.Now().UTC(), Kind: kind,
		Payload: EventPayload{Session: &SessionEvent{
			SessionID: product.ID, Title: product.Title, ProviderProfileID: product.ProviderProfileID, ModelID: product.ModelID,
			PermissionMode: product.PermissionMode, ActiveLane: string(sessionLane(product)), Archived: product.Archived,
		}},
	})
}
