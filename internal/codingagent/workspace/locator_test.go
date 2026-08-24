package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestResolveWorktreeReturnsStableHistoryFingerprint(t *testing.T) {
	root := committedRepository(t)
	nested := filepath.Join(root, "nested")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	first, err := ResolveWorktree(context.Background(), nested)
	if err != nil {
		t.Fatal(err)
	}
	if !locatorSamePath(first.Root, root) || first.GitDir == "" || first.GitCommonDir == "" || first.RepositoryFingerprint == "" {
		t.Fatalf("resolved worktree = %#v", first)
	}
	clone := filepath.Join(t.TempDir(), "clone")
	runGit(t, "", "clone", "--quiet", root, clone)
	second, err := ResolveWorktree(context.Background(), clone)
	if err != nil {
		t.Fatal(err)
	}
	if second.RepositoryFingerprint != first.RepositoryFingerprint || locatorSamePath(second.GitCommonDir, first.GitCommonDir) {
		t.Fatalf("clone identity mismatch: first=%#v second=%#v", first, second)
	}
}

func TestResolveEmptyRepositoryDoesNotInventRelocationIdentity(t *testing.T) {
	root := filepath.Join(t.TempDir(), "empty")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "init", "--quiet")
	resolved, err := ResolveWorktree(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.RepositoryFingerprint != "" {
		t.Fatalf("empty repository fingerprint = %q", resolved.RepositoryFingerprint)
	}
}

func committedRepository(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "init", "--quiet")
	runGit(t, root, "config", "user.name", "CodePilot Test")
	runGit(t, root, "config", "user.email", "codepilot@example.invalid")
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "main.go")
	runGit(t, root, "commit", "--quiet", "-m", "initial")
	return filepath.Clean(root)
}

func runGit(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	values := arguments
	if directory != "" {
		values = append([]string{"-C", directory}, arguments...)
	}
	command := exec.Command("git", values...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", arguments, err, output)
	}
}

func locatorSamePath(left, right string) bool {
	left, right = locatorCanonicalPath(left), locatorCanonicalPath(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func locatorCanonicalPath(value string) string {
	value = filepath.Clean(value)
	if resolved, err := filepath.EvalSymlinks(value); err == nil {
		return filepath.Clean(resolved)
	}
	return value
}
