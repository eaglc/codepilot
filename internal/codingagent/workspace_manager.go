package codingagent

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	workspaceinfra "github.com/eaglc/codepilot/internal/codingagent/workspace"
)

const (
	maxCatalogWorkspaces = 128
	maxCatalogWorktrees  = 512
)

// ProcessCloser releases worktree-scoped transient capabilities such as LSP.
type ProcessCloser interface {
	CloseWorktree(ctx context.Context, worktreeID string) error
}

// WorkspaceManager validates durable bindings against live Git metadata and owns
// explicit relocation. It never repairs a path as a side effect of loading it.
type WorkspaceManager struct {
	repository WorkspaceRepository
	closer     ProcessCloser
	mu         sync.Mutex
	active     WorktreeID
}

// NewWorkspaceManager creates a validated Coding workspace controller.
func NewWorkspaceManager(repository WorkspaceRepository, closer ProcessCloser) (*WorkspaceManager, error) {
	if repository == nil {
		return nil, errors.New("create workspace manager: repository is required")
	}
	return &WorkspaceManager{repository: repository, closer: closer}, nil
}

// LoadWorktree returns a binding only when its current Git identity is valid.
func (m *WorkspaceManager) LoadWorktree(ctx context.Context, id WorktreeID) (Worktree, error) {
	if m == nil || id == "" {
		return Worktree{}, errors.New("load Coding worktree: id is required")
	}
	value, err := m.repository.LoadWorktree(ctx, id)
	if err != nil {
		return Worktree{}, err
	}
	workspace, err := m.repository.LoadWorkspace(ctx, value.WorkspaceID)
	if err != nil {
		return Worktree{}, fmt.Errorf("load Coding worktree: load workspace identity: %w", err)
	}
	status, message := probeBinding(ctx, workspace, value)
	if status != WorktreeAvailable {
		return Worktree{}, fmt.Errorf("%s", message)
	}
	return value, nil
}

// ListWorkspaces returns a bounded deterministic live-health projection.
func (m *WorkspaceManager) ListWorkspaces(ctx context.Context) ([]WorkspaceSummary, error) {
	if m == nil {
		return nil, errors.New("workspace manager is unavailable")
	}
	workspaces, err := m.repository.ListWorkspaces(ctx)
	if err != nil {
		return nil, err
	}
	if len(workspaces) > maxCatalogWorkspaces {
		return nil, errors.New("workspace catalog exceeds the safe limit")
	}
	result := make([]WorkspaceSummary, 0, len(workspaces))
	totalWorktrees := 0
	for _, value := range workspaces {
		worktrees, err := m.repository.ListWorktrees(ctx, value.ID)
		if err != nil {
			return nil, err
		}
		totalWorktrees += len(worktrees)
		if totalWorktrees > maxCatalogWorktrees {
			return nil, errors.New("worktree catalog exceeds the safe limit")
		}
		summary := WorkspaceSummary{ID: value.ID, DisplayName: value.DisplayName, Trusted: value.Trusted}
		for _, worktree := range worktrees {
			status, message := probeBinding(ctx, value, worktree)
			summary.Worktrees = append(summary.Worktrees, WorktreeSummary{ID: worktree.ID, Root: worktree.Root, Availability: status, Message: message})
		}
		sort.Slice(summary.Worktrees, func(left, right int) bool {
			return summary.Worktrees[left].ID < summary.Worktrees[right].ID
		})
		result = append(result, summary)
	}
	sort.Slice(result, func(left, right int) bool { return result[left].ID < result[right].ID })
	return result, nil
}

// RelocateWorktree verifies a user-selected candidate against the immutable
// history fingerprint, closes transient state, then durably rebinds the path.
func (m *WorkspaceManager) RelocateWorktree(ctx context.Context, request RelocateWorktreeRequest) (Worktree, error) {
	if m == nil || request.WorktreeID == "" || strings.TrimSpace(request.NewPath) == "" {
		return Worktree{}, errors.New("worktree and new path are required")
	}
	stored, err := m.repository.LoadWorktree(ctx, request.WorktreeID)
	if err != nil {
		return Worktree{}, err
	}
	workspace, err := m.repository.LoadWorkspace(ctx, stored.WorkspaceID)
	if err != nil {
		return Worktree{}, err
	}
	if status, _ := probeBinding(ctx, workspace, stored); status == WorktreeAvailable {
		return Worktree{}, errors.New("the stored worktree is still available; open the candidate as a separate worktree")
	}
	resolved, err := workspaceinfra.ResolveWorktree(ctx, request.NewPath)
	if err != nil {
		return Worktree{}, errors.New("the selected relocation target is not an available Git worktree")
	}
	if workspace.RepositoryFingerprint == "" {
		return Worktree{}, errors.New("the stored workspace has no verifiable Git history identity and cannot be relocated safely")
	}
	if workspaceinfra.VerifyRepositoryFingerprint(ctx, resolved.Root, workspace.RepositoryFingerprint) != nil {
		return Worktree{}, errors.New("the selected worktree does not match the stored Git history identity")
	}
	if m.closer != nil {
		closeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err = m.closer.CloseWorktree(closeCtx, string(stored.ID))
		cancel()
		if err != nil {
			return Worktree{}, errors.New("transient worktree services could not be stopped before relocation")
		}
	}
	return m.repository.RelocateWorktree(ctx, stored.ID, stored.Root, resolved.Root, resolved.GitDir, resolved.GitCommonDir, time.Now().UTC())
}

// ActivateWorktree validates the target and closes the previously active
// worktree's transient processes before a cross-worktree session switch.
func (m *WorkspaceManager) ActivateWorktree(ctx context.Context, id WorktreeID) error {
	if _, err := m.LoadWorktree(ctx, id); err != nil {
		return err
	}
	m.mu.Lock()
	previous := m.active
	if previous == id {
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()
	if previous != "" && m.closer != nil {
		closeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := m.closer.CloseWorktree(closeCtx, string(previous))
		cancel()
		if err != nil {
			return errors.New("previous worktree services could not be stopped")
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active != previous {
		return errors.New("active worktree changed concurrently")
	}
	m.active = id
	return nil
}

func probeBinding(ctx context.Context, workspace Workspace, worktree Worktree) (WorktreeAvailability, string) {
	resolved, err := workspaceinfra.ResolveWorktree(ctx, worktree.Root)
	if err != nil {
		return WorktreeUnavailable, "The stored worktree path is unavailable and must be relocated."
	}
	if !samePath(resolved.Root, worktree.Root) || !samePath(resolved.GitDir, worktree.GitDir) || !samePath(resolved.GitCommonDir, workspace.GitCommonDir) {
		if workspace.RepositoryFingerprint != "" && workspaceinfra.VerifyRepositoryFingerprint(ctx, resolved.Root, workspace.RepositoryFingerprint) != nil {
			return WorktreeIdentityChanged, "The stored path now points to different Git history and will not be opened."
		}
		return WorktreeUnavailable, "The stored Git paths changed and require explicit relocation."
	}
	if workspace.RepositoryFingerprint != "" && workspaceinfra.VerifyRepositoryFingerprint(ctx, resolved.Root, workspace.RepositoryFingerprint) != nil {
		return WorktreeIdentityChanged, "The stored path now points to different Git history and will not be opened."
	}
	return WorktreeAvailable, ""
}

func samePath(left, right string) bool {
	left, right = canonicalPath(left), canonicalPath(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func canonicalPath(value string) string {
	return workspaceinfra.CanonicalPath(value)
}

var _ WorkspaceController = (*WorkspaceManager)(nil)
