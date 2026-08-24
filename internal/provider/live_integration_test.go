package provider_test

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/eaglc/codepilot/internal/llm"
	"github.com/eaglc/codepilot/internal/provider"
	deepseekadapter "github.com/eaglc/codepilot/internal/provider/deepseek"
	ollamaadapter "github.com/eaglc/codepilot/internal/provider/ollama"
	openaiadapter "github.com/eaglc/codepilot/internal/provider/openai"
)

// These tests are intentionally part of the normal package while remaining
// strictly opt-in. Merely having an API key or Ollama installed must never make
// `go test ./...` perform network I/O or spend model tokens.
func TestLiveProviderCatalogCompleteAndStream(t *testing.T) {
	if testing.Short() {
		t.Skip("live Provider tests are disabled in short mode")
	}
	client := &http.Client{Timeout: 2 * time.Minute}
	tests := []liveProviderTest{
		{
			name: "openai", enableEnv: "CODEPILOT_LIVE_OPENAI", keyEnv: "OPENAI_API_KEY",
			baseURLEnv: "CODEPILOT_LIVE_OPENAI_BASE_URL", modelEnv: "CODEPILOT_LIVE_OPENAI_MODEL",
			kind: provider.KindOpenAI, defaultModel: openaiadapter.Defaults().ModelID,
			adapter: openaiadapter.New(client),
		},
		{
			name: "deepseek", enableEnv: "CODEPILOT_LIVE_DEEPSEEK", keyEnv: "DEEPSEEK_API_KEY",
			baseURLEnv: "CODEPILOT_LIVE_DEEPSEEK_BASE_URL", modelEnv: "CODEPILOT_LIVE_DEEPSEEK_MODEL",
			kind: provider.KindDeepSeek, defaultModel: deepseekadapter.Defaults().ModelID,
			adapter: deepseekadapter.New(client),
		},
		{
			name: "ollama", enableEnv: "CODEPILOT_LIVE_OLLAMA",
			baseURLEnv: "CODEPILOT_LIVE_OLLAMA_URL", modelEnv: "CODEPILOT_LIVE_OLLAMA_MODEL",
			kind: provider.KindOllama, defaultModel: ollamaadapter.Defaults().ModelID,
			adapter: ollamaadapter.New(client),
		},
	}
	enabled := 0
	for _, test := range tests {
		test := test
		if !liveEnabled(os.Getenv(test.enableEnv)) {
			continue
		}
		enabled++
		t.Run(test.name, func(t *testing.T) { test.run(t) })
	}
	if enabled == 0 {
		t.Skip("set a CODEPILOT_LIVE_OPENAI, CODEPILOT_LIVE_DEEPSEEK, or CODEPILOT_LIVE_OLLAMA switch to run live probes")
	}
}

type liveProviderTest struct {
	name, enableEnv, keyEnv, baseURLEnv, modelEnv string
	kind                                          provider.Kind
	defaultModel                                  string
	adapter                                       provider.Adapter
}

func (test liveProviderTest) run(t *testing.T) {
	t.Helper()
	credential := provider.Credential([]byte(strings.TrimSpace(os.Getenv(test.keyEnv))))
	defer wipeLiveCredential(credential)
	if test.keyEnv != "" && len(credential) == 0 {
		t.Fatalf("%s=1 requires %s", test.enableEnv, test.keyEnv)
	}
	modelID := strings.TrimSpace(os.Getenv(test.modelEnv))
	if modelID == "" {
		modelID = test.defaultModel
	}
	profile := provider.Profile{
		ID: provider.ProfileID("live-" + test.name), Kind: test.kind, DisplayName: "Live " + test.name,
		BaseURL: strings.TrimSpace(os.Getenv(test.baseURLEnv)), DefaultModel: modelID,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	models, err := test.adapter.ListModels(ctx, profile, credential)
	if err != nil {
		t.Fatalf("list live %s models: %v", test.name, err)
	}
	if len(models) == 0 || !containsLiveModel(models, modelID) {
		t.Fatalf("live %s model %q was not returned by the catalog (%d model(s))", test.name, modelID, len(models))
	}
	model, err := test.adapter.CreateModel(ctx, provider.ModelConfig{Profile: profile, ModelID: modelID, Credential: credential})
	if err != nil {
		t.Fatalf("create live %s model: %v", test.name, err)
	}
	request := liveRequest(profile.ID, modelID, "Reply briefly with CODEPILOT_OK.")
	response, err := model.Complete(ctx, request)
	if err != nil {
		t.Fatalf("complete live %s request: %v", test.name, err)
	}
	assertLiveResponse(t, test.name, profile.ID, modelID, response)

	request = liveRequest(profile.ID, modelID, "Reply briefly with CODEPILOT_STREAM_OK.")
	stream, err := model.Stream(ctx, request)
	if err != nil {
		t.Fatalf("start live %s stream: %v", test.name, err)
	}
	response, err = llm.CollectStream(stream)
	if err != nil {
		t.Fatalf("collect live %s stream: %v", test.name, err)
	}
	assertLiveResponse(t, test.name, profile.ID, modelID, response)
}

func liveRequest(profileID provider.ProfileID, modelID, prompt string) llm.ChatRequest {
	return llm.ChatRequest{
		Model: llm.ModelRef{Provider: string(profileID), Model: modelID}, MaxOutputTokens: 128,
		Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentText, Text: prompt}}, Timestamp: time.Now().UTC()}},
	}
}

func assertLiveResponse(t *testing.T, name string, profileID provider.ProfileID, modelID string, response llm.Message) {
	t.Helper()
	if err := response.Validate(); err != nil {
		t.Fatalf("validate live %s response: %v", name, err)
	}
	if response.Role != llm.RoleAssistant || response.Provider != string(profileID) || response.Model != modelID {
		t.Fatalf("unexpected live %s response identity: role=%s provider=%q model=%q", name, response.Role, response.Provider, response.Model)
	}
}

func containsLiveModel(models []llm.Model, requested string) bool {
	for _, model := range models {
		available := strings.TrimSpace(model.Ref.Model)
		if available == requested || available == requested+":latest" || requested == available+":latest" {
			return true
		}
	}
	return false
}

func liveEnabled(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func wipeLiveCredential(value provider.Credential) {
	for index := range value {
		value[index] = 0
	}
}
