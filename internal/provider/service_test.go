package provider

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/eaglc/codepilot/internal/session"
)

func TestServiceConfigureProviderChecksCatalogAccessThenPersists(t *testing.T) {
	events := make([]string, 0, 3)
	adapter := &testAdapter{
		kind: KindOpenAI,
		defaults: Defaults{
			DisplayName:     "OpenAI",
			BaseURL:         openAIBaseURL,
			ModelID:         openAIRecommended,
			NeedsCredential: true,
		},
		list: func(request ModelListRequest) ([]Model, error) {
			events = append(events, "list_models")
			if request.BaseURL != openAIBaseURL || request.ModelID != openAIRecommended || string(request.Secret) != "test-secret" {
				t.Fatalf("unexpected model catalog request: %#v", request)
			}
			return []Model{{ID: "available-model"}}, nil
		},
	}
	profiles := newTestProfileStore(&events)
	credentials := newTestCredentialStore(&events, CredentialInKeyring)
	service := newTestService(t, profiles, credentials, adapter)
	validatedAt := time.Date(2026, 8, 20, 8, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return validatedAt }
	service.idGenerator = func() (session.ProviderProfileID, error) { return "prv_test", nil }

	profile, err := service.ConfigureProvider(context.Background(), session.ConfigureProviderRequest{
		Kind:            "openai",
		CredentialInput: []byte("test-secret"),
	})
	if err != nil {
		t.Fatalf("configure provider: %v", err)
	}
	if !reflect.DeepEqual(events, []string{"list_models", "credential.put", "profile.save"}) {
		t.Fatalf("unexpected transaction order: %#v", events)
	}
	if profile.ID != "prv_test" || profile.BaseURL != "" || profile.ModelID != openAIRecommended || profile.ValidatedAt != validatedAt {
		t.Fatalf("unexpected profile view: %#v", profile)
	}
	if profile.CredentialRef != "keyring:provider/prv_test" || profile.CredentialLocation != string(CredentialInKeyring) {
		t.Fatalf("unexpected credential metadata: %#v", profile)
	}
	persisted := profiles.values[profile.ID]
	if persisted.BaseURL != "" || persisted.CredentialRef != profile.CredentialRef {
		t.Fatalf("unexpected persisted profile: %#v", persisted)
	}
	if string(credentials.values[profile.ID]) != "test-secret" {
		t.Fatal("credential was not copied into the credential store")
	}
}

func TestServiceConfigureProviderMemoryCredentialHasNoReference(t *testing.T) {
	adapter := validOpenAIAdapter()
	profiles := newTestProfileStore(nil)
	credentials := newTestCredentialStore(nil, CredentialInMemory)
	service := newTestService(t, profiles, credentials, adapter)
	service.idGenerator = func() (session.ProviderProfileID, error) { return "prv_memory", nil }

	profile, err := service.ConfigureProvider(context.Background(), session.ConfigureProviderRequest{
		Kind:            "openai",
		CredentialInput: []byte("memory-secret"),
	})
	if err != nil {
		t.Fatalf("configure provider: %v", err)
	}
	if profile.CredentialRef != "" || profile.CredentialLocation != string(CredentialInMemory) {
		t.Fatalf("memory fallback leaked into persisted reference: %#v", profile)
	}
	if profiles.values[profile.ID].CredentialRef != "" {
		t.Fatal("memory credential reference was persisted")
	}
}

func TestServiceConfigureProviderDoesNotPersistFailedCredentialCheck(t *testing.T) {
	adapter := validOpenAIAdapter()
	adapter.listErr = &providerEndpointError{operation: "list provider models", status: 401}
	profiles := newTestProfileStore(nil)
	credentials := newTestCredentialStore(nil, CredentialInKeyring)
	service := newTestService(t, profiles, credentials, adapter)

	_, err := service.ConfigureProvider(context.Background(), session.ConfigureProviderRequest{
		Kind:            "openai",
		CredentialInput: []byte("rejected-secret"),
	})
	if err == nil {
		t.Fatal("expected provider validation error")
	}
	if len(profiles.values) != 0 || len(credentials.values) != 0 {
		t.Fatal("failed validation changed persisted state")
	}
	if err.Error() != "Provider authentication failed. Check the API key." {
		t.Fatalf("unexpected safe error: %v", err)
	}
}

func TestServiceConfigureProviderRollsBackCredential(t *testing.T) {
	events := make([]string, 0, 4)
	adapter := validOpenAIAdapter()
	adapter.list = func(ModelListRequest) ([]Model, error) {
		events = append(events, "list_models")
		return []Model{{ID: "available-model"}}, nil
	}
	profiles := newTestProfileStore(&events)
	profiles.saveErr = errors.New("disk unavailable")
	credentials := newTestCredentialStore(&events, CredentialInKeyring)
	service := newTestService(t, profiles, credentials, adapter)
	service.idGenerator = func() (session.ProviderProfileID, error) { return "prv_rollback", nil }

	_, err := service.ConfigureProvider(context.Background(), session.ConfigureProviderRequest{
		Kind:            "openai",
		CredentialInput: []byte("rollback-secret"),
	})
	if err == nil {
		t.Fatal("expected profile persistence error")
	}
	if !reflect.DeepEqual(events, []string{"list_models", "credential.put", "profile.save", "credential.delete"}) {
		t.Fatalf("unexpected rollback order: %#v", events)
	}
	if len(credentials.values) != 0 {
		t.Fatal("credential was not rolled back")
	}
}

func TestServiceListModelsAndCreateChatModelUseStoredContext(t *testing.T) {
	chatModel := &testChatModel{}
	adapter := validOpenAIAdapter()
	adapter.list = func(request ModelListRequest) ([]Model, error) {
		if request.BaseURL != openAIBaseURL || request.ModelID != "configured-model" || string(request.Secret) != "stored-secret" {
			t.Fatalf("unexpected model list request: %#v", request)
		}
		return []Model{{ID: "configured-model", DisplayName: "Configured", Recommended: true, Source: ModelSourceCatalog}}, nil
	}
	adapter.newChat = func(request ChatModelRequest) (model.ToolCallingChatModel, error) {
		if request.BaseURL != openAIBaseURL || request.ModelID != "turn-model" || string(request.Secret) != "stored-secret" {
			t.Fatalf("unexpected chat model request: %#v", request)
		}
		return chatModel, nil
	}
	profiles := newTestProfileStore(nil)
	profiles.values["prv_existing"] = Profile{
		ID: "prv_existing", Kind: KindOpenAI, DisplayName: "OpenAI", ModelID: "configured-model",
		CredentialRef: "keyring:provider/prv_existing", ValidatedAt: time.Now().UTC(),
	}
	credentials := newTestCredentialStore(nil, CredentialInKeyring)
	credentials.values["prv_existing"] = Secret("stored-secret")
	service := newTestService(t, profiles, credentials, adapter)

	models, err := service.ListModels(context.Background(), "prv_existing")
	if err != nil {
		t.Fatalf("list models: %v", err)
	}
	if len(models) != 1 || models[0].ID != "configured-model" || models[0].Source != string(ModelSourceCatalog) {
		t.Fatalf("unexpected model options: %#v", models)
	}
	created, err := service.NewChatModel(context.Background(), ModelRef{Provider: "prv_existing", Model: "turn-model"})
	if err != nil {
		t.Fatalf("create chat model: %v", err)
	}
	if created != chatModel {
		t.Fatal("service returned a different chat model")
	}
}

func TestServiceValidateSelectionPersistsOnlySuccessfulSelectedModel(t *testing.T) {
	adapter := validOpenAIAdapter()
	profiles := newTestProfileStore(nil)
	profiles.values["prv_existing"] = Profile{
		ID: "prv_existing", Kind: KindOpenAI, DisplayName: "OpenAI", ModelID: "bootstrap-model",
		CredentialRef: "keyring:provider/prv_existing", ValidatedAt: time.Now().UTC().Add(-time.Hour),
	}
	credentials := newTestCredentialStore(nil, CredentialInKeyring)
	credentials.values["prv_existing"] = Secret("stored-secret")
	service := newTestService(t, profiles, credentials, adapter)
	validatedAt := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return validatedAt }

	adapter.validation = ValidationResult{Stage: ValidationStageModel, UserMessage: "The selected model is unavailable."}
	result, err := service.ValidateSelection(context.Background(), session.ModelSelection{ProviderProfileID: "prv_existing", ModelID: "bad-model"})
	if err != nil || result.Valid || profiles.values["prv_existing"].ModelID != "bootstrap-model" {
		t.Fatalf("failed validation changed profile: result=%#v err=%v profile=%#v", result, err, profiles.values["prv_existing"])
	}

	adapter.validation = ValidationResult{Valid: true}
	result, err = service.ValidateSelection(context.Background(), session.ModelSelection{ProviderProfileID: "prv_existing", ModelID: "selected-model"})
	if err != nil || !result.Valid {
		t.Fatalf("validate selected model: result=%#v err=%v", result, err)
	}
	persisted := profiles.values["prv_existing"]
	if persisted.ModelID != "selected-model" || persisted.ValidatedAt != validatedAt {
		t.Fatalf("selected model was not persisted: %#v", persisted)
	}
}

func TestServiceRejectsInvalidConfigurationAsInvalidInput(t *testing.T) {
	service := newTestService(t, newTestProfileStore(nil), newTestCredentialStore(nil, CredentialInMemory), validOpenAIAdapter())
	_, err := service.ConfigureProvider(context.Background(), session.ConfigureProviderRequest{Kind: "unsupported"})
	var appError *session.AppError
	if !errors.As(err, &appError) || appError.Code != session.ErrInvalidInput {
		t.Fatalf("expected invalid-input AppError, got %v", err)
	}
}

func validOpenAIAdapter() *testAdapter {
	return &testAdapter{
		kind: KindOpenAI,
		defaults: Defaults{
			DisplayName:     "OpenAI",
			BaseURL:         openAIBaseURL,
			ModelID:         openAIRecommended,
			NeedsCredential: true,
		},
		validation: ValidationResult{Valid: true},
		models:     []Model{{ID: openAIRecommended, DisplayName: openAIRecommended}},
	}
}

func newTestService(t *testing.T, profiles *testProfileStore, credentials *testCredentialStore, adapter Adapter) *Service {
	t.Helper()
	service, err := NewService(profiles, credentials, []Adapter{adapter})
	if err != nil {
		t.Fatalf("create provider service: %v", err)
	}
	return service
}

type testProfileStore struct {
	values  map[session.ProviderProfileID]Profile
	events  *[]string
	saveErr error
}

func newTestProfileStore(events *[]string) *testProfileStore {
	return &testProfileStore{values: make(map[session.ProviderProfileID]Profile), events: events}
}

func (s *testProfileStore) ListProfiles(context.Context) ([]Profile, error) {
	values := make([]Profile, 0, len(s.values))
	for _, value := range s.values {
		values = append(values, value)
	}
	return values, nil
}

func (s *testProfileStore) LoadProfile(_ context.Context, id session.ProviderProfileID) (Profile, error) {
	value, found := s.values[id]
	if !found {
		return Profile{}, &session.AppError{Code: session.ErrNotFound, UserMessage: "Provider profile was not found."}
	}
	return value, nil
}

func (s *testProfileStore) SaveProfile(_ context.Context, value Profile) error {
	if s.events != nil {
		*s.events = append(*s.events, "profile.save")
	}
	if s.saveErr != nil {
		return s.saveErr
	}
	s.values[value.ID] = value
	return nil
}

func (s *testProfileStore) DeleteProfile(_ context.Context, id session.ProviderProfileID) error {
	delete(s.values, id)
	return nil
}

type testCredentialStore struct {
	values    map[session.ProviderProfileID]Secret
	events    *[]string
	location  CredentialLocation
	putErr    error
	deleteErr error
}

func newTestCredentialStore(events *[]string, location CredentialLocation) *testCredentialStore {
	return &testCredentialStore{values: make(map[session.ProviderProfileID]Secret), events: events, location: location}
}

func (s *testCredentialStore) Get(_ context.Context, id session.ProviderProfileID) (Secret, bool, error) {
	value, found := s.values[id]
	return append(Secret(nil), value...), found, nil
}

func (s *testCredentialStore) Put(_ context.Context, id session.ProviderProfileID, secret Secret) (CredentialLocation, error) {
	if s.events != nil {
		*s.events = append(*s.events, "credential.put")
	}
	if s.putErr != nil {
		return "", s.putErr
	}
	s.values[id] = append(Secret(nil), secret...)
	return s.location, nil
}

func (s *testCredentialStore) Delete(_ context.Context, id session.ProviderProfileID) error {
	if s.events != nil {
		*s.events = append(*s.events, "credential.delete")
	}
	if s.deleteErr != nil {
		return s.deleteErr
	}
	delete(s.values, id)
	return nil
}

type testChatModel struct{}

func (m *testChatModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return schema.AssistantMessage("ok", nil), nil
}

func (m *testChatModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, nil
}

func (m *testChatModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}
