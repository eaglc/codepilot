package einoadapter

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/eaglc/codepilot/internal/llm"
	"github.com/eaglc/codepilot/internal/provider"
)

type fakeModel struct {
	input        []*schema.Message
	tools        []*schema.ToolInfo
	generateErr  error
	streamErr    error
	streamChunks []*schema.Message
}

func (m *fakeModel) Generate(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	m.input = input
	if m.generateErr != nil {
		return nil, m.generateErr
	}
	return schema.AssistantMessage("done", nil), nil
}

func (m *fakeModel) Stream(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.input = input
	if m.streamErr != nil {
		return nil, m.streamErr
	}
	chunks := m.streamChunks
	if chunks == nil {
		chunks = []*schema.Message{
			{Role: schema.Assistant, Content: "do"},
			{Role: schema.Assistant, Content: "ne", ToolCalls: []schema.ToolCall{{ID: "call-1", Type: "function", Function: schema.FunctionCall{Name: "read", Arguments: "{}"}}}},
		}
	}
	return schema.StreamReaderFromArray(chunks), nil
}

func TestRuntimeProviderErrorsAreClassifiedWithoutSDKDetails(t *testing.T) {
	inner := &fakeModel{generateErr: errors.New("status_code=401 secret-sdk-detail")}
	ref := llm.ModelRef{Provider: "profile", Model: "model"}
	wrapped, err := New(inner, ref, nil)
	if err != nil {
		t.Fatalf("new adapter: %v", err)
	}
	_, err = wrapped.Complete(context.Background(), llm.ChatRequest{
		Model: ref, Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentText, Text: "hello"}}}},
	})
	code, message, _, ok := provider.ErrorInfo(err)
	if !ok || code != provider.ErrorAuthenticationFailed || message == "" {
		t.Fatalf("unexpected Provider error: code=%q message=%q ok=%v err=%v", code, message, ok, err)
	}
	if errors.Is(err, inner.generateErr) && err.Error() == inner.generateErr.Error() {
		t.Fatalf("SDK error was exposed directly: %v", err)
	}
}

func (m *fakeModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	m.tools = tools
	return m, nil
}

func TestStreamNormalizesMessagesToolsAndTerminalResponse(t *testing.T) {
	inner := &fakeModel{}
	ref := llm.ModelRef{Provider: "profile", Model: "model"}
	wrapped, err := New(inner, ref, nil)
	if err != nil {
		t.Fatalf("new adapter: %v", err)
	}
	request := llm.ChatRequest{
		Model: ref, SystemPrompt: "policy",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentText, Text: "hello"}}}},
		Tools:    []llm.ToolDefinition{{Name: "read", Description: "read a file", InputSchema: []byte(`{"type":"object","properties":{}}`)}},
	}
	stream, err := wrapped.Stream(context.Background(), request)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	response, err := llm.CollectStream(stream)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(inner.input) != 2 || inner.input[0].Role != schema.System || inner.input[1].Content != "hello" {
		t.Fatalf("converted input = %#v", inner.input)
	}
	if len(inner.tools) != 1 || inner.tools[0].Name != "read" {
		t.Fatalf("converted tools = %#v", inner.tools)
	}
	if response.Provider != "profile" || response.Model != "model" || response.Content[0].Text != "done" {
		t.Fatalf("response = %#v", response)
	}
	calls := response.ToolCalls()
	if len(calls) != 1 || calls[0].ID != "call-1" || calls[0].Name != "read" {
		t.Fatalf("tool calls = %#v", calls)
	}
}

func TestStreamRejectsUnboundedBufferedResponse(t *testing.T) {
	inner := &fakeModel{streamChunks: []*schema.Message{{Role: schema.Assistant, Content: strings.Repeat("x", maxStreamResponseBytes+1)}}}
	ref := llm.ModelRef{Provider: "profile", Model: "model"}
	wrapped, err := New(inner, ref, nil)
	if err != nil {
		t.Fatal(err)
	}
	stream, err := wrapped.Stream(context.Background(), llm.ChatRequest{
		Model: ref, Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentText, Text: "hello"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if event, recvErr := stream.Recv(); recvErr != nil || event.Kind != llm.StreamResponseStarted {
		t.Fatalf("start event = %#v, %v", event, recvErr)
	}
	if _, recvErr := stream.Recv(); recvErr == nil || !strings.Contains(recvErr.Error(), "buffered size limit") {
		t.Fatalf("oversized stream error = %v", recvErr)
	}
}

func TestToolResultErrorIsMadeVisibleToTheModel(t *testing.T) {
	converted, err := toEinoMessage(llm.Message{
		Role: llm.RoleTool, ToolCallID: "call-1", ToolName: "read", IsError: true,
		Content: []llm.Content{{Type: llm.ContentText, Text: "permission denied"}},
	})
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if converted.Content != "[tool_error]\npermission denied" {
		t.Fatalf("tool content = %q", converted.Content)
	}
}
