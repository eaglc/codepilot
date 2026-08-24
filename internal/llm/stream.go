package llm

import "fmt"

// StreamEventKind identifies one normalized provider stream transition.
type StreamEventKind string

const (
	StreamResponseStarted  StreamEventKind = "response_started"
	StreamContentStarted   StreamEventKind = "content_started"
	StreamTextDelta        StreamEventKind = "text_delta"
	StreamThinkingDelta    StreamEventKind = "thinking_delta"
	StreamToolCallDelta    StreamEventKind = "tool_call_delta"
	StreamContentFinished  StreamEventKind = "content_finished"
	StreamUsageUpdated     StreamEventKind = "usage_updated"
	StreamResponseFinished StreamEventKind = "response_finished"
	StreamResponseFailed   StreamEventKind = "response_failed"
)

// StreamEvent is internal to the LLM/Agent boundary and must not be exposed to product UIs.
type StreamEvent struct {
	Kind         StreamEventKind `json:"kind"`
	Sequence     uint64          `json:"sequence"`
	ResponseID   string          `json:"response_id,omitempty"`
	ContentIndex int             `json:"content_index,omitempty"`
	Delta        string          `json:"delta,omitempty"`
	ToolCallID   string          `json:"tool_call_id,omitempty"`
	ToolName     string          `json:"tool_name,omitempty"`
	Message      *Message        `json:"message,omitempty"`
	Usage        *Usage          `json:"usage,omitempty"`
	ErrorCode    string          `json:"error_code,omitempty"`
	ErrorMessage string          `json:"error_message,omitempty"`
}

// Validate checks the normalized stream envelope.
func (e StreamEvent) Validate() error {
	switch e.Kind {
	case StreamResponseStarted, StreamContentStarted, StreamContentFinished:
		return nil
	case StreamTextDelta, StreamThinkingDelta:
		if e.Delta == "" {
			return fmt.Errorf("validate %s event: delta is empty", e.Kind)
		}
	case StreamToolCallDelta:
		if e.ToolCallID == "" {
			return fmt.Errorf("validate tool-call delta: tool call id is required")
		}
	case StreamUsageUpdated:
		if e.Usage == nil {
			return fmt.Errorf("validate usage event: usage is missing")
		}
	case StreamResponseFinished:
		if e.Message == nil {
			return fmt.Errorf("validate response-finished event: message is missing")
		}
		if err := e.Message.Validate(); err != nil {
			return fmt.Errorf("validate response-finished event: %w", err)
		}
	case StreamResponseFailed:
		if e.ErrorMessage == "" {
			return fmt.Errorf("validate response-failed event: error message is required")
		}
	default:
		return fmt.Errorf("validate stream event: unsupported kind %q", e.Kind)
	}
	return nil
}
