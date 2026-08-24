// Package tool defines provider-neutral executable tool contracts.
package tool

import (
	"encoding/json"
	"fmt"

	"github.com/eaglc/codepilot/internal/llm"
)

// ResultStatus describes the normalized outcome of one tool execution.
type ResultStatus string

const (
	ResultCompleted   ResultStatus = "completed"
	ResultDenied      ResultStatus = "denied"
	ResultInvalid     ResultStatus = "invalid"
	ResultFailed      ResultStatus = "failed"
	ResultCancelled   ResultStatus = "cancelled"
	ResultInterrupted ResultStatus = "interrupted"
)

// Interrupt contains resumable, provider-neutral external-input metadata.
type Interrupt struct {
	ID      string          `json:"id"`
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}

// Result is returned by an executable tool and later converted by Agent into an LLM tool-result message.
type Result struct {
	Status    ResultStatus    `json:"status"`
	Content   []llm.Content   `json:"content"`
	Details   json.RawMessage `json:"details,omitempty"`
	Interrupt *Interrupt      `json:"interrupt,omitempty"`
}

// Validate checks execution-result invariants before persistence or model delivery.
func (r Result) Validate() error {
	switch r.Status {
	case ResultCompleted, ResultDenied, ResultInvalid, ResultFailed, ResultCancelled, ResultInterrupted:
	default:
		return fmt.Errorf("validate tool result: unsupported status %q", r.Status)
	}
	if len(r.Content) == 0 {
		return fmt.Errorf("validate tool result: content is empty")
	}
	for index, content := range r.Content {
		if err := content.Validate(); err != nil {
			return fmt.Errorf("validate tool result content %d: %w", index, err)
		}
		if content.Type != llm.ContentText && content.Type != llm.ContentImage {
			return fmt.Errorf("validate tool result: content type %q is not allowed", content.Type)
		}
	}
	if len(r.Details) != 0 && !json.Valid(r.Details) {
		return fmt.Errorf("validate tool result: details are not valid JSON")
	}
	if r.Status == ResultInterrupted {
		if r.Interrupt == nil || r.Interrupt.ID == "" || r.Interrupt.Kind == "" {
			return fmt.Errorf("validate interrupted tool result: interrupt metadata is incomplete")
		}
	} else if r.Interrupt != nil {
		return fmt.Errorf("validate tool result: interrupt is only valid for interrupted status")
	}
	return nil
}

// IsError reports whether the result should be presented to the model as an error.
func (r Result) IsError() bool {
	return r.Status == ResultDenied || r.Status == ResultInvalid || r.Status == ResultFailed || r.Status == ResultCancelled
}

// Clone returns a defensive copy of the result.
func (r Result) Clone() Result {
	clone := r
	clone.Content = make([]llm.Content, len(r.Content))
	for index, content := range r.Content {
		clone.Content[index] = content.Clone()
	}
	clone.Details = append(json.RawMessage(nil), r.Details...)
	if r.Interrupt != nil {
		interrupt := *r.Interrupt
		interrupt.Payload = append(json.RawMessage(nil), r.Interrupt.Payload...)
		clone.Interrupt = &interrupt
	}
	return clone
}
