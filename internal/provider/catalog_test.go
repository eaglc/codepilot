package provider

import (
	"context"
	"testing"

	"github.com/cloudwego/eino/components/model"
)

func TestCatalogPreservesRegistrationOrder(t *testing.T) {
	first := &testAdapter{kind: KindOllama, defaults: Defaults{DisplayName: "Ollama", BaseURL: ollamaBaseURL, ModelID: ollamaRecommended}}
	second := &testAdapter{kind: KindOpenAI, defaults: Defaults{DisplayName: "OpenAI", BaseURL: openAIBaseURL, ModelID: openAIRecommended}}

	catalog, err := NewCatalog([]Adapter{first, second})
	if err != nil {
		t.Fatalf("create catalog: %v", err)
	}
	kinds := catalog.Kinds()
	if len(kinds) != 2 || kinds[0] != KindOllama || kinds[1] != KindOpenAI {
		t.Fatalf("unexpected catalog order: %#v", kinds)
	}
	kinds[0] = KindDeepSeek
	if catalog.Kinds()[0] != KindOllama {
		t.Fatal("Kinds exposed the catalog backing slice")
	}
}

func TestCatalogRejectsDuplicateAndNilAdapters(t *testing.T) {
	valid := &testAdapter{kind: KindOllama, defaults: Defaults{DisplayName: "Ollama", BaseURL: ollamaBaseURL, ModelID: ollamaRecommended}}
	tests := []struct {
		name     string
		adapters []Adapter
	}{
		{name: "none"},
		{name: "nil", adapters: []Adapter{nil}},
		{name: "duplicate", adapters: []Adapter{valid, valid}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewCatalog(test.adapters); err == nil {
				t.Fatal("expected catalog validation error")
			}
		})
	}
}

type testAdapter struct {
	kind        Kind
	defaults    Defaults
	validation  ValidationResult
	validateErr error
	models      []Model
	listErr     error
	chatModel   model.ToolCallingChatModel
	chatErr     error
	validate    func(ValidationRequest) (ValidationResult, error)
	list        func(ModelListRequest) ([]Model, error)
	newChat     func(ChatModelRequest) (model.ToolCallingChatModel, error)
}

func (a *testAdapter) Kind() Kind {
	return a.kind
}

func (a *testAdapter) Defaults() Defaults {
	return a.defaults
}

func (a *testAdapter) Validate(_ context.Context, request ValidationRequest) (ValidationResult, error) {
	if a.validate != nil {
		return a.validate(request)
	}
	return a.validation, a.validateErr
}

func (a *testAdapter) ListModels(_ context.Context, request ModelListRequest) ([]Model, error) {
	if a.list != nil {
		return a.list(request)
	}
	return append([]Model(nil), a.models...), a.listErr
}

func (a *testAdapter) NewChatModel(_ context.Context, request ChatModelRequest) (model.ToolCallingChatModel, error) {
	if a.newChat != nil {
		return a.newChat(request)
	}
	return a.chatModel, a.chatErr
}
