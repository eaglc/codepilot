package ui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/eaglc/codepilot/internal/session"
)

func TestProviderPickerConfiguresListsAndSwitches(t *testing.T) {
	controller := &testModelController{
		profiles: []session.ProviderProfile{{ID: "prv_existing", DisplayName: "Existing"}},
		configured: session.ProviderProfile{
			ID: "prv_new", Kind: "openai", DisplayName: "OpenAI", ModelID: "gpt-test",
		},
		models: []session.ModelOption{{ID: "gpt-test", DisplayName: "GPT Test", Recommended: true}},
	}
	picker := NewProviderPicker(controller)

	openCommand := picker.Open(context.Background())
	if picker.Stage() != ProviderPickerLoadingProfiles {
		t.Fatalf("unexpected opening stage: %s", picker.Stage())
	}
	picker, _ = picker.Update(openCommand())
	if picker.Stage() != ProviderPickerChooseProvider || len(picker.Profiles()) != 1 {
		t.Fatalf("profiles were not loaded: stage=%s profiles=%#v", picker.Stage(), picker.Profiles())
	}

	credential := []byte("short-lived-secret")
	configureCommand := picker.Configure(session.ConfigureProviderRequest{
		Kind:            "openai",
		CredentialInput: credential,
	})
	for _, value := range credential {
		if value != 0 {
			t.Fatal("picker retained the caller credential buffer")
		}
	}
	picker, modelCommand := picker.Update(configureCommand())
	if string(controller.credentialSeen) != "short-lived-secret" {
		t.Fatal("controller did not receive the temporary credential")
	}
	if modelCommand == nil || picker.Stage() != ProviderPickerLoadingModels {
		t.Fatalf("configuration did not start model loading: %s", picker.Stage())
	}
	picker, _ = picker.Update(modelCommand())
	if picker.Stage() != ProviderPickerChooseModel || picker.ProfileID() != "prv_new" || len(picker.Models()) != 1 {
		t.Fatalf("models were not loaded: stage=%s models=%#v", picker.Stage(), picker.Models())
	}

	switchCommand := picker.SwitchModel(session.ModelSelection{ProviderProfileID: "prv_new", ModelID: "gpt-test"})
	picker, _ = picker.Update(switchCommand())
	if picker.Stage() != ProviderPickerClosed {
		t.Fatalf("successful switch did not close picker: %s", picker.Stage())
	}
	if controller.selection.ProviderProfileID != "prv_new" || controller.selection.ModelID != "gpt-test" {
		t.Fatalf("unexpected switch selection: %#v", controller.selection)
	}
}

func TestProviderPickerCancelIgnoresStaleCommandResult(t *testing.T) {
	controller := &testModelController{listProfilesErr: context.Canceled}
	picker := NewProviderPicker(controller)
	command := picker.Open(context.Background())
	picker.Cancel()

	picker, _ = picker.Update(command())
	if picker.Stage() != ProviderPickerClosed || picker.Message() != "" {
		t.Fatalf("stale result reopened cancelled picker: stage=%s message=%q", picker.Stage(), picker.Message())
	}
}

func TestProviderPickerIgnoresSupersededModelLoad(t *testing.T) {
	controller := &testModelController{models: []session.ModelOption{{ID: "model"}}}
	picker := NewProviderPicker(controller)
	picker.Open(context.Background())
	first := picker.LoadModels("prv_first")
	second := picker.LoadModels("prv_second")

	picker, _ = picker.Update(first())
	if picker.ProfileID() != "prv_second" || len(picker.Models()) != 0 {
		t.Fatalf("stale model result replaced the active request: profile=%q models=%#v", picker.ProfileID(), picker.Models())
	}
	picker, _ = picker.Update(second())
	if picker.Stage() != ProviderPickerChooseModel || len(picker.Models()) != 1 {
		t.Fatalf("latest model result was not applied: stage=%s models=%#v", picker.Stage(), picker.Models())
	}
}

func TestProviderPickerShowsOnlySafeApplicationError(t *testing.T) {
	controller := &testModelController{listProfilesErr: errors.New("upstream included secret-value")}
	picker := NewProviderPicker(controller)
	picker, _ = picker.Update(picker.Open(context.Background())())
	if picker.Stage() != ProviderPickerFailed || picker.Message() != "Provider setup could not be completed." {
		t.Fatalf("unexpected picker error: stage=%s message=%q", picker.Stage(), picker.Message())
	}
}

func TestProviderPickerModelValidationFailureKeepsModelChoices(t *testing.T) {
	controller := &testModelController{switchErr: &session.AppError{
		Code:        session.ErrProviderUnavailable,
		UserMessage: "The selected model is unavailable.",
	}}
	picker := NewProviderPicker(controller)
	picker.stage = ProviderPickerChooseModel
	picker.profileID = "prv_existing"
	picker.models = []session.ModelOption{{ID: "first"}, {ID: "second"}}

	command := picker.SwitchModel(session.ModelSelection{ProviderProfileID: picker.profileID, ModelID: "first"})
	picker, _ = picker.Update(command())
	if picker.Stage() != ProviderPickerChooseModel || picker.Message() != "The selected model is unavailable." {
		t.Fatalf("failed selection stage=%s message=%q", picker.Stage(), picker.Message())
	}
	if view := picker.View(); !strings.Contains(view, "Error: The selected model is unavailable.") || !strings.Contains(view, "second") {
		t.Fatalf("failed selection did not retain model choices: %q", view)
	}
}

func TestProviderPickerGroupsDeduplicatesAndSelectsCurrentModel(t *testing.T) {
	now := time.Now().UTC()
	controller := &testModelController{
		profiles: []session.ProviderProfile{
			{ID: "prv_newer_duplicate", Kind: "deepseek", DisplayName: "DeepSeek", ModelID: "deepseek-v4-pro", ValidatedAt: now},
			{ID: "prv_current", Kind: "deepseek", DisplayName: "DeepSeek", ModelID: "deepseek-v4-pro", ValidatedAt: now.Add(-time.Hour)},
			{ID: "prv_openai", Kind: "openai", DisplayName: "OpenAI", ModelID: "gpt-test", ValidatedAt: now},
		},
		models: []session.ModelOption{
			{ID: "deepseek-v4-flash", DisplayName: "Flash"},
			{ID: "deepseek-v4-pro", DisplayName: "Pro"},
			{ID: "deepseek-v4-pro", DisplayName: "Duplicate Pro", Recommended: true},
		},
	}
	picker := NewProviderPicker(controller)
	command := picker.OpenForSelection(context.Background(), session.ModelSelection{
		ProviderProfileID: "prv_current",
		ModelID:           "deepseek-v4-pro",
	})
	picker.Update(command())

	if len(picker.Profiles()) != 2 {
		t.Fatalf("deduplicated profiles = %#v", picker.Profiles())
	}
	view := picker.View()
	if !strings.Contains(view, "Configured models") || !strings.Contains(view, "Add provider") {
		t.Fatalf("provider groups are not visually separated: %q", view)
	}
	if strings.Count(view, "deepseek-v4-pro") != 1 || !strings.Contains(view, "> DeepSeek  ·  deepseek-v4-pro  (current)") {
		t.Fatalf("current configured model was not deduplicated and selected: %q", view)
	}

	if command := picker.HandleKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})); command != nil {
		t.Fatal("selecting the current configured model should close without validation")
	}
	if picker.Stage() != ProviderPickerClosed {
		t.Fatalf("current configured model left picker at %s", picker.Stage())
	}
}

func TestProviderPickerSwitchesConfiguredModelWithoutSecondModelPicker(t *testing.T) {
	controller := &testModelController{profiles: []session.ProviderProfile{
		{ID: "prv_first", Kind: "openai", DisplayName: "OpenAI", ModelID: "gpt-first"},
		{ID: "prv_second", Kind: "deepseek", DisplayName: "DeepSeek", ModelID: "deepseek-second"},
	}}
	picker := NewProviderPicker(controller)
	picker.Update(picker.OpenForSelection(context.Background(), session.ModelSelection{ProviderProfileID: "prv_first", ModelID: "gpt-first"})())
	picker.HandleKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))

	command := picker.HandleKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if command == nil || picker.Stage() != ProviderPickerSwitching {
		t.Fatalf("configured model did not switch directly: stage=%s", picker.Stage())
	}
	picker.Update(command())
	if controller.selection.ProviderProfileID != "prv_second" || controller.selection.ModelID != "deepseek-second" {
		t.Fatalf("configured selection = %#v", controller.selection)
	}
	if picker.Stage() != ProviderPickerClosed {
		t.Fatalf("successful direct switch left picker at %s", picker.Stage())
	}
}

type testModelController struct {
	profiles        []session.ProviderProfile
	configured      session.ProviderProfile
	models          []session.ModelOption
	selection       session.ModelSelection
	credentialSeen  []byte
	listProfilesErr error
	configureErr    error
	listModelsErr   error
	switchErr       error
}

func (c *testModelController) ListProviderProfiles(context.Context) ([]session.ProviderProfile, error) {
	return append([]session.ProviderProfile(nil), c.profiles...), c.listProfilesErr
}

func (c *testModelController) ConfigureProvider(_ context.Context, request session.ConfigureProviderRequest) (session.ProviderProfile, error) {
	c.credentialSeen = append([]byte(nil), request.CredentialInput...)
	return c.configured, c.configureErr
}

func (c *testModelController) ListModels(context.Context, session.ProviderProfileID) ([]session.ModelOption, error) {
	return append([]session.ModelOption(nil), c.models...), c.listModelsErr
}

func (c *testModelController) SwitchModel(_ context.Context, selection session.ModelSelection) error {
	c.selection = selection
	return c.switchErr
}
