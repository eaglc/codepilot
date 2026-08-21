package agent

import (
	"encoding/json"
	"time"

	"github.com/eaglc/codepilot/internal/tool"
)

// InvocationRole identifies a provider-neutral conversation role.
type InvocationRole string

const (
	// InvocationRoleUser identifies user-authored input.
	InvocationRoleUser InvocationRole = "user"
	// InvocationRoleAssistant identifies durable assistant history.
	InvocationRoleAssistant InvocationRole = "assistant"
)

// InvocationEventKind identifies one provider-neutral runtime event.
type InvocationEventKind string

const (
	// InvocationEventAssistantText carries assistant text in display order.
	InvocationEventAssistantText InvocationEventKind = "assistant-text"
	// InvocationEventToolStarted indicates that a registered tool began running.
	InvocationEventToolStarted InvocationEventKind = "tool-started"
	// InvocationEventToolFinished carries a normalized tool outcome.
	InvocationEventToolFinished InvocationEventKind = "tool-finished"
	// InvocationEventInterrupted indicates that external input is required.
	InvocationEventInterrupted InvocationEventKind = "interrupted"
)

// InvocationStatus describes how one model/tool loop stopped.
type InvocationStatus string

const (
	// InvocationCompleted indicates that the model produced a final response.
	InvocationCompleted InvocationStatus = "completed"
	// InvocationInterrupted indicates that execution can be resumed from a checkpoint.
	InvocationInterrupted InvocationStatus = "interrupted"
	// InvocationCancelled indicates that the caller cancelled execution.
	InvocationCancelled InvocationStatus = "cancelled"
	// InvocationLimitReached indicates that a step or duration limit stopped execution.
	InvocationLimitReached InvocationStatus = "limit-reached"
)

// InterruptResponseKind describes the externally validated response to a pause.
type InterruptResponseKind string

const (
	// InterruptApproved resumes the interrupted operation through its normal policy boundary.
	InterruptApproved InterruptResponseKind = "approved"
	// InterruptRejected returns a denial to the model without re-running the operation.
	InterruptRejected InterruptResponseKind = "rejected"
	// InterruptCancelled stops the interrupted operation without re-running it.
	InterruptCancelled InterruptResponseKind = "cancelled"
)

// InvocationMessage is one provider-neutral conversation message.
type InvocationMessage struct {
	Role    InvocationRole
	Content string
}

// InvocationLimits bounds model iterations and elapsed execution time.
type InvocationLimits struct {
	MaxSteps    int
	MaxDuration time.Duration
}

// InvocationInput contains everything required for one new model/tool loop.
// Coding-specific facts remain captured inside Tools rather than this protocol.
type InvocationInput struct {
	ID           string
	CheckpointID string
	Model        ModelRef
	SystemPrompt string
	Messages     []InvocationMessage
	Tools        *tool.Registry
	Limits       InvocationLimits
}

// ResumeInput targets one Eino root-cause interrupt in an existing checkpoint.
type ResumeInput struct {
	CheckpointID string
	InterruptID  string
	Response     InterruptResponse
}

// InterruptResponse is safe, already validated external input for a resume.
type InterruptResponse struct {
	Kind InterruptResponseKind `json:"kind"`
}

// InvocationToolEvent is a bounded summary of one tool transition.
type InvocationToolEvent struct {
	Name    string
	Status  tool.ResultStatus
	Summary string
}

// InvocationInterrupt is a provider-neutral resumable pause. ID is the
// runtime root-cause ID; product request IDs remain inside Payload.
type InvocationInterrupt struct {
	ID      string
	Kind    string
	Payload json.RawMessage
}

// InvocationEvent is the typed event union emitted while an invocation runs.
type InvocationEvent struct {
	Kind      InvocationEventKind
	Text      string
	Tool      *InvocationToolEvent
	Interrupt *InvocationInterrupt
}

// InvocationResult contains neutral terminal facts for the business agent.
type InvocationResult struct {
	Status            InvocationStatus
	FinalText         string
	Steps             int
	TerminationReason string
	Interrupt         *InvocationInterrupt
}
