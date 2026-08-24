package codingtools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eaglc/codepilot/internal/codingagent"
	"github.com/eaglc/codepilot/internal/tool"
)

func TestGitReadToolsExposeOnlyBoundedInspection(t *testing.T) {
	root, commitID := gitReadFixture(t)
	registry, err := NewFactory(Options{}).CreateTools(context.Background(), codingagent.ToolScope{SessionID: "session", WorkspaceID: "workspace", WorktreeID: "worktree", WorktreeRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	logResult := executeGitRead(t, registry, "git_log", `{"limit":5}`)
	if logResult.Status != tool.ResultCompleted || !strings.Contains(logResult.Content[0].Text, commitID) || !strings.Contains(logResult.Content[0].Text, "initial") {
		t.Fatalf("git log result = %#v", logResult)
	}
	branches := executeGitRead(t, registry, "git_branches", `{}`)
	if branches.Status != tool.ResultCompleted || !strings.Contains(branches.Content[0].Text, commitID) {
		t.Fatalf("git branches result = %#v", branches)
	}
	commit := executeGitRead(t, registry, "git_show_commit", `{"commit_id":"`+commitID+`"}`)
	if commit.Status != tool.ResultCompleted || !strings.Contains(commit.Content[0].Text, "initial") || strings.Contains(commit.Content[0].Text, "package main") {
		t.Fatalf("git commit metadata result = %#v", commit)
	}
}

func TestGitReadToolsRejectRevisionSyntaxAndUnboundedLimit(t *testing.T) {
	root, commitID := gitReadFixture(t)
	registry, _ := NewFactory(Options{}).CreateTools(context.Background(), codingagent.ToolScope{SessionID: "session", WorkspaceID: "workspace", WorktreeID: "worktree", WorktreeRoot: root})
	for _, value := range []string{"HEAD", commitID + "^", "--all"} {
		result := executeGitRead(t, registry, "git_show_commit", `{"commit_id":"`+value+`"}`)
		if result.Status != tool.ResultInvalid {
			t.Fatalf("revision %q result = %#v", value, result)
		}
	}
	result := executeGitRead(t, registry, "git_log", `{"limit":51}`)
	if result.Status != tool.ResultInvalid {
		t.Fatalf("unbounded log result = %#v", result)
	}
}

func executeGitRead(t *testing.T, registry *tool.Registry, name, arguments string) tool.Result {
	t.Helper()
	result, err := registry.Execute(context.Background(), tool.Call{ID: "call-" + name, Name: name, Arguments: json.RawMessage(arguments)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func gitReadFixture(t *testing.T) (string, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, arguments := range [][]string{{"init", "--quiet"}, {"config", "user.name", "CodePilot Test"}, {"config", "user.email", "codepilot@example.invalid"}} {
		runGitFixture(t, root, arguments...)
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitFixture(t, root, "add", "main.go")
	runGitFixture(t, root, "commit", "--quiet", "-m", "initial")
	command := exec.Command("git", "-C", root, "rev-parse", "HEAD")
	output, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	return root, strings.TrimSpace(string(output))
}

func runGitFixture(t *testing.T, root string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", arguments, err, output)
	}
}
