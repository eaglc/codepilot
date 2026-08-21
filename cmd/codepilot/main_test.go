package main

import (
	"bytes"
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunVersionDoesNotStartApplication(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if exitCode := run(context.Background(), []string{"--version"}, strings.NewReader(""), &stdout, &stderr); exitCode != 0 {
		t.Fatalf("version exit code = %d, stderr=%q", exitCode, stderr.String())
	}
	if !strings.Contains(stdout.String(), "codepilot "+version) {
		t.Fatalf("version output = %q", stdout.String())
	}
}

func TestRunRejectsUnexpectedArgument(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if exitCode := run(context.Background(), []string{"unexpected"}, strings.NewReader(""), &stdout, &stderr); exitCode != 2 {
		t.Fatalf("argument exit code = %d", exitCode)
	}
	if !strings.Contains(stderr.String(), "unexpected positional arguments") {
		t.Fatalf("argument error = %q", stderr.String())
	}
}

func TestRunHelpExitsSuccessfully(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if exitCode := run(context.Background(), []string{"--help"}, strings.NewReader(""), &stdout, &stderr); exitCode != 0 {
		t.Fatalf("help exit code = %d", exitCode)
	}
	if !strings.Contains(stderr.String(), "Usage of codepilot") {
		t.Fatalf("help output = %q", stderr.String())
	}
}

func TestRunDeclinedWorkspaceTrustExitsWithoutStartingTUI(t *testing.T) {
	worktree := t.TempDir()
	command := exec.Command("git", "init", "--quiet")
	command.Dir = worktree
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("initialize Git fixture: %v: %s", err, output)
	}
	root := t.TempDir()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run(context.Background(), []string{
		"--workspace", worktree,
		"--config-dir", filepath.Join(root, "config"),
		"--state-dir", filepath.Join(root, "state"),
	}, strings.NewReader("n\n"), &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("declined trust exit code = %d, stderr=%q", exitCode, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Trust this Git worktree?") || !strings.Contains(stdout.String(), "no session was created") {
		t.Fatalf("trust output = %q", stdout.String())
	}
}
