package llm

import "context"

// ChatRequest contains one complete provider-neutral model request.
type ChatRequest struct {
	Model           ModelRef         `json:"model"`
	SystemPrompt    string           `json:"system_prompt"`
	Messages        []Message        `json:"messages"`
	Tools           []ToolDefinition `json:"tools,omitempty"`
	MaxOutputTokens int              `json:"max_output_tokens,omitempty"`
	ThinkingLevel   string           `json:"thinking_level,omitempty"`
}

// Validate checks request data before it crosses a provider boundary.
func (r ChatRequest) Validate() error {
	if err := r.Model.Validate(); err != nil {
		return err
	}
	for _, message := range r.Messages {
		if err := message.Validate(); err != nil {
			return err
		}
	}
	for _, definition := range r.Tools {
		if err := definition.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// Stream yields normalized events and returns io.EOF after the terminal event.
type Stream interface {
	Recv() (StreamEvent, error)
	Close() error
}

// ChatModel performs provider-neutral complete and streaming model calls.
type ChatModel interface {
	Complete(ctx context.Context, request ChatRequest) (Message, error)
	Stream(ctx context.Context, request ChatRequest) (Stream, error)
}

// ModelFactory resolves a model implementation without exposing provider SDK types.
type ModelFactory interface {
	CreateModel(ctx context.Context, ref ModelRef) (ChatModel, error)
}

// ModelCatalog resolves model capabilities independently from model creation.
// Agent uses it opportunistically and falls back to conservative limits when a
// provider cannot report metadata.
type ModelCatalog interface {
	DescribeModel(ctx context.Context, ref ModelRef) (Model, error)
}

// CollectStream validates and collects a stream until a terminal response arrives.
func CollectStream(stream Stream) (Message, error) {
	defer stream.Close()
	for {
		event, err := stream.Recv()
		if err != nil {
			return Message{}, err
		}
		if err := event.Validate(); err != nil {
			return Message{}, err
		}
		switch event.Kind {
		case StreamResponseFinished:
			return event.Message.Clone(), nil
		case StreamResponseFailed:
			return Message{}, &ResponseError{Code: event.ErrorCode, Message: event.ErrorMessage}
		}
	}
}

// ResponseError is a safe normalized provider failure.
type ResponseError struct {
	Code    string
	Message string
}

func (e *ResponseError) Error() string {
	if e == nil {
		return "model response error"
	}
	return e.Message
}

// Temporary classifies normalized transient response failures.
func (e *ResponseError) Temporary() bool {
	if e == nil {
		return false
	}
	switch e.Code {
	case "rate_limited", "timeout", "server_error", "service_unavailable", "connection_failed":
		return true
	default:
		return false
	}
}

// RetryReason returns a bounded normalized code.
func (e *ResponseError) RetryReason() string {
	if e == nil || e.Code == "" {
		return "model_response_error"
	}
	return e.Code
}
