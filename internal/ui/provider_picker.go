package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/eaglc/codepilot/internal/codingagent"
)

type providerStage uint8

const (
	providerProfiles providerStage = iota
	providerModels
	providerCredential
	providerForm
)

type providerPicker struct {
	active          bool
	required        bool
	stage           providerStage
	profiles        []codingagent.ProviderProfile
	models          []providerModelOption
	selectedProfile codingagent.ProviderProfile
	cursor          int
	busy            bool
	busyMessage     string
	message         string
	errorMessage    string
	form            providerFormState
}

type providerModelOption struct {
	ID          string
	DisplayName string
	Reasoning   bool
	Available   bool
	Configured  bool
	Current     bool
}

type providerFormState struct {
	id         string
	kind       string
	display    string
	baseURL    string
	model      string
	credential []rune
	field      int
	cursor     int
}

type providerProfilesMsg struct {
	profiles []codingagent.ProviderProfile
	err      error
}

type providerModelsMsg struct {
	profile codingagent.ProviderProfile
	models  []codingagent.ProviderModel
	err     error
}

type providerSavedMsg struct {
	profile codingagent.ProviderProfile
	err     error
}

type providerSelectedMsg struct {
	session codingagent.Session
	err     error
}

func newProviderPicker(message string, required bool) providerPicker {
	return providerPicker{active: true, required: required, stage: providerProfiles, message: strings.TrimSpace(message)}
}

func (m *Model) loadProviderProfiles() tea.Cmd {
	client, ctx := m.client, m.ctx
	m.picker.busy = true
	m.picker.busyMessage = "Loading Provider profiles…"
	return func() tea.Msg {
		profiles, err := client.ListProviderProfiles(ctx)
		return providerProfilesMsg{profiles: profiles, err: err}
	}
}

func (m *Model) loadProviderModels(profile codingagent.ProviderProfile) tea.Cmd {
	client, ctx := m.client, m.ctx
	m.picker.busy = true
	m.picker.busyMessage = "Loading models from " + profile.DisplayName + "…"
	m.picker.errorMessage = ""
	return func() tea.Msg {
		models, err := client.ListProviderModels(ctx, profile.ID)
		return providerModelsMsg{profile: profile, models: models, err: err}
	}
}

func (m *Model) applyProviderProfiles(message providerProfilesMsg) {
	m.picker.busy = false
	m.picker.busyMessage = ""
	if message.err != nil {
		m.picker.errorMessage = safeError(message.err)
		return
	}
	m.picker.profiles = message.profiles
	m.picker.stage = providerProfiles
	m.picker.cursor = 0
	for index, profile := range message.profiles {
		if profile.ID == m.snapshot.Session.ProviderProfileID {
			m.picker.cursor = index
			break
		}
	}
}

func (m *Model) applyProviderModels(message providerModelsMsg) {
	m.picker.busy = false
	m.picker.busyMessage = ""
	m.picker.selectedProfile = message.profile
	m.picker.models = m.mergeProviderModels(message.profile, message.models)
	m.picker.stage = providerModels
	m.picker.cursor = 0
	for index, model := range m.picker.models {
		if model.Current || model.Configured {
			m.picker.cursor = index
			if model.Current {
				break
			}
		}
	}
	if message.err != nil {
		m.picker.errorMessage = safeError(message.err) + "  Saved model choices are still shown; selecting one retries the access check."
	} else if len(m.picker.models) == 0 {
		m.picker.errorMessage = "No models were returned. Edit the profile or install a model."
	} else {
		m.picker.errorMessage = ""
	}
}

func (m *Model) mergeProviderModels(profile codingagent.ProviderProfile, discovered []codingagent.ProviderModel) []providerModelOption {
	values := make([]providerModelOption, 0, len(discovered)+2)
	indexes := make(map[string]int, len(discovered)+2)
	add := func(id, display string, available, configured, current, reasoning bool) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		if index, ok := indexes[id]; ok {
			values[index].Available = values[index].Available || available
			values[index].Configured = values[index].Configured || configured
			values[index].Current = values[index].Current || current
			values[index].Reasoning = values[index].Reasoning || reasoning
			if values[index].DisplayName == "" && strings.TrimSpace(display) != "" {
				values[index].DisplayName = strings.TrimSpace(display)
			}
			return
		}
		display = strings.TrimSpace(display)
		if display == "" {
			display = id
		}
		indexes[id] = len(values)
		values = append(values, providerModelOption{ID: id, DisplayName: display, Available: available, Configured: configured, Current: current, Reasoning: reasoning})
	}
	if profile.ID == m.snapshot.Session.ProviderProfileID {
		add(m.snapshot.Session.ModelID, m.snapshot.Session.ModelID, false, true, true, false)
	}
	add(profile.DefaultModel, profile.DefaultModel, false, true, false, false)
	for _, model := range discovered {
		add(model.ID, model.DisplayName, true, false, false, model.Reasoning)
	}
	return values
}

func (m *Model) applyProviderSaved(message providerSavedMsg) tea.Cmd {
	m.picker.busy = false
	m.picker.busyMessage = ""
	if message.err != nil {
		m.picker.errorMessage = safeError(message.err)
		return nil
	}
	m.clearProviderSecret()
	if message.profile.RequiresCredential && !message.profile.CredentialConfigured {
		m.beginProviderCredential(message.profile)
		m.picker.errorMessage = "The API key was not stored. Enter a non-empty API key and try again."
		return nil
	}
	m.picker.message = "Configuration saved."
	return m.loadProviderModels(message.profile)
}

func (m *Model) applyProviderSelected(message providerSelectedMsg) tea.Cmd {
	m.picker.busy = false
	m.picker.busyMessage = ""
	if message.err != nil {
		m.picker.errorMessage = safeError(message.err)
		return nil
	}
	m.snapshot.Session = message.session
	m.picker = providerPicker{}
	m.errorMessage = ""
	m.status = "Ready"
	return m.loadSnapshot()
}

func (m *Model) handleProviderKey(message tea.KeyPressMsg) tea.Cmd {
	key := message.Key()
	if key.Mod&tea.ModCtrl != 0 && key.Code == 'c' {
		return tea.Quit
	}
	if m.picker.busy {
		return nil
	}
	if key.Code == tea.KeyEscape || key.Code == tea.KeyEsc {
		if m.picker.stage != providerProfiles {
			m.clearProviderSecret()
			m.picker.stage = providerProfiles
			m.picker.errorMessage = ""
			return nil
		}
		if !m.picker.required {
			m.picker = providerPicker{}
		} else {
			m.picker.errorMessage = "A working Provider and model must be selected before chatting."
		}
		return nil
	}
	switch m.picker.stage {
	case providerProfiles:
		return m.handleProviderProfilesKey(key)
	case providerModels:
		return m.handleProviderModelsKey(key)
	case providerCredential:
		return m.handleProviderCredentialKey(key)
	case providerForm:
		return m.handleProviderFormKey(key)
	}
	return nil
}

func (m *Model) handleProviderProfilesKey(key tea.Key) tea.Cmd {
	switch key.Code {
	case tea.KeyUp:
		m.picker.cursor = max(0, m.picker.cursor-1)
	case tea.KeyDown:
		m.picker.cursor = min(max(0, len(m.picker.profiles)-1), m.picker.cursor+1)
	case tea.KeyEnter:
		if profile, ok := m.currentProviderProfile(); ok {
			if profile.RequiresCredential && !profile.CredentialConfigured {
				m.beginProviderCredential(profile)
				return nil
			}
			return m.loadProviderModels(profile)
		}
	default:
		switch strings.ToLower(key.Text) {
		case "n":
			m.beginProviderForm(codingagent.ProviderProfile{})
		case "e":
			if profile, ok := m.currentProviderProfile(); ok {
				m.beginProviderForm(profile)
			}
		case "k":
			if profile, ok := m.currentProviderProfile(); ok {
				m.beginProviderCredential(profile)
			}
		case "r":
			return m.loadProviderProfiles()
		}
	}
	return nil
}

func (m *Model) handleProviderModelsKey(key tea.Key) tea.Cmd {
	switch key.Code {
	case tea.KeyUp:
		m.picker.cursor = max(0, m.picker.cursor-1)
	case tea.KeyDown:
		m.picker.cursor = min(max(0, len(m.picker.models)-1), m.picker.cursor+1)
	case tea.KeyEnter:
		if len(m.picker.models) == 0 {
			return nil
		}
		model := m.picker.models[m.picker.cursor]
		client, ctx, sessionID, profileID := m.client, m.ctx, m.sessionID, m.picker.selectedProfile.ID
		m.picker.busy = true
		m.picker.busyMessage = "Checking API access for " + model.ID + "…"
		m.picker.errorMessage = ""
		return func() tea.Msg {
			session, err := client.SelectProviderModel(ctx, sessionID, profileID, model.ID)
			return providerSelectedMsg{session: session, err: err}
		}
	default:
		switch strings.ToLower(key.Text) {
		case "e":
			m.beginProviderForm(m.picker.selectedProfile)
		case "k":
			m.beginProviderCredential(m.picker.selectedProfile)
		}
	}
	return nil
}

func (m *Model) beginProviderForm(profile codingagent.ProviderProfile) {
	form := providerFormState{id: profile.ID, kind: profile.Kind, display: profile.DisplayName, baseURL: profile.BaseURL, model: profile.DefaultModel}
	if form.kind == "" {
		form.kind, form.display, form.model = "openai", "OpenAI", "gpt-5.6-sol"
	}
	m.picker.form = form
	m.picker.stage = providerForm
	m.picker.selectedProfile = profile
	m.picker.message = ""
	m.picker.errorMessage = ""
}

func (m *Model) beginProviderCredential(profile codingagent.ProviderProfile) {
	if !profile.RequiresCredential {
		m.picker.errorMessage = profile.DisplayName + " does not use an API key."
		return
	}
	m.clearProviderSecret()
	m.picker.selectedProfile = profile
	m.picker.form = providerFormState{
		id: profile.ID, kind: profile.Kind, display: profile.DisplayName, baseURL: profile.BaseURL,
		model: profile.DefaultModel, field: 4,
	}
	m.picker.stage = providerCredential
	m.picker.message = ""
	m.picker.errorMessage = ""
}

func (m *Model) handleProviderCredentialKey(key tea.Key) tea.Cmd {
	if key.Code == tea.KeyEnter {
		return m.saveProviderCredential()
	}
	form := &m.picker.form
	value := []rune(m.providerFormValue())
	switch key.Code {
	case tea.KeyLeft:
		form.cursor = max(0, form.cursor-1)
	case tea.KeyRight:
		form.cursor = min(len(value), form.cursor+1)
	case tea.KeyHome:
		form.cursor = 0
	case tea.KeyEnd:
		form.cursor = len(value)
	case tea.KeyBackspace:
		if form.cursor > 0 {
			value = append(value[:form.cursor-1], value[form.cursor:]...)
			form.cursor--
			m.setProviderFormValue(value)
		}
	case tea.KeyDelete:
		if form.cursor < len(value) {
			value = append(value[:form.cursor], value[form.cursor+1:]...)
			m.setProviderFormValue(value)
		}
	default:
		if key.Text != "" && key.Mod&tea.ModCtrl == 0 {
			m.insertProviderFormRunes([]rune(key.Text))
		}
	}
	return nil
}

func (m *Model) handleProviderFormKey(key tea.Key) tea.Cmd {
	form := &m.picker.form
	if form.field == 0 {
		if form.id == "" && (key.Code == tea.KeyLeft || key.Code == tea.KeyRight || key.Code == tea.KeyUp || key.Code == tea.KeyDown) {
			m.cycleProviderKind(key.Code == tea.KeyRight || key.Code == tea.KeyDown)
			return nil
		}
		if key.Code == tea.KeyEnter || key.Code == tea.KeyTab {
			form.field++
			form.cursor = len([]rune(m.providerFormValue()))
		}
		return nil
	}
	if key.Code == tea.KeyTab || key.Code == tea.KeyEnter {
		if form.field == 4 {
			return m.saveProviderForm()
		}
		form.field++
		form.cursor = len([]rune(m.providerFormValue()))
		return nil
	}
	value := []rune(m.providerFormValue())
	switch key.Code {
	case tea.KeyLeft:
		form.cursor = max(0, form.cursor-1)
	case tea.KeyRight:
		form.cursor = min(len(value), form.cursor+1)
	case tea.KeyHome:
		form.cursor = 0
	case tea.KeyEnd:
		form.cursor = len(value)
	case tea.KeyBackspace:
		if form.cursor > 0 {
			value = append(value[:form.cursor-1], value[form.cursor:]...)
			form.cursor--
			m.setProviderFormValue(value)
		}
	case tea.KeyDelete:
		if form.cursor < len(value) {
			value = append(value[:form.cursor], value[form.cursor+1:]...)
			m.setProviderFormValue(value)
		}
	default:
		if key.Text != "" && key.Mod&tea.ModCtrl == 0 {
			m.insertProviderFormRunes([]rune(key.Text))
		}
	}
	return nil
}

func (m *Model) pasteProviderInput(value string) {
	if !m.picker.active || (m.picker.stage != providerForm && m.picker.stage != providerCredential) || m.picker.busy || m.picker.form.field == 0 {
		return
	}
	m.insertProviderFormRunes([]rune(value))
}

func (m *Model) insertProviderFormRunes(inserted []rune) {
	form := &m.picker.form
	limit := 1024
	if form.field == 4 {
		limit = 4096
	}
	value := []rune(m.providerFormValue())
	if len(value) >= limit {
		return
	}
	if len(inserted) > limit-len(value) {
		inserted = inserted[:limit-len(value)]
	}
	result := make([]rune, 0, len(value)+len(inserted))
	result = append(result, value[:form.cursor]...)
	result = append(result, inserted...)
	result = append(result, value[form.cursor:]...)
	form.cursor += len(inserted)
	m.setProviderFormValue(result)
}

func (m *Model) providerFormValue() string {
	switch m.picker.form.field {
	case 0:
		return m.picker.form.kind
	case 1:
		return m.picker.form.display
	case 2:
		return m.picker.form.baseURL
	case 3:
		return m.picker.form.model
	default:
		return string(m.picker.form.credential)
	}
}

func (m *Model) setProviderFormValue(value []rune) {
	switch m.picker.form.field {
	case 1:
		m.picker.form.display = string(value)
	case 2:
		m.picker.form.baseURL = string(value)
	case 3:
		m.picker.form.model = string(value)
	case 4:
		for index := range m.picker.form.credential {
			m.picker.form.credential[index] = 0
		}
		m.picker.form.credential = append([]rune(nil), value...)
	}
}

func (m *Model) cycleProviderKind(forward bool) {
	kinds := []string{"openai", "deepseek", "ollama"}
	index := 0
	for candidate := range kinds {
		if kinds[candidate] == m.picker.form.kind {
			index = candidate
			break
		}
	}
	if forward {
		index = (index + 1) % len(kinds)
	} else {
		index = (index + len(kinds) - 1) % len(kinds)
	}
	m.picker.form.kind = kinds[index]
	switch kinds[index] {
	case "openai":
		m.picker.form.display, m.picker.form.model = "OpenAI", "gpt-5.6-sol"
	case "deepseek":
		m.picker.form.display, m.picker.form.model = "DeepSeek", "deepseek-v4-flash"
	case "ollama":
		m.picker.form.display, m.picker.form.model, m.picker.form.credential = "Ollama", "qwen-coder", nil
	}
}

func (m *Model) saveProviderForm() tea.Cmd {
	form := &m.picker.form
	credential := []byte(string(form.credential))
	request := codingagent.ConfigureProviderRequest{
		ID: form.id, Kind: form.kind, DisplayName: strings.TrimSpace(form.display), BaseURL: strings.TrimSpace(form.baseURL),
		DefaultModel: strings.TrimSpace(form.model), Credential: credential,
	}
	m.clearProviderSecret()
	client, ctx := m.client, m.ctx
	m.picker.busy = true
	m.picker.busyMessage = "Saving Provider profile…"
	m.picker.errorMessage = ""
	return func() tea.Msg {
		profile, err := client.ConfigureProvider(ctx, request)
		for index := range credential {
			credential[index] = 0
		}
		return providerSavedMsg{profile: profile, err: err}
	}
}

func (m *Model) saveProviderCredential() tea.Cmd {
	form := &m.picker.form
	if len(form.credential) == 0 {
		m.picker.errorMessage = "API key cannot be empty."
		return nil
	}
	credential := []byte(string(form.credential))
	request := codingagent.ConfigureProviderRequest{
		ID: form.id, Kind: form.kind, DisplayName: form.display, BaseURL: form.baseURL,
		DefaultModel: form.model, Credential: credential,
	}
	m.clearProviderSecret()
	client, ctx := m.client, m.ctx
	m.picker.busy = true
	m.picker.busyMessage = "Saving API key…"
	m.picker.errorMessage = ""
	return func() tea.Msg {
		profile, err := client.ConfigureProvider(ctx, request)
		for index := range credential {
			credential[index] = 0
		}
		return providerSavedMsg{profile: profile, err: err}
	}
}

func (m *Model) clearProviderSecret() {
	for index := range m.picker.form.credential {
		m.picker.form.credential[index] = 0
	}
	m.picker.form.credential = nil
}

func (m *Model) currentProviderProfile() (codingagent.ProviderProfile, bool) {
	if len(m.picker.profiles) == 0 || m.picker.cursor < 0 || m.picker.cursor >= len(m.picker.profiles) {
		return codingagent.ProviderProfile{}, false
	}
	return m.picker.profiles[m.picker.cursor], true
}

func (m *Model) providerView(width, height int) tea.View {
	lines := []string{truncateANSI(theme.header.Render("CodePilot")+theme.muted.Render("  Provider & model setup"), width), ""}
	var viewCursor *tea.Cursor
	switch m.picker.stage {
	case providerProfiles:
		lines = append(lines, theme.assistant.Render("Choose a Provider profile"))
		if len(m.picker.profiles) == 0 && !m.picker.busy {
			lines = append(lines, theme.muted.Render("  No profiles. Press n to create one."))
		}
		start, end := pickerWindow(m.picker.cursor, len(m.picker.profiles), max(1, height-10))
		if start > 0 {
			lines = append(lines, theme.muted.Render("  …"))
		}
		for index := start; index < end; index++ {
			profile := m.picker.profiles[index]
			marker := "  "
			if index == m.picker.cursor {
				marker = "❯ "
			}
			credential := ""
			if profile.RequiresCredential && !profile.CredentialConfigured {
				credential = "  credential required"
			} else if profile.RequiresCredential {
				credential = "  API key configured"
			}
			configuredModel := profile.DefaultModel
			if profile.ID == m.snapshot.Session.ProviderProfileID && strings.TrimSpace(m.snapshot.Session.ModelID) != "" {
				configuredModel = m.snapshot.Session.ModelID + "  current"
			}
			lines = append(lines, marker+profile.DisplayName+theme.muted.Render("  "+profile.Kind+"  "+configuredModel+credential))
		}
		if end < len(m.picker.profiles) {
			lines = append(lines, theme.muted.Render("  …"))
		}
		lines = append(lines, "", theme.muted.Render("↑/↓ choose  Enter continue  k API key  e advanced  n new  r refresh  Esc close"))
	case providerModels:
		lines = append(lines, theme.assistant.Render("Choose a model for "+m.picker.selectedProfile.DisplayName))
		start, end := pickerWindow(m.picker.cursor, len(m.picker.models), max(1, height-10))
		if start > 0 {
			lines = append(lines, theme.muted.Render("  …"))
		}
		for index := start; index < end; index++ {
			model := m.picker.models[index]
			marker := "  "
			if index == m.picker.cursor {
				marker = "❯ "
			}
			badges := ""
			if model.Current {
				badges += "  current"
			} else if model.Configured {
				badges += "  configured"
			}
			if !model.Available {
				badges += "  saved"
			}
			lines = append(lines, marker+model.DisplayName+theme.muted.Render(badges))
		}
		if end < len(m.picker.models) {
			lines = append(lines, theme.muted.Render("  …"))
		}
		lines = append(lines, "", theme.muted.Render("↑/↓ choose  Enter check access & select  k change API key  e advanced  Esc back"))
	case providerCredential:
		lines = append(lines, theme.assistant.Render("API key for "+m.picker.selectedProfile.DisplayName))
		lines = append(lines, "", theme.muted.Render("  Provider: ")+m.picker.selectedProfile.DisplayName)
		prefix := theme.muted.Render("❯ API key: ")
		masked := []rune(strings.Repeat("•", len(m.picker.form.credential)))
		viewport := renderInputViewport(masked, m.picker.form.cursor, max(1, width-ansi.StringWidth(prefix)))
		cursorY := len(lines)
		lines = append(lines, prefix+viewport.text)
		if !m.picker.busy {
			viewCursor = nativeTextCursor(ansi.StringWidth(prefix)+viewport.cursorOffset, cursorY)
		}
		if m.picker.selectedProfile.CredentialConfigured {
			lines = append(lines, theme.muted.Render("  A key is already configured. Saving replaces it."))
		}
		lines = append(lines, "", theme.muted.Render("Enter save and load models  Esc back"))
	case providerForm:
		lines = append(lines, theme.assistant.Render("Provider profile"))
		labels := []string{"Type", "Display name", "Base URL (blank = default)", "Default model", "API key (blank = keep current)"}
		values := []string{m.picker.form.kind, m.picker.form.display, m.picker.form.baseURL, m.picker.form.model, strings.Repeat("•", len(m.picker.form.credential))}
		for index, label := range labels {
			marker := "  "
			value := values[index]
			cursor := 0
			if index == m.picker.form.field {
				marker = "❯ "
				if index != 0 {
					cursor = m.picker.form.cursor
				}
			}
			prefix := theme.muted.Render(marker + label + ": ")
			if index == m.picker.form.field {
				viewport := renderInputViewport([]rune(value), cursor, max(1, width-ansi.StringWidth(prefix)))
				cursorY := len(lines)
				lines = append(lines, prefix+viewport.text)
				if !m.picker.busy {
					viewCursor = nativeTextCursor(ansi.StringWidth(prefix)+viewport.cursorOffset, cursorY)
				}
			} else {
				lines = append(lines, prefix+value)
			}
		}
		lines = append(lines, "", theme.muted.Render("Tab/Enter next  Enter on API key saves  Esc back; arrows change new profile type"))
	}
	if m.picker.busy {
		lines = append(lines, "", theme.muted.Render(m.picker.busyMessage))
	}
	if m.picker.message != "" {
		lines = append(lines, "", theme.warning.Render(m.picker.message))
	}
	if m.picker.errorMessage != "" {
		lines = append(lines, "", theme.failure.Render(m.picker.errorMessage))
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	for index := range lines {
		lines[index] = truncateANSI(lines[index], width)
	}
	view := tea.NewView(strings.Join(lines[:min(len(lines), height)], "\n"))
	view.AltScreen = true
	view.MouseMode = tea.MouseModeCellMotion
	view.WindowTitle = "CodePilot Provider Setup"
	view.BackgroundColor = lipgloss.Color("#111318")
	view.ForegroundColor = lipgloss.Color("#E5E7EB")
	if viewCursor != nil && viewCursor.Y < height {
		view.Cursor = viewCursor
	}
	return view
}
