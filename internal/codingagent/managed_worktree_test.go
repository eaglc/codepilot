package codingagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestGitManagedWorktreeCapturesAndIntegratesExactlyOnce(t *testing.T) {
	ctx := context.Background()
	root := committedManagedTestRepository(t)
	head := managedTestGit(t, root, "rev-parse", "HEAD")
	artifacts := &managedMemoryArtifacts{values: make(map[string]Artifact)}
	manager, err := NewGitManagedWorktreeManager(filepath.Join(t.TempDir(), "managed"), artifacts)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := manager.Prepare(ctx, root, ManagedWorktree{ID: "managed-child-1", BaselineCommit: head, Status: ManagedWorktreePending}, nil)
	if err != nil || prepared.Status != ManagedWorktreeReady || samePath(prepared.Root, root) {
		t.Fatalf("prepared managed worktree = %#v, %v", prepared, err)
	}
	if err := os.WriteFile(filepath.Join(prepared.Root, "a.txt"), []byte("agent-a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	child := ChildAgent{ID: "child-1", WorkflowID: "workflow-1", NodeID: "implement-a", Task: AgentTask{WritePaths: []string{"a.txt"}}}
	captured, err := manager.Capture(ctx, prepared, child, []string{"isolated check passed"})
	if err != nil || captured.Status != ManagedWorktreeCaptured || captured.ChangeSet == nil || len(captured.ChangeSet.Files) != 1 {
		t.Fatalf("captured managed worktree = %#v, %v", captured, err)
	}
	active, _ := os.ReadFile(filepath.Join(root, "a.txt"))
	if string(active) != "base-a\n" {
		t.Fatalf("isolated child modified active worktree: %q", active)
	}
	preview, err := manager.PreviewIntegration(ctx, root, *captured.ChangeSet)
	if err != nil || preview.AlreadyApplied || !strings.Contains(preview.Patch, "+agent-a") {
		t.Fatalf("integration preview = %#v, %v", preview, err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("user-a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ApplyIntegration(ctx, root, *captured.ChangeSet); err == nil || !strings.Contains(err.Error(), "drifted") {
		t.Fatalf("target drift did not block integration: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("base-a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	first, err := manager.ApplyIntegration(ctx, root, *captured.ChangeSet)
	if err != nil || first.AlreadyApplied {
		t.Fatalf("first integration = %#v, %v", first, err)
	}
	second, err := manager.ApplyIntegration(ctx, root, *captured.ChangeSet)
	if err != nil || !second.AlreadyApplied {
		t.Fatalf("idempotent integration = %#v, %v", second, err)
	}
	captured.Status = ManagedWorktreeCleanupPending
	change := *captured.ChangeSet
	change.IntegratedAt = time.Now().UTC()
	captured.ChangeSet = &change
	if err := manager.Cleanup(ctx, root, captured); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(captured.Root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("managed worktree remains after cleanup: %v", err)
	}
}

func TestGitManagedWorktreeRejectsOutOfScopeChangeAndRetainsFiles(t *testing.T) {
	ctx := context.Background()
	root := committedManagedTestRepository(t)
	head := managedTestGit(t, root, "rev-parse", "HEAD")
	artifacts := &managedMemoryArtifacts{values: make(map[string]Artifact)}
	manager, _ := NewGitManagedWorktreeManager(filepath.Join(t.TempDir(), "managed"), artifacts)
	prepared, err := manager.Prepare(ctx, root, ManagedWorktree{ID: "managed-child-scope", BaselineCommit: head, Status: ManagedWorktreePending}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prepared.Root, "b.txt"), []byte("escaped\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	child := ChildAgent{ID: "child-scope", WorkflowID: "workflow-1", NodeID: "implement-a", Task: AgentTask{WritePaths: []string{"a.txt"}}}
	if _, err := manager.Capture(ctx, prepared, child, nil); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("out-of-scope capture = %v", err)
	}
	if content, err := os.ReadFile(filepath.Join(prepared.Root, "b.txt")); err != nil || string(content) != "escaped\n" {
		t.Fatalf("failed artifact generation lost isolated work: %q, %v", content, err)
	}
}

type managedMemoryArtifacts struct {
	mu     sync.Mutex
	values map[string]Artifact
}

func (s *managedMemoryArtifacts) SaveArtifact(_ context.Context, artifact Artifact) (ArtifactRef, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	digest := sha256.Sum256(artifact.Data)
	id := "sha256:" + hex.EncodeToString(digest[:])
	s.values[id] = Artifact{MediaType: artifact.MediaType, Data: append([]byte(nil), artifact.Data...)}
	return ArtifactRef{ID: id, MediaType: artifact.MediaType, Size: int64(len(artifact.Data))}, nil
}

func (s *managedMemoryArtifacts) LoadArtifact(_ context.Context, ref ArtifactRef) (Artifact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, found := s.values[ref.ID]
	if !found {
		return Artifact{}, errors.New("artifact not found")
	}
	return Artifact{MediaType: value.MediaType, Data: append([]byte(nil), value.Data...)}, nil
}

func committedManagedTestRepository(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("base-a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "b.txt"), []byte("base-b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	managedTestGit(t, root, "init", "--quiet")
	managedTestGit(t, root, "config", "user.name", "CodePilot Test")
	managedTestGit(t, root, "config", "user.email", "test@example.invalid")
	managedTestGit(t, root, "add", "a.txt", "b.txt")
	managedTestGit(t, root, "commit", "--quiet", "-m", "initial")
	return root
}

func managedTestGit(t *testing.T, root string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", arguments, err, output)
	}
	return strings.TrimSpace(string(output))
}
