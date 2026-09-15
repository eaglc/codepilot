package codingagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/eaglc/codepilot/internal/llm"
	"github.com/eaglc/codepilot/internal/tool"
)

const integrateChangeSetToolName = "integrate_change_set"

type integrateChangeSetTool struct {
	children   ChildAgentRepository
	manager    ManagedWorktreeManager
	activeRoot string
	childID    ChildAgentID
	changeID   string
}

type integrateChangeSetArguments struct {
	ChangeSetID string `json:"change_set_id"`
}

type integrateChangeSetApproval struct {
	Kind              string            `json:"kind"`
	Version           int               `json:"version"`
	Summary           string            `json:"summary"`
	Patch             string            `json:"patch"`
	Files             []string          `json:"files"`
	BeforeState       map[string]string `json:"before_state"`
	ChangeSetID       string            `json:"change_set_id"`
	ChildAgentID      ChildAgentID      `json:"child_agent_id"`
	PatchSHA256       string            `json:"patch_sha256"`
	AllowSessionGrant bool              `json:"allow_session_grant"`
	Digest            string            `json:"digest"`
}

func (t *integrateChangeSetTool) Definition() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        integrateChangeSetToolName,
		Description: "Request approval and integrate the one exact verified isolated-worktree change set assigned to this node. The product loads and revalidates the stored patch; patch text cannot be supplied or changed by the model.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"change_set_id":{"type":"string","minLength":1}},"required":["change_set_id"],"additionalProperties":false}`),
	}
}

func (*integrateChangeSetTool) ReplayPolicy() tool.ReplayPolicy { return tool.ReplayNever }

func (t *integrateChangeSetTool) Execute(ctx context.Context, call tool.Call, _ tool.ProgressSink) (tool.Result, error) {
	arguments, change, err := t.resolve(ctx, call.Arguments)
	if err != nil {
		return integrationToolResult(tool.ResultInvalid, err.Error(), nil), nil
	}
	preview, err := t.manager.PreviewIntegration(ctx, t.activeRoot, change)
	if err != nil {
		return integrationToolResult(tool.ResultFailed, err.Error(), nil), nil
	}
	payload := integrateChangeSetApproval{
		Kind: "coding_patch_approval_v1", Version: 1,
		Summary: "Integrate verified change set " + arguments.ChangeSetID + " from isolated child " + string(t.childID) + ".",
		Patch:   preview.Patch, Files: append([]string(nil), preview.Files...), BeforeState: cloneStringMap(preview.BeforeState),
		ChangeSetID: arguments.ChangeSetID, ChildAgentID: t.childID, PatchSHA256: preview.PatchSHA256, AllowSessionGrant: false,
	}
	payload.Digest = integrationApprovalDigest(payload)
	encoded, err := json.Marshal(payload)
	if err != nil {
		return tool.Result{}, err
	}
	return tool.Result{
		Status:    tool.ResultInterrupted,
		Content:   []llm.Content{{Type: llm.ContentText, Text: "Approval is required before this exact isolated change set is integrated into the active worktree."}},
		Interrupt: &tool.Interrupt{ID: integrationApprovalID(call, payload.Digest), Kind: "approval", Payload: encoded},
	}, nil
}

func (t *integrateChangeSetTool) Resume(ctx context.Context, call tool.Call, interrupt tool.Interrupt, resolution tool.Result, _ tool.ProgressSink) (tool.Result, error) {
	if resolution.Status != tool.ResultCompleted {
		return resolution.Clone(), nil
	}
	if interrupt.Kind != "approval" {
		return integrationToolResult(tool.ResultFailed, "The saved change-set approval has an unsupported kind.", nil), nil
	}
	var payload integrateChangeSetApproval
	if json.Unmarshal(interrupt.Payload, &payload) != nil || payload.Kind != "coding_patch_approval_v1" || payload.Version != 1 || payload.AllowSessionGrant || payload.Digest == "" || payload.Digest != integrationApprovalDigest(payload) || interrupt.ID != integrationApprovalID(call, payload.Digest) {
		return integrationToolResult(tool.ResultFailed, "The saved change-set approval failed its integrity check.", nil), nil
	}
	arguments, change, err := t.resolve(ctx, call.Arguments)
	if err != nil || arguments.ChangeSetID != payload.ChangeSetID || payload.ChildAgentID != t.childID || payload.PatchSHA256 != change.PatchSHA256 {
		return integrationToolResult(tool.ResultFailed, "The approved change set no longer matches the assigned integration node.", nil), nil
	}
	preview, err := t.manager.PreviewIntegration(ctx, t.activeRoot, change)
	if err != nil {
		return integrationToolResult(tool.ResultFailed, err.Error(), nil), nil
	}
	if preview.Patch != payload.Patch || !reflect.DeepEqual(preview.Files, payload.Files) || preview.PatchSHA256 != payload.PatchSHA256 {
		return integrationToolResult(tool.ResultFailed, "The stored change-set patch changed after approval; integration was stopped.", nil), nil
	}
	preview, err = t.manager.ApplyIntegration(ctx, t.activeRoot, change)
	if err != nil {
		return integrationToolResult(tool.ResultFailed, err.Error(), nil), nil
	}
	child, err := t.markIntegratedAndCleanup(ctx)
	if err != nil {
		return integrationToolResult(tool.ResultFailed, err.Error(), nil), nil
	}
	details, err := json.Marshal(struct {
		Kind   string `json:"kind"`
		Detail string `json:"detail"`
		Diff   struct {
			Text  string   `json:"text"`
			Files []string `json:"files"`
		} `json:"diff"`
	}{Kind: "coding_patch_v1", Detail: "Integrated verified isolated change set " + change.ID + ".", Diff: struct {
		Text  string   `json:"text"`
		Files []string `json:"files"`
	}{Text: preview.Patch, Files: append([]string(nil), preview.Files...)}})
	if err != nil {
		return tool.Result{}, err
	}
	message := fmt.Sprintf("Integrated change set %s from %s into %d file(s).", change.ID, child.ID, len(preview.Files))
	if preview.AlreadyApplied {
		message = "Verified the previously applied change set and completed its durable cleanup state."
	}
	return integrationToolResult(tool.ResultCompleted, message, details), nil
}

func (t *integrateChangeSetTool) resolve(ctx context.Context, raw json.RawMessage) (integrateChangeSetArguments, ChangeSet, error) {
	var arguments integrateChangeSetArguments
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&arguments); err != nil || strings.TrimSpace(arguments.ChangeSetID) == "" || arguments.ChangeSetID != strings.TrimSpace(arguments.ChangeSetID) {
		return arguments, ChangeSet{}, errors.New("integrate_change_set requires the exact assigned change_set_id")
	}
	if arguments.ChangeSetID != t.changeID {
		return arguments, ChangeSet{}, errors.New("the requested change set is not assigned to this Integrate node")
	}
	child, err := t.children.LoadChildAgent(ctx, t.childID)
	if err != nil || child.Status != ChildAgentCompleted || child.ManagedWorktree == nil || child.ManagedWorktree.ChangeSet == nil || child.ManagedWorktree.ChangeSet.ID != arguments.ChangeSetID {
		return arguments, ChangeSet{}, errors.New("the assigned isolated child change set is unavailable")
	}
	if child.ManagedWorktree.Status != ManagedWorktreeCaptured && child.ManagedWorktree.Status != ManagedWorktreeCleanupPending && child.ManagedWorktree.Status != ManagedWorktreeCleaned {
		return arguments, ChangeSet{}, errors.New("the assigned isolated change set is not ready for integration")
	}
	return arguments, *child.ManagedWorktree.ChangeSet, nil
}

func (t *integrateChangeSetTool) markIntegratedAndCleanup(ctx context.Context) (ChildAgent, error) {
	child, err := t.children.LoadChildAgent(ctx, t.childID)
	if err != nil {
		return child, err
	}
	if child.ManagedWorktree.Status == ManagedWorktreeCaptured {
		expected := child.Revision
		managed := *child.ManagedWorktree
		change := *managed.ChangeSet
		if change.IntegratedAt.IsZero() {
			change.IntegratedAt = time.Now().UTC()
		}
		managed.ChangeSet, managed.Status = &change, ManagedWorktreeCleanupPending
		child.ManagedWorktree = &managed
		child.UpdatedAt = change.IntegratedAt
		child.Revision++
		if err := t.children.SaveChildAgent(ctx, child, expected); err != nil {
			return child, fmt.Errorf("record integrated change set before cleanup: %w", err)
		}
	}
	if child.ManagedWorktree.Status == ManagedWorktreeCleanupPending {
		if err := t.manager.Cleanup(ctx, t.activeRoot, *child.ManagedWorktree); err != nil {
			return child, fmt.Errorf("change set was integrated but managed worktree cleanup is pending: %w", err)
		}
		expected := child.Revision
		managed := *child.ManagedWorktree
		managed.Status, managed.CleanedAt = ManagedWorktreeCleaned, time.Now().UTC()
		child.ManagedWorktree = &managed
		child.UpdatedAt = managed.CleanedAt
		child.Revision++
		if err := t.children.SaveChildAgent(ctx, child, expected); err != nil {
			return child, fmt.Errorf("record managed worktree cleanup: %w", err)
		}
	}
	return child, nil
}

func integrationApprovalDigest(payload integrateChangeSetApproval) string {
	payload.Digest = ""
	encoded, _ := json.Marshal(payload)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func integrationApprovalID(call tool.Call, digest string) string {
	value := sha256.Sum256([]byte(call.ID + "\x00" + integrateChangeSetToolName + "\x00" + digest))
	return "approval_" + hex.EncodeToString(value[:16])
}

func integrationToolResult(status tool.ResultStatus, text string, details json.RawMessage) tool.Result {
	return tool.Result{Status: status, Content: []llm.Content{{Type: llm.ContentText, Text: boundedUTF8(text, 4096)}}, Details: details}
}

func cloneStringMap(value map[string]string) map[string]string {
	clone := make(map[string]string, len(value))
	for key, item := range value {
		clone[key] = item
	}
	return clone
}

var _ tool.ResumableTool = (*integrateChangeSetTool)(nil)
