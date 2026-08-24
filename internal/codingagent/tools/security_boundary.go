package codingtools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/eaglc/codepilot/internal/codingagent"
	"github.com/eaglc/codepilot/internal/llm"
	"github.com/eaglc/codepilot/internal/tool"
)

type securityBoundary struct {
	inner  tool.Tool
	policy *codingagent.SecurityPolicy
}

type sensitiveReadApprovalPayload struct {
	Kind     string `json:"kind"`
	Version  int    `json:"version"`
	ToolName string `json:"tool_name"`
	Path     string `json:"path"`
	Summary  string `json:"summary"`
	Digest   string `json:"digest"`
}

func withSecurityBoundary(inner tool.Tool, policy *codingagent.SecurityPolicy) tool.Tool {
	return &securityBoundary{inner: inner, policy: policy}
}

func (b *securityBoundary) Definition() llm.ToolDefinition  { return b.inner.Definition() }
func (b *securityBoundary) ReplayPolicy() tool.ReplayPolicy { return b.inner.ReplayPolicy() }

func (b *securityBoundary) Execute(ctx context.Context, call tool.Call, progress tool.ProgressSink) (tool.Result, error) {
	path, sensitive, err := b.sensitivePath(call)
	if err != nil {
		return invalidResult("The sensitive-path policy could not validate this request."), nil
	}
	if sensitive {
		if call.Name == "search_code" {
			return deniedResult("Sensitive files are excluded from search_code. Use read_file to request one explicit, redacted read."), nil
		}
		payload := sensitiveReadApprovalPayload{
			Kind: "coding_sensitive_read_approval_v1", Version: 1, ToolName: call.Name, Path: path,
			Summary: "Read sensitive path " + path + " with secret values redacted",
		}
		payload.Digest = sensitiveReadDigest(payload, call)
		encoded, marshalErr := json.Marshal(payload)
		if marshalErr != nil {
			return tool.Result{}, marshalErr
		}
		return tool.Result{
			Status:    tool.ResultInterrupted,
			Content:   []llm.Content{{Type: llm.ContentText, Text: "Approval is required before reading this sensitive path. Recognized secret values will still be redacted."}},
			Interrupt: &tool.Interrupt{ID: approvalID(call, payload.Digest), Kind: "approval", Payload: encoded},
		}, nil
	}
	return b.executeInner(ctx, call, progress)
}

func (b *securityBoundary) Resume(ctx context.Context, call tool.Call, interrupt tool.Interrupt, resolution tool.Result, progress tool.ProgressSink) (tool.Result, error) {
	var payload sensitiveReadApprovalPayload
	if interrupt.Kind == "approval" && json.Unmarshal(interrupt.Payload, &payload) == nil && payload.Kind == "coding_sensitive_read_approval_v1" {
		if resolution.Status != tool.ResultCompleted {
			return b.policy.SanitizeToolResult(call.Name, resolution), nil
		}
		path, sensitive, err := b.sensitivePath(call)
		if err != nil || !sensitive || payload.Version != 1 || payload.ToolName != call.Name || payload.Path != path || payload.Digest == "" || payload.Digest != sensitiveReadDigest(payload, call) || interrupt.ID != approvalID(call, payload.Digest) {
			return failedResult("The saved sensitive-read approval failed its integrity check."), nil
		}
		return b.executeInner(ctx, call, progress)
	}
	if resumable, ok := b.inner.(tool.ResumableTool); ok {
		result, err := resumable.Resume(ctx, call, interrupt, resolution, progress)
		return b.sanitize(ctx, call.Name, result, err)
	}
	return b.policy.SanitizeToolResult(call.Name, resolution), nil
}

func (b *securityBoundary) executeInner(ctx context.Context, call tool.Call, progress tool.ProgressSink) (tool.Result, error) {
	result, err := b.inner.Execute(ctx, call, progress)
	return b.sanitize(ctx, call.Name, result, err)
}

func (b *securityBoundary) sanitize(ctx context.Context, toolName string, result tool.Result, err error) (tool.Result, error) {
	if err != nil {
		if ctx.Err() != nil {
			return tool.Result{}, ctx.Err()
		}
		message := b.policy.SanitizeText(err.Error())
		if message != err.Error() {
			return tool.Result{}, errors.New(message)
		}
		return tool.Result{}, err
	}
	return b.policy.SanitizeToolResult(toolName, result), nil
}

func (b *securityBoundary) sensitivePath(call tool.Call) (string, bool, error) {
	if call.Name != "read_file" && call.Name != "search_code" && call.Name != "git_diff" {
		return "", false, nil
	}
	var arguments struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(call.Arguments, &arguments); err != nil {
		return "", false, err
	}
	path := strings.TrimSpace(strings.ReplaceAll(arguments.Path, "\\", "/"))
	if path == "" {
		return "", false, nil
	}
	return path, b.policy.IsSensitivePath(path), nil
}

func sensitiveReadDigest(payload sensitiveReadApprovalPayload, call tool.Call) string {
	copy := payload
	copy.Digest = ""
	encoded, _ := json.Marshal(copy)
	seed := append(encoded, 0)
	seed = append(seed, []byte(call.ID+"\x00"+call.IdempotencyKey+"\x00"+string(call.Arguments))...)
	digest := sha256.Sum256(seed)
	return hex.EncodeToString(digest[:])
}

var _ tool.ResumableTool = (*securityBoundary)(nil)
