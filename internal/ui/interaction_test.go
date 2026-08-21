package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/eaglc/codepilot/internal/session"
)

func TestModelComposerStartsNaturalLanguageTurn(t *testing.T) {
	client := newInteractionClient()
	model := NewModel(client, nil, client.snapshot)
	model.composer = []rune("Fix the failing parser test")

	_, command := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if command == nil || !model.inputBusy || len(model.composer) != 0 {
		t.Fatalf("composer did not start a turn: busy=%t input=%q", model.inputBusy, string(model.composer))
	}
	message := command()
	model.Update(message)
	if client.turnText != "Fix the failing parser test" || model.activeTurn != "turn_interaction" {
		t.Fatalf("turn was not routed to the session client: text=%q turn=%q", client.turnText, model.activeTurn)
	}
}

func TestModelSessionPickerListsAndSwitches(t *testing.T) {
	client := newInteractionClient()
	client.sessions = []session.SessionSummary{
		{ID: client.snapshot.Session.ID, WorktreeID: client.snapshot.Session.WorktreeID, Title: "Current", PermissionMode: session.PermissionAsk},
		{ID: "ses_target", WorktreeID: client.snapshot.Session.WorktreeID, Title: "Target", PermissionMode: session.PermissionReadOnly},
	}
	model := NewModel(client, nil, client.snapshot)
	model.composer = []rune("/session list")

	_, openCommand := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if openCommand == nil || model.sessionPicker.Stage() != SessionPickerLoading {
		t.Fatalf("session picker did not start loading: %s", model.sessionPicker.Stage())
	}
	model.Update(openCommand())
	if model.sessionPicker.Stage() != SessionPickerChoosing || len(model.sessionPicker.Sessions()) != 2 {
		t.Fatalf("session picker did not load choices: stage=%s values=%#v", model.sessionPicker.Stage(), model.sessionPicker.Sessions())
	}
	model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
	_, switchCommand := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if switchCommand == nil {
		t.Fatal("session picker did not start switching")
	}
	model.Update(switchCommand())
	if client.switchedSession != "ses_target" || model.sessionPicker.Stage() != SessionPickerClosed {
		t.Fatalf("session switch = %q, picker stage = %s", client.switchedSession, model.sessionPicker.Stage())
	}
}

func TestSessionPickerConfirmsCrossWorktreeSwitch(t *testing.T) {
	client := newInteractionClient()
	client.sessions = []session.SessionSummary{{ID: "ses_other", WorktreeID: "wt_other", Title: "Other", PermissionMode: session.PermissionAsk}}
	picker := NewSessionPicker(client)
	picker.Update(picker.OpenForWorktree(context.Background(), session.SessionFilter{}, "wt_interaction")())
	if command := picker.HandleKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})); command != nil || picker.Stage() != SessionPickerConfirming {
		t.Fatalf("cross-worktree selection did not request confirmation: stage=%s", picker.Stage())
	}
	command := picker.HandleKey(tea.KeyPressMsg(tea.Key{Code: 'y', Text: "y"}))
	if command == nil {
		t.Fatal("confirmed cross-worktree selection did not start switching")
	}
	picker.Update(command())
	if client.switchedSession != "ses_other" || picker.Stage() != SessionPickerClosed {
		t.Fatalf("cross-worktree switch = %q stage=%s", client.switchedSession, picker.Stage())
	}
}

func TestModelApprovalKeysSubmitExactResolution(t *testing.T) {
	client := newInteractionClient()
	model := NewModel(client, nil, client.snapshot)
	model.activeTurn = "turn_interaction"
	model.snapshot.RuntimeState = session.RuntimeAwaitingApproval
	model.approval = &session.ApprovalRequest{
		ID: "approval_interaction", SessionID: client.snapshot.Session.ID, TurnID: model.activeTurn,
		Action: session.Action{Kind: session.ActionApplyPatch, Summary: "Apply parser fix"},
	}

	_, command := model.Update(tea.KeyPressMsg(tea.Key{Code: 'y', Text: "y"}))
	if command == nil || model.approval != nil {
		t.Fatal("approval command was not created")
	}
	model.Update(command())
	if client.resolution.RequestID != "approval_interaction" || client.resolution.Decision.Kind != session.ApprovalAllowOnce {
		t.Fatalf("unexpected approval resolution: %#v", client.resolution)
	}
}

func TestProviderPickerPastedCredentialIsMasked(t *testing.T) {
	controller := &testModelController{
		configured: session.ProviderProfile{ID: "prv_new", Kind: "openai", DisplayName: "OpenAI", ModelID: "gpt-test"},
		models:     []session.ModelOption{{ID: "gpt-test", DisplayName: "GPT Test"}},
	}
	picker := NewProviderPicker(controller)
	picker.Update(picker.Open(context.Background())())
	if command := picker.HandleKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})); command != nil {
		t.Fatal("selecting OpenAI should first request its API key")
	}
	model := &Model{providerPicker: picker}
	model.Update(tea.PasteMsg{Content: "sk-interaction-secret\r\n"})
	if view := picker.View(); strings.Contains(view, "sk-interaction-secret") || !strings.Contains(view, "••") {
		t.Fatalf("provider view did not mask the credential: %q", view)
	}
	configureCommand := picker.HandleKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if configureCommand == nil {
		t.Fatal("completed provider form did not start validation")
	}
	picker.Update(configureCommand())
	if string(controller.credentialSeen) != "sk-interaction-secret" {
		t.Fatalf("provider controller received pasted credential %q", controller.credentialSeen)
	}
}

func TestModelPasteAddsSanitizedMultilineComposerInput(t *testing.T) {
	model := NewModel(nil, nil, testSnapshot())
	model.Update(tea.PasteMsg{Content: "first line\r\nsecond\x1b[31m line"})
	if value := string(model.composer); value != "first line\nsecond line" {
		t.Fatalf("sanitized composer paste = %q", value)
	}
}

func TestModelProviderPickerWindowsArrowUpdatesSelectionAndHidesComposer(t *testing.T) {
	client := newInteractionClient()
	model := NewModel(client, nil, client.snapshot)
	openCommand := model.providerPicker.Open(context.Background())
	model.providerPicker.Update(openCommand())
	model.Update(tea.WindowSizeMsg{Width: 80, Height: 20})

	initial := model.View().Content
	if !strings.Contains(initial, "› OpenAI") {
		t.Fatalf("initial provider selection is missing:\n%s", initial)
	}
	if strings.Contains(initial, "/help") {
		t.Fatalf("composer help leaked into the provider picker:\n%s", initial)
	}

	// The native Windows input path may put the physical arrow in BaseCode.
	model.Update(tea.KeyPressMsg(tea.Key{BaseCode: tea.KeyDown}))
	updated := model.View().Content
	if !strings.Contains(updated, "› DeepSeek") || strings.Contains(updated, "› OpenAI") {
		t.Fatalf("provider selection did not follow the Windows arrow event:\n%s", updated)
	}
	if strings.Contains(updated, "/help") {
		t.Fatalf("composer help appeared after provider navigation:\n%s", updated)
	}
}

func TestModelSlashCompletionFiltersAndInsertsCommand(t *testing.T) {
	client := newInteractionClient()
	model := NewModel(client, nil, client.snapshot)
	model.Update(tea.WindowSizeMsg{Width: 100, Height: 24})

	model.Update(tea.KeyPressMsg(tea.Key{Code: '/', Text: "/"}))
	if view := model.View().Content; !strings.Contains(view, "Commands") || !strings.Contains(view, "/model") || !strings.Contains(view, "/permissions") {
		t.Fatalf("slash did not open command completion:\n%s", view)
	}
	model.Update(tea.KeyPressMsg(tea.Key{Code: 'm', Text: "m"}))
	model.Update(tea.KeyPressMsg(tea.Key{Code: 'o', Text: "o"}))
	if len(model.completion.items) != 1 || model.completion.items[0].value != "/model" {
		t.Fatalf("/mo completion = %#v", model.completion.items)
	}
	model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if string(model.composer) != "/model" || model.completion.active() {
		t.Fatalf("selected command composer=%q completion=%#v", model.composer, model.completion)
	}
	_, command := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if command == nil || model.providerPicker.Stage() != ProviderPickerLoadingProfiles {
		t.Fatalf("inserted /model was not executable: stage=%s", model.providerPicker.Stage())
	}
}

func TestModelSlashCompletionExpandsExecutableSubcommands(t *testing.T) {
	items := commandCompletionItems("session ")
	values := make([]string, 0, len(items))
	for _, item := range items {
		values = append(values, item.value)
	}
	for _, expected := range []string{"/session create [TITLE]", "/session list", "/session list --all", "/session switch ID", "/session rename NAME", "/session archive"} {
		if !containsCompletionValue(values, expected) {
			t.Fatalf("expanded session commands %q do not contain %q", values, expected)
		}
	}
	if containsCompletionValue(values, "/session ACTION") {
		t.Fatalf("generic session command was not expanded: %q", values)
	}

	filtered := commandCompletionItems("session li")
	if len(filtered) != 2 || filtered[0].value != "/session list" || filtered[1].value != "/session list --all" {
		t.Fatalf("session list prefix completion = %#v", filtered)
	}
}

func TestModelSlashCompletionClosesForFreeformArguments(t *testing.T) {
	model := NewModel(newInteractionClient(), nil, testSnapshot())
	model.composer = []rune("/session create ")
	model.refreshCompletion()
	if model.completion.active() {
		t.Fatalf("free-form title kept command completion open: %#v", model.completion)
	}
}

func containsCompletionValue(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func TestModelFileCompletionLoadsFiltersAndInsertsMention(t *testing.T) {
	client := newInteractionClient()
	client.files = session.WorkspaceFileList{Files: []session.WorkspaceFile{
		{Path: "README.md"},
		{Path: "internal/", Directory: true},
		{Path: "internal/ui/model.go"},
		{Path: "internal/ui/update.go"},
	}}
	model := NewModel(client, nil, client.snapshot)

	_, loadCommand := model.Update(tea.KeyPressMsg(tea.Key{Text: "Fix @"}))
	if loadCommand == nil || !model.completion.loading {
		t.Fatal("@ did not start workspace file loading")
	}
	model.Update(loadCommand())
	if len(model.completion.items) != 4 {
		t.Fatalf("loaded file completion = %#v", model.completion.items)
	}
	model.Update(tea.KeyPressMsg(tea.Key{Text: "mod"}))
	if len(model.completion.items) != 1 || model.completion.items[0].value != "internal/ui/model.go" {
		t.Fatalf("@mod completion = %#v", model.completion.items)
	}
	model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if value := string(model.composer); value != "Fix @internal/ui/model.go " {
		t.Fatalf("file mention composer = %q", value)
	}
}

func TestModelFileCompletionKeepsDirectorySelectionOpen(t *testing.T) {
	client := newInteractionClient()
	client.files = session.WorkspaceFileList{Files: []session.WorkspaceFile{
		{Path: "internal/", Directory: true},
		{Path: "internal/ui/", Directory: true},
		{Path: "internal/ui/model.go"},
	}}
	model := NewModel(client, nil, client.snapshot)

	_, loadCommand := model.Update(tea.KeyPressMsg(tea.Key{Text: "@int"}))
	model.Update(loadCommand())
	model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if value := string(model.composer); value != "@internal/" {
		t.Fatalf("directory mention composer = %q", value)
	}
	if !model.completion.active() || len(model.completion.items) != 2 {
		t.Fatalf("directory selection did not continue browsing: %#v", model.completion)
	}
}

type interactionClient struct {
	snapshot        session.SessionSnapshot
	sessions        []session.SessionSummary
	turnText        string
	switchedSession session.SessionID
	resolution      session.ApprovalResolution
	files           session.WorkspaceFileList
}

func newInteractionClient() *interactionClient {
	snapshot := testSnapshot()
	snapshot.Session.WorkspaceID = "ws_interaction"
	snapshot.Session.WorktreeID = "wt_interaction"
	snapshot.Session.ProviderProfileID = "prv_interaction"
	return &interactionClient{snapshot: snapshot}
}

func (c *interactionClient) StartTurn(_ context.Context, text string) (session.TurnID, error) {
	c.turnText = text
	return "turn_interaction", nil
}

func (*interactionClient) CancelTurn(context.Context) error { return nil }

func (c *interactionClient) ResolveApproval(_ context.Context, resolution session.ApprovalResolution) error {
	c.resolution = resolution
	return nil
}

func (c *interactionClient) CurrentSession(context.Context) (session.SessionSnapshot, error) {
	return cloneSnapshot(c.snapshot), nil
}

func (c *interactionClient) CreateSession(context.Context, session.CreateSessionRequest) (session.SessionSummary, error) {
	return session.SessionSummary{ID: "ses_created"}, nil
}

func (c *interactionClient) ListSessions(context.Context, session.SessionFilter) ([]session.SessionSummary, error) {
	return append([]session.SessionSummary(nil), c.sessions...), nil
}

func (c *interactionClient) SwitchSession(_ context.Context, id session.SessionID) error {
	c.switchedSession = id
	c.snapshot.Session.ID = id
	return nil
}

func (*interactionClient) RenameSession(context.Context, session.SessionID, string) error { return nil }

func (*interactionClient) ArchiveSession(context.Context, session.SessionID) error { return nil }

func (*interactionClient) ListProviderProfiles(context.Context) ([]session.ProviderProfile, error) {
	return nil, nil
}

func (*interactionClient) ConfigureProvider(context.Context, session.ConfigureProviderRequest) (session.ProviderProfile, error) {
	return session.ProviderProfile{}, nil
}

func (*interactionClient) ListModels(context.Context, session.ProviderProfileID) ([]session.ModelOption, error) {
	return nil, nil
}

func (*interactionClient) SwitchModel(context.Context, session.ModelSelection) error { return nil }

func (*interactionClient) OpenWorkspace(context.Context, string) (session.WorktreeSummary, error) {
	return session.WorktreeSummary{}, nil
}

func (*interactionClient) ListWorkspaces(context.Context) ([]session.WorktreeSummary, error) {
	return []session.WorktreeSummary{{ID: "wt_interaction", Root: "repo", Available: true, LastUsedAt: time.Now()}}, nil
}

func (c *interactionClient) ListWorkspaceFiles(context.Context, int) (session.WorkspaceFileList, error) {
	return session.WorkspaceFileList{Files: append([]session.WorkspaceFile(nil), c.files.Files...), Truncated: c.files.Truncated}, nil
}

func (*interactionClient) ReadDiff(_ context.Context, kind session.DiffKind) (session.DiffResult, error) {
	return session.DiffResult{Kind: kind}, nil
}

func (c *interactionClient) SetPermissionMode(_ context.Context, mode session.PermissionMode) error {
	c.snapshot.Session.PermissionMode = mode
	return nil
}
