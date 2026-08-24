package codingagent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// WorktreeAvailability is the current, non-persisted health of a stored binding.
type WorktreeAvailability string

const (
	WorktreeAvailable       WorktreeAvailability = "available"
	WorktreeUnavailable     WorktreeAvailability = "unavailable"
	WorktreeIdentityChanged WorktreeAvailability = "identity_changed"
)

// WorkspaceSummary is the bounded product projection used by workspace pickers.
type WorkspaceSummary struct {
	ID          WorkspaceID       `json:"id"`
	DisplayName string            `json:"display_name"`
	Trusted     bool              `json:"trusted"`
	Worktrees   []WorktreeSummary `json:"worktrees"`
}

// WorktreeSummary exposes only the stored local path and a safe health category.
type WorktreeSummary struct {
	ID           WorktreeID           `json:"id"`
	Root         string               `json:"root"`
	Availability WorktreeAvailability `json:"availability"`
	Message      string               `json:"message,omitempty"`
}

// RelocateWorktreeRequest explicitly maps one unavailable durable binding to a
// user-selected Git worktree after repository identity validation.
type RelocateWorktreeRequest struct {
	WorktreeID WorktreeID
	NewPath    string
}

// WorkspaceController validates, lists and explicitly repairs product bindings.
// It is a Coding product port; generic Agent and LLM packages never see paths.
type WorkspaceController interface {
	WorktreeReader
	ListWorkspaces(ctx context.Context) ([]WorkspaceSummary, error)
	RelocateWorktree(ctx context.Context, request RelocateWorktreeRequest) (Worktree, error)
	ActivateWorktree(ctx context.Context, id WorktreeID) error
}

// ListWorkspaces returns current workspace/worktree health for presentation.
func (s *Service) ListWorkspaces(ctx context.Context) ([]WorkspaceSummary, error) {
	if s == nil || s.deps.Workspaces == nil {
		return nil, errors.New("list Coding workspaces: workspace management is unavailable")
	}
	values, err := s.deps.Workspaces.ListWorkspaces(ctx)
	if err != nil {
		return nil, fmt.Errorf("list Coding workspaces: %w", err)
	}
	return values, nil
}

// RelocateWorktree performs an explicit, identity-checked durable repair.
func (s *Service) RelocateWorktree(ctx context.Context, request RelocateWorktreeRequest) (Worktree, error) {
	if s == nil || s.deps.Workspaces == nil {
		return Worktree{}, errors.New("relocate Coding worktree: workspace management is unavailable")
	}
	request.NewPath = strings.TrimSpace(request.NewPath)
	if request.WorktreeID == "" || request.NewPath == "" {
		return Worktree{}, errors.New("relocate Coding worktree: worktree and new path are required")
	}
	value, err := s.deps.Workspaces.RelocateWorktree(ctx, request)
	if err != nil {
		return Worktree{}, fmt.Errorf("relocate Coding worktree: %w", err)
	}
	if err := s.publishWorkspaceEvent(ctx, value); err != nil {
		return value, fmt.Errorf("relocate Coding worktree: publish update after durable commit: %w", err)
	}
	return value, nil
}

func (s *Service) publishWorkspaceEvent(ctx context.Context, worktree Worktree) error {
	id, err := newID("event")
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.eventSeq++
	sequence := s.eventSeq
	active := s.active
	s.mu.Unlock()
	return s.deps.Events.PublishCodingEvent(ctx, Event{
		ID: id, Sequence: sequence, SessionID: active, Kind: EventWorkspaceChanged,
		Timestamp: time.Now().UTC(), Payload: EventPayload{Workspace: &WorkspaceEvent{
			WorkspaceID: string(worktree.WorkspaceID), WorktreeID: string(worktree.ID), Changed: true,
		}},
	})
}
