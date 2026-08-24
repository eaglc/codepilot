// Package einoadapter translates between Eino SDK values and the provider-neutral LLM contract.
package einoadapter

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	jsonschema "github.com/eino-contrib/jsonschema"

	"github.com/eaglc/codepilot/internal/llm"
	"github.com/eaglc/codepilot/internal/provider"
)

const maxStreamResponseBytes = 8 << 20

// RequestOptionMapper adds provider-specific options without leaking them into llm.ChatRequest.
type RequestOptionMapper func(llm.ChatRequest) ([]model.Option, error)

// Model wraps one immutable Eino model as an llm.ChatModel.
type Model struct {
	ref     llm.ModelRef
	inner   model.ToolCallingChatModel
	options RequestOptionMapper
}

// New creates a narrow Eino-to-LLM adapter.
func New(inner model.ToolCallingChatModel, ref llm.ModelRef, options RequestOptionMapper) (*Model, error) {
	if inner == nil {
		return nil, errors.New("create Eino model adapter: model is required")
	}
	if err := ref.Validate(); err != nil {
		return nil, fmt.Errorf("create Eino model adapter: %w", err)
	}
	return &Model{ref: ref, inner: inner, options: options}, nil
}

// Complete performs a non-streaming call using only normalized messages and tools.
func (m *Model) Complete(ctx context.Context, request llm.ChatRequest) (llm.Message, error) {
	bound, messages, options, err := m.prepare(request)
	if err != nil {
		return llm.Message{}, err
	}
	response, err := bound.Generate(ctx, messages, options...)
	if err != nil {
		return llm.Message{}, provider.ClassifyTransportError("provider.complete", err)
	}
	return fromEinoResponse(response, m.ref)
}

// Stream performs a streaming call and emits normalized LLM events.
func (m *Model) Stream(ctx context.Context, request llm.ChatRequest) (llm.Stream, error) {
	bound, messages, options, err := m.prepare(request)
	if err != nil {
		return nil, err
	}
	reader, err := bound.Stream(ctx, messages, options...)
	if err != nil {
		return nil, provider.ClassifyTransportError("provider.stream", err)
	}
	return &stream{
		reader:  reader,
		ref:     m.ref,
		pending: []llm.StreamEvent{{Kind: llm.StreamResponseStarted}},
	}, nil
}

func (m *Model) prepare(request llm.ChatRequest) (model.ToolCallingChatModel, []*schema.Message, []model.Option, error) {
	if err := request.Validate(); err != nil {
		return nil, nil, nil, fmt.Errorf("prepare Eino request: %w", err)
	}
	if request.Model != m.ref {
		return nil, nil, nil, fmt.Errorf("prepare Eino request: model %s/%s does not match adapter %s/%s", request.Model.Provider, request.Model.Model, m.ref.Provider, m.ref.Model)
	}
	messages, err := toEinoMessages(request)
	if err != nil {
		return nil, nil, nil, err
	}
	bound := m.inner
	if len(request.Tools) != 0 {
		definitions, err := toEinoTools(request.Tools)
		if err != nil {
			return nil, nil, nil, err
		}
		bound, err = m.inner.WithTools(definitions)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("bind Eino tools: %w", err)
		}
	}
	var options []model.Option
	if m.options != nil {
		mapped, err := m.options(request)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("map Eino request options: %w", err)
		}
		options = append(options, mapped...)
	} else {
		if request.MaxOutputTokens > 0 {
			options = append(options, model.WithMaxTokens(request.MaxOutputTokens))
		}
		if request.ThinkingLevel != "" {
			return nil, nil, nil, fmt.Errorf("prepare Eino request: thinking level %q is unsupported by this provider", request.ThinkingLevel)
		}
	}
	return bound, messages, options, nil
}

func toEinoMessages(request llm.ChatRequest) ([]*schema.Message, error) {
	messages := make([]*schema.Message, 0, len(request.Messages)+1)
	if request.SystemPrompt != "" {
		messages = append(messages, schema.SystemMessage(request.SystemPrompt))
	}
	for index, message := range request.Messages {
		converted, err := toEinoMessage(message)
		if err != nil {
			return nil, fmt.Errorf("convert Eino request message %d: %w", index, err)
		}
		messages = append(messages, converted)
	}
	return messages, nil
}

func toEinoMessage(message llm.Message) (*schema.Message, error) {
	switch message.Role {
	case llm.RoleUser:
		converted := &schema.Message{Role: schema.User}
		for _, content := range message.Content {
			switch content.Type {
			case llm.ContentText:
				converted.UserInputMultiContent = append(converted.UserInputMultiContent, schema.MessageInputPart{Type: schema.ChatMessagePartTypeText, Text: content.Text})
			case llm.ContentImage:
				encoded := base64.StdEncoding.EncodeToString(content.Data)
				converted.UserInputMultiContent = append(converted.UserInputMultiContent, schema.MessageInputPart{
					Type:  schema.ChatMessagePartTypeImageURL,
					Image: &schema.MessageInputImage{MessagePartCommon: schema.MessagePartCommon{Base64Data: &encoded, MIMEType: content.MIMEType}},
				})
			}
		}
		if len(converted.UserInputMultiContent) == 1 && converted.UserInputMultiContent[0].Type == schema.ChatMessagePartTypeText {
			converted.Content = converted.UserInputMultiContent[0].Text
			converted.UserInputMultiContent = nil
		}
		return converted, nil
	case llm.RoleAssistant:
		converted := &schema.Message{Role: schema.Assistant}
		var text, thinking strings.Builder
		for _, content := range message.Content {
			switch content.Type {
			case llm.ContentText:
				text.WriteString(content.Text)
			case llm.ContentThinking:
				if !content.Redacted {
					thinking.WriteString(content.Text)
				}
			case llm.ContentToolCall:
				converted.ToolCalls = append(converted.ToolCalls, schema.ToolCall{
					ID: content.ToolCall.ID, Type: "function",
					Function: schema.FunctionCall{Name: content.ToolCall.Name, Arguments: string(content.ToolCall.Arguments)},
				})
			}
		}
		converted.Content = text.String()
		converted.ReasoningContent = thinking.String()
		return converted, nil
	case llm.RoleTool:
		var text strings.Builder
		for _, content := range message.Content {
			if content.Type == llm.ContentImage {
				return nil, errors.New("tool-result images are unsupported by the Eino chat protocol")
			}
			text.WriteString(content.Text)
		}
		value := text.String()
		if message.IsError {
			value = "[tool_error]\n" + value
		}
		converted := schema.ToolMessage(value, message.ToolCallID)
		converted.ToolName = message.ToolName
		return converted, nil
	default:
		return nil, fmt.Errorf("unsupported role %q", message.Role)
	}
}

func toEinoTools(definitions []llm.ToolDefinition) ([]*schema.ToolInfo, error) {
	tools := make([]*schema.ToolInfo, 0, len(definitions))
	for _, definition := range definitions {
		var parameters jsonschema.Schema
		if err := json.Unmarshal(definition.InputSchema, &parameters); err != nil {
			return nil, fmt.Errorf("convert Eino tool %q schema: %w", definition.Name, err)
		}
		tools = append(tools, &schema.ToolInfo{
			Name: definition.Name, Desc: definition.Description,
			ParamsOneOf: schema.NewParamsOneOfByJSONSchema(&parameters),
		})
	}
	return tools, nil
}

func fromEinoResponse(response *schema.Message, ref llm.ModelRef) (llm.Message, error) {
	if response == nil {
		return llm.Message{}, errors.New("convert Eino response: message is nil")
	}
	if response.Role != schema.Assistant {
		return llm.Message{}, fmt.Errorf("convert Eino response: expected assistant role, got %q", response.Role)
	}
	converted := llm.Message{Role: llm.RoleAssistant, Provider: ref.Provider, Model: ref.Model, Timestamp: time.Now().UTC()}
	if response.Content != "" {
		converted.Content = append(converted.Content, llm.Content{Type: llm.ContentText, Text: response.Content})
	}
	if response.ReasoningContent != "" {
		converted.Content = append(converted.Content, llm.Content{Type: llm.ContentThinking, Text: response.ReasoningContent})
	}
	for _, call := range response.ToolCalls {
		arguments := json.RawMessage(call.Function.Arguments)
		if len(arguments) == 0 {
			arguments = json.RawMessage("{}")
		}
		converted.Content = append(converted.Content, llm.Content{Type: llm.ContentToolCall, ToolCall: &llm.ToolCall{ID: call.ID, Name: call.Function.Name, Arguments: arguments}})
	}
	if response.ResponseMeta != nil {
		converted.StopReason = normalizeStopReason(response.ResponseMeta.FinishReason)
		converted.Usage = normalizeUsage(response.ResponseMeta.Usage)
	}
	if err := converted.Validate(); err != nil {
		return llm.Message{}, fmt.Errorf("convert Eino response: %w", err)
	}
	return converted, nil
}

func normalizeStopReason(reason string) llm.StopReason {
	switch reason {
	case "stop":
		return llm.StopReasonStop
	case "length":
		return llm.StopReasonLength
	case "tool_calls", "tool_use":
		return llm.StopReasonToolUse
	case "error", "content_filter":
		return llm.StopReasonError
	default:
		return ""
	}
}

func normalizeUsage(usage *schema.TokenUsage) *llm.Usage {
	if usage == nil {
		return nil
	}
	return &llm.Usage{
		InputTokens: usage.PromptTokens, OutputTokens: usage.CompletionTokens,
		CacheReadTokens: usage.PromptTokenDetails.CachedTokens,
		ReasoningTokens: usage.CompletionTokensDetails.ReasoningTokens,
		TotalTokens:     usage.TotalTokens,
	}
}

type stream struct {
	reader    *schema.StreamReader[*schema.Message]
	ref       llm.ModelRef
	pending   []llm.StreamEvent
	chunks    []*schema.Message
	bytes     int
	sequence  uint64
	exhausted bool
}

func (s *stream) Recv() (llm.StreamEvent, error) {
	for {
		if len(s.pending) != 0 {
			event := s.pending[0]
			s.pending = s.pending[1:]
			s.sequence++
			event.Sequence = s.sequence
			return event, nil
		}
		if s.exhausted {
			return llm.StreamEvent{}, io.EOF
		}
		chunk, err := s.reader.Recv()
		if errors.Is(err, io.EOF) {
			s.exhausted = true
			if len(s.chunks) == 0 {
				return llm.StreamEvent{}, errors.New("receive Eino stream: ended without a response")
			}
			response, err := schema.ConcatMessages(s.chunks)
			if err != nil {
				return llm.StreamEvent{}, fmt.Errorf("receive Eino stream: concatenate response: %w", err)
			}
			converted, err := fromEinoResponse(response, s.ref)
			if err != nil {
				return llm.StreamEvent{}, err
			}
			s.pending = append(s.pending, llm.StreamEvent{Kind: llm.StreamResponseFinished, Message: &converted})
			continue
		}
		if err != nil {
			return llm.StreamEvent{}, provider.ClassifyTransportError("provider.stream_receive", err)
		}
		if chunk == nil {
			continue
		}
		chunkBytes := bufferedChunkBytes(chunk)
		if chunkBytes > maxStreamResponseBytes-s.bytes {
			s.exhausted = true
			s.reader.Close()
			return llm.StreamEvent{}, errors.New("receive Eino stream: response exceeded the buffered size limit")
		}
		s.bytes += chunkBytes
		s.chunks = append(s.chunks, chunk)
		if chunk.Content != "" {
			s.pending = append(s.pending, llm.StreamEvent{Kind: llm.StreamTextDelta, Delta: chunk.Content})
		}
		if chunk.ReasoningContent != "" {
			s.pending = append(s.pending, llm.StreamEvent{Kind: llm.StreamThinkingDelta, Delta: chunk.ReasoningContent})
		}
		for _, call := range chunk.ToolCalls {
			if call.ID != "" {
				s.pending = append(s.pending, llm.StreamEvent{Kind: llm.StreamToolCallDelta, ToolCallID: call.ID, ToolName: call.Function.Name, Delta: call.Function.Arguments})
			}
		}
		if chunk.ResponseMeta != nil && chunk.ResponseMeta.Usage != nil {
			s.pending = append(s.pending, llm.StreamEvent{Kind: llm.StreamUsageUpdated, Usage: normalizeUsage(chunk.ResponseMeta.Usage)})
		}
	}
}

func bufferedChunkBytes(chunk *schema.Message) int {
	if chunk == nil {
		return 0
	}
	size := len(chunk.Content) + len(chunk.ReasoningContent)
	for _, call := range chunk.ToolCalls {
		size += len(call.ID) + len(call.Type) + len(call.Function.Name) + len(call.Function.Arguments)
	}
	return size
}

func (s *stream) Close() error {
	if s.reader != nil {
		s.reader.Close()
	}
	s.exhausted = true
	return nil
}
