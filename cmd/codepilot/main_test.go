package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentsession "github.com/eaglc/codepilot/internal/agent/session"
	"github.com/eaglc/codepilot/internal/codingagent"
	codingfile "github.com/eaglc/codepilot/internal/codingstore/file"
	sessionfile "github.com/eaglc/codepilot/internal/sessionstore/file"
)

func TestRunVersionDoesNotStartApplication(t *testing.T) {
	originalVersion := version
	originalCommit := commit
	originalBuildDate := buildDate
	version = "1.2.3"
	commit = "0123456789abcdef"
	buildDate = "2026-08-24T12:30:00Z"
	t.Cleanup(func() {
		version = originalVersion
		commit = originalCommit
		buildDate = originalBuildDate
	})

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if exitCode := run(context.Background(), []string{"--version"}, strings.NewReader(""), &stdout, &stderr); exitCode != 0 {
		t.Fatalf("version exit code = %d, stderr=%q", exitCode, stderr.String())
	}
	if got, want := stdout.String(), "codepilot 1.2.3 (commit 0123456789abcdef, built 2026-08-24T12:30:00Z)\n"; got != want {
		t.Fatalf("version output = %q", stdout.String())
	}
}

func TestBuildVersionStringSanitizesInjectedMetadata(t *testing.T) {
	originalVersion := version
	originalCommit := commit
	originalBuildDate := buildDate
	version = "\n"
	commit = "abc\ndef"
	buildDate = "\t"
	t.Cleanup(func() {
		version = originalVersion
		commit = originalCommit
		buildDate = originalBuildDate
	})

	if got, want := buildVersionString(), "codepilot dev (commit abc?def, built unknown)"; got != want {
		t.Fatalf("build version = %q, want %q", got, want)
	}
}

func TestRunRejectsUnexpectedArgument(t *testing.T) {
	for _, argument := range []string{"unexpected", "migrate"} {
		t.Run(argument, func(t *testing.T) {
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			if exitCode := run(context.Background(), []string{argument}, strings.NewReader(""), &stdout, &stderr); exitCode != 2 {
				t.Fatalf("argument exit code = %d", exitCode)
			}
			if !strings.Contains(stderr.String(), "unexpected positional arguments") {
				t.Fatalf("argument error = %q", stderr.String())
			}
		})
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

func TestStringListFlagCollectsRepeatedNonEmptyValues(t *testing.T) {
	var values stringListFlag
	if err := values.Set(" private-data "); err != nil {
		t.Fatalf("set first path: %v", err)
	}
	if err := values.Set(".env.local"); err != nil {
		t.Fatalf("set second path: %v", err)
	}
	if err := values.Set("  "); err == nil {
		t.Fatal("empty sensitive path was accepted")
	}
	if got := values.String(); got != "private-data,.env.local" {
		t.Fatalf("flag values = %q", got)
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

func TestDoctorAndExplicitRepairCommands(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	if _, err := codingfile.NewRepository(stateDir); err != nil {
		t.Fatal(err)
	}
	agents, err := sessionfile.NewRepository(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := agents.Create(context.Background(), agentsession.Metadata{ID: "agent-orphan-cli", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}

	var doctorOut bytes.Buffer
	var doctorErr bytes.Buffer
	if code := run(context.Background(), []string{"doctor", "--state-dir", stateDir, "--json"}, strings.NewReader(""), &doctorOut, &doctorErr); code != 0 {
		t.Fatalf("doctor exit=%d stderr=%q", code, doctorErr.String())
	}
	var diagnosis codingagent.ConsistencyReport
	if err := json.Unmarshal(doctorOut.Bytes(), &diagnosis); err != nil {
		t.Fatalf("decode doctor report: %v: %s", err, doctorOut.String())
	}
	if len(diagnosis.Issues) != 1 || diagnosis.Issues[0].Kind != codingagent.IssueOrphanAgentSession {
		t.Fatalf("doctor report = %#v", diagnosis)
	}

	var repairOut bytes.Buffer
	var repairErr bytes.Buffer
	if code := run(context.Background(), []string{"repair", "--state-dir", stateDir, "--json"}, strings.NewReader(""), &repairOut, &repairErr); code != 0 {
		t.Fatalf("repair exit=%d stderr=%q output=%q", code, repairErr.String(), repairOut.String())
	}
	var repair codingagent.ConsistencyRepairReport
	if err := json.Unmarshal(repairOut.Bytes(), &repair); err != nil {
		t.Fatalf("decode repair report: %v", err)
	}
	if len(repair.Actions) != 1 || !repair.Actions[0].Completed || len(repair.After.Issues) != 0 {
		t.Fatalf("repair report = %#v", repair)
	}
	metadata, err := agents.List(context.Background())
	if err != nil || len(metadata) != 1 || !metadata[0].Archived {
		t.Fatalf("archived metadata = %#v, %v", metadata, err)
	}
}
