package codingtools

import (
	"context"
	"strings"
	"time"

	"github.com/eaglc/codepilot/internal/codingagent"
	"github.com/eaglc/codepilot/internal/llm"
	"github.com/eaglc/codepilot/internal/tool"
)

// permissionRequirement describes the authorization fact for one prepared
// operation. Concrete tools own argument validation and approval previews;
// permissionBoundary owns every mode/grant decision.
type permissionRequirement struct {
	required        bool
	request         codingagent.PermissionRequest
	automatic       bool
	readOnlyMessage string
	approval        tool.Result
}

type permissionRequirementProvider interface {
	PermissionRequirement(context.Context, tool.Call) (permissionRequirement, tool.Result, error)
}

type permissionBoundary struct {
	inner  tool.Tool
	mode   codingagent.PermissionMode
	grants []codingagent.PermissionGrant
	now    func() time.Time
}

func withPermissionBoundary(inner tool.Tool, mode codingagent.PermissionMode, grants []codingagent.PermissionGrant) tool.Tool {
	if _, controlled := inner.(permissionRequirementProvider); !controlled {
		return inner
	}
	return &permissionBoundary{
		inner: inner, mode: mode, grants: append([]codingagent.PermissionGrant(nil), grants...), now: time.Now,
	}
}

func (b *permissionBoundary) Definition() llm.ToolDefinition  { return b.inner.Definition() }
func (b *permissionBoundary) ReplayPolicy() tool.ReplayPolicy { return b.inner.ReplayPolicy() }

func (b *permissionBoundary) Execute(ctx context.Context, call tool.Call, progress tool.ProgressSink) (tool.Result, error) {
	provider := b.inner.(permissionRequirementProvider)
	requirement, terminal, err := provider.PermissionRequirement(ctx, call)
	if err != nil || terminal.Status != "" {
		return terminal, err
	}
	if !requirement.required {
		return b.inner.Execute(ctx, call, progress)
	}
	if strings.TrimSpace(requirement.request.ToolName) == "" || strings.TrimSpace(requirement.request.Action) == "" {
		return failedResult("The tool produced an invalid permission request."), nil
	}
	if requirement.approval.Status != tool.ResultInterrupted || requirement.approval.Interrupt == nil {
		return failedResult("The tool produced an invalid approval request."), nil
	}
	switch b.mode {
	case codingagent.PermissionReadOnly:
		message := strings.TrimSpace(requirement.readOnlyMessage)
		if message == "" {
			message = "The read-only permission mode does not allow this operation."
		}
		return deniedResult(message), nil
	case codingagent.PermissionAsk, "":
		if b.granted(requirement.request) {
			return b.inner.Execute(ctx, call, progress)
		}
		return requirement.approval.Clone(), nil
	case codingagent.PermissionAutoEdit:
		if requirement.automatic || b.granted(requirement.request) {
			return b.inner.Execute(ctx, call, progress)
		}
		return requirement.approval.Clone(), nil
	default:
		return deniedResult("The session permission mode does not allow this operation."), nil
	}
}

func (b *permissionBoundary) Resume(ctx context.Context, call tool.Call, interrupt tool.Interrupt, resolution tool.Result, progress tool.ProgressSink) (tool.Result, error) {
	if resolution.Status != tool.ResultCompleted {
		return resolution.Clone(), nil
	}
	switch b.mode {
	case codingagent.PermissionReadOnly:
		return deniedResult("The session is now read-only; the approved operation was not executed."), nil
	case codingagent.PermissionAsk, "", codingagent.PermissionAutoEdit:
	default:
		return deniedResult("The session permission mode no longer allows the approved operation."), nil
	}
	resumable, ok := b.inner.(tool.ResumableTool)
	if !ok {
		return failedResult("The approved tool does not support resumed execution."), nil
	}
	return resumable.Resume(ctx, call, interrupt, resolution, progress)
}

func (b *permissionBoundary) granted(request codingagent.PermissionRequest) bool {
	now := time.Now().UTC()
	if b.now != nil {
		now = b.now().UTC()
	}
	return codingagent.PermissionGranted(b.grants, request, now)
}

var _ tool.ResumableTool = (*permissionBoundary)(nil)
