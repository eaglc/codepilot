package codingtools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/eaglc/codepilot/internal/codingagent"
	"github.com/eaglc/codepilot/internal/llm"
	"github.com/eaglc/codepilot/internal/tool"
)

type memoryArtifactStore struct {
	artifacts []codingagent.Artifact
}

func (s *memoryArtifactStore) SaveArtifact(_ context.Context, artifact codingagent.Artifact) (codingagent.ArtifactRef, error) {
	s.artifacts = append(s.artifacts, codingagent.Artifact{MediaType: artifact.MediaType, Data: append([]byte(nil), artifact.Data...)})
	return codingagent.ArtifactRef{ID: fmt.Sprintf("sha256:test-%d", len(s.artifacts)), MediaType: artifact.MediaType, Size: int64(len(artifact.Data))}, nil
}

func (s *memoryArtifactStore) LoadArtifact(_ context.Context, reference codingagent.ArtifactRef) (codingagent.Artifact, error) {
	var index int
	if _, err := fmt.Sscanf(reference.ID, "sha256:test-%d", &index); err != nil || index <= 0 || index > len(s.artifacts) {
		return codingagent.Artifact{}, fmt.Errorf("artifact not found")
	}
	artifact := s.artifacts[index-1]
	if int64(len(artifact.Data)) != reference.Size || artifact.MediaType != reference.MediaType {
		return codingagent.Artifact{}, fmt.Errorf("artifact reference mismatch")
	}
	return codingagent.Artifact{MediaType: artifact.MediaType, Data: append([]byte(nil), artifact.Data...)}, nil
}

func TestDetectCheckPlansUsesOnlyRecognizedProjectMarkers(t *testing.T) {
	root := t.TempDir()
	for name, content := range map[string]string{
		"go.mod": "module example.test/project\n", "pyproject.toml": "[project]\nname='test'\n",
		"package.json": `{"scripts":{"test":"arbitrary repository command","build":"another command","postinstall":"must-not-be-a-plan"}}`,
	} {
		if err := os.WriteFile(root+string(os.PathSeparator)+name, []byte(content), 0o600); err != nil {
			t.Fatalf("write marker %s: %v", name, err)
		}
	}
	if err := os.Mkdir(root+string(os.PathSeparator)+"tests", 0o700); err != nil {
		t.Fatalf("create tests directory: %v", err)
	}
	plans := detectCheckPlans(root)
	ids := make([]string, len(plans))
	for index, plan := range plans {
		ids[index] = plan.ID
		command := strings.Join(append([]string{plan.Executable}, plan.Arguments...), " ")
		if strings.Contains(command, "arbitrary repository command") || strings.Contains(command, "another command") {
			t.Fatalf("repository script text entered trusted plan: %#v", plan)
		}
	}
	want := []string{"go.build", "go.test", "go.vet", "node.build", "node.test", "python.compile", "python.test"}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Fatalf("detected plan IDs = %v, want %v", ids, want)
	}
}

func TestRunChecksAcceptsOnlyPlanIDRequiresApprovalAndStoresLargeOutput(t *testing.T) {
	artifacts := &memoryArtifactStore{}
	plan := checkPlan{
		ID: "test.helper", Language: "test", Label: "Helper check", Executable: os.Args[0],
		Arguments: []string{"-test.run=^TestCheckHelperProcess$", "--", "large"},
	}
	executable := &runChecksTool{
		root: t.TempDir(), plans: []checkPlan{plan},
		displayLimit: 64, outputLimit: 4096, timeout: 10 * time.Second, artifacts: artifacts,
	}
	controlled := withPermissionBoundary(executable, codingagent.PermissionAsk, nil)
	call := tool.Call{ID: "call-check", Name: "run_checks", Arguments: json.RawMessage(`{"plan_id":"test.helper"}`), IdempotencyKey: "turn:call-check"}
	pending, err := controlled.Execute(context.Background(), call, nil)
	if err != nil || pending.Status != tool.ResultInterrupted || pending.Interrupt == nil {
		t.Fatalf("request check approval: result=%#v err=%v", pending, err)
	}
	result, err := controlled.(tool.ResumableTool).Resume(context.Background(), call, *pending.Interrupt, tool.Result{
		Status: tool.ResultCompleted, Content: []llm.Content{{Type: llm.ContentText, Text: "approved"}},
	}, nil)
	if err != nil || result.Status != tool.ResultCompleted {
		t.Fatalf("run approved check: result=%#v err=%v", result, err)
	}
	if len(artifacts.artifacts) != 1 || len(artifacts.artifacts[0].Data) <= executable.displayLimit || !strings.Contains(result.Content[0].Text, "sha256:test-1") {
		t.Fatalf("large output artifact/result: artifacts=%#v result=%#v", artifacts.artifacts, result)
	}
	denied, err := controlled.Execute(context.Background(), tool.Call{ID: "bad", Name: "run_checks", Arguments: json.RawMessage(`{"plan_id":"test.helper","command":"rm -rf ."}`)}, nil)
	if err != nil || denied.Status != tool.ResultInvalid {
		t.Fatalf("model-supplied command was not rejected: result=%#v err=%v", denied, err)
	}
}

func TestRunChecksReadOnlyModeAndUnknownPlanAreDenied(t *testing.T) {
	executable := &runChecksTool{plans: []checkPlan{{ID: "go.test"}}}
	controlled := withPermissionBoundary(executable, codingagent.PermissionReadOnly, nil)
	readOnly, err := controlled.Execute(context.Background(), tool.Call{Arguments: json.RawMessage(`{"plan_id":"go.test"}`)}, nil)
	if err != nil || readOnly.Status != tool.ResultDenied {
		t.Fatalf("read-only check result=%#v err=%v", readOnly, err)
	}
	unknown, err := controlled.Execute(context.Background(), tool.Call{Arguments: json.RawMessage(`{"plan_id":"unknown"}`)}, nil)
	if err != nil || unknown.Status != tool.ResultDenied {
		t.Fatalf("unknown check result=%#v err=%v", unknown, err)
	}
}

func TestRunChecksSessionGrantSkipsRepeatedApproval(t *testing.T) {
	now := time.Now().UTC()
	plan := checkPlan{
		ID: "test.helper", Language: "test", Label: "Helper check", Executable: os.Args[0],
		Arguments: []string{"-test.run=^TestCheckHelperProcess$", "--", "small"},
	}
	executable := &runChecksTool{
		root: t.TempDir(), plans: []checkPlan{plan, {ID: "test.other", Label: "Other check", Executable: os.Args[0]}},
		displayLimit: 1024, outputLimit: 4096, timeout: 10 * time.Second,
	}
	grants := []codingagent.PermissionGrant{{
		ID: "grant-check", Scope: codingagent.PermissionGrantSession, ToolName: "run_checks", Action: codingagent.PermissionExecutePlanAction(plan.ID),
		SourceTurnID: "turn", SourceInterruptID: "approval", CreatedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour),
	}}
	controlled := withPermissionBoundary(executable, codingagent.PermissionAsk, grants)
	result, err := controlled.Execute(context.Background(), tool.Call{ID: "call", Name: "run_checks", Arguments: json.RawMessage(`{"plan_id":"test.helper"}`)}, nil)
	if err != nil || result.Status != tool.ResultCompleted {
		t.Fatalf("granted check: result=%#v err=%v", result, err)
	}
	result, err = controlled.Execute(context.Background(), tool.Call{ID: "other", Name: "run_checks", Arguments: json.RawMessage(`{"plan_id":"test.other"}`)}, nil)
	if err != nil || result.Status != tool.ResultInterrupted {
		t.Fatalf("different check plan reused grant: result=%#v err=%v", result, err)
	}
}

func TestRunChecksTimeoutCancelsProcessGroup(t *testing.T) {
	plan := checkPlan{
		ID: "test.timeout", Language: "test", Label: "Timeout check", Executable: os.Args[0],
		Arguments: []string{"-test.run=^TestCheckHelperProcess$", "--", "sleep"},
	}
	executable := &runChecksTool{root: t.TempDir(), plans: []checkPlan{plan}, displayLimit: 1024, outputLimit: 4096, timeout: 100 * time.Millisecond}
	started := time.Now()
	result, err := executable.run(context.Background(), plan)
	if err != nil || result.Status != tool.ResultFailed || !strings.Contains(result.Content[0].Text, "timed out") {
		t.Fatalf("timeout check: result=%#v err=%v", result, err)
	}
	if time.Since(started) > 5*time.Second {
		t.Fatalf("process group cancellation took too long: %s", time.Since(started))
	}
}

func TestRunChecksRedactsSecretsBeforeStoringLargeOutput(t *testing.T) {
	artifacts := &memoryArtifactStore{}
	security, err := codingagent.NewSecurityPolicy(nil)
	if err != nil {
		t.Fatalf("create security policy: %v", err)
	}
	plan := checkPlan{
		ID: "test.secret-output", Language: "test", Label: "Secret output", Executable: os.Args[0],
		Arguments: []string{"-test.run=^TestCheckHelperProcess$", "--", "secret-large"},
	}
	executable := &runChecksTool{
		root: t.TempDir(), plans: []checkPlan{plan}, security: security,
		displayLimit: 64, outputLimit: 4096, timeout: 10 * time.Second, artifacts: artifacts,
	}
	result, err := executable.run(context.Background(), plan)
	if err != nil || result.Status != tool.ResultCompleted || len(artifacts.artifacts) != 1 {
		t.Fatalf("run secret-output check: result=%#v artifacts=%#v err=%v", result, artifacts.artifacts, err)
	}
	encodedResult, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("encode result: %v", err)
	}
	for name, value := range map[string][]byte{"result": encodedResult, "artifact": artifacts.artifacts[0].Data} {
		if strings.Contains(string(value), "top-secret-value") || !strings.Contains(string(value), codingagent.RedactedValue) {
			t.Fatalf("%s was not redacted before persistence: %q", name, value)
		}
	}
}

func TestTrustedCheckEnvironmentExcludesUnlistedSecrets(t *testing.T) {
	t.Setenv("CODEPILOT_SECRET_SHOULD_NOT_PASS", "top-secret")
	for _, value := range trustedCheckEnvironment() {
		if strings.Contains(value, "CODEPILOT_SECRET_SHOULD_NOT_PASS") || strings.Contains(value, "top-secret") {
			t.Fatalf("unlisted environment variable entered child process: %q", value)
		}
	}
}

func TestCheckHelperProcess(t *testing.T) {
	separator := -1
	for index, argument := range os.Args {
		if argument == "--" {
			separator = index
			break
		}
	}
	if separator < 0 || separator+1 >= len(os.Args) {
		return
	}
	switch os.Args[separator+1] {
	case "large":
		fmt.Print(strings.Repeat("check-output\n", 64))
	case "small":
		fmt.Print("ok\n")
	case "sleep":
		time.Sleep(30 * time.Second)
	case "secret-large":
		fmt.Print(strings.Repeat("API_KEY=top-secret-value\n", 64))
	}
}
