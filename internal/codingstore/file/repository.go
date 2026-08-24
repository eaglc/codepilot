// Package file persists Coding Agent product metadata separately from Agent journals.
package file

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/eaglc/codepilot/internal/codingagent"
)

const formatVersion = 1

var validID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// Repository stores versioned product sessions, workspaces, and worktrees.
type Repository struct {
	root string
	mu   sync.RWMutex
}

type envelope[T any] struct {
	Version int `json:"version"`
	Value   T   `json:"value"`
}

// NewRepository opens or creates the Coding product metadata root.
func NewRepository(root string) (*Repository, error) {
	absolute, err := repositoryRoot(root)
	if err != nil {
		return nil, err
	}
	for _, directory := range []string{"coding-sessions", "coding-workspaces", "coding-worktrees", "coding-artifacts", filepath.Join("coding-transactions", "session-create"), worktreeRelocationDirectory} {
		if err := os.MkdirAll(filepath.Join(absolute, directory), 0o700); err != nil {
			return nil, fmt.Errorf("create Coding file repository: create %s: %w", directory, err)
		}
	}
	repository := &Repository{root: absolute}
	if err := repository.recoverWorktreeRelocations(context.Background()); err != nil {
		return nil, fmt.Errorf("create Coding file repository: recover worktree relocation: %w", err)
	}
	return repository, nil
}

// OpenRepository opens existing product metadata without creating directories.
// It is intended for read-only consistency diagnostics.
func OpenRepository(root string) (*Repository, error) {
	absolute, err := repositoryRoot(root)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return nil, fmt.Errorf("open Coding file repository: %w", err)
	}
	if !info.IsDir() {
		return nil, errors.New("open Coding file repository: root is not a directory")
	}
	return &Repository{root: absolute}, nil
}

func repositoryRoot(root string) (string, error) {
	if root == "" {
		return "", errors.New("create Coding file repository: root is required")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("create Coding file repository: resolve root: %w", err)
	}
	return filepath.Clean(absolute), nil
}

// SaveWorkspace creates or updates mutable workspace metadata while preserving identity.
func (r *Repository) SaveWorkspace(ctx context.Context, value codingagent.Workspace) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := codingagent.ValidateWorkspace(value); err != nil {
		return fmt.Errorf("save Coding workspace: %w", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	path, err := r.path("coding-workspaces", string(value.ID))
	if err != nil {
		return err
	}
	previous, found, err := readEnvelope[codingagent.Workspace](path)
	if err != nil {
		return fmt.Errorf("save Coding workspace %q: %w", value.ID, err)
	}
	if found && (previous.ID != value.ID || !previous.CreatedAt.Equal(value.CreatedAt) || (previous.RepositoryFingerprint != "" && previous.RepositoryFingerprint != value.RepositoryFingerprint)) {
		return fmt.Errorf("save Coding workspace %q: immutable identity changed", value.ID)
	}
	return writeEnvelope(path, value)
}

// LoadWorkspace loads one durable logical workspace.
func (r *Repository) LoadWorkspace(ctx context.Context, id codingagent.WorkspaceID) (codingagent.Workspace, error) {
	if err := ctx.Err(); err != nil {
		return codingagent.Workspace{}, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.loadWorkspaceLocked(id)
}

// ListWorkspaces returns stable workspace ordering by ID.
func (r *Repository) ListWorkspaces(ctx context.Context) ([]codingagent.Workspace, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	values, err := listEnvelopes[codingagent.Workspace](ctx, filepath.Join(r.root, "coding-workspaces"))
	if err != nil {
		return nil, fmt.Errorf("list Coding workspaces: %w", err)
	}
	sort.Slice(values, func(left, right int) bool { return values[left].ID < values[right].ID })
	return values, nil
}

// SaveWorktree creates or updates last-use metadata for a concrete checkout.
func (r *Repository) SaveWorktree(ctx context.Context, value codingagent.Worktree) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := codingagent.ValidateWorktree(value); err != nil {
		return fmt.Errorf("save Coding worktree: %w", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := r.loadWorkspaceLocked(value.WorkspaceID); err != nil {
		return fmt.Errorf("save Coding worktree %q: %w", value.ID, err)
	}
	path, err := r.path("coding-worktrees", string(value.ID))
	if err != nil {
		return err
	}
	previous, found, err := readEnvelope[codingagent.Worktree](path)
	if err != nil {
		return fmt.Errorf("save Coding worktree %q: %w", value.ID, err)
	}
	if found && (previous.ID != value.ID || previous.WorkspaceID != value.WorkspaceID || previous.Root != value.Root || previous.GitDir != value.GitDir || !previous.CreatedAt.Equal(value.CreatedAt)) {
		return fmt.Errorf("save Coding worktree %q: immutable identity changed", value.ID)
	}
	return writeEnvelope(path, value)
}

// RelocateWorktree is the only repository operation allowed to change a
// checkout root. expectedRoot makes the update fail if another process or
// repair already changed the binding.
func (r *Repository) RelocateWorktree(ctx context.Context, id codingagent.WorktreeID, expectedRoot, root, gitDir, gitCommonDir string, lastUsedAt time.Time) (codingagent.Worktree, error) {
	if err := ctx.Err(); err != nil {
		return codingagent.Worktree{}, err
	}
	if id == "" || expectedRoot == "" || root == "" || gitDir == "" || gitCommonDir == "" || lastUsedAt.IsZero() {
		return codingagent.Worktree{}, errors.New("relocate Coding worktree: identity, locations, and timestamp are required")
	}
	root, gitDir, gitCommonDir = filepath.Clean(root), filepath.Clean(gitDir), filepath.Clean(gitCommonDir)
	if !filepath.IsAbs(root) || !filepath.IsAbs(gitDir) || !filepath.IsAbs(gitCommonDir) {
		return codingagent.Worktree{}, errors.New("relocate Coding worktree: locations must be absolute")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	value, err := r.loadWorktreeLocked(id)
	if err != nil {
		return codingagent.Worktree{}, err
	}
	workspace, err := r.loadWorkspaceLocked(value.WorkspaceID)
	if err != nil {
		return codingagent.Worktree{}, err
	}
	if value.Root != expectedRoot && !codingagent.SameLocation(value.Root, root) {
		return codingagent.Worktree{}, fmt.Errorf("relocate Coding worktree %q: stored location changed", id)
	}
	all, err := listEnvelopes[codingagent.Worktree](ctx, filepath.Join(r.root, "coding-worktrees"))
	if err != nil {
		return codingagent.Worktree{}, fmt.Errorf("relocate Coding worktree %q: list locations: %w", id, err)
	}
	for _, other := range all {
		if other.ID != id && codingagent.SameLocation(other.Root, root) {
			return codingagent.Worktree{}, fmt.Errorf("relocate Coding worktree %q: target location is already registered", id)
		}
	}
	beforeWorktree := value
	beforeWorkspace := workspace
	value.Root, value.GitDir, value.LastUsedAt = root, gitDir, lastUsedAt
	if err := codingagent.ValidateWorktree(value); err != nil {
		return codingagent.Worktree{}, fmt.Errorf("relocate Coding worktree: %w", err)
	}
	workspace.GitCommonDir, workspace.UpdatedAt = gitCommonDir, lastUsedAt
	if err := codingagent.ValidateWorkspace(workspace); err != nil {
		return codingagent.Worktree{}, fmt.Errorf("relocate Coding workspace: %w", err)
	}
	intent := worktreeRelocationIntent{
		ID: id, BeforeWorktree: beforeWorktree, AfterWorktree: value,
		BeforeWorkspace: beforeWorkspace, AfterWorkspace: workspace,
	}
	intentPath, err := r.path(worktreeRelocationDirectory, string(id))
	if err != nil {
		return codingagent.Worktree{}, err
	}
	if err := writeEnvelope(intentPath, intent); err != nil {
		return codingagent.Worktree{}, fmt.Errorf("relocate Coding worktree %q: persist transaction intent: %w", id, err)
	}
	if err := r.commitWorktreeRelocationLocked(intent); err != nil {
		return codingagent.Worktree{}, fmt.Errorf("relocate Coding worktree %q: %w", id, err)
	}
	return value, nil
}

// LoadWorktree loads one durable concrete checkout.
func (r *Repository) LoadWorktree(ctx context.Context, id codingagent.WorktreeID) (codingagent.Worktree, error) {
	if err := ctx.Err(); err != nil {
		return codingagent.Worktree{}, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.loadWorktreeLocked(id)
}

// ListWorktrees returns stable worktree ordering for one workspace.
func (r *Repository) ListWorktrees(ctx context.Context, workspaceID codingagent.WorkspaceID) ([]codingagent.Worktree, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	values, err := listEnvelopes[codingagent.Worktree](ctx, filepath.Join(r.root, "coding-worktrees"))
	if err != nil {
		return nil, fmt.Errorf("list Coding worktrees: %w", err)
	}
	filtered := values[:0]
	for _, value := range values {
		if value.WorkspaceID == workspaceID {
			filtered = append(filtered, value)
		}
	}
	sort.Slice(filtered, func(left, right int) bool { return filtered[left].ID < filtered[right].ID })
	return filtered, nil
}

// CreateSession writes a new immutable product-to-Agent/worktree binding.
func (r *Repository) CreateSession(ctx context.Context, value codingagent.Session) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := codingagent.ValidateSession(value); err != nil {
		return fmt.Errorf("create Coding session: %w", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	worktree, err := r.loadWorktreeLocked(value.WorktreeID)
	if err != nil {
		return fmt.Errorf("create Coding session %q: %w", value.ID, err)
	}
	if worktree.WorkspaceID != value.WorkspaceID {
		return fmt.Errorf("create Coding session %q: worktree belongs to another workspace", value.ID)
	}
	path, err := r.path("coding-sessions", string(value.ID))
	if err != nil {
		return err
	}
	if _, found, err := readEnvelope[codingagent.Session](path); err != nil {
		return fmt.Errorf("create Coding session %q: %w", value.ID, err)
	} else if found {
		return fmt.Errorf("create Coding session %q: already exists", value.ID)
	}
	return writeEnvelope(path, value)
}

// LoadSession loads one durable Coding product session.
func (r *Repository) LoadSession(ctx context.Context, id codingagent.SessionID) (codingagent.Session, error) {
	if err := ctx.Err(); err != nil {
		return codingagent.Session{}, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.loadSessionLocked(id)
}

// ListSessions returns sessions by most recently updated first.
func (r *Repository) ListSessions(ctx context.Context) ([]codingagent.Session, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	values, err := listEnvelopes[codingagent.Session](ctx, filepath.Join(r.root, "coding-sessions"))
	if err != nil {
		return nil, fmt.Errorf("list Coding sessions: %w", err)
	}
	sort.Slice(values, func(left, right int) bool {
		if values[left].UpdatedAt.Equal(values[right].UpdatedAt) {
			return values[left].ID < values[right].ID
		}
		return values[left].UpdatedAt.After(values[right].UpdatedAt)
	})
	return values, nil
}

// SaveSession updates mutable product session metadata without changing its durable binding.
func (r *Repository) SaveSession(ctx context.Context, value codingagent.Session) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := codingagent.ValidateSession(value); err != nil {
		return fmt.Errorf("save Coding session: %w", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	previous, err := r.loadSessionLocked(value.ID)
	if err != nil {
		return err
	}
	if previous.AgentSessionID != value.AgentSessionID || previous.WorkspaceID != value.WorkspaceID || previous.WorktreeID != value.WorktreeID || !previous.CreatedAt.Equal(value.CreatedAt) {
		return fmt.Errorf("save Coding session %q: immutable binding changed", value.ID)
	}
	path, err := r.path("coding-sessions", string(value.ID))
	if err != nil {
		return err
	}
	return writeEnvelope(path, value)
}

// BeginSessionCreation writes the recoverable intent before either session repository is changed.
func (r *Repository) BeginSessionCreation(ctx context.Context, intent codingagent.SessionCreationIntent) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := codingagent.ValidateSessionCreationIntent(intent); err != nil {
		return fmt.Errorf("begin Coding session creation: %w", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	path, err := r.path(filepath.Join("coding-transactions", "session-create"), string(intent.ID))
	if err != nil {
		return err
	}
	previous, found, err := readEnvelope[codingagent.SessionCreationIntent](path)
	if err != nil {
		return fmt.Errorf("begin Coding session creation %q: %w", intent.ID, err)
	}
	if found {
		if !reflect.DeepEqual(previous.Session, intent.Session) || !previous.CreatedAt.Equal(intent.CreatedAt) {
			return fmt.Errorf("begin Coding session creation %q: immutable intent changed", intent.ID)
		}
		return nil
	}
	return writeEnvelope(path, intent)
}

// CompleteSessionCreation marks a durable creation transaction reconciled.
func (r *Repository) CompleteSessionCreation(ctx context.Context, id codingagent.SessionCreationIntentID, completedAt time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if id == "" || completedAt.IsZero() {
		return errors.New("complete Coding session creation: id and timestamp are required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	path, err := r.path(filepath.Join("coding-transactions", "session-create"), string(id))
	if err != nil {
		return err
	}
	intent, found, err := readEnvelope[codingagent.SessionCreationIntent](path)
	if err != nil {
		return fmt.Errorf("complete Coding session creation %q: %w", id, err)
	}
	if !found {
		return fmt.Errorf("complete Coding session creation %q: not found", id)
	}
	if intent.Status == codingagent.SessionCreationCompleted {
		return nil
	}
	intent.Status = codingagent.SessionCreationCompleted
	intent.UpdatedAt = completedAt
	intent.CompletedAt = completedAt
	return writeEnvelope(path, intent)
}

// ListSessionCreationIntents returns stable creation transaction ordering.
func (r *Repository) ListSessionCreationIntents(ctx context.Context) ([]codingagent.SessionCreationIntent, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	values, err := listEnvelopes[codingagent.SessionCreationIntent](ctx, filepath.Join(r.root, "coding-transactions", "session-create"))
	if err != nil {
		return nil, fmt.Errorf("list Coding session creation intents: %w", err)
	}
	sort.Slice(values, func(left, right int) bool { return values[left].ID < values[right].ID })
	return values, nil
}

func (r *Repository) loadWorkspaceLocked(id codingagent.WorkspaceID) (codingagent.Workspace, error) {
	path, err := r.path("coding-workspaces", string(id))
	if err != nil {
		return codingagent.Workspace{}, err
	}
	value, found, err := readEnvelope[codingagent.Workspace](path)
	if err != nil {
		return codingagent.Workspace{}, fmt.Errorf("load Coding workspace %q: %w", id, err)
	}
	if !found {
		return codingagent.Workspace{}, fmt.Errorf("Coding workspace %q not found", id)
	}
	return value, nil
}

func (r *Repository) loadWorktreeLocked(id codingagent.WorktreeID) (codingagent.Worktree, error) {
	path, err := r.path("coding-worktrees", string(id))
	if err != nil {
		return codingagent.Worktree{}, err
	}
	value, found, err := readEnvelope[codingagent.Worktree](path)
	if err != nil {
		return codingagent.Worktree{}, fmt.Errorf("load Coding worktree %q: %w", id, err)
	}
	if !found {
		return codingagent.Worktree{}, fmt.Errorf("load Coding worktree %q: %w", id, codingagent.ErrWorktreeNotFound)
	}
	return value, nil
}

func (r *Repository) loadSessionLocked(id codingagent.SessionID) (codingagent.Session, error) {
	path, err := r.path("coding-sessions", string(id))
	if err != nil {
		return codingagent.Session{}, err
	}
	value, found, err := readEnvelope[codingagent.Session](path)
	if err != nil {
		return codingagent.Session{}, fmt.Errorf("load Coding session %q: %w", id, err)
	}
	if !found {
		return codingagent.Session{}, fmt.Errorf("load Coding session %q: %w", id, codingagent.ErrSessionNotFound)
	}
	return value, nil
}

func (r *Repository) path(directory, id string) (string, error) {
	if !validID.MatchString(id) {
		return "", fmt.Errorf("Coding metadata id %q is invalid", id)
	}
	return filepath.Join(r.root, directory, id+".json"), nil
}

func readEnvelope[T any](path string) (T, bool, error) {
	var zero T
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return zero, false, nil
	}
	if err != nil {
		return zero, false, err
	}
	defer file.Close()
	var stored envelope[T]
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&stored); err != nil {
		return zero, false, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return zero, false, errors.New("multiple JSON values are not allowed")
	}
	if stored.Version != formatVersion {
		return zero, false, fmt.Errorf("unsupported format version %d", stored.Version)
	}
	return stored.Value, true, nil
}

func listEnvelopes[T any](ctx context.Context, directory string) ([]T, error) {
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	values := make([]T, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		value, found, err := readEnvelope[T](filepath.Join(directory, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", entry.Name(), err)
		}
		if found {
			values = append(values, value)
		}
	}
	return values, nil
}

func writeEnvelope[T any](path string, value T) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".codepilot-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	encoder := json.NewEncoder(temporary)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(envelope[T]{Version: formatVersion, Value: value}); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}
