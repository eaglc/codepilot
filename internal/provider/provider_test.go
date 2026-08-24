package provider

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/eaglc/codepilot/internal/llm"
)

func TestValidateCredentialReferenceUsesSharedBoundedFormat(t *testing.T) {
	tests := []struct {
		value   string
		wantErr bool
	}{
		{value: "keyring:openai/default"},
		{value: "", wantErr: true},
		{value: "bad reference", wantErr: true},
		{value: strings.Repeat("a", 257), wantErr: true},
	}
	for _, test := range tests {
		if err := ValidateCredentialReference(test.value); (err != nil) != test.wantErr {
			t.Fatalf("ValidateCredentialReference(%q) error = %v, wantErr %v", test.value, err, test.wantErr)
		}
	}
}

type testAdapter struct {
	profileID  ProfileID
	modelID    string
	credential string
	models     []llm.Model
	listError  error
	listCalls  int
}

func (*testAdapter) Kind() Kind { return "test" }

func (a *testAdapter) ListModels(_ context.Context, profile Profile, _ Credential) ([]llm.Model, error) {
	a.listCalls++
	if a.listError != nil {
		return nil, a.listError
	}
	if a.models != nil {
		return append([]llm.Model(nil), a.models...), nil
	}
	return []llm.Model{{Ref: llm.ModelRef{Provider: string(profile.ID), Model: "model"}}}, nil
}

func TestDescribeModelCachesDiscoveredCapabilities(t *testing.T) {
	profiles := NewMemoryProfileRepository()
	credentials := NewMemoryCredentialStore()
	if err := profiles.SaveProfile(context.Background(), Profile{ID: "profile-1", Kind: "test"}); err != nil {
		t.Fatalf("save profile: %v", err)
	}
	adapter := &testAdapter{models: []llm.Model{{
		Ref: llm.ModelRef{Provider: "profile-1", Model: "model-a"}, ContextWindow: 200_000, MaxOutput: 10_000,
		Tokenizer: llm.TokenizerMetadata{ID: "test-tokenizer", Source: "test"},
	}}}
	service, err := NewService(profiles, credentials, adapter)
	if err != nil {
		t.Fatalf("create service: %v", err)
	}
	first, err := service.DescribeModel(context.Background(), llm.ModelRef{Provider: "profile-1", Model: "model-a"})
	if err != nil {
		t.Fatalf("describe model: %v", err)
	}
	second, err := service.DescribeModel(context.Background(), first.Ref)
	if err != nil {
		t.Fatalf("describe cached model: %v", err)
	}
	if adapter.listCalls != 1 || second.ContextWindow != 200_000 || second.Tokenizer.ID != "test-tokenizer" {
		t.Fatalf("cached model = %#v; list calls = %d", second, adapter.listCalls)
	}
}

func (a *testAdapter) CreateModel(_ context.Context, config ModelConfig) (llm.ChatModel, error) {
	a.profileID = config.Profile.ID
	a.modelID = config.ModelID
	a.credential = string(config.Credential)
	return testChatModel{}, nil
}

type testChatModel struct{}

func (testChatModel) Complete(context.Context, llm.ChatRequest) (llm.Message, error) {
	return llm.Message{}, nil
}

func (testChatModel) Stream(context.Context, llm.ChatRequest) (llm.Stream, error) {
	return emptyStream{}, nil
}

type emptyStream struct{}

func (emptyStream) Recv() (llm.StreamEvent, error) { return llm.StreamEvent{}, io.EOF }
func (emptyStream) Close() error                   { return nil }

func TestServiceResolvesProfileWithoutLeakingCredentialIntoModelRef(t *testing.T) {
	profiles := NewMemoryProfileRepository()
	credentials := NewMemoryCredentialStore()
	if err := profiles.SaveProfile(context.Background(), Profile{ID: "profile-1", Kind: "test", CredentialRef: "secret-1"}); err != nil {
		t.Fatalf("save profile: %v", err)
	}
	if err := credentials.SaveCredential(context.Background(), "secret-1", Credential("token")); err != nil {
		t.Fatalf("save credential: %v", err)
	}
	adapter := &testAdapter{}
	service, err := NewService(profiles, credentials, adapter)
	if err != nil {
		t.Fatalf("create service: %v", err)
	}
	if _, err := service.CreateModel(context.Background(), llm.ModelRef{Provider: "profile-1", Model: "model-a"}); err != nil {
		t.Fatalf("create model: %v", err)
	}
	if adapter.profileID != "profile-1" || adapter.modelID != "model-a" || adapter.credential != "token" {
		t.Fatalf("adapter values = %q, %q, %q", adapter.profileID, adapter.modelID, adapter.credential)
	}
}

func TestServiceSavesOverwritesAndDeletesCredential(t *testing.T) {
	profiles := NewMemoryProfileRepository()
	credentials := NewMemoryCredentialStore()
	service, err := NewService(profiles, credentials)
	if err != nil {
		t.Fatalf("create service: %v", err)
	}
	ctx := context.Background()
	if err := service.SaveCredential(ctx, "profile-1", Credential("first")); err != nil {
		t.Fatalf("save credential: %v", err)
	}
	if err := service.SaveCredential(ctx, "profile-1", Credential("second")); err != nil {
		t.Fatalf("overwrite credential: %v", err)
	}
	value, found, err := credentials.LoadCredential(ctx, "profile-1")
	if err != nil || !found || string(value) != "second" {
		t.Fatalf("load saved credential: value=%q found=%v err=%v", value, found, err)
	}
	if err := service.DeleteCredential(ctx, "profile-1"); err != nil {
		t.Fatalf("delete credential: %v", err)
	}
	if _, found, err := credentials.LoadCredential(ctx, "profile-1"); err != nil || found {
		t.Fatalf("load deleted credential: found=%v err=%v", found, err)
	}
}

func TestServicePreflightPersistsSuccessfulValidation(t *testing.T) {
	profiles := NewMemoryProfileRepository()
	credentials := NewMemoryCredentialStore()
	profile := Profile{ID: "profile-1", Kind: "test", DisplayName: "Test", DefaultModel: "model-a"}
	if err := profiles.SaveProfile(context.Background(), profile); err != nil {
		t.Fatalf("save profile: %v", err)
	}
	adapter := &testAdapter{models: []llm.Model{{Ref: llm.ModelRef{Provider: "profile-1", Model: "model-a"}}}}
	service, err := NewService(profiles, credentials, adapter)
	if err != nil {
		t.Fatalf("create service: %v", err)
	}
	result, err := service.Preflight(context.Background(), llm.ModelRef{Provider: "profile-1", Model: "model-a"})
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if result.ProfileID != profile.ID || result.ModelID != "model-a" || result.ValidatedAt.IsZero() {
		t.Fatalf("unexpected preflight result: %#v", result)
	}
	stored, err := profiles.LoadProfile(context.Background(), profile.ID)
	if err != nil || stored.ValidatedAt != result.ValidatedAt {
		t.Fatalf("validation time was not persisted: profile=%#v err=%v", stored, err)
	}
}

func TestServicePreflightClassifiesMissingCredentialAndModel(t *testing.T) {
	tests := []struct {
		name          string
		credentialRef string
		models        []llm.Model
		wantCode      ErrorCode
	}{
		{name: "credential", credentialRef: "missing", wantCode: ErrorCredentialMissing},
		{name: "model", models: []llm.Model{{Ref: llm.ModelRef{Provider: "profile-1", Model: "another-model"}}}, wantCode: ErrorModelNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			profiles := NewMemoryProfileRepository()
			if err := profiles.SaveProfile(context.Background(), Profile{ID: "profile-1", Kind: "test", CredentialRef: test.credentialRef}); err != nil {
				t.Fatalf("save profile: %v", err)
			}
			service, err := NewService(profiles, NewMemoryCredentialStore(), &testAdapter{models: test.models})
			if err != nil {
				t.Fatalf("create service: %v", err)
			}
			_, err = service.Preflight(context.Background(), llm.ModelRef{Provider: "profile-1", Model: "model-a"})
			code, _, _, ok := ErrorInfo(err)
			if !ok || code != test.wantCode {
				t.Fatalf("preflight error code = %q, ok=%v, err=%v", code, ok, err)
			}
		})
	}
}
