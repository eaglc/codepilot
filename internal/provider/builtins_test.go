package provider_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/eaglc/codepilot/internal/llm"
	"github.com/eaglc/codepilot/internal/provider"
	deepseekadapter "github.com/eaglc/codepilot/internal/provider/deepseek"
	"github.com/eaglc/codepilot/internal/provider/internal/builtin"
	ollamaadapter "github.com/eaglc/codepilot/internal/provider/ollama"
	openaiadapter "github.com/eaglc/codepilot/internal/provider/openai"
)

func TestBuiltinAdaptersCreateModelsAndDiscoverCatalog(t *testing.T) {
	tests := []struct {
		kind       provider.Kind
		adapter    func(*http.Client) provider.Adapter
		defaults   builtin.Defaults
		credential provider.Credential
	}{
		{kind: provider.KindOpenAI, adapter: func(client *http.Client) provider.Adapter { return openaiadapter.New(client) }, defaults: openaiadapter.Defaults(), credential: provider.Credential("token")},
		{kind: provider.KindDeepSeek, adapter: func(client *http.Client) provider.Adapter { return deepseekadapter.New(client) }, defaults: deepseekadapter.Defaults(), credential: provider.Credential("token")},
		{kind: provider.KindOllama, adapter: func(client *http.Client) provider.Adapter { return ollamaadapter.New(client) }, defaults: ollamaadapter.Defaults()},
	}
	for _, test := range tests {
		t.Run(string(test.kind), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				writer.Header().Set("Content-Type", "application/json")
				if test.kind == provider.KindOllama {
					if request.URL.Path == "/api/show" {
						_, _ = writer.Write([]byte(`{"model_info":{"general.architecture":"qwen2","qwen2.context_length":131072}}`))
						return
					}
					_, _ = writer.Write([]byte(`{"models":[{"name":"` + test.defaults.ModelID + `"}]}`))
					return
				}
				if request.Header.Get("Authorization") != "Bearer token" {
					writer.WriteHeader(http.StatusUnauthorized)
					return
				}
				_, _ = writer.Write([]byte(`{"data":[{"id":"` + test.defaults.ModelID + `"}]}`))
			}))
			defer server.Close()
			adapter := test.adapter(server.Client())
			profile := provider.Profile{ID: provider.ProfileID("profile-" + test.kind), Kind: test.kind, BaseURL: server.URL, DefaultModel: test.defaults.ModelID}
			created, err := adapter.CreateModel(context.Background(), provider.ModelConfig{Profile: profile, ModelID: test.defaults.ModelID, Credential: test.credential})
			if err != nil {
				t.Fatalf("create model: %v", err)
			}
			if created == nil {
				t.Fatal("created model is nil")
			}
			models, err := adapter.ListModels(context.Background(), profile, test.credential)
			if err != nil {
				t.Fatalf("list configured model: %v", err)
			}
			if len(models) != 1 || models[0].Ref != (llm.ModelRef{Provider: string(profile.ID), Model: test.defaults.ModelID}) {
				t.Fatalf("models = %#v", models)
			}
			if models[0].ContextWindow <= 0 || models[0].MaxOutput <= 0 || models[0].Tokenizer.ID == "" || models[0].Tokenizer.Source == "" {
				t.Fatalf("model capabilities were not discovered: %#v", models[0])
			}
		})
	}
}
