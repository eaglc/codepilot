package file

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"

	"github.com/eaglc/codepilot/internal/codingagent"
)

const worktreeRelocationDirectory = "coding-transactions/worktree-relocate"

type worktreeRelocationIntent struct {
	ID              codingagent.WorktreeID `json:"id"`
	BeforeWorktree  codingagent.Worktree   `json:"before_worktree"`
	AfterWorktree   codingagent.Worktree   `json:"after_worktree"`
	BeforeWorkspace codingagent.Workspace  `json:"before_workspace"`
	AfterWorkspace  codingagent.Workspace  `json:"after_workspace"`
}

// recoverWorktreeRelocations completes transactions interrupted between the
// worktree and workspace atomic-file replacements.
func (r *Repository) recoverWorktreeRelocations(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	intents, err := listEnvelopes[worktreeRelocationIntent](ctx, filepath.Join(r.root, worktreeRelocationDirectory))
	if err != nil {
		return err
	}
	sort.Slice(intents, func(left, right int) bool { return intents[left].ID < intents[right].ID })
	for _, intent := range intents {
		if err := r.commitWorktreeRelocationLocked(intent); err != nil {
			return err
		}
	}
	return nil
}

func (r *Repository) commitWorktreeRelocationLocked(intent worktreeRelocationIntent) error {
	if err := validateWorktreeRelocationIntent(intent); err != nil {
		return err
	}
	currentWorktree, err := r.loadWorktreeLocked(intent.ID)
	if err != nil {
		return err
	}
	currentWorkspace, err := r.loadWorkspaceLocked(intent.BeforeWorkspace.ID)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(currentWorktree, intent.BeforeWorktree) && !reflect.DeepEqual(currentWorktree, intent.AfterWorktree) {
		return fmt.Errorf("recover Coding worktree relocation %q: worktree changed outside the transaction", intent.ID)
	}
	if !reflect.DeepEqual(currentWorkspace, intent.BeforeWorkspace) && !reflect.DeepEqual(currentWorkspace, intent.AfterWorkspace) {
		return fmt.Errorf("recover Coding worktree relocation %q: workspace changed outside the transaction", intent.ID)
	}
	worktreePath, err := r.path("coding-worktrees", string(intent.ID))
	if err != nil {
		return err
	}
	if err := writeEnvelope(worktreePath, intent.AfterWorktree); err != nil {
		return err
	}
	workspacePath, err := r.path("coding-workspaces", string(intent.AfterWorkspace.ID))
	if err != nil {
		return err
	}
	if err := writeEnvelope(workspacePath, intent.AfterWorkspace); err != nil {
		return err
	}
	intentPath, err := r.path(worktreeRelocationDirectory, string(intent.ID))
	if err != nil {
		return err
	}
	if err := os.Remove(intentPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove completed relocation intent: %w", err)
	}
	return nil
}

func validateWorktreeRelocationIntent(intent worktreeRelocationIntent) error {
	if intent.ID == "" || intent.BeforeWorktree.ID != intent.ID || intent.AfterWorktree.ID != intent.ID ||
		intent.BeforeWorktree.WorkspaceID != intent.BeforeWorkspace.ID || intent.AfterWorktree.WorkspaceID != intent.AfterWorkspace.ID ||
		intent.BeforeWorkspace.ID != intent.AfterWorkspace.ID || intent.BeforeWorktree.CreatedAt != intent.AfterWorktree.CreatedAt ||
		intent.BeforeWorkspace.CreatedAt != intent.AfterWorkspace.CreatedAt || intent.BeforeWorkspace.RepositoryFingerprint != intent.AfterWorkspace.RepositoryFingerprint {
		return fmt.Errorf("recover Coding worktree relocation %q: transaction identity is invalid", intent.ID)
	}
	if err := codingagent.ValidateWorktree(intent.BeforeWorktree); err != nil {
		return fmt.Errorf("recover Coding worktree relocation %q: invalid prior worktree: %w", intent.ID, err)
	}
	if err := codingagent.ValidateWorktree(intent.AfterWorktree); err != nil {
		return fmt.Errorf("recover Coding worktree relocation %q: invalid target worktree: %w", intent.ID, err)
	}
	if err := codingagent.ValidateWorkspace(intent.BeforeWorkspace); err != nil {
		return fmt.Errorf("recover Coding worktree relocation %q: invalid prior workspace: %w", intent.ID, err)
	}
	if err := codingagent.ValidateWorkspace(intent.AfterWorkspace); err != nil {
		return fmt.Errorf("recover Coding worktree relocation %q: invalid target workspace: %w", intent.ID, err)
	}
	return nil
}
