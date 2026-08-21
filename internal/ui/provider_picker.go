package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/eaglc/codepilot/internal/session"
)

// ProviderPickerStage describes the visible step in the provider/model flow.
type ProviderPickerStage string

const (
	ProviderPickerClosed          ProviderPickerStage = "closed"
	ProviderPickerLoadingProfiles ProviderPickerStage = "loading-profiles"
	ProviderPickerChooseProvider  ProviderPickerStage = "choose-provider"
	ProviderPickerConfiguring     ProviderPickerStage = "configuring"
	ProviderPickerLoadingModels   ProviderPickerStage = "loading-models"
	ProviderPickerChooseModel     ProviderPickerStage = "choose-model"
	ProviderPickerEnteringConfig  ProviderPickerStage = "entering-configuration"
	ProviderPickerSwitching       ProviderPickerStage = "switching"
	ProviderPickerFailed          ProviderPickerStage = "failed"
)

// ProviderChoice is a secret-free built-in choice rendered by the TUI.
type ProviderChoice struct {
	Kind            string
	DisplayName     string
	NeedsCredential bool
	NeedsEndpoint   bool
}

var providerChoices = []ProviderChoice{
	{Kind: "openai", DisplayName: "OpenAI", NeedsCredential: true},
	{Kind: "deepseek", DisplayName: "DeepSeek", NeedsCredential: true},
	{Kind: "ollama", DisplayName: "Ollama"},
	{Kind: "openai-compatible", DisplayName: "Custom OpenAI-compatible", NeedsCredential: true, NeedsEndpoint: true},
}

const maxProviderFieldRunes = 4096

// ProviderChoices returns the stable picker order without exposing its backing slice.
func ProviderChoices() []ProviderChoice {
	return append([]ProviderChoice(nil), providerChoices...)
}

// ProviderPicker coordinates asynchronous provider configuration and model switching.
// Field editing and visual styling remain separate so this state machine can be tested
// without a terminal.
type ProviderPicker struct {
	controller  ModelController
	stage       ProviderPickerStage
	profiles    []session.ProviderProfile
	models      []session.ModelOption
	profileID   session.ProviderProfileID
	message     string
	cancel      context.CancelFunc
	generation  uint64
	cursor      int
	choice      ProviderChoice
	fields      []providerInputField
	fieldIndex  int
	current     session.ModelSelection
	preferred   string
	returnStage ProviderPickerStage
}

type providerInputField struct {
	label  string
	secret bool
	value  []rune
}

// NewProviderPicker creates a closed provider picker.
func NewProviderPicker(controller ModelController) *ProviderPicker {
	return &ProviderPicker{controller: controller, stage: ProviderPickerClosed}
}

// Init returns no command. The owner opens the picker explicitly when needed.
func (p *ProviderPicker) Init() tea.Cmd {
	return nil
}

// Open starts a fresh, cancellable profile-loading flow.
func (p *ProviderPicker) Open(parent context.Context) tea.Cmd {
	return p.OpenForSelection(parent, session.ModelSelection{})
}

// OpenForSelection opens the picker and keeps the active provider/model
// selected when it is present in the configured profile list.
func (p *ProviderPicker) OpenForSelection(parent context.Context, current session.ModelSelection) tea.Cmd {
	ctx := p.resetContext(parent)
	p.stage = ProviderPickerLoadingProfiles
	p.message = ""
	p.models = nil
	p.profileID = ""
	p.cursor = 0
	p.current = current
	p.preferred = ""
	p.clearConfiguration()
	return listProviderProfilesCmd(ctx, p.controller, p.generation)
}

// Configure validates and stores a provider using a short-lived credential copy.
// The caller's byte buffer is zeroed before this method returns.
func (p *ProviderPicker) Configure(request session.ConfigureProviderRequest) tea.Cmd {
	ctx := p.beginOperation()
	generation := p.generation
	p.stage = ProviderPickerConfiguring
	p.message = ""

	credential := append([]byte(nil), request.CredentialInput...)
	wipeBytes(request.CredentialInput)
	request.CredentialInput = credential
	return func() tea.Msg {
		defer wipeBytes(credential)
		if p.controller == nil {
			return providerConfiguredMsg{generation: generation, message: "Provider setup is unavailable."}
		}
		profile, err := p.controller.ConfigureProvider(ctx, request)
		return providerConfiguredMsg{generation: generation, profile: profile, message: pickerErrorMessage(err)}
	}
}

// LoadModels fetches choices for an existing provider profile.
func (p *ProviderPicker) LoadModels(profileID session.ProviderProfileID) tea.Cmd {
	ctx := p.beginOperation()
	p.stage = ProviderPickerLoadingModels
	p.profileID = profileID
	p.preferred = p.preferredModel(profileID)
	p.models = nil
	p.message = ""
	return listModelsCmd(ctx, p.controller, profileID, p.generation)
}

// SwitchModel validates and applies a selection at a Session turn boundary.
func (p *ProviderPicker) SwitchModel(selection session.ModelSelection) tea.Cmd {
	ctx := p.beginOperation()
	generation := p.generation
	p.returnStage = p.stage
	p.stage = ProviderPickerSwitching
	p.message = ""
	return func() tea.Msg {
		if p.controller == nil {
			return modelSwitchedMsg{generation: generation, message: "Provider setup is unavailable."}
		}
		err := p.controller.SwitchModel(ctx, selection)
		return modelSwitchedMsg{generation: generation, message: pickerErrorMessage(err)}
	}
}

// Cancel stops in-flight work and closes the picker.
func (p *ProviderPicker) Cancel() {
	if p.cancel != nil {
		p.cancel()
	}
	p.cancel = nil
	p.generation++
	p.stage = ProviderPickerClosed
	p.models = nil
	p.message = ""
	p.cursor = 0
	p.clearConfiguration()
}

// Update applies asynchronous command results for composition into the root model.
func (p *ProviderPicker) Update(message tea.Msg) (*ProviderPicker, tea.Cmd) {
	switch value := message.(type) {
	case providerProfilesLoadedMsg:
		if value.generation != p.generation || p.stage == ProviderPickerClosed {
			return p, nil
		}
		if value.message != "" {
			p.fail(value.message)
			return p, nil
		}
		p.profiles = deduplicateProfiles(value.profiles, p.current.ProviderProfileID)
		p.cursor = profileCursor(p.profiles, p.current.ProviderProfileID)
		p.stage = ProviderPickerChooseProvider
	case providerConfiguredMsg:
		if value.generation != p.generation || p.stage == ProviderPickerClosed {
			return p, nil
		}
		if value.message != "" {
			p.fail(value.message)
			return p, nil
		}
		p.upsertProfile(value.profile)
		return p, p.LoadModels(value.profile.ID)
	case providerModelsLoadedMsg:
		if value.generation != p.generation || p.stage == ProviderPickerClosed {
			return p, nil
		}
		if value.message != "" {
			p.fail(value.message)
			return p, nil
		}
		p.models = deduplicateModels(value.models)
		p.cursor = modelCursor(p.models, p.preferred)
		p.stage = ProviderPickerChooseModel
	case modelSwitchedMsg:
		if value.generation != p.generation || p.stage == ProviderPickerClosed {
			return p, nil
		}
		if value.message != "" {
			p.stage = p.returnStage
			if p.stage != ProviderPickerChooseProvider && p.stage != ProviderPickerChooseModel {
				p.stage = ProviderPickerChooseProvider
			}
			p.message = value.message
			return p, nil
		}
		p.Cancel()
	}
	return p, nil
}

// HandleKey applies picker navigation and field editing on the Bubble Tea loop.
func (p *ProviderPicker) HandleKey(message tea.KeyPressMsg) tea.Cmd {
	if p == nil || p.stage == ProviderPickerClosed {
		return nil
	}
	key := message.Key()
	if key.Code == tea.KeyEscape || key.Code == tea.KeyEsc {
		p.Cancel()
		return nil
	}
	switch p.stage {
	case ProviderPickerChooseProvider:
		count := len(p.profiles) + len(providerChoices)
		if movePickerCursor(&p.cursor, message, count) {
			return nil
		}
		if isEnterKey(key.Code) {
			if p.cursor < len(p.profiles) {
				profile := p.profiles[p.cursor]
				selection := session.ModelSelection{ProviderProfileID: profile.ID, ModelID: profile.ModelID}
				if selection == p.current {
					p.Cancel()
					return nil
				}
				return p.SwitchModel(selection)
			}
			return p.beginConfiguration(providerChoices[p.cursor-len(p.profiles)])
		}
	case ProviderPickerChooseModel:
		if movePickerCursor(&p.cursor, message, len(p.models)) {
			return nil
		}
		if isEnterKey(key.Code) && len(p.models) > 0 {
			model := p.models[p.cursor]
			return p.SwitchModel(session.ModelSelection{ProviderProfileID: p.profileID, ModelID: model.ID})
		}
	case ProviderPickerEnteringConfig:
		return p.handleConfigurationKey(key)
	case ProviderPickerFailed:
		if isEnterKey(key.Code) {
			return p.Open(context.Background())
		}
	}
	return nil
}

// HandlePaste appends bracketed-paste content to the active configuration
// field. Control sequences are removed before any non-secret field is rendered.
func (p *ProviderPicker) HandlePaste(message tea.PasteMsg) {
	if p == nil || p.stage != ProviderPickerEnteringConfig {
		return
	}
	if len(p.fields) == 0 || p.fieldIndex < 0 || p.fieldIndex >= len(p.fields) {
		p.fail("Provider configuration fields are unavailable.")
		return
	}
	input := sanitizePasteRunes(message.Content, false)
	if len(input) == 0 {
		return
	}
	p.message = ""
	field := &p.fields[p.fieldIndex]
	remaining := maxProviderFieldRunes - len(field.value)
	if remaining <= 0 {
		return
	}
	if len(input) > remaining {
		input = input[:remaining]
	}
	field.value = append(field.value, input...)
}

// View returns a minimal accessible representation for the surrounding TUI.
func (p *ProviderPicker) View() string {
	if p == nil || p.stage == ProviderPickerClosed {
		return ""
	}
	if p.stage == ProviderPickerFailed {
		return "Provider setup failed\n" + p.message
	}
	switch p.stage {
	case ProviderPickerLoadingProfiles:
		return "Provider setup\nLoading configured providers..."
	case ProviderPickerChooseProvider:
		lines := []string{"Select provider or model", "", "Configured models"}
		if p.message != "" {
			lines = append(lines, "Error: "+p.message)
		}
		total := len(p.profiles) + len(providerChoices)
		start, end := pickerWindow(p.cursor, total, maxVisiblePickerItems)
		if len(p.profiles) == 0 {
			lines = append(lines, "  None configured")
		} else if start > 0 {
			lines = append(lines, "  …")
		}
		profileEnd := min(end, len(p.profiles))
		for index := start; index < profileEnd; index++ {
			profile := p.profiles[index]
			label := fmt.Sprintf("%s  ·  %s", profile.DisplayName, profile.ModelID)
			if profile.ID == p.current.ProviderProfileID && profile.ModelID == p.current.ModelID {
				label += "  (current)"
			}
			lines = append(lines, pickerLine(index == p.cursor, label))
		}
		if profileEnd < len(p.profiles) {
			lines = append(lines, "  …")
		}
		lines = append(lines, "", "Add provider")
		choiceStart := len(p.profiles)
		for index := max(start, choiceStart); index < end; index++ {
			choice := providerChoices[index-choiceStart]
			lines = append(lines, pickerLine(index == p.cursor, choice.DisplayName))
		}
		if end < total {
			lines = append(lines, "  …")
		}
		return strings.Join(lines, "\n")
	case ProviderPickerEnteringConfig:
		lines := []string{"Configure " + p.choice.DisplayName, "Credentials are checked now; the selected model is validated afterward."}
		if p.message != "" {
			lines = append(lines, "Error: "+p.message)
		}
		for index, field := range p.fields {
			value := string(field.value)
			if field.secret {
				value = strings.Repeat("•", len(field.value))
			}
			if value == "" {
				value = "<required>"
			}
			lines = append(lines, pickerLine(index == p.fieldIndex, field.label+": "+value))
		}
		return strings.Join(lines, "\n")
	case ProviderPickerConfiguring:
		return "Provider setup\nChecking credentials and endpoint..."
	case ProviderPickerLoadingModels:
		return "Provider setup\nLoading available models..."
	case ProviderPickerChooseModel:
		lines := []string{"Select model"}
		if p.message != "" {
			lines = append(lines, "Error: "+p.message)
		}
		start, end := pickerWindow(p.cursor, len(p.models), maxVisiblePickerItems)
		if start > 0 {
			lines = append(lines, "  …")
		}
		for index := start; index < end; index++ {
			model := p.models[index]
			label := model.DisplayName
			if label == "" {
				label = model.ID
			}
			if model.Recommended {
				label += " (recommended)"
			}
			lines = append(lines, pickerLine(index == p.cursor, label))
		}
		if end < len(p.models) {
			lines = append(lines, "  …")
		}
		if len(p.models) == 0 {
			lines = append(lines, "No models were returned.")
		}
		return strings.Join(lines, "\n")
	case ProviderPickerSwitching:
		return "Provider setup\nValidating selected model..."
	default:
		return fmt.Sprintf("Provider setup: %s", p.stage)
	}
}

// Stage returns the current picker stage.
func (p *ProviderPicker) Stage() ProviderPickerStage {
	if p == nil {
		return ProviderPickerClosed
	}
	return p.stage
}

// Profiles returns a secret-free defensive copy.
func (p *ProviderPicker) Profiles() []session.ProviderProfile {
	if p == nil {
		return nil
	}
	return append([]session.ProviderProfile(nil), p.profiles...)
}

// Models returns a defensive copy of the current profile's model choices.
func (p *ProviderPicker) Models() []session.ModelOption {
	if p == nil {
		return nil
	}
	return append([]session.ModelOption(nil), p.models...)
}

// ProfileID identifies the profile whose models are currently displayed.
func (p *ProviderPicker) ProfileID() session.ProviderProfileID {
	if p == nil {
		return ""
	}
	return p.profileID
}

// Message returns the safe user-facing failure message.
func (p *ProviderPicker) Message() string {
	if p == nil {
		return ""
	}
	return p.message
}

func (p *ProviderPicker) resetContext(parent context.Context) context.Context {
	if p.cancel != nil {
		p.cancel()
	}
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	p.cancel = cancel
	p.generation++
	return ctx
}

func (p *ProviderPicker) beginOperation() context.Context {
	if p.cancel != nil {
		p.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	p.generation++
	return ctx
}

func (p *ProviderPicker) fail(message string) {
	p.stage = ProviderPickerFailed
	p.message = message
}

func (p *ProviderPicker) beginConfiguration(choice ProviderChoice) tea.Cmd {
	p.choice = choice
	p.cursor = 0
	p.fieldIndex = 0
	p.fields = nil
	if choice.NeedsEndpoint {
		p.fields = append(p.fields,
			providerInputField{label: "Base URL"},
			providerInputField{label: "Model ID"},
		)
	}
	if choice.NeedsCredential {
		p.fields = append(p.fields, providerInputField{label: "API key", secret: true})
	}
	if len(p.fields) == 0 {
		return p.Configure(session.ConfigureProviderRequest{Kind: choice.Kind, DisplayName: choice.DisplayName})
	}
	p.stage = ProviderPickerEnteringConfig
	return nil
}

func (p *ProviderPicker) handleConfigurationKey(key tea.Key) tea.Cmd {
	if len(p.fields) == 0 || p.fieldIndex < 0 || p.fieldIndex >= len(p.fields) {
		p.fail("Provider configuration fields are unavailable.")
		return nil
	}
	field := &p.fields[p.fieldIndex]
	if key.Code == tea.KeyBackspace {
		if len(field.value) > 0 {
			field.value[len(field.value)-1] = 0
			field.value = field.value[:len(field.value)-1]
		}
		return nil
	}
	if isEnterKey(key.Code) {
		if strings.TrimSpace(string(field.value)) == "" {
			p.message = field.label + " is required."
			return nil
		}
		p.message = ""
		if p.fieldIndex < len(p.fields)-1 {
			p.fieldIndex++
			return nil
		}
		request := p.configurationRequest()
		command := p.Configure(request)
		p.clearConfiguration()
		return command
	}
	if key.Text != "" && key.Mod&tea.ModCtrl == 0 {
		remaining := maxProviderFieldRunes - len(field.value)
		if remaining <= 0 {
			return nil
		}
		input := []rune(key.Text)
		if len(input) > remaining {
			input = input[:remaining]
		}
		field.value = append(field.value, input...)
	}
	return nil
}

// sanitizePasteRunes strips terminal control sequences before pasted content
// reaches either a rendered field or an in-memory credential buffer.
func sanitizePasteRunes(value string, allowNewlines bool) []rune {
	value = strings.ReplaceAll(ansi.Strip(value), "\r\n", "\n")
	result := make([]rune, 0, len(value))
	for _, current := range value {
		switch current {
		case '\n', '\r':
			if allowNewlines {
				result = append(result, '\n')
			}
		default:
			if !unicode.IsControl(current) {
				result = append(result, current)
			}
		}
	}
	return result
}

func (p *ProviderPicker) configurationRequest() session.ConfigureProviderRequest {
	request := session.ConfigureProviderRequest{Kind: p.choice.Kind, DisplayName: p.choice.DisplayName}
	for _, field := range p.fields {
		switch field.label {
		case "Base URL":
			request.BaseURL = strings.TrimSpace(string(field.value))
		case "Model ID":
			request.ModelID = strings.TrimSpace(string(field.value))
		case "API key":
			request.CredentialInput = []byte(string(field.value))
		}
	}
	return request
}

func (p *ProviderPicker) clearConfiguration() {
	for fieldIndex := range p.fields {
		for valueIndex := range p.fields[fieldIndex].value {
			p.fields[fieldIndex].value[valueIndex] = 0
		}
	}
	p.fields = nil
	p.fieldIndex = 0
	p.choice = ProviderChoice{}
}

func movePickerCursor(cursor *int, message tea.KeyPressMsg, count int) bool {
	if count <= 0 {
		*cursor = 0
		return false
	}
	switch pickerNavigationDirection(message) {
	case -1:
		*cursor = (*cursor - 1 + count) % count
		return true
	case 1:
		*cursor = (*cursor + 1) % count
		return true
	default:
		return false
	}
}

// pickerNavigationDirection also checks alternate key codes because the
// Windows Console API can report a physical arrow through BaseCode while Code
// is empty or contains translated text.
func pickerNavigationDirection(message tea.KeyPressMsg) int {
	key := message.Key()
	for _, code := range []rune{key.Code, key.BaseCode, key.ShiftedCode} {
		switch code {
		case tea.KeyUp:
			return -1
		case tea.KeyDown:
			return 1
		}
	}
	keystroke := strings.ToLower(strings.TrimSpace(message.Keystroke()))
	switch {
	case keystroke == "up" || strings.HasSuffix(keystroke, "+up"):
		return -1
	case keystroke == "down" || strings.HasSuffix(keystroke, "+down"):
		return 1
	default:
		return 0
	}
}

func pickerLine(selected bool, value string) string {
	if selected {
		return "> " + value
	}
	return "  " + value
}

func isEnterKey(code rune) bool {
	return code == tea.KeyEnter || code == tea.KeyReturn || code == tea.KeyKpEnter
}

func (p *ProviderPicker) upsertProfile(profile session.ProviderProfile) {
	for index := range p.profiles {
		if p.profiles[index].ID == profile.ID {
			p.profiles[index] = profile
			return
		}
	}
	p.profiles = append(p.profiles, profile)
}

func (p *ProviderPicker) preferredModel(profileID session.ProviderProfileID) string {
	if profileID == p.current.ProviderProfileID && strings.TrimSpace(p.current.ModelID) != "" {
		return p.current.ModelID
	}
	for _, profile := range p.profiles {
		if profile.ID == profileID {
			return profile.ModelID
		}
	}
	return ""
}

// deduplicateProfiles keeps the active profile selectable; otherwise the most
// recently validated duplicate represents the same provider/model endpoint.
func deduplicateProfiles(values []session.ProviderProfile, currentID session.ProviderProfileID) []session.ProviderProfile {
	result := make([]session.ProviderProfile, 0, len(values))
	indexes := make(map[string]int, len(values))
	for _, value := range values {
		key := strings.ToLower(strings.TrimSpace(value.Kind)) + "\x00" +
			strings.ToLower(strings.TrimRight(strings.TrimSpace(value.BaseURL), "/")) + "\x00" +
			strings.ToLower(strings.TrimSpace(value.ModelID))
		if index, exists := indexes[key]; exists {
			if value.ID == currentID || (result[index].ID != currentID && value.ValidatedAt.After(result[index].ValidatedAt)) {
				result[index] = value
			}
			continue
		}
		indexes[key] = len(result)
		result = append(result, value)
	}
	return result
}

func deduplicateModels(values []session.ModelOption) []session.ModelOption {
	result := make([]session.ModelOption, 0, len(values))
	indexes := make(map[string]int, len(values))
	for _, value := range values {
		key := strings.ToLower(strings.TrimSpace(value.ID))
		if key == "" {
			continue
		}
		if index, exists := indexes[key]; exists {
			result[index].Recommended = result[index].Recommended || value.Recommended
			continue
		}
		indexes[key] = len(result)
		result = append(result, value)
	}
	return result
}

func profileCursor(values []session.ProviderProfile, id session.ProviderProfileID) int {
	for index, value := range values {
		if value.ID == id {
			return index
		}
	}
	return 0
}

func modelCursor(values []session.ModelOption, id string) int {
	for index, value := range values {
		if value.ID == id {
			return index
		}
	}
	return 0
}

func listProviderProfilesCmd(ctx context.Context, controller ModelController, generation uint64) tea.Cmd {
	return func() tea.Msg {
		if controller == nil {
			return providerProfilesLoadedMsg{generation: generation, message: "Provider setup is unavailable."}
		}
		profiles, err := controller.ListProviderProfiles(ctx)
		return providerProfilesLoadedMsg{generation: generation, profiles: profiles, message: pickerErrorMessage(err)}
	}
}

func listModelsCmd(ctx context.Context, controller ModelController, profileID session.ProviderProfileID, generation uint64) tea.Cmd {
	return func() tea.Msg {
		if controller == nil {
			return providerModelsLoadedMsg{generation: generation, message: "Provider setup is unavailable."}
		}
		models, err := controller.ListModels(ctx, profileID)
		return providerModelsLoadedMsg{generation: generation, models: models, message: pickerErrorMessage(err)}
	}
}

func pickerErrorMessage(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return "Provider setup was cancelled."
	}
	return SafeErrorMessage(err, "Provider setup could not be completed.")
}

func wipeBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

type providerProfilesLoadedMsg struct {
	generation uint64
	profiles   []session.ProviderProfile
	message    string
}

type providerConfiguredMsg struct {
	generation uint64
	profile    session.ProviderProfile
	message    string
}

type providerModelsLoadedMsg struct {
	generation uint64
	models     []session.ModelOption
	message    string
}

type modelSwitchedMsg struct {
	generation uint64
	message    string
}
