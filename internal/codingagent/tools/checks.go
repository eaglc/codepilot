package codingtools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/eaglc/codepilot/internal/codingagent"
	"github.com/eaglc/codepilot/internal/llm"
	"github.com/eaglc/codepilot/internal/tool"
)

type checkPlan struct {
	ID         string
	Language   string
	Label      string
	Executable string
	Arguments  []string
}

type checkApprovalPayload struct {
	Kind    string `json:"kind"`
	Version int    `json:"version"`
	PlanID  string `json:"plan_id"`
	Digest  string `json:"digest"`
}

type listCheckPlansTool struct{ plans []checkPlan }

func (*listCheckPlansTool) ReplayPolicy() tool.ReplayPolicy { return tool.ReplaySafe }

func (*listCheckPlansTool) Definition() llm.ToolDefinition {
	return llm.ToolDefinition{Name: "list_check_plans", Description: "List trusted, workspace-detected validation plan IDs. Plans contain fixed executables and arguments; no shell command is accepted.", InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)}
}

func (t *listCheckPlansTool) Execute(ctx context.Context, call tool.Call, _ tool.ProgressSink) (tool.Result, error) {
	var arguments struct{}
	if err := decodeArguments(call.Arguments, &arguments); err != nil {
		return invalidResult(err.Error()), nil
	}
	if err := ctx.Err(); err != nil {
		return tool.Result{}, err
	}
	if len(t.plans) == 0 {
		return completedResult("No trusted check plans were detected for this worktree.", json.RawMessage(`{"kind":"coding_check_plans_v1","plans":[]}`)), nil
	}
	type planView struct {
		ID       string `json:"id"`
		Language string `json:"language"`
		Label    string `json:"label"`
		Command  string `json:"command"`
	}
	views := make([]planView, len(t.plans))
	lines := make([]string, len(t.plans))
	for index, plan := range t.plans {
		command := strings.Join(append([]string{plan.Executable}, plan.Arguments...), " ")
		views[index] = planView{ID: plan.ID, Language: plan.Language, Label: plan.Label, Command: command}
		lines[index] = fmt.Sprintf("%s  %s  (%s)", plan.ID, plan.Label, command)
	}
	details, _ := json.Marshal(struct {
		Kind  string     `json:"kind"`
		Plans []planView `json:"plans"`
	}{Kind: "coding_check_plans_v1", Plans: views})
	return completedResult(strings.Join(lines, "\n"), details), nil
}

type runChecksTool struct {
	root         string
	plans        []checkPlan
	displayLimit int
	outputLimit  int
	timeout      time.Duration
	artifacts    codingagent.ArtifactStore
	security     *codingagent.SecurityPolicy
}

func (t *runChecksTool) Definition() llm.ToolDefinition {
	ids := make([]string, len(t.plans))
	for index, plan := range t.plans {
		ids[index] = plan.ID
	}
	planIDSchema := map[string]any{"type": "string"}
	if len(ids) != 0 {
		planIDSchema["enum"] = ids
	}
	schema, _ := json.Marshal(map[string]any{
		"type": "object", "properties": map[string]any{"plan_id": planIDSchema},
		"required": []string{"plan_id"}, "additionalProperties": false,
	})
	return llm.ToolDefinition{Name: "run_checks", Description: "Run one trusted validation plan by plan_id. The tool never accepts executable names, arguments, shell text, cwd, or environment variables.", InputSchema: schema}
}

func (*runChecksTool) ReplayPolicy() tool.ReplayPolicy { return tool.ReplayNever }

func (t *runChecksTool) Execute(ctx context.Context, call tool.Call, progress tool.ProgressSink) (tool.Result, error) {
	_, plan, result := t.requestedPlan(call)
	if result != nil {
		return *result, nil
	}
	return t.runPlan(ctx, plan, progress)
}

func (t *runChecksTool) PermissionRequirement(_ context.Context, call tool.Call) (permissionRequirement, tool.Result, error) {
	planID, plan, result := t.requestedPlan(call)
	if result != nil {
		return permissionRequirement{}, *result, nil
	}
	payload := checkApprovalPayload{Kind: "coding_check_approval_v1", Version: 1, PlanID: planID}
	payload.Digest = checkApprovalDigest(payload, call)
	command := strings.Join(append([]string{plan.Executable}, plan.Arguments...), " ")
	approval := tool.Result{
		Status:  tool.ResultInterrupted,
		Content: []llm.Content{{Type: llm.ContentText, Text: "Approval is required before running trusted check plan " + plan.ID + "."}},
		Interrupt: &tool.Interrupt{
			ID: approvalID(call, payload.Digest), Kind: "approval",
			Payload: mustJSON(map[string]any{"kind": payload.Kind, "version": payload.Version, "plan_id": payload.PlanID, "digest": payload.Digest, "summary": "Run " + plan.Label, "command": command}),
		},
	}
	return permissionRequirement{
		required: true,
		request: codingagent.PermissionRequest{
			ToolName: "run_checks", Action: codingagent.PermissionExecutePlanAction(plan.ID),
		},
		readOnlyMessage: "The read-only permission mode does not allow project code or build tools to execute.",
		approval:        approval,
	}, tool.Result{}, nil
}

func (t *runChecksTool) Resume(ctx context.Context, call tool.Call, interrupt tool.Interrupt, resolution tool.Result, progress tool.ProgressSink) (tool.Result, error) {
	if resolution.Status != tool.ResultCompleted {
		return resolution, nil
	}
	if interrupt.Kind != "approval" {
		return failedResult("The pending check has an unsupported interrupt type."), nil
	}
	var payload checkApprovalPayload
	if err := json.Unmarshal(interrupt.Payload, &payload); err != nil || payload.Kind != "coding_check_approval_v1" || payload.Version != 1 {
		return failedResult("The saved check approval request is invalid."), nil
	}
	planID, plan, invalid := t.requestedPlan(call)
	if invalid != nil {
		return *invalid, nil
	}
	if payload.PlanID != planID || payload.Digest == "" || payload.Digest != checkApprovalDigest(payload, call) || interrupt.ID != approvalID(call, payload.Digest) {
		return failedResult("The saved check approval request failed its integrity check."), nil
	}
	if progress != nil {
		return t.runPlan(ctx, plan, progress)
	}
	return t.run(ctx, plan)
}

func (t *runChecksTool) runPlan(ctx context.Context, plan checkPlan, progress tool.ProgressSink) (tool.Result, error) {
	if progress != nil {
		_ = progress.PublishToolProgress(ctx, tool.Progress{Summary: "Running " + plan.Label})
	}
	return t.run(ctx, plan)
}

func (t *runChecksTool) requestedPlan(call tool.Call) (string, checkPlan, *tool.Result) {
	var arguments struct {
		PlanID string `json:"plan_id"`
	}
	if err := decodeArguments(call.Arguments, &arguments); err != nil {
		result := invalidResult(err.Error())
		return "", checkPlan{}, &result
	}
	arguments.PlanID = strings.TrimSpace(arguments.PlanID)
	for _, plan := range t.plans {
		if plan.ID == arguments.PlanID {
			return arguments.PlanID, plan, nil
		}
	}
	result := deniedResult("The requested plan_id was not generated for this worktree.")
	return arguments.PlanID, checkPlan{}, &result
}

func (t *runChecksTool) run(ctx context.Context, plan checkPlan) (tool.Result, error) {
	executable, err := exec.LookPath(plan.Executable)
	if err != nil {
		return failedResult(fmt.Sprintf("The trusted check executable %q is unavailable.", plan.Executable)), nil
	}
	runContext, cancelTimeout := context.WithTimeout(ctx, t.timeout)
	defer cancelTimeout()
	outputContext, cancelOutput := context.WithCancel(runContext)
	defer cancelOutput()
	output := &checkOutput{limit: t.outputLimit, onLimit: cancelOutput}
	command := exec.CommandContext(outputContext, executable, plan.Arguments...)
	command.Dir = t.root
	command.Env = trustedCheckEnvironment()
	command.Stdout = output
	command.Stderr = output
	configureCheckCommand(command)
	started := time.Now()
	runErr := command.Run()
	duration := time.Since(started)
	if ctx.Err() != nil {
		return tool.Result{}, ctx.Err()
	}
	text := t.security.RedactText(strings.ToValidUTF8(string(output.Bytes()), "�"))
	bytes := []byte(text)
	display := boundedCheckText(text, t.displayLimit)
	var artifact *codingagent.ArtifactRef
	if len(bytes) > t.displayLimit || output.truncated {
		if t.artifacts == nil {
			return failedResult("Check output exceeded the inline limit, but Artifact Store is unavailable."), nil
		}
		reference, artifactErr := t.artifacts.SaveArtifact(ctx, codingagent.Artifact{MediaType: "text/plain; charset=utf-8", Data: append([]byte(nil), bytes...)})
		if artifactErr != nil {
			return failedResult("Check output exceeded the inline limit and could not be stored as an Artifact."), nil
		}
		artifact = &reference
	}
	exitCode := 0
	status := tool.ResultCompleted
	terminal := "passed"
	if errors.Is(runContext.Err(), context.DeadlineExceeded) {
		status, terminal = tool.ResultFailed, "timed out"
	} else if output.truncated {
		status, terminal = tool.ResultFailed, "exceeded the output limit"
	} else if runErr != nil {
		status, terminal = tool.ResultFailed, "failed"
		var exitError *exec.ExitError
		if errors.As(runErr, &exitError) {
			exitCode = exitError.ExitCode()
		} else {
			exitCode = -1
		}
	}
	summary := fmt.Sprintf("%s %s in %s", plan.ID, terminal, duration.Round(time.Millisecond))
	if strings.TrimSpace(display) == "" {
		display = "The check produced no output."
	}
	if artifact != nil {
		display += fmt.Sprintf("\n\nFull bounded output: %s (%d bytes)", artifact.ID, artifact.Size)
	}
	details, _ := json.Marshal(map[string]any{
		"kind": "coding_check_v1", "detail": display, "plan_id": plan.ID, "language": plan.Language,
		"exit_code": exitCode, "duration_ms": duration.Milliseconds(), "output_truncated": output.truncated, "artifact": artifact,
	})
	return tool.Result{Status: status, Content: []llm.Content{{Type: llm.ContentText, Text: summary + "\n" + display}}, Details: details}, nil
}

func detectCheckPlans(root string) []checkPlan {
	var plans []checkPlan
	if regularMarker(root, "go.mod") {
		plans = append(plans,
			checkPlan{ID: "go.test", Language: "go", Label: "Go tests", Executable: "go", Arguments: []string{"test", "./..."}},
			checkPlan{ID: "go.vet", Language: "go", Label: "Go vet", Executable: "go", Arguments: []string{"vet", "./..."}},
			checkPlan{ID: "go.build", Language: "go", Label: "Go build", Executable: "go", Arguments: []string{"build", "./..."}},
		)
	}
	python := regularMarker(root, "pyproject.toml") || regularMarker(root, "setup.py") || regularMarker(root, "setup.cfg") || regularMarker(root, "requirements.txt")
	if python {
		plans = append(plans, checkPlan{ID: "python.compile", Language: "python", Label: "Python compile check", Executable: pythonExecutable(), Arguments: []string{"-m", "compileall", "-q", "."}})
		if regularMarker(root, "pytest.ini") || regularMarker(root, "tox.ini") || regularMarker(root, "tests") {
			plans = append(plans, checkPlan{ID: "python.test", Language: "python", Label: "Python tests", Executable: pythonExecutable(), Arguments: []string{"-m", "pytest"}})
		}
	}
	if scripts, ok := nodeScripts(root); ok {
		if strings.TrimSpace(scripts["test"]) != "" {
			plans = append(plans, checkPlan{ID: "node.test", Language: "node", Label: "Node tests", Executable: "npm", Arguments: []string{"test", "--if-present"}})
		}
		if strings.TrimSpace(scripts["build"]) != "" {
			plans = append(plans, checkPlan{ID: "node.build", Language: "node", Label: "Node build", Executable: "npm", Arguments: []string{"run", "build", "--if-present"}})
		}
	}
	sort.Slice(plans, func(left, right int) bool { return plans[left].ID < plans[right].ID })
	return plans
}

func regularMarker(root, name string) bool {
	info, err := os.Lstat(filepath.Join(root, name))
	return err == nil && (info.Mode().IsRegular() || (name == "tests" && info.IsDir())) && info.Mode()&os.ModeSymlink == 0
}

func nodeScripts(root string) (map[string]string, bool) {
	path := filepath.Join(root, "package.json")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > 1<<20 {
		return nil, false
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var value struct {
		Scripts map[string]string `json:"scripts"`
	}
	if json.Unmarshal(content, &value) != nil {
		return nil, false
	}
	return value.Scripts, true
}

func pythonExecutable() string {
	if _, err := exec.LookPath("python3"); err == nil {
		return "python3"
	}
	return "python"
}

func trustedCheckEnvironment() []string {
	names := []string{"PATH", "PATHEXT", "SYSTEMROOT", "WINDIR", "TEMP", "TMP", "USERPROFILE", "HOME", "APPDATA", "LOCALAPPDATA", "GOCACHE", "GOMODCACHE", "GOPATH", "GOROOT", "GOENV", "GOPROXY", "GOSUMDB"}
	values := make([]string, 0, len(names)+2)
	for _, name := range names {
		if value, found := os.LookupEnv(name); found {
			values = append(values, name+"="+value)
		}
	}
	return append(values, "CI=1", "NO_COLOR=1")
}

type checkOutput struct {
	mu        sync.Mutex
	buffer    bytes.Buffer
	limit     int
	truncated bool
	onLimit   context.CancelFunc
}

func (o *checkOutput) Write(value []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	original := len(value)
	remaining := o.limit - o.buffer.Len()
	if remaining <= 0 {
		o.triggerLimit()
		return original, nil
	}
	if len(value) > remaining {
		_, _ = o.buffer.Write(value[:remaining])
		o.triggerLimit()
		return original, nil
	}
	_, _ = o.buffer.Write(value)
	return original, nil
}

func (o *checkOutput) triggerLimit() {
	if o.truncated {
		return
	}
	o.truncated = true
	if o.onLimit != nil {
		o.onLimit()
	}
}

func (o *checkOutput) Bytes() []byte {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]byte(nil), o.buffer.Bytes()...)
}

func boundedCheckText(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for !utf8.ValidString(value) && len(value) != 0 {
		value = value[:len(value)-1]
	}
	return value + "\n...[inline output truncated]"
}

func checkApprovalDigest(payload checkApprovalPayload, call tool.Call) string {
	copy := payload
	copy.Digest = ""
	encoded, _ := json.Marshal(copy)
	digest := sha256.Sum256(append(append(encoded, 0), []byte(call.ID+"\x00"+call.IdempotencyKey+"\x00"+string(call.Arguments))...))
	return hex.EncodeToString(digest[:])
}

func mustJSON(value any) json.RawMessage {
	encoded, _ := json.Marshal(value)
	return encoded
}

var _ tool.ResumableTool = (*runChecksTool)(nil)
