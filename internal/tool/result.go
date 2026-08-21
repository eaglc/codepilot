package tool

import "encoding/json"

// ResultStatus describes the normalized outcome of a tool invocation.
type ResultStatus string

const (
	// ResultCompleted indicates that the tool completed, including nonzero checks.
	ResultCompleted ResultStatus = "completed"
	// ResultDenied indicates that policy or the user rejected the action.
	ResultDenied ResultStatus = "denied"
	// ResultInvalid indicates invalid model-controlled arguments.
	ResultInvalid ResultStatus = "invalid"
	// ResultFailed indicates an unexpected tool or infrastructure failure.
	ResultFailed ResultStatus = "failed"
	// ResultCancelled indicates that the invocation context was cancelled.
	ResultCancelled ResultStatus = "cancelled"
	// ResultInterrupted indicates that execution paused for external input.
	ResultInterrupted ResultStatus = "interrupted"
)

// Result is a bounded, secret-free response returned to the agent runtime.
type Result struct {
	Status    ResultStatus
	Content   string
	Data      json.RawMessage
	Interrupt *Interrupt
}

// Interrupt describes a resumable pause without carrying business objects.
type Interrupt struct {
	ID      string
	Kind    string
	Payload json.RawMessage
}
