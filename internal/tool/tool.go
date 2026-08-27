package tool

import (
	"context"
	"encoding/json"

	"github.com/eaglc/codepilot/internal/llm"
)

// ReplayPolicy controls how recovery treats a tool that started without a durable result.
// It is an alias of the provider-neutral protocol type for compatibility.
type ReplayPolicy = llm.ReplayPolicy

const (
	// ReplayNever requires an explicit external decision before another execution.
	ReplayNever = llm.ReplayNever
	// ReplaySafe permits recovery to execute the tool again after validation.
	ReplaySafe = llm.ReplaySafe
	// ReplayIdempotent permits retry with the original idempotency key.
	ReplayIdempotent = llm.ReplayIdempotent
)

// Call is a complete tool invocation produced by an assistant message.
type Call struct {
	ID             string
	Name           string
	Arguments      json.RawMessage
	IdempotencyKey string
}

// Progress is bounded transient execution progress. It is never authoritative state.
type Progress struct {
	Summary string
	Details json.RawMessage
}

// ProgressSink receives optional tool progress through the Agent-owned execution boundary.
type ProgressSink interface {
	PublishToolProgress(ctx context.Context, progress Progress) error
}

// Tool is executable behavior. It does not persist activities or publish product events.
type Tool interface {
	Definition() llm.ToolDefinition
	ReplayPolicy() ReplayPolicy
	Execute(ctx context.Context, call Call, progress ProgressSink) (Result, error)
}

// ControlPolicy describes provider-neutral orchestration semantics for a Tool.
// Product concepts such as Plan and Workflow remain outside the tool package.
type ControlPolicy struct {
	// Exclusive requires the assistant message to contain only this Tool call.
	Exclusive bool
	// HandoffAfterResolution ends the Run after an interrupted call is resolved
	// successfully, returning control to the product coordinator.
	HandoffAfterResolution bool
}

// ControlTool marks a Tool as a product-coordination boundary.
type ControlTool interface {
	Tool
	ControlPolicy() ControlPolicy
}

// ResumableTool completes an execution that previously returned ResultInterrupted.
// The tool owns only validation and execution; Agent still owns all journaling and events.
type ResumableTool interface {
	Tool
	Resume(ctx context.Context, call Call, interrupt Interrupt, resolution Result, progress ProgressSink) (Result, error)
}
