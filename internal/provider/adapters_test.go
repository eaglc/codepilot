package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestOpenAIAdapterListsModelsWithoutLeakingResponseDetails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/models" {
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatal("missing bearer credential")
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"data":[{"id":"z-model"},{"id":"a-model"},{"id":"a-model"}]}`))
	}))
	defer server.Close()

	adapter := NewOpenAIAdapter(server.Client())
	models, err := adapter.ListModels(context.Background(), ModelListRequest{
		BaseURL: server.URL,
		ModelID: "configured-model",
		Secret:  Secret("test-key"),
	})
	if err != nil {
		t.Fatalf("list OpenAI models: %v", err)
	}
	ids := make([]string, 0, len(models))
	for _, value := range models {
		ids = append(ids, value.ID)
	}
	if !reflect.DeepEqual(ids, []string{"a-model", "z-model"}) {
		t.Fatalf("unexpected models: %#v", models)
	}
	for _, value := range models {
		if value.Recommended {
			t.Fatalf("unavailable configured model marked a remote option as recommended: %#v", models)
		}
	}
}

func TestCompatibleAdapterFallsBackWhenModelsEndpointIsUnsupported(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()

	adapter := NewCompatibleAdapter(server.Client())
	models, err := adapter.ListModels(context.Background(), ModelListRequest{
		BaseURL: server.URL,
		ModelID: "custom-model",
		Secret:  Secret("test-key"),
	})
	if err != nil {
		t.Fatalf("list compatible models: %v", err)
	}
	if len(models) != 1 || models[0].ID != "custom-model" || models[0].Source != ModelSourceConfigured {
		t.Fatalf("unexpected fallback model: %#v", models)
	}
}

func TestOllamaAdapterListsModelsDeterministically(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/tags" {
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"models":[{"name":"z:latest"},{"model":"a:latest"},{"name":"a:latest"}]}`))
	}))
	defer server.Close()

	adapter := NewOllamaAdapter(server.Client())
	models, err := adapter.ListModels(context.Background(), ModelListRequest{BaseURL: server.URL, ModelID: "qwen-coder"})
	if err != nil {
		t.Fatalf("list Ollama models: %v", err)
	}
	ids := make([]string, 0, len(models))
	for _, value := range models {
		ids = append(ids, value.ID)
	}
	if !reflect.DeepEqual(ids, []string{"a:latest", "z:latest"}) {
		t.Fatalf("unexpected models: %#v", models)
	}
}

func TestDeepSeekAdapterProbesV4WithoutToolChoice(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/chat/completions" {
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
		var payload struct {
			Model      string            `json:"model"`
			Tools      []json.RawMessage `json:"tools"`
			ToolChoice json.RawMessage   `json:"tool_choice"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatalf("decode DeepSeek probe: %v", err)
		}
		if payload.Model != "deepseek-v4-flash" {
			t.Fatalf("probe model = %q", payload.Model)
		}
		if len(payload.Tools) != 1 {
			t.Fatalf("probe tools = %d, want 1", len(payload.Tools))
		}
		if len(payload.ToolChoice) != 0 {
			t.Fatalf("DeepSeek V4 thinking probe included tool_choice: %s", payload.ToolChoice)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{
			"id":"probe-response",
			"object":"chat.completion",
			"created":1,
			"model":"deepseek-v4-flash",
			"choices":[{
				"index":0,
				"message":{
					"role":"assistant",
					"content":"",
					"reasoning_content":"The requested tool confirms function calling.",
					"tool_calls":[{
						"id":"call_probe",
						"type":"function",
						"function":{"name":"codepilot_provider_probe","arguments":"{}"}
					}]
				},
				"finish_reason":"tool_calls"
			}],
			"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
		}`))
	}))
	defer server.Close()

	adapter := NewDeepSeekAdapter(server.Client())
	result, err := adapter.Validate(context.Background(), ValidationRequest{
		BaseURL: server.URL,
		ModelID: "deepseek-v4-flash",
		Secret:  Secret("test-key"),
	})
	if err != nil || !result.Valid {
		t.Fatalf("DeepSeek V4 probe failed: result=%#v err=%v", result, err)
	}
	if defaults := adapter.Defaults(); defaults.ModelID != "deepseek-v4-flash" {
		t.Fatalf("DeepSeek default model = %q", defaults.ModelID)
	}
}

func TestValidationFromErrorUsesWrappedProviderCause(t *testing.T) {
	tests := []struct {
		name    string
		cause   error
		message string
	}{
		{name: "authentication", cause: &providerEndpointError{operation: "probe", status: 401}, message: "Provider authentication failed. Check the API key."},
		{name: "model", cause: &providerEndpointError{operation: "probe", status: 404}, message: "The selected model is unavailable."},
		{name: "bad request", cause: &providerEndpointError{operation: "probe", status: 400}, message: "The provider rejected the selected model validation request."},
		{name: "tool request", cause: errors.New("HTTP 400: tool_choice is unsupported"), message: "The provider rejected the tool-calling validation request."},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := validationFromError(&adapterOperationError{operation: "validate model", cause: test.cause})
			if result.UserMessage != test.message {
				t.Fatalf("validation message = %q, want %q", result.UserMessage, test.message)
			}
		})
	}
}

func TestProbeToolCallingHandlesSuccessAndEmptyResponse(t *testing.T) {
	success := &probeChatModel{response: &schema.Message{ToolCalls: []schema.ToolCall{{Function: schema.FunctionCall{Name: providerProbeToolName}}}}}
	result, err := probeToolCalling(context.Background(), success)
	if err != nil || !result.Valid {
		t.Fatalf("expected successful probe, result=%#v err=%v", result, err)
	}

	empty := &probeChatModel{}
	result, err = probeToolCalling(context.Background(), empty)
	if err != nil || result.Valid || result.Stage != ValidationStageToolCalling {
		t.Fatalf("expected safe empty-response failure, result=%#v err=%v", result, err)
	}
}

type probeChatModel struct {
	response *schema.Message
}

func (m *probeChatModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return m.response, nil
}

func (m *probeChatModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, nil
}

func (m *probeChatModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}
