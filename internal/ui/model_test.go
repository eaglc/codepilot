package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/eaglc/codepilot/internal/codingagent"
)

type fakeClient struct{ snapshot codingagent.Snapshot }

func (c fakeClient) Snapshot(context.Context, codingagent.SessionID) (codingagent.Snapshot, error) {
	return c.snapshot, nil
}
func (fakeClient) ListSessions(context.Context, codingagent.SessionListOptions) ([]codingagent.Session, error) {
	return nil, nil
}
func (fakeClient) CreateSession(context.Context, codingagent.Session) (codingagent.Session, error) {
	return codingagent.Session{}, nil
}
func (c fakeClient) SwitchSession(context.Context, codingagent.SessionID) (codingagent.Snapshot, error) {
	return c.snapshot, nil
}
func (fakeClient) RenameSession(context.Context, codingagent.SessionID, string) (codingagent.Session, error) {
	return codingagent.Session{}, nil
}
func (fakeClient) SetPermissionMode(context.Context, codingagent.SessionID, codingagent.PermissionMode) (codingagent.Session, error) {
	return codingagent.Session{}, nil
}
func (fakeClient) ArchiveSession(context.Context, codingagent.SessionID) (codingagent.Session, error) {
	return codingagent.Session{}, nil
}
func (c fakeClient) ForkLane(context.Context, codingagent.ForkLaneRequest) (codingagent.Snapshot, error) {
	return c.snapshot, nil
}
func (fakeClient) StartTurn(context.Context, codingagent.TurnRequest) (codingagent.TurnResult, error) {
	return codingagent.TurnResult{}, nil
}
func (fakeClient) ResumeTurn(context.Context, codingagent.ResumeTurnRequest) (codingagent.TurnResult, error) {
	return codingagent.TurnResult{}, nil
}
func (fakeClient) RecoverTurn(context.Context, codingagent.RecoverTurnRequest) (codingagent.TurnResult, error) {
	return codingagent.TurnResult{}, nil
}
func (fakeClient) CancelTurn(context.Context, codingagent.SessionID) error { return nil }
func (fakeClient) ListProviderProfiles(context.Context) ([]codingagent.ProviderProfile, error) {
	return nil, nil
}
func (fakeClient) ConfigureProvider(context.Context, codingagent.ConfigureProviderRequest) (codingagent.ProviderProfile, error) {
	return codingagent.ProviderProfile{}, nil
}
func (fakeClient) ListProviderModels(context.Context, string) ([]codingagent.ProviderModel, error) {
	return nil, nil
}
func (fakeClient) SelectProviderModel(context.Context, codingagent.SessionID, string, string) (codingagent.Session, error) {
	return codingagent.Session{}, nil
}
func (fakeClient) ListWorkspaces(context.Context) ([]codingagent.WorkspaceSummary, error) {
	return nil, nil
}
func (fakeClient) RelocateWorktree(context.Context, codingagent.RelocateWorktreeRequest) (codingagent.Worktree, error) {
	return codingagent.Worktree{}, nil
}

type cancelClient struct {
	fakeClient
	calls int
	id    codingagent.SessionID
}

type workspaceClient struct {
	fakeClient
	workspaces []codingagent.WorkspaceSummary
	sessions   []codingagent.Session
	snapshots  map[codingagent.SessionID]codingagent.Snapshot
	relocated  codingagent.RelocateWorktreeRequest
}

func (c *workspaceClient) ListWorkspaces(context.Context) ([]codingagent.WorkspaceSummary, error) {
	return c.workspaces, nil
}
func (c *workspaceClient) RelocateWorktree(_ context.Context, request codingagent.RelocateWorktreeRequest) (codingagent.Worktree, error) {
	c.relocated = request
	return codingagent.Worktree{ID: request.WorktreeID, Root: request.NewPath}, nil
}
func (c *workspaceClient) ListSessions(_ context.Context, options codingagent.SessionListOptions) ([]codingagent.Session, error) {
	var values []codingagent.Session
	for _, session := range c.sessions {
		if options.WorktreeID == "" || session.WorktreeID == options.WorktreeID {
			values = append(values, session)
		}
	}
	return values, nil
}
func (c *workspaceClient) SwitchSession(_ context.Context, id codingagent.SessionID) (codingagent.Snapshot, error) {
	return c.snapshots[id], nil
}

func (c *cancelClient) CancelTurn(_ context.Context, id codingagent.SessionID) error {
	c.calls++
	c.id = id
	return nil
}

type providerPickerClient struct {
	fakeClient
	models            []codingagent.ProviderModel
	modelsErr         error
	configuredProfile codingagent.ProviderProfile
	credentialSeen    string
	configureCalls    int
	listModelsCalls   int
}

type recoveryClient struct {
	fakeClient
	request codingagent.RecoverTurnRequest
	calls   int
	result  codingagent.TurnResult
}

type approvalClient struct {
	fakeClient
	request codingagent.ResumeTurnRequest
	calls   int
}

type commandRoutingClient struct {
	fakeClient
	startCalls int
}

func (c *commandRoutingClient) StartTurn(context.Context, codingagent.TurnRequest) (codingagent.TurnResult, error) {
	c.startCalls++
	return codingagent.TurnResult{}, nil
}

func (c *approvalClient) ResumeTurn(_ context.Context, request codingagent.ResumeTurnRequest) (codingagent.TurnResult, error) {
	c.calls++
	c.request = request
	return codingagent.TurnResult{}, nil
}

type sessionClient struct {
	fakeClient
	sessions       []codingagent.Session
	snapshots      map[codingagent.SessionID]codingagent.Snapshot
	created        codingagent.Session
	createdResult  codingagent.Session
	archived       codingagent.SessionID
	forkRequest    codingagent.ForkLaneRequest
	forkedSnapshot codingagent.Snapshot
	permissionID   codingagent.SessionID
	permissionMode codingagent.PermissionMode
	renamedID      codingagent.SessionID
	renamedTitle   string
	turnRequest    codingagent.TurnRequest
}

func (c *sessionClient) ListSessions(context.Context, codingagent.SessionListOptions) ([]codingagent.Session, error) {
	return append([]codingagent.Session(nil), c.sessions...), nil
}

func (c *sessionClient) CreateSession(_ context.Context, session codingagent.Session) (codingagent.Session, error) {
	c.created = session
	return c.createdResult, nil
}

func (c *sessionClient) SwitchSession(_ context.Context, id codingagent.SessionID) (codingagent.Snapshot, error) {
	return c.snapshots[id], nil
}

func (c *sessionClient) ArchiveSession(_ context.Context, id codingagent.SessionID) (codingagent.Session, error) {
	c.archived = id
	return codingagent.Session{ID: id, Archived: true}, nil
}

func (c *sessionClient) RenameSession(_ context.Context, id codingagent.SessionID, title string) (codingagent.Session, error) {
	c.renamedID = id
	c.renamedTitle = title
	session := c.snapshot.Session
	session.ID = id
	session.Title = title
	return session, nil
}

func (c *sessionClient) StartTurn(_ context.Context, request codingagent.TurnRequest) (codingagent.TurnResult, error) {
	c.turnRequest = request
	return codingagent.TurnResult{Status: "completed"}, nil
}

func (c *sessionClient) ForkLane(_ context.Context, request codingagent.ForkLaneRequest) (codingagent.Snapshot, error) {
	c.forkRequest = request
	return c.forkedSnapshot, nil
}

func (c *sessionClient) SetPermissionMode(_ context.Context, id codingagent.SessionID, mode codingagent.PermissionMode) (codingagent.Session, error) {
	c.permissionID = id
	c.permissionMode = mode
	session := c.snapshot.Session
	session.ID = id
	session.PermissionMode = mode
	return session, nil
}

func (c *recoveryClient) RecoverTurn(_ context.Context, request codingagent.RecoverTurnRequest) (codingagent.TurnResult, error) {
	c.calls++
	c.request = request
	return c.result, nil
}

func (c *providerPickerClient) ConfigureProvider(_ context.Context, request codingagent.ConfigureProviderRequest) (codingagent.ProviderProfile, error) {
	c.configureCalls++
	c.credentialSeen = string(request.Credential)
	return c.configuredProfile, nil
}

func (c *providerPickerClient) ListProviderModels(context.Context, string) ([]codingagent.ProviderModel, error) {
	c.listModelsCalls++
	return c.models, c.modelsErr
}

func TestToolResultCollapsesThenShowsWideDiffPane(t *testing.T) {
	bridge, err := NewEventBridge(4)
	if err != nil {
		t.Fatalf("create bridge: %v", err)
	}
	snapshot := codingagent.Snapshot{
		Session: codingagent.Session{ID: "session", Title: "repo", ProviderProfileID: "openai", ModelID: "model"},
		Transcript: []codingagent.TranscriptItem{
			{Kind: codingagent.TranscriptText, Role: codingagent.TranscriptRoleUser, Text: "change it"},
			{Kind: codingagent.TranscriptToolCall, Tool: &codingagent.TranscriptTool{CallID: "call-1", Name: "apply_patch", Status: "requested"}},
			{Kind: codingagent.TranscriptToolResult, Tool: &codingagent.TranscriptTool{
				CallID: "call-1", Name: "apply_patch", Status: "completed", Summary: "Applied patch", Detail: "verbose tool output",
				Diff: &codingagent.InlineDiff{Text: "--- a/main.go\n+++ b/main.go\n-old\n+new\n", Files: []string{"main.go"}},
			}},
		},
	}
	model, err := NewModel(context.Background(), fakeClient{snapshot: snapshot}, bridge, snapshot)
	if err != nil {
		t.Fatalf("create model: %v", err)
	}
	model.width, model.height = 100, 24
	view := model.View().Content
	if strings.Contains(view, "verbose tool output") {
		t.Fatal("tool detail is expanded by default")
	}
	if strings.Contains(view, "Applied patch") || strings.Contains(view, "Applied changes") || strings.Contains(view, "+new") || strings.Contains(view, "Conversation") {
		t.Fatalf("collapsed tool leaked summary or diff = %q", view)
	}
	clickY := -1
	for row, callID := range model.hitRows {
		if callID == "call-1" {
			clickY = row
			break
		}
	}
	if clickY < 0 {
		t.Fatal("tool activity did not create a clickable row")
	}
	model.Update(tea.MouseClickMsg(tea.Mouse{X: 3, Y: clickY, Button: tea.MouseLeft}))
	model.Update(tea.MouseReleaseMsg(tea.Mouse{X: 3, Y: clickY, Button: tea.MouseLeft}))
	if view = model.View().Content; !strings.Contains(view, "verbose tool output") || !strings.Contains(view, "Changes  •  apply_patch  •  1 file") || !strings.Contains(view, "-old") || !strings.Contains(view, "+new") || !strings.Contains(view, "│") {
		t.Fatalf("expanded tool view = %q", view)
	}
}

func TestNarrowToolDiffFallsBackToInlineExpansion(t *testing.T) {
	bridge, _ := NewEventBridge(2)
	defer bridge.Close()
	snapshot := codingagent.Snapshot{
		Session: codingagent.Session{ID: "session", Title: "repo"},
		Transcript: []codingagent.TranscriptItem{{Kind: codingagent.TranscriptToolResult, Tool: &codingagent.TranscriptTool{
			CallID: "call", Name: "apply_patch", Status: "completed",
			Diff: &codingagent.InlineDiff{Text: "--- a/main.go\n+++ b/main.go\n@@ -1 +1 @@\n-old\n+new\n", Files: []string{"main.go"}},
		}}},
	}
	model, err := NewModel(context.Background(), fakeClient{snapshot: snapshot}, bridge, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	model.width, model.height = 80, 20
	model.selectBlock(model.selectableBlocks()[0])
	model.expanded["call"] = true
	view := ansi.Strip(model.View().Content)
	if !strings.Contains(view, "Applied changes") || !strings.Contains(view, "-old") || strings.Contains(view, "Changes  •  apply_patch") {
		t.Fatalf("narrow diff fallback = %q", view)
	}
}

func TestDiffPaneShowsOldAndNewLineNumbersWithChangeColors(t *testing.T) {
	rows := diffPaneContentLines("--- a/main.go\n+++ b/main.go\n@@ -4,2 +7,2 @@\n-old\n+new\n same\n", 80)
	if len(rows) != 6 {
		t.Fatalf("diff rows = %#v", rows)
	}
	removed := ansi.Strip(rows[3])
	added := ansi.Strip(rows[4])
	contextLine := ansi.Strip(rows[5])
	if !strings.Contains(removed, "4") || !strings.Contains(removed, "-old") || !strings.Contains(added, "7") || !strings.Contains(added, "+new") || !strings.Contains(contextLine, "5") || !strings.Contains(contextLine, "8") {
		t.Fatalf("numbered diff rows: removed=%q added=%q context=%q", removed, added, contextLine)
	}
	if rows[3] != theme.removed.Render(removed) || rows[4] != theme.added.Render(added) {
		t.Fatalf("diff change colors were not applied: removed=%q added=%q", rows[3], rows[4])
	}
}

func TestMouseWheelOverDiffPaneScrollsDiffIndependently(t *testing.T) {
	bridge, _ := NewEventBridge(2)
	defer bridge.Close()
	var diff strings.Builder
	diff.WriteString("--- a/main.go\n+++ b/main.go\n@@ -1,20 +1,20 @@\n")
	for index := 0; index < 20; index++ {
		fmt.Fprintf(&diff, "-old %d\n+new %d\n", index, index)
	}
	snapshot := codingagent.Snapshot{
		Session: codingagent.Session{ID: "session", Title: "repo"},
		Transcript: []codingagent.TranscriptItem{{Kind: codingagent.TranscriptToolResult, Tool: &codingagent.TranscriptTool{
			CallID: "call", Name: "apply_patch", Status: "completed",
			Diff: &codingagent.InlineDiff{Text: diff.String(), Files: []string{"main.go"}},
		}}},
	}
	model, err := NewModel(context.Background(), fakeClient{snapshot: snapshot}, bridge, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	model.width, model.height = 120, 12
	model.selectBlock(model.selectableBlocks()[0])
	model.expanded["call"] = true
	_ = model.View()
	if !model.diffPaneActive || model.diffMaxScroll == 0 {
		t.Fatalf("long diff did not activate a scrollable pane: active=%v max=%d", model.diffPaneActive, model.diffMaxScroll)
	}
	conversationScroll := model.scroll
	model.Update(tea.MouseWheelMsg(tea.Mouse{X: model.width - 2, Y: 4, Button: tea.MouseWheelDown}))
	if model.diffScroll == 0 || model.scroll != conversationScroll {
		t.Fatalf("right-pane wheel routing: diff=%d conversation=%d want conversation=%d", model.diffScroll, model.scroll, conversationScroll)
	}
}

func TestConversationShowsDistanceFromBottomAfterPageUp(t *testing.T) {
	bridge, err := NewEventBridge(2)
	if err != nil {
		t.Fatal(err)
	}
	defer bridge.Close()
	snapshot := codingagent.Snapshot{Session: codingagent.Session{ID: "session", Title: "repo", ProviderProfileID: "provider", ModelID: "model"}}
	for index := 0; index < 24; index++ {
		snapshot.Transcript = append(snapshot.Transcript, codingagent.TranscriptItem{Kind: codingagent.TranscriptText, Role: codingagent.TranscriptRoleUser, Text: "message"})
	}
	model, err := NewModel(context.Background(), fakeClient{snapshot: snapshot}, bridge, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	model.width, model.height = 100, 10
	_ = model.View()
	model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyPgUp}))
	if view := model.View().Content; !strings.Contains(view, "lines below") {
		t.Fatalf("scrolled conversation has no position feedback: %q", view)
	}
}

func TestConversationScrollbarSupportsWheelAndDrag(t *testing.T) {
	bridge, err := NewEventBridge(2)
	if err != nil {
		t.Fatal(err)
	}
	defer bridge.Close()
	snapshot := codingagent.Snapshot{Session: codingagent.Session{ID: "session", Title: "repo", ProviderProfileID: "provider", ModelID: "model"}}
	for index := 0; index < 24; index++ {
		snapshot.Transcript = append(snapshot.Transcript, codingagent.TranscriptItem{Kind: codingagent.TranscriptText, Role: codingagent.TranscriptRoleUser, Text: fmt.Sprintf("message %d", index)})
	}
	model, err := NewModel(context.Background(), fakeClient{snapshot: snapshot}, bridge, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	model.width, model.height = 50, 12
	view := model.View().Content
	if !model.scrollbar.active || !strings.Contains(view, "█") || !strings.Contains(view, "│") {
		t.Fatalf("overflowing conversation has no scrollbar: state=%#v view=%q", model.scrollbar, view)
	}
	bottom := model.scroll
	model.Update(tea.MouseWheelMsg(tea.Mouse{X: 10, Y: 4, Button: tea.MouseWheelUp}))
	_ = model.View()
	if model.scroll >= bottom || model.followBottom {
		t.Fatalf("mouse wheel did not scroll up: scroll=%d bottom=%d follow=%v", model.scroll, bottom, model.followBottom)
	}

	right := model.width - 1
	model.Update(tea.MouseClickMsg(tea.Mouse{X: right, Y: model.scrollbar.trackTop, Button: tea.MouseLeft}))
	model.Update(tea.MouseReleaseMsg(tea.Mouse{X: right, Y: model.scrollbar.trackTop, Button: tea.MouseLeft}))
	if model.scroll != 0 || model.followBottom {
		t.Fatalf("top track click did not move to start: scroll=%d follow=%v", model.scroll, model.followBottom)
	}
	_ = model.View()
	model.Update(tea.MouseClickMsg(tea.Mouse{X: right, Y: model.scrollbar.thumbTop, Button: tea.MouseLeft}))
	bottomY := model.scrollbar.trackTop + model.scrollbar.trackHeight - 1
	model.Update(tea.MouseMotionMsg(tea.Mouse{X: right, Y: bottomY, Button: tea.MouseLeft}))
	model.Update(tea.MouseReleaseMsg(tea.Mouse{X: right, Y: bottomY, Button: tea.MouseLeft}))
	if model.scroll != model.scrollbar.maxScroll || !model.followBottom || model.scrollbar.dragging {
		t.Fatalf("scrollbar drag did not reach end: state=%#v scroll=%d follow=%v", model.scrollbar, model.scroll, model.followBottom)
	}
}

func TestComposerFooterPlacesStatusBelowInput(t *testing.T) {
	bridge, _ := NewEventBridge(2)
	defer bridge.Close()
	snapshot := codingagent.Snapshot{
		Session: codingagent.Session{ID: "session", Title: "repo"},
		Metrics: codingagent.SessionMetrics{ContextTokens: 40, TotalTokens: 150, Cost: .03},
	}
	model, err := NewModel(context.Background(), fakeClient{snapshot: snapshot}, bridge, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	model.width, model.height = 60, 10
	lines := strings.Split(model.View().Content, "\n")
	if len(lines) != model.height ||
		!strings.Contains(lines[len(lines)-4], "❯") ||
		!strings.Contains(ansi.Strip(lines[len(lines)-3]), "────") ||
		!strings.Contains(ansi.Strip(lines[len(lines)-2]), "Current context 40") ||
		!strings.Contains(ansi.Strip(lines[len(lines)-1]), "Ready") {
		t.Fatalf("composer spacing lines=%d height=%d tail=%q", len(lines), model.height, lines[max(0, len(lines)-3):])
	}
	view := ansi.Strip(model.View().Content)
	if strings.Contains(view, "Enter send") || !strings.Contains(view, "Current context 40") || !strings.Contains(view, "Tokens 150") || !strings.Contains(view, "────") {
		t.Fatalf("composer footer did not separate metrics from conversation: %q", view)
	}
}

func TestComposerUsesNativeCursorForChineseAndEmojiInput(t *testing.T) {
	bridge, _ := NewEventBridge(2)
	defer bridge.Close()
	snapshot := codingagent.Snapshot{Session: codingagent.Session{ID: "session", Title: "repo"}}
	model, err := NewModel(context.Background(), fakeClient{snapshot: snapshot}, bridge, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	model.width, model.height = 40, 10
	for _, value := range []string{"中", "文", "🙂"} {
		model.Update(tea.KeyPressMsg(tea.Key{Text: value}))
	}
	view := model.View()
	if string(model.input) != "中文🙂" || model.cursor != 3 {
		t.Fatalf("input=%q cursor=%d", string(model.input), model.cursor)
	}
	if view.Cursor == nil || view.Cursor.X != ansi.StringWidth("❯ 中文🙂") || view.Cursor.Y != model.height-4 {
		t.Fatalf("native cursor=%#v want x=%d y=%d", view.Cursor, ansi.StringWidth("❯ 中文🙂"), model.height-4)
	}
	prompt := strings.Split(view.Content, "\n")[model.height-4]
	if got := ansi.Strip(prompt); got != "❯ 中文🙂" || strings.Contains(prompt, "\x1b[7m") {
		t.Fatalf("prompt contains a fake cursor or duplicate text: %q", prompt)
	}

	model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyLeft}))
	view = model.View()
	if view.Cursor == nil || view.Cursor.X != ansi.StringWidth("❯ 中文") {
		t.Fatalf("cursor after moving left=%#v", view.Cursor)
	}
	model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyBackspace}))
	view = model.View()
	if string(model.input) != "中🙂" || model.cursor != 1 || view.Cursor == nil || view.Cursor.X != ansi.StringWidth("❯ 中") {
		t.Fatalf("after backspace input=%q cursor=%d native=%#v", string(model.input), model.cursor, view.Cursor)
	}
	model.busy = true
	if cursor := model.View().Cursor; cursor != nil {
		t.Fatalf("busy composer exposed an input cursor: %#v", cursor)
	}
}

func TestComposerHorizontallyKeepsWideCursorOnScreen(t *testing.T) {
	bridge, _ := NewEventBridge(2)
	defer bridge.Close()
	snapshot := codingagent.Snapshot{Session: codingagent.Session{ID: "session", Title: "repo"}}
	model, err := NewModel(context.Background(), fakeClient{snapshot: snapshot}, bridge, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	model.width, model.height = 20, 10
	model.input = []rune(strings.Repeat("界", 20))
	model.cursor = len(model.input)
	view := model.View()
	if view.Cursor == nil || view.Cursor.X < 0 || view.Cursor.X >= model.width {
		t.Fatalf("off-screen cursor: %#v", view.Cursor)
	}
	prompt := strings.Split(view.Content, "\n")[model.height-4]
	if width := ansi.StringWidth(prompt); width > model.width {
		t.Fatalf("prompt width=%d exceeds terminal width=%d: %q", width, model.width, prompt)
	}
}

func TestOverlayInputsUseNativeWideCharacterCursors(t *testing.T) {
	provider := Model{picker: providerPicker{active: true, stage: providerForm, form: providerFormState{
		field: 1, display: "中文模型", cursor: 2,
	}}}
	providerView := provider.providerView(60, 14)
	wantProviderX := ansi.StringWidth("❯ Display name: 中文")
	if providerView.Cursor == nil || providerView.Cursor.X != wantProviderX || providerView.Cursor.Y != 4 || strings.Contains(providerView.Content, "\x1b[7m") {
		t.Fatalf("provider cursor=%#v want x=%d y=4", providerView.Cursor, wantProviderX)
	}

	workspace := Model{workspacePicker: workspacePicker{
		active: true, relocating: true, pathInput: []rune(`H:\项目`),
		items: []workspacePickerItem{{
			workspace: codingagent.WorkspaceSummary{ID: "workspace", DisplayName: "Project"},
			worktree:  codingagent.WorktreeSummary{ID: "worktree", Root: `H:\old`, Availability: codingagent.WorktreeUnavailable},
		}},
	}}
	workspaceView := workspace.workspaceView(60, 14)
	wantWorkspaceX := ansi.StringWidth(`❯ H:\项目`)
	if workspaceView.Cursor == nil || workspaceView.Cursor.X != wantWorkspaceX || workspaceView.Cursor.Y != 7 {
		t.Fatalf("workspace cursor=%#v want x=%d y=7", workspaceView.Cursor, wantWorkspaceX)
	}
}

func TestToolRowsShowFileRangesAndChangeCounts(t *testing.T) {
	bridge, _ := NewEventBridge(2)
	defer bridge.Close()
	snapshot := codingagent.Snapshot{
		Session: codingagent.Session{ID: "session", Title: "repo"},
		Transcript: []codingagent.TranscriptItem{
			{Kind: codingagent.TranscriptToolResult, Tool: &codingagent.TranscriptTool{
				CallID: "read", Name: "read_file", Status: "completed", Summary: "Read file",
				Resources: []codingagent.ToolResource{{Path: "internal/ui/model.go", StartLine: 923, EndLine: 981}},
			}},
			{Kind: codingagent.TranscriptToolResult, Tool: &codingagent.TranscriptTool{
				CallID: "write", Name: "apply_patch", Status: "completed", Summary: "Applied patch",
				Resources: []codingagent.ToolResource{{Path: "internal/ui/model.go", AddedLines: 12, DeletedLines: 3}},
			}},
			{Kind: codingagent.TranscriptToolResult, Tool: &codingagent.TranscriptTool{
				CallID: "check", Name: "run_checks", Status: "completed", Summary: "All tests passed", Detail: "verbose test output",
			}},
		},
	}
	model, err := NewModel(context.Background(), fakeClient{snapshot: snapshot}, bridge, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	view := ansi.Strip(model.View().Content)
	for _, expected := range []string{"internal/ui/model.go  lines 923–981 (59 lines)", "internal/ui/model.go  +12 -3"} {
		if !strings.Contains(view, expected) {
			t.Fatalf("tool resource view does not contain %q: %q", expected, view)
		}
	}
	for _, hidden := range []string{"Read file", "Applied patch", "All tests passed", "verbose test output"} {
		if strings.Contains(view, hidden) {
			t.Fatalf("collapsed tool exposed %q: %q", hidden, view)
		}
	}
}

func TestExpandedToolDiffNormalizesLineEndingsAndEscapesTerminalControls(t *testing.T) {
	model := Model{expanded: map[string]bool{"replace": true}}
	rows := model.toolRows(codingagent.TranscriptTool{
		CallID: "replace", Name: "replace_file", Status: "completed",
		Diff: &codingagent.InlineDiff{Text: "--- a/README.md\r\n+++ b/README.md\r\n-旧内容\r\n+新内容\x1b[31m\r\n"},
	}, 100)
	plain := ansi.Strip(renderedRows(rows))
	if strings.Contains(plain, "\r") {
		t.Fatalf("rendered diff contains a carriage return: %q", plain)
	}
	for _, expected := range []string{"--- a/README.md", "-旧内容", `+新内容\x1b[31m`} {
		if !strings.Contains(plain, expected) {
			t.Fatalf("rendered diff does not contain %q: %q", expected, plain)
		}
	}
}

func TestToolDetailsSupportKeyboardSelectionAndExpansion(t *testing.T) {
	bridge, _ := NewEventBridge(2)
	defer bridge.Close()
	snapshot := codingagent.Snapshot{
		Session: codingagent.Session{ID: "session", Title: "repo"},
		Transcript: []codingagent.TranscriptItem{{Kind: codingagent.TranscriptToolResult, Tool: &codingagent.TranscriptTool{
			CallID: "call", Name: "read_file", Status: "completed", Summary: "Read main.go", Detail: "package main",
		}}},
	}
	model, err := NewModel(context.Background(), fakeClient{snapshot: snapshot}, bridge, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if view := model.View().Content; strings.Contains(view, "Read main.go") || strings.Contains(view, "package main") {
		t.Fatalf("collapsed read_file exposed content: %q", view)
	}
	model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab}))
	if model.selectedTool != "call" || !strings.Contains(model.statusLine(), "Tool selected") {
		t.Fatalf("selected tool=%q status=%q", model.selectedTool, model.statusLine())
	}
	model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if !model.expanded["call"] || !strings.Contains(model.View().Content, "package main") {
		t.Fatalf("expanded=%v view=%q", model.expanded["call"], model.View().Content)
	}
	model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyLeft}))
	if model.expanded["call"] {
		t.Fatal("left arrow did not collapse selected tool")
	}
	model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	if model.selectedTool != "" {
		t.Fatalf("escape kept tool selected: %q", model.selectedTool)
	}
}

func TestStatusDisplaysDurableTurnMetrics(t *testing.T) {
	bridge, _ := NewEventBridge(2)
	defer bridge.Close()
	snapshot := codingagent.Snapshot{
		Session: codingagent.Session{ID: "session", Title: "repo"},
		Transcript: []codingagent.TranscriptItem{{
			ID: "user", TurnID: "turn", Kind: codingagent.TranscriptText, Role: codingagent.TranscriptRoleUser, Text: "work",
		}},
		Metrics: codingagent.SessionMetrics{
			LatestTurnID: "turn", Steps: 2, ContextTokens: 40, TotalTokens: 150, Cost: .03,
			StartedAt: time.Now().Add(-4 * time.Second), FinishedAt: time.Now(), Elapsed: 4 * time.Second,
		},
	}
	model, err := NewModel(context.Background(), fakeClient{snapshot: snapshot}, bridge, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	status := ansi.Strip(model.statusLine())
	for _, unexpected := range []string{"Step", "Context", "Tokens", "Cost", "Time"} {
		if strings.Contains(status, unexpected) {
			t.Fatalf("current status mixed in %q: %q", unexpected, status)
		}
	}
	metrics := ansi.Strip(model.sessionMetricsLine())
	for _, expected := range []string{"Current context 40", "Session total", "Tokens 150", "Cost $0.0300"} {
		if !strings.Contains(metrics, expected) {
			t.Fatalf("session metrics do not contain %q: %q", expected, metrics)
		}
	}
	turn := ansi.Strip(renderedRows(model.conversationRows(80)))
	for _, expected := range []string{"Worked for 4s", "2 steps"} {
		if !strings.Contains(turn, expected) {
			t.Fatalf("turn metrics do not contain %q: %q", expected, turn)
		}
	}
}

func TestAssistantMarkdownRendersHeadingsListsAndHighlightedCode(t *testing.T) {
	bridge, _ := NewEventBridge(2)
	defer bridge.Close()
	snapshot := codingagent.Snapshot{
		Session:    codingagent.Session{ID: "session", Title: "repo"},
		Transcript: []codingagent.TranscriptItem{{ID: "answer", Kind: codingagent.TranscriptText, Role: codingagent.TranscriptRoleAssistant, Text: "# Result\n\n- passed\n\n```go\npackage main\nfunc main() {}\n```"}},
	}
	model, err := NewModel(context.Background(), fakeClient{snapshot: snapshot}, bridge, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	lines := model.appendMarkdown(nil, "answer", snapshot.Transcript[0].Text, 80)
	var rendered strings.Builder
	for _, line := range lines {
		rendered.WriteString(line.text)
		rendered.WriteByte('\n')
	}
	output := rendered.String()
	for _, expected := range []string{"Result", "passed", "package", "main"} {
		if !strings.Contains(output, expected) {
			t.Fatalf("Markdown output does not contain %q: %q", expected, output)
		}
	}
	if strings.Contains(output, "```go") || !strings.Contains(output, "\x1b[") {
		t.Fatalf("Markdown/code block was not rendered with terminal styles: %q", output)
	}
}

func TestAssistantStreamingAndDurableTextUseTheSameRenderer(t *testing.T) {
	bridge, _ := NewEventBridge(2)
	defer bridge.Close()
	text := "# Result\n\n- passed\n\n```go\npackage main\n```"
	snapshot := codingagent.Snapshot{Session: codingagent.Session{ID: "session", Title: "repo"}}
	model, err := NewModel(context.Background(), fakeClient{snapshot: snapshot}, bridge, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	model.liveAssistant = text
	live := renderedRows(model.conversationRows(80))
	model.liveAssistant = ""
	model.snapshot.Transcript = []codingagent.TranscriptItem{{ID: "answer", Kind: codingagent.TranscriptText, Role: codingagent.TranscriptRoleAssistant, Text: text}}
	durable := renderedRows(model.conversationRows(80))
	if live != durable {
		t.Fatalf("streaming and durable rendering differ:\nlive=%q\ndurable=%q", live, durable)
	}
}

func TestMarkdownCanBeToggledByCommandAndShortcut(t *testing.T) {
	bridge, _ := NewEventBridge(2)
	defer bridge.Close()
	snapshot := codingagent.Snapshot{Session: codingagent.Session{ID: "session", Title: "repo"}}
	model, err := NewModel(context.Background(), fakeClient{snapshot: snapshot}, bridge, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	model.replaceInput("/md off")
	model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if model.markdownEnabled {
		t.Fatal("/md off kept Markdown enabled")
	}
	plain := renderedRows(model.appendAssistant(nil, "answer", "# Result\n- passed", 80))
	if !strings.Contains(plain, "# Result") || !strings.Contains(plain, "- passed") {
		t.Fatalf("plain assistant rendering changed Markdown source: %q", plain)
	}
	model.Update(tea.KeyPressMsg(tea.Key{Code: 'm', Mod: tea.ModAlt}))
	if !model.markdownEnabled {
		t.Fatal("Alt+M did not re-enable Markdown")
	}
}

func TestMessagesAndToolResultsCanBeSelectedAndCopied(t *testing.T) {
	bridge, _ := NewEventBridge(2)
	defer bridge.Close()
	snapshot := codingagent.Snapshot{
		Session: codingagent.Session{ID: "session", Title: "repo"},
		Transcript: []codingagent.TranscriptItem{
			{ID: "user", Kind: codingagent.TranscriptText, Role: codingagent.TranscriptRoleUser, Text: "copy this request"},
			{ID: "assistant", Kind: codingagent.TranscriptText, Role: codingagent.TranscriptRoleAssistant, Text: "copy this answer"},
			{Kind: codingagent.TranscriptToolResult, Tool: &codingagent.TranscriptTool{CallID: "call", Name: "read_file", Summary: "Read main.go", Detail: "package main"}},
		},
	}
	model, err := NewModel(context.Background(), fakeClient{snapshot: snapshot}, bridge, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	plainRows := renderedRows(model.conversationRows(80))
	if strings.Contains(plainRows, "YOU") || strings.Contains(plainRows, "ASSISTANT") || !strings.Contains(plainRows, "❯ copy this request") {
		t.Fatalf("conversation message presentation = %q", plainRows)
	}
	if prompt := model.promptLine(); !strings.Contains(prompt, "❯") || strings.Contains(prompt, "INPUT") {
		t.Fatalf("input marker is not distinct: %q", prompt)
	}
	model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab}))
	if !strings.HasPrefix(model.selectedBlock, messageSelectionPrefix) || model.selectedTool != "" {
		t.Fatalf("first selected block = %q tool=%q", model.selectedBlock, model.selectedTool)
	}
	selectedRows := model.conversationRows(80)
	selectedText := "❯ copy this request"
	wantSelected := theme.selection.Render(selectedText + strings.Repeat(" ", 78-ansi.StringWidth(selectedText)))
	if len(selectedRows) < 1 || selectedRows[0].text != wantSelected {
		t.Fatalf("selected message is not visibly highlighted: %#v", selectedRows[:min(1, len(selectedRows))])
	}
	_, command := model.Update(tea.KeyPressMsg(tea.Key{Text: "y"}))
	if command == nil || fmt.Sprint(command()) != "copy this request" {
		t.Fatalf("message clipboard command = %#v", command)
	}

	model.cycleSelection()
	model.cycleSelection()
	model.cycleSelection()
	if model.selectedTool != "call" {
		t.Fatalf("selected tool = %q block=%q", model.selectedTool, model.selectedBlock)
	}
	command = model.copySelection()
	if command == nil || fmt.Sprint(command()) != "package main" {
		t.Fatalf("tool clipboard content = %#v", command)
	}
}

func TestSelectedTextCopiesDuringApprovalAndBusyTurn(t *testing.T) {
	bridge, _ := NewEventBridge(2)
	defer bridge.Close()
	snapshot := codingagent.Snapshot{
		Session: codingagent.Session{ID: "session", Title: "repo"},
		Transcript: []codingagent.TranscriptItem{
			{ID: "assistant", Kind: codingagent.TranscriptText, Role: codingagent.TranscriptRoleAssistant, Text: "copy while waiting"},
		},
		PendingInterrupts: []codingagent.PendingInterrupt{{TurnID: "turn", InterruptID: "approval", Kind: "approval", Summary: "Create file"}},
	}
	client := &cancelClient{fakeClient: fakeClient{snapshot: snapshot}}
	model, err := NewModel(context.Background(), client, bridge, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	model.busy = true
	model.markdownEnabled = false
	model.textSelection = textSelection{dragged: true, anchor: textPosition{row: 0, column: 0}, focus: textPosition{row: 0, column: 3}}
	_, command := model.Update(tea.KeyPressMsg(tea.Key{Code: 'c', Mod: tea.ModCtrl}))
	if command == nil || !strings.Contains(fmt.Sprint(command()), "copy") {
		t.Fatalf("clipboard command during approval = %#v", command)
	}
	if client.calls != 0 || model.textSelection.hasRange() {
		t.Fatalf("copy cancelled the turn or kept selection: cancel calls=%d selection=%#v", client.calls, model.textSelection)
	}
}

func TestConsecutiveCreateFileActivitiesRenderAndSelectAsDirectoryTrees(t *testing.T) {
	bridge, _ := NewEventBridge(2)
	defer bridge.Close()
	toolPair := func(callID, name, path string) []codingagent.TranscriptItem {
		return []codingagent.TranscriptItem{
			{Kind: codingagent.TranscriptToolCall, Tool: &codingagent.TranscriptTool{CallID: callID, Name: name, Status: "requested"}},
			{Kind: codingagent.TranscriptToolResult, Tool: &codingagent.TranscriptTool{
				CallID: callID, Name: name, Status: "completed", Summary: "Completed",
				Resources: []codingagent.ToolResource{{Path: path}},
				Diff:      &codingagent.InlineDiff{Text: fmt.Sprintf("--- /dev/null\n+++ b/%s\n@@ -0,0 +1 @@\n+created\n", path), Files: []string{path}},
			}},
		}
	}
	transcript := append(toolPair("create-1", createFileToolName, "cmd/app/main.go"), toolPair("create-2", createFileToolName, "internal/config/config.go")...)
	transcript = append(transcript, toolPair("read-1", "read_file", "go.mod")...)
	transcript = append(transcript, toolPair("create-3", createFileToolName, "README.md")...)
	snapshot := codingagent.Snapshot{Session: codingagent.Session{ID: "session", Title: "repo"}, Transcript: transcript}
	model, err := NewModel(context.Background(), fakeClient{snapshot: snapshot}, bridge, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	plain := ansi.Strip(renderedRows(model.conversationRows(100)))
	if count := strings.Count(plain, createFileToolName); count != 2 {
		t.Fatalf("create_file presentation count=%d, want 2 groups: %q", count, plain)
	}
	for _, expected := range []string{"2 files", "├── cmd/", "│   └── app/", "main.go", "internal/", "config.go", "README.md"} {
		if !strings.Contains(plain, expected) {
			t.Fatalf("create_file tree does not contain %q: %q", expected, plain)
		}
	}
	blocks := model.selectableBlocks()
	if len(blocks) != 3 || blocks[0].toolID != "create-1" || !strings.Contains(blocks[0].text, "cmd/") || !strings.Contains(blocks[0].text, "app/") || !strings.Contains(blocks[0].text, "config.go") {
		t.Fatalf("grouped selectable blocks = %#v", blocks)
	}
	model.width, model.height = 120, 24
	model.selectBlock(blocks[0])
	model.expanded["create-1"] = true
	wide := ansi.Strip(model.View().Content)
	if !strings.Contains(wide, "Changes  •  create_file  •  2 files") || !strings.Contains(wide, "cmd/app/main.go") || !strings.Contains(wide, "internal/config/config.go") || strings.Count(wide, "+created") != 2 {
		t.Fatalf("grouped create_file diff pane = %q", wide)
	}
}

func TestConversationUsesPromptBlocksWithoutRoleHeadersAndShowsTurnTime(t *testing.T) {
	bridge, _ := NewEventBridge(2)
	defer bridge.Close()
	started := time.Date(2026, time.August, 24, 10, 0, 0, 0, time.UTC)
	snapshot := codingagent.Snapshot{
		Session: codingagent.Session{ID: "session", Title: "repo"},
		Transcript: []codingagent.TranscriptItem{
			{ID: "user", TurnID: "turn", Timestamp: started, Kind: codingagent.TranscriptText, Role: codingagent.TranscriptRoleUser, Text: "explain this"},
			{ID: "assistant", TurnID: "turn", Timestamp: started.Add(4 * time.Second), Kind: codingagent.TranscriptText, Role: codingagent.TranscriptRoleAssistant, Text: "Here is the answer."},
		},
		Metrics: codingagent.SessionMetrics{LatestTurnID: "turn", StartedAt: started, FinishedAt: started.Add(4 * time.Second), Elapsed: 4 * time.Second},
	}
	model, err := NewModel(context.Background(), fakeClient{snapshot: snapshot}, bridge, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	model.markdownEnabled = false
	rows := model.conversationRows(80)
	plain := renderedRows(rows)
	for _, expected := range []string{"❯ explain this", "✻ Worked for 4s", "Here is the answer."} {
		if !strings.Contains(plain, expected) {
			t.Fatalf("conversation does not contain %q: %q", expected, plain)
		}
	}
	if strings.Contains(plain, "YOU") || strings.Contains(plain, "ASSISTANT") {
		t.Fatalf("conversation kept role headers: %q", plain)
	}
	userLine := "❯ explain this" + strings.Repeat(" ", 78-ansi.StringWidth("❯ explain this"))
	if len(rows) == 0 || rows[0].text != theme.userInput.Render(userLine) {
		t.Fatalf("user prompt block has no distinct background: %#v", rows[:min(1, len(rows))])
	}
}

func TestMouseSelectsPartialTextAcrossMessagesAndBlankClickClearsBlock(t *testing.T) {
	bridge, _ := NewEventBridge(2)
	defer bridge.Close()
	snapshot := codingagent.Snapshot{
		Session: codingagent.Session{ID: "session", Title: "repo"},
		Transcript: []codingagent.TranscriptItem{
			{ID: "user", Kind: codingagent.TranscriptText, Role: codingagent.TranscriptRoleUser, Text: "alpha beta"},
			{ID: "assistant", Kind: codingagent.TranscriptText, Role: codingagent.TranscriptRoleAssistant, Text: "gamma delta"},
		},
	}
	model, err := NewModel(context.Background(), fakeClient{snapshot: snapshot}, bridge, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	model.width, model.height = 80, 20
	model.markdownEnabled = false
	_ = model.View()
	startY, endY, blankY := -1, -1, -1
	startX, endX := -1, -1
	for y, hit := range model.hitTextRows {
		plain := strings.TrimRight(hit.text, " ")
		if index := strings.Index(plain, "beta"); index >= 0 {
			startY, startX = y, ansi.StringWidth(plain[:index])
		}
		if index := strings.Index(plain, "gamma"); index >= 0 {
			endY, endX = y, ansi.StringWidth(plain[:index])+ansi.StringWidth("gamma")-1
		}
		if strings.TrimSpace(plain) == "" {
			blankY = y
		}
	}
	if startY < 0 || endY < 0 || blankY < 0 {
		t.Fatalf("mouse text rows = %#v", model.hitTextRows)
	}
	model.Update(tea.MouseClickMsg(tea.Mouse{X: startX, Y: startY, Button: tea.MouseLeft}))
	model.Update(tea.MouseMotionMsg(tea.Mouse{X: endX, Y: endY, Button: tea.MouseLeft}))
	model.Update(tea.MouseReleaseMsg(tea.Mouse{X: endX, Y: endY, Button: tea.MouseLeft}))
	if !model.textSelection.hasRange() || !strings.Contains(model.statusLine(), "Text selected") {
		t.Fatalf("text selection = %#v status=%q", model.textSelection, model.statusLine())
	}
	selected := model.selectedText(model.conversationRows(model.width))
	if !strings.HasPrefix(selected, "beta") || strings.Contains(selected, "ASSISTANT") || !strings.HasSuffix(selected, "gamma") {
		t.Fatalf("cross-message selected text = %q", selected)
	}
	command := model.copyTextSelection()
	if command == nil || !strings.Contains(fmt.Sprint(command()), "beta") || model.textSelection.hasRange() {
		t.Fatalf("text clipboard command=%#v selection=%#v", command, model.textSelection)
	}

	_ = model.View()
	model.Update(tea.MouseClickMsg(tea.Mouse{X: 2, Y: startY, Button: tea.MouseLeft}))
	model.Update(tea.MouseReleaseMsg(tea.Mouse{X: 2, Y: startY, Button: tea.MouseLeft}))
	if model.selectedBlock == "" {
		t.Fatal("message click did not create a block selection")
	}
	model.Update(tea.MouseClickMsg(tea.Mouse{X: 0, Y: blankY, Button: tea.MouseLeft}))
	if model.selectedBlock != "" || model.textSelection.hasRange() {
		t.Fatalf("blank click kept selection: block=%q text=%#v", model.selectedBlock, model.textSelection)
	}
}

func TestSlashInputShowsFilteredRegistryCompletion(t *testing.T) {
	bridge, _ := NewEventBridge(2)
	defer bridge.Close()
	snapshot := codingagent.Snapshot{Session: codingagent.Session{ID: "session", Title: "repo"}}
	model, err := NewModel(context.Background(), fakeClient{snapshot: snapshot}, bridge, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	model.width, model.height = 100, 16
	model.replaceInput("/m")
	view := model.View().Content
	if matches := model.commandMatches(); len(matches) != 2 || !strings.Contains(view, "/provider") || !strings.Contains(view, "/md [on|off]") {
		t.Fatalf("filtered command completion = %q", view)
	}
	model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
	model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab}))
	if got := string(model.input); got != "/md " || !model.completionDismissed {
		t.Fatalf("completed command = %q", got)
	}
	model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if model.markdownEnabled {
		t.Fatal("completed /md command was not dispatched")
	}
	model.helpActive = true
	if help := model.View().Content; !strings.Contains(help, "/md [on|off]") {
		t.Fatalf("help did not use command registry: %q", help)
	}
}

func TestCommandCompletionAcceptsDismissesAndUsesAWindow(t *testing.T) {
	bridge, _ := NewEventBridge(2)
	defer bridge.Close()
	snapshot := codingagent.Snapshot{Session: codingagent.Session{ID: "session", Title: "repo"}}
	model, err := NewModel(context.Background(), fakeClient{snapshot: snapshot}, bridge, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	model.replaceInput("/")
	if _, command := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})); command != nil || !model.helpActive {
		t.Fatal("Enter on / did not directly execute the initially selected command")
	}
	model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))

	model.replaceInput("/")
	if !model.completionActive() {
		t.Fatal("slash did not activate command completion")
	}
	model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	if !model.completionDismissed || string(model.input) != "/" || model.completionActive() {
		t.Fatalf("dismissed=%v input=%q active=%v", model.completionDismissed, model.input, model.completionActive())
	}
	model.Update(tea.KeyPressMsg(tea.Key{Code: 's', Text: "s"}))
	if model.completionDismissed || len(model.commandMatches()) != 1 || model.commandMatches()[0].name != "/session" {
		t.Fatalf("printable input did not reopen filtered completion: dismissed=%v matches=%#v", model.completionDismissed, model.commandMatches())
	}
	if _, command := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})); command == nil || !model.sessionPicker.active {
		t.Fatal("Enter did not directly execute the selected command")
	}

	model.sessionPicker = sessionPicker{}
	model.replaceInput("/r")
	if _, command := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})); command != nil {
		t.Fatal("required-argument completion executed without its argument")
	}
	if string(model.input) != "/rename " || !model.completionDismissed {
		t.Fatalf("argument command completion = %q dismissed=%v", model.input, model.completionDismissed)
	}

	model.replaceInput("/")
	model.moveCompletionSelection(-1)
	lines := model.commandCompletionLines(100, 3)
	if len(lines) != 3 || !strings.Contains(strings.Join(lines, "\n"), "/exit") {
		t.Fatalf("windowed completion = %#v", lines)
	}
	commands := registeredCommands()
	wantMuted := truncateANSI(theme.muted.Render(fmt.Sprintf("  %-22s— %s", commands[len(commands)-2].usage, commands[len(commands)-2].description)), 100)
	if lines[1] != wantMuted {
		t.Fatalf("unselected completion is not fully muted: got %q want %q", lines[1], wantMuted)
	}
}

func TestCommandLookupRejectsRemovedAndUnexpectedArguments(t *testing.T) {
	if _, _, found := lookupCommand("/new"); found {
		t.Fatal("removed /new command is still registered")
	}
	if _, _, found := lookupCommand("/clear unexpected"); found {
		t.Fatal("argument was accepted by no-argument command")
	}
	if _, _, found := lookupCommand("/fork assistant-entry"); found {
		t.Fatal("raw /fork entry-id form is still accepted")
	}
	command, argument, found := lookupCommand("/model")
	if !found || command.name != "/provider" || argument != "" {
		t.Fatalf("model alias lookup = %#v arg=%q found=%v", command, argument, found)
	}
}

func TestFirstPromptSilentlyTitlesAnEmptySession(t *testing.T) {
	bridge, _ := NewEventBridge(2)
	defer bridge.Close()
	snapshot := codingagent.Snapshot{Session: codingagent.Session{ID: "session", Title: "", WorktreeID: "worktree"}}
	client := &sessionClient{fakeClient: fakeClient{snapshot: snapshot}}
	model, err := NewModel(context.Background(), client, bridge, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	prompt := "  " + strings.Repeat("界", 61) + "  "
	model.replaceInput(prompt)
	_, command := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if command == nil {
		t.Fatal("first prompt did not start a turn and auto-title batch")
	}
	batch, ok := command().(tea.BatchMsg)
	if !ok || len(batch) != 2 {
		t.Fatalf("first prompt command = %#v", command())
	}
	model.Update(batch[1]())
	wantTitle := strings.Repeat("界", 60) + "…"
	if client.renamedID != "session" || client.renamedTitle != wantTitle || model.snapshot.Session.Title != wantTitle {
		t.Fatalf("rename id=%q title=%q snapshot=%q", client.renamedID, client.renamedTitle, model.snapshot.Session.Title)
	}
	if !model.busy || model.status != "Agent is working..." {
		t.Fatalf("silent title changed turn state: busy=%v status=%q", model.busy, model.status)
	}
	model.Update(batch[0]())
	if client.turnRequest.SessionID != "session" || client.turnRequest.Text != strings.TrimSpace(prompt) {
		t.Fatalf("turn request = %#v", client.turnRequest)
	}
}

func TestTitleFromPromptUsesRunesAndPreservesShortText(t *testing.T) {
	if got := titleFromPrompt("  short title  "); got != "short title" {
		t.Fatalf("short title = %q", got)
	}
	want := strings.Repeat("好", 60) + "…"
	if got := titleFromPrompt(strings.Repeat("好", 61)); got != want {
		t.Fatalf("Unicode title = %q want %q", got, want)
	}
}

func renderedRows(rows []renderRow) string {
	values := make([]string, 0, len(rows))
	for _, row := range rows {
		values = append(values, row.text)
	}
	return strings.TrimSpace(strings.Join(values, "\n"))
}

func acceptAndSubmitCompletion(model *Model) tea.Cmd {
	_, command := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	return command
}

func TestCtrlCCallsProductCancelAPI(t *testing.T) {
	bridge, _ := NewEventBridge(2)
	snapshot := codingagent.Snapshot{Session: codingagent.Session{ID: "session-cancel"}}
	client := &cancelClient{fakeClient: fakeClient{snapshot: snapshot}}
	model, err := NewModel(context.Background(), client, bridge, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	localCancelled := false
	model.busy = true
	model.turnCancel = func() { localCancelled = true }
	_, command := model.Update(tea.KeyPressMsg(tea.Key{Code: 'c', Mod: tea.ModCtrl}))
	if command == nil || !localCancelled {
		t.Fatal("Ctrl+C did not request both immediate and product cancellation")
	}
	message := command()
	if _, ok := message.(cancelResultMsg); !ok {
		t.Fatalf("cancel command message = %#v", message)
	}
	if client.calls != 1 || client.id != snapshot.Session.ID {
		t.Fatalf("cancel calls=%d id=%q", client.calls, client.id)
	}
}

func TestSessionPickerSwitchResetsTransientStateAndRejectsStaleMessages(t *testing.T) {
	bridge, _ := NewEventBridge(4)
	first := codingagent.Snapshot{
		Revision: 5, Session: codingagent.Session{ID: "session-a", WorktreeID: "worktree", Title: "A"},
		PendingInterrupts: []codingagent.PendingInterrupt{{TurnID: "turn-a", InterruptID: "approval-a", Kind: "approval"}},
	}
	second := codingagent.Snapshot{Revision: 1, Session: codingagent.Session{ID: "session-b", WorktreeID: "worktree", Title: "B"}, Transcript: []codingagent.TranscriptItem{{ID: "b", Kind: codingagent.TranscriptText, Text: "session b"}}}
	client := &sessionClient{
		fakeClient: fakeClient{snapshot: first}, sessions: []codingagent.Session{first.Session, second.Session},
		snapshots: map[codingagent.SessionID]codingagent.Snapshot{"session-a": first, "session-b": second},
	}
	model, err := NewModel(context.Background(), client, bridge, first)
	if err != nil {
		t.Fatal(err)
	}
	_, command := model.Update(tea.KeyPressMsg(tea.Key{Code: 's', Mod: tea.ModCtrl}))
	if command == nil {
		t.Fatal("/session did not load sessions")
	}
	model.Update(command())
	if !model.sessionPicker.active || len(model.sessionPicker.sessions) != 2 {
		t.Fatalf("session picker = %#v", model.sessionPicker)
	}
	model.liveAssistant = "old delta"
	model.activities["old-call"] = codingagent.ToolActivityEvent{CallID: "old-call", Name: "old"}
	model.history = []string{"old prompt"}
	model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
	oldGeneration := model.generation
	_, command = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if command == nil {
		t.Fatal("session switch did not create a command")
	}
	model.Update(command())
	if model.sessionID != "session-b" || model.snapshot.Session.ID != "session-b" || model.generation == oldGeneration {
		t.Fatalf("active session = %q snapshot=%q generation=%d", model.sessionID, model.snapshot.Session.ID, model.generation)
	}
	if model.liveAssistant != "" || len(model.activities) != 0 || len(model.history) != 0 || model.pendingApproval() != nil {
		t.Fatalf("transient state leaked after switch: live=%q activities=%#v history=%#v approval=%#v", model.liveAssistant, model.activities, model.history, model.pendingApproval())
	}
	model.Update(snapshotMsg{snapshot: codingagent.Snapshot{Revision: 99, Session: first.Session}, sessionID: "session-a", generation: oldGeneration})
	model.Update(eventMsg{event: codingagent.Event{SessionID: "session-a", Kind: codingagent.EventAssistantOutputDelta, Payload: codingagent.EventPayload{AssistantOutput: &codingagent.AssistantOutputEvent{Delta: "stale"}}}})
	model.Update(turnResultMsg{sessionID: "session-a", generation: oldGeneration, result: codingagent.TurnResult{Status: "completed"}})
	if model.snapshot.Session.ID != "session-b" || model.liveAssistant != "" {
		t.Fatalf("stale session state was applied: snapshot=%q live=%q", model.snapshot.Session.ID, model.liveAssistant)
	}
}

func TestClearCommandUsesProductSessionAPI(t *testing.T) {
	bridge, _ := NewEventBridge(4)
	initial := codingagent.Snapshot{Session: codingagent.Session{
		ID: "session-a", WorkspaceID: "workspace", WorktreeID: "worktree", Title: "A",
		ProviderProfileID: "profile", ModelID: "model", PermissionMode: codingagent.PermissionAsk,
	}}
	created := codingagent.Session{ID: "session-new", WorkspaceID: "workspace", WorktreeID: "worktree", ProviderProfileID: "profile", ModelID: "model", PermissionMode: codingagent.PermissionAsk}
	createdSnapshot := codingagent.Snapshot{Session: created}
	client := &sessionClient{
		fakeClient: fakeClient{snapshot: initial}, createdResult: created,
		snapshots: map[codingagent.SessionID]codingagent.Snapshot{"session-new": createdSnapshot},
	}
	model, err := NewModel(context.Background(), client, bridge, initial)
	if err != nil {
		t.Fatal(err)
	}
	model.replaceInput("/clear")
	command := acceptAndSubmitCompletion(model)
	if command == nil {
		t.Fatal("/clear did not create a command")
	}
	model.Update(command())
	if client.created.Title != "" || client.created.WorktreeID != "worktree" || model.sessionID != "session-new" {
		t.Fatalf("clear session request=%#v active=%q", client.created, model.sessionID)
	}
}

func TestForkCommandOpensHistoryPickerAndUsesSourceEntryID(t *testing.T) {
	bridge, _ := NewEventBridge(4)
	initial := codingagent.Snapshot{
		Session: codingagent.Session{ID: "session", Title: "Conversation"},
		Transcript: []codingagent.TranscriptItem{
			{ID: "user-entry", SourceEntryID: "user-entry", Kind: codingagent.TranscriptText, Role: codingagent.TranscriptRoleUser, Text: "How should this work?", Timestamp: time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)},
			{ID: "assistant-entry:1", SourceEntryID: "assistant-entry", Kind: codingagent.TranscriptText, Role: codingagent.TranscriptRoleAssistant, Text: "Use the durable source entry.", Timestamp: time.Date(2026, 8, 24, 10, 1, 0, 0, time.UTC)},
		},
	}
	client := &sessionClient{
		fakeClient:     fakeClient{snapshot: initial},
		forkedSnapshot: codingagent.Snapshot{Session: codingagent.Session{ID: "session", ActiveLane: "branch"}},
	}
	model, err := NewModel(context.Background(), client, bridge, initial)
	if err != nil {
		t.Fatal(err)
	}
	model.replaceInput("/fork")
	if command := acceptAndSubmitCompletion(model); command != nil || !model.forkPicker.active || len(model.forkPicker.items) != 2 {
		t.Fatalf("fork picker did not open: command=%#v picker=%#v", command, model.forkPicker)
	}
	view := model.View().Content
	if !strings.Contains(view, "Fork conversation") || !strings.Contains(view, "How should this work?") || !strings.Contains(view, "Use the durable source entry.") || strings.Contains(view, "assistant-entry") {
		t.Fatalf("fork picker view = %q", view)
	}
	_, command := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if command == nil || !model.forkPicker.loading {
		t.Fatal("fork selection did not create a command")
	}
	model.Update(command())
	if client.forkRequest.SessionID != "session" || client.forkRequest.FromEntryID != "assistant-entry" || model.snapshot.Session.ActiveLane != "branch" || model.forkPicker.active {
		t.Fatalf("fork request=%#v snapshot=%#v picker=%#v", client.forkRequest, model.snapshot, model.forkPicker)
	}
}

func TestHelpAndUnknownSlashCommandsNeverReachAgent(t *testing.T) {
	bridge, _ := NewEventBridge(4)
	initial := codingagent.Snapshot{Session: codingagent.Session{ID: "session", Title: "repo", PermissionMode: codingagent.PermissionAsk}}
	client := &commandRoutingClient{fakeClient: fakeClient{snapshot: initial}}
	model, err := NewModel(context.Background(), client, bridge, initial)
	if err != nil {
		t.Fatal(err)
	}
	model.input = []rune("/help")
	if command := acceptAndSubmitCompletion(model); command != nil {
		t.Fatal("/help unexpectedly started an asynchronous command")
	}
	if help := model.View().Content; !model.helpActive || !strings.Contains(help, "/permissions") || !strings.Contains(help, "/clear") || strings.Contains(help, "/new") {
		t.Fatalf("help page = %q", model.View().Content)
	}
	model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))

	model.input = []rune("/new")
	if _, command := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})); command != nil {
		t.Fatal("removed /new command reached asynchronous execution")
	}
	if !strings.Contains(model.errorMessage, "Unknown command") {
		t.Fatalf("removed /new error = %q", model.errorMessage)
	}

	model.input = []rune("/does-not-exist")
	if _, command := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})); command != nil {
		t.Fatal("unknown slash command reached asynchronous execution")
	}
	if client.startCalls != 0 || !strings.Contains(model.errorMessage, "Unknown command") {
		t.Fatalf("start calls=%d error=%q", client.startCalls, model.errorMessage)
	}
}

func TestPlanCommandStartsExplicitReadOnlyPlanTurn(t *testing.T) {
	bridge, _ := NewEventBridge(4)
	initial := codingagent.Snapshot{Session: codingagent.Session{ID: "session", Title: "repo", PermissionMode: codingagent.PermissionAsk}}
	client := &sessionClient{fakeClient: fakeClient{snapshot: initial}}
	model, err := NewModel(context.Background(), client, bridge, initial)
	if err != nil {
		t.Fatal(err)
	}
	command := runPlanCommand(model, "inspect the workflow")
	if command == nil {
		t.Fatal("/plan request did not start a turn")
	}
	model.Update(command())
	if client.turnRequest.Mode != codingagent.TurnModePlan || client.turnRequest.Text != "inspect the workflow" {
		t.Fatalf("Plan Turn request = %#v", client.turnRequest)
	}
	runPlanCommand(model, "")
	if !model.planInput || !strings.Contains(model.promptLine(), "Plan") {
		t.Fatalf("empty /plan did not switch input state: prompt=%q", model.promptLine())
	}
}

func TestPlanApprovalSupportsRevisionFeedbackAtSingleUserBoundary(t *testing.T) {
	bridge, _ := NewEventBridge(4)
	snapshot := codingagent.Snapshot{
		Session: codingagent.Session{ID: "session", Title: "repo"}, PendingPlanApproval: true,
		ActivePlan: &codingagent.PlanSnapshot{
			ID: "plan", TurnID: "turn", Version: 1, Goal: "Implement Plan mode.",
			Scope:    codingagent.PlanScope{Included: []string{"internal/codingagent"}},
			Findings: []string{"P0 exists."}, Risks: []string{"Keep approval separate."},
			Steps:              []codingagent.PlanStep{{ID: "implement", Goal: "Implement P1.", Validation: []string{"Run tests."}}},
			AcceptanceCriteria: []string{"Tests pass."}, RecommendedStrategy: codingagent.ExecutionSingle,
		},
		PendingInterrupts: []codingagent.PendingInterrupt{{TurnID: "turn", InterruptID: "plan-approval", Kind: "plan_approval", PlanID: "plan", PlanVersion: 1, Summary: "Review Plan v1"}},
	}
	client := &approvalClient{fakeClient: fakeClient{snapshot: snapshot}}
	model, err := NewModel(context.Background(), client, bridge, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
	if _, command := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})); command != nil || !model.planFeedback {
		t.Fatal("Plan revision choice did not open feedback input")
	}
	for _, character := range "keep the UI compact" {
		model.Update(tea.KeyPressMsg(tea.Key{Code: character, Text: string(character)}))
	}
	_, command := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if command == nil {
		t.Fatal("Plan feedback did not produce a decision command")
	}
	_ = command()
	if client.calls != 1 || client.request.Decision != codingagent.ResolutionDenied || client.request.GrantScope != codingagent.PermissionGrantOnce || client.request.Message != "keep the UI compact" {
		t.Fatalf("Plan revision request = %#v calls=%d", client.request, client.calls)
	}
}

func TestAgentPlanEntrySuggestionRequiresExplicitUserChoice(t *testing.T) {
	bridge, _ := NewEventBridge(4)
	snapshot := codingagent.Snapshot{
		Session: codingagent.Session{ID: "session", Title: "repo"}, PendingPlanEntryApproval: true,
		ActiveTurn: &codingagent.TurnSnapshot{ID: "turn", Phase: codingagent.TurnPhaseAwaitingPlanEntryApproval, Status: codingagent.TurnInterrupted},
		PendingInterrupts: []codingagent.PendingInterrupt{{
			TurnID: "turn", InterruptID: "plan-entry", Kind: "plan_entry_approval",
			Summary:         "This migration crosses public interfaces and needs a reviewed sequence.",
			PlanEntryReason: codingagent.PlanEntryMigrationCompatibility,
		}},
	}
	client := &approvalClient{fakeClient: fakeClient{snapshot: snapshot}}
	model, err := NewModel(context.Background(), client, bridge, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	view := model.View().Content
	for _, expected := range []string{"Plan mode suggested", "This migration crosses public interfaces", "Enter Plan mode", "Continue Direct", "Cancel task"} {
		if !strings.Contains(view, expected) {
			t.Fatalf("Plan entry view does not contain %q: %s", expected, view)
		}
	}
	model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
	_, command := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if command == nil {
		t.Fatal("Continue Direct choice did not create a decision command")
	}
	_ = command()
	if client.calls != 1 || client.request.Decision != codingagent.ResolutionDenied || client.request.GrantScope != codingagent.PermissionGrantOnce {
		t.Fatalf("Plan entry decision = %#v calls=%d", client.request, client.calls)
	}
}

func TestPlanClarificationOffersRecommendedChoicesAndFreeFormOther(t *testing.T) {
	bridge, _ := NewEventBridge(4)
	prompt := codingagent.ClarificationPrompt{Questions: []codingagent.ClarificationRequest{
		{
			ID: "storage", Header: "Storage", Question: "Which storage should the feature use?",
			SelectionMode: codingagent.ClarificationSelectionSingle,
			Options: []codingagent.ClarificationOption{
				{ID: "file", Label: "File", Description: "Keep local persistence simple.", Recommended: true},
				{ID: "database", Label: "Database", Description: "Support stronger querying."},
			},
		},
		{
			ID: "compatibility", Header: "Compatibility", Question: "Which compatibility target should apply?",
			SelectionMode: codingagent.ClarificationSelectionMultiple,
			Options: []codingagent.ClarificationOption{
				{ID: "current", Label: "Current version", Description: "Keep the implementation focused.", Recommended: true},
				{ID: "legacy", Label: "Legacy versions", Description: "Preserve older behavior."},
			},
		},
	}}
	snapshot := codingagent.Snapshot{
		Session:           codingagent.Session{ID: "session", Title: "repo"},
		PendingInterrupts: []codingagent.PendingInterrupt{{TurnID: "turn", InterruptID: "clarification-1", Kind: "clarification", Summary: prompt.Questions[0].Question, Clarification: &prompt}},
	}
	client := &approvalClient{fakeClient: fakeClient{snapshot: snapshot}}
	model, err := NewModel(context.Background(), client, bridge, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	view := model.View().Content
	for _, expected := range []string{"Plan needs your input", "Question 1/2", "File (Recommended)", "Other"} {
		if !strings.Contains(view, expected) {
			t.Fatalf("clarification view does not contain %q: %s", expected, view)
		}
	}
	if _, command := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})); command != nil || model.clarificationIndex != 1 || len(model.clarificationAnswers) != 1 {
		t.Fatal("first answer did not advance to the next question")
	}
	view = model.View().Content
	if !strings.Contains(view, "Question 2/2") || !strings.Contains(view, "Multiple choice") || !strings.Contains(view, "Current version (Recommended)") {
		t.Fatalf("second clarification view is incomplete: %s", view)
	}
	model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeySpace, Text: " "}))
	view = model.View().Content
	if !strings.Contains(view, "[x]") {
		t.Fatalf("multiple-choice selection is not visible: %s", view)
	}
	model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
	model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
	if _, command := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})); command != nil || !model.clarificationOther {
		t.Fatal("Other choice did not open free-form input")
	}
	for _, character := range "Use the existing embedded store" {
		model.Update(tea.KeyPressMsg(tea.Key{Code: character, Text: string(character)}))
	}
	view = model.View().Content
	if !strings.Contains(view, "Other ❯ Use the existing embedded store") || strings.Contains(view, "Current version (Recommended)") {
		t.Fatalf("free-form input is not shown inline: %s", view)
	}
	_, command := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if command == nil {
		t.Fatal("free-form clarification did not produce a resume command")
	}
	_ = command()
	if client.calls != 1 || client.request.Decision != codingagent.ResolutionApproved || len(client.request.Details) == 0 {
		t.Fatalf("clarification resume = %#v calls=%d", client.request, client.calls)
	}
}

func TestDeliverablePlanApprovalFinishesWithoutExecuteLabel(t *testing.T) {
	pending := codingagent.PendingInterrupt{Kind: "plan_approval", PlanCompletion: codingagent.PlanCompletionDeliverable}
	model := &Model{}
	choices := model.approvalChoices(pending)
	if len(choices) == 0 || choices[0].label != "Accept Plan and finish" {
		t.Fatalf("deliverable Plan choices = %#v", choices)
	}
}

func TestPermissionsPickerChangesCurrentSessionMode(t *testing.T) {
	bridge, _ := NewEventBridge(4)
	initial := codingagent.Snapshot{Session: codingagent.Session{ID: "session", Title: "repo", PermissionMode: codingagent.PermissionAsk}}
	client := &sessionClient{fakeClient: fakeClient{snapshot: initial}}
	model, err := NewModel(context.Background(), client, bridge, initial)
	if err != nil {
		t.Fatal(err)
	}
	model.replaceInput("/permissions")
	acceptAndSubmitCompletion(model)
	if !model.permissionPicker.active || !strings.Contains(model.View().Content, "Ask before acting") {
		t.Fatalf("permissions page = %q", model.View().Content)
	}
	model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
	_, command := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if command == nil {
		t.Fatal("permission selection did not create a command")
	}
	model.Update(command())
	if client.permissionID != "session" || client.permissionMode != codingagent.PermissionAutoEdit || model.snapshot.Session.PermissionMode != codingagent.PermissionAutoEdit || model.permissionPicker.active {
		t.Fatalf("permission id=%q mode=%q snapshot=%q picker=%#v", client.permissionID, client.permissionMode, model.snapshot.Session.PermissionMode, model.permissionPicker)
	}
}

func TestClearCreatesCleanSessionWithoutDeletingCurrentConversation(t *testing.T) {
	bridge, _ := NewEventBridge(4)
	initial := codingagent.Snapshot{Session: codingagent.Session{
		ID: "session-old", WorkspaceID: "workspace", WorktreeID: "worktree", Title: "Old",
		ProviderProfileID: "profile", ModelID: "model", PermissionMode: codingagent.PermissionAsk,
	}}
	created := codingagent.Session{ID: "session-clean", WorkspaceID: "workspace", WorktreeID: "worktree", PermissionMode: codingagent.PermissionAsk}
	client := &sessionClient{
		fakeClient: fakeClient{snapshot: initial}, createdResult: created,
		snapshots: map[codingagent.SessionID]codingagent.Snapshot{created.ID: {Session: created}},
	}
	model, err := NewModel(context.Background(), client, bridge, initial)
	if err != nil {
		t.Fatal(err)
	}
	model.replaceInput("/clear")
	command := acceptAndSubmitCompletion(model)
	if command == nil {
		t.Fatal("/clear did not create a clean session")
	}
	model.Update(command())
	if client.created.Title != "" || client.created.WorktreeID != "worktree" || client.archived != "" || model.sessionID != "session-clean" || len(model.history) != 0 {
		t.Fatalf("created=%#v archived=%q active=%q history=%#v", client.created, client.archived, model.sessionID, model.history)
	}
}

func TestInputHistoryIsRestoredFromDurableUserTranscriptOnly(t *testing.T) {
	bridge, _ := NewEventBridge(2)
	defer bridge.Close()
	snapshot := codingagent.Snapshot{
		Session: codingagent.Session{ID: "session", Title: "repo"},
		Transcript: []codingagent.TranscriptItem{
			{Kind: codingagent.TranscriptText, Role: codingagent.TranscriptRoleUser, Text: "first prompt"},
			{Kind: codingagent.TranscriptText, Role: codingagent.TranscriptRoleAssistant, Text: "assistant answer"},
			{Kind: codingagent.TranscriptToolResult, Role: codingagent.TranscriptRoleTool, Tool: &codingagent.TranscriptTool{Detail: "tool output"}},
			{Kind: codingagent.TranscriptText, Role: codingagent.TranscriptRoleUser, Text: "second prompt"},
		},
	}
	model, err := NewModel(context.Background(), fakeClient{snapshot: snapshot}, bridge, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp}))
	if got := string(model.input); got != "second prompt" {
		t.Fatalf("latest history = %q", got)
	}
	model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp}))
	if got := string(model.input); got != "first prompt" {
		t.Fatalf("older history = %q", got)
	}
}

func TestCommandSubmissionZeroesComposerBackingMemory(t *testing.T) {
	bridge, _ := NewEventBridge(2)
	defer bridge.Close()
	snapshot := codingagent.Snapshot{Session: codingagent.Session{ID: "session", Title: "repo"}}
	model, err := NewModel(context.Background(), fakeClient{snapshot: snapshot}, bridge, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	model.input = []rune("/help")
	backing := model.input
	acceptAndSubmitCompletion(model)
	for _, value := range backing {
		if value != 0 {
			t.Fatalf("composer backing memory was not cleared: %#v", backing)
		}
	}
}

func TestWorkspacePickerRepairsUnavailableBindingAndSwitchesSession(t *testing.T) {
	bridge, _ := NewEventBridge(4)
	initial := codingagent.Snapshot{Session: codingagent.Session{ID: "session-a", WorkspaceID: "workspace-a", WorktreeID: "worktree-a", ProviderProfileID: "profile", ModelID: "model", PermissionMode: codingagent.PermissionAsk}}
	target := codingagent.Session{ID: "session-b", WorkspaceID: "workspace-b", WorktreeID: "worktree-b", ProviderProfileID: "profile", ModelID: "model", PermissionMode: codingagent.PermissionAsk}
	client := &workspaceClient{
		fakeClient: fakeClient{snapshot: initial}, sessions: []codingagent.Session{target},
		workspaces: []codingagent.WorkspaceSummary{{ID: "workspace-b", DisplayName: "Moved repo", Trusted: true, Worktrees: []codingagent.WorktreeSummary{{ID: "worktree-b", Root: "missing-root", Availability: codingagent.WorktreeUnavailable}}}},
		snapshots:  map[codingagent.SessionID]codingagent.Snapshot{target.ID: {Session: target}},
	}
	model, err := NewModel(context.Background(), client, bridge, initial)
	if err != nil {
		t.Fatal(err)
	}
	model.replaceInput("/workspace")
	command := acceptAndSubmitCompletion(model)
	if command == nil {
		t.Fatal("/workspace did not load catalog")
	}
	model.Update(command())
	if !model.workspacePicker.active || len(model.workspacePicker.items) != 1 || !strings.Contains(model.View().Content, "unavailable") {
		t.Fatalf("workspace picker = %#v view=%q", model.workspacePicker, model.View().Content)
	}
	model.Update(tea.KeyPressMsg(tea.Key{Code: 'r', Text: "r"}))
	model.Update(tea.PasteMsg{Content: "new-root"})
	_, command = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if command == nil {
		t.Fatal("workspace repair did not create command")
	}
	model.Update(command())
	if client.relocated.WorktreeID != "worktree-b" || client.relocated.NewPath != "new-root" || model.sessionID != "session-b" || model.workspacePicker.active {
		t.Fatalf("relocation=%#v session=%q picker=%#v", client.relocated, model.sessionID, model.workspacePicker)
	}
}

func TestSuccessfulSnapshotRefreshDoesNotEraseTurnError(t *testing.T) {
	bridge, _ := NewEventBridge(1)
	snapshot := codingagent.Snapshot{Session: codingagent.Session{ID: "session", Title: "repo"}}
	model, err := NewModel(context.Background(), fakeClient{snapshot: snapshot}, bridge, snapshot)
	if err != nil {
		t.Fatalf("create model: %v", err)
	}
	model.errorMessage = "persistent failure"
	model.Update(snapshotMsg{snapshot: snapshot})
	if model.errorMessage != "persistent failure" {
		t.Fatalf("snapshot refresh erased error: %q", model.errorMessage)
	}
}

func TestPendingApprovalShowsProposedDiffAndChoiceList(t *testing.T) {
	bridge, _ := NewEventBridge(1)
	snapshot := codingagent.Snapshot{
		Session: codingagent.Session{ID: "session", Title: "repo"},
		PendingInterrupts: []codingagent.PendingInterrupt{{
			TurnID: "turn", InterruptID: "approval", Kind: "approval", Summary: "Edit main.go",
			CanGrantSession: true,
			Proposed: &codingagent.ProposedChange{
				Kind: "patch", Summary: "Edit main.go", AddedLines: 1, DeletedLines: 1,
				Diff: codingagent.InlineDiff{Files: []string{"main.go"}, Text: "--- a/main.go\n+++ b/main.go\n-old\n+new\n"},
			},
		}},
	}
	model, err := NewModel(context.Background(), fakeClient{snapshot: snapshot}, bridge, snapshot)
	if err != nil {
		t.Fatalf("create model: %v", err)
	}
	model.width, model.height = 100, 16
	view := model.View().Content
	if !strings.Contains(ansi.Strip(strings.Split(view, "\n")[0]), "CodePilot") {
		t.Fatalf("approval replaced the conversation window: %q", view)
	}
	for _, expected := range []string{"Permission required", "Proposed changes", "main.go", "1. Allow once", "2. Allow for this session", "3. Deny", "4. Cancel action"} {
		if !strings.Contains(view, expected) {
			t.Fatalf("approval view does not contain %q: %q", expected, view)
		}
	}
}

func TestPendingApprovalChoiceUsesExplicitSessionGrantScope(t *testing.T) {
	bridge, _ := NewEventBridge(1)
	snapshot := codingagent.Snapshot{
		Session: codingagent.Session{ID: "session", Title: "repo"},
		Transcript: []codingagent.TranscriptItem{{Kind: codingagent.TranscriptToolCall, Tool: &codingagent.TranscriptTool{
			CallID: "call", Name: createFileToolName, Status: "interrupted",
		}}},
		PendingInterrupts: []codingagent.PendingInterrupt{{
			TurnID: "turn", InterruptID: "approval", Kind: "approval", ToolCallID: "call", Summary: "Create main.go", CanGrantSession: true,
		}},
	}
	client := &approvalClient{fakeClient: fakeClient{snapshot: snapshot}}
	model, err := NewModel(context.Background(), client, bridge, snapshot)
	if err != nil {
		t.Fatalf("create model: %v", err)
	}
	approval := ansi.Strip(renderedRows(model.approvalRows(snapshot.PendingInterrupts[0], 120)))
	if !strings.Contains(approval, "Allow for this session (new files in this worktree)") || !strings.Contains(approval, "safety checks still apply") {
		t.Fatalf("create_file session scope is unclear: %q", approval)
	}
	model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
	_, command := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if command == nil {
		t.Fatal("session approval choice did not produce a command")
	}
	_ = command()
	if client.calls != 1 || client.request.Decision != codingagent.ResolutionApproved || client.request.GrantScope != codingagent.PermissionGrantSession {
		t.Fatalf("session approval request = %#v calls=%d", client.request, client.calls)
	}
}

func TestPendingCheckApprovalShowsTrustedPlanAndFixedCommand(t *testing.T) {
	bridge, _ := NewEventBridge(1)
	snapshot := codingagent.Snapshot{
		Session: codingagent.Session{ID: "session", Title: "repo"},
		PendingInterrupts: []codingagent.PendingInterrupt{{
			TurnID: "turn", InterruptID: "approval", Kind: "approval", Summary: "Run Go tests",
			Proposed: &codingagent.ProposedChange{Kind: "check", Summary: "Run Go tests", PlanID: "go.test", Command: "go test ./..."},
		}},
	}
	model, err := NewModel(context.Background(), fakeClient{snapshot: snapshot}, bridge, snapshot)
	if err != nil {
		t.Fatalf("create model: %v", err)
	}
	model.width, model.height = 100, 14
	view := model.View().Content
	if !strings.Contains(view, "Proposed check") || !strings.Contains(view, "go.test") || !strings.Contains(view, "go test ./...") {
		t.Fatalf("check approval view = %q", view)
	}
}

func TestPendingSensitiveReadShowsRedactionGuaranteeWithoutSessionGrant(t *testing.T) {
	bridge, _ := NewEventBridge(1)
	snapshot := codingagent.Snapshot{
		Session: codingagent.Session{ID: "session", Title: "repo"},
		PendingInterrupts: []codingagent.PendingInterrupt{{
			TurnID: "turn", InterruptID: "approval", Kind: "approval", Summary: "Read sensitive path .env with secret values redacted",
			Proposed: &codingagent.ProposedChange{Kind: "sensitive_read", Path: ".env"},
		}},
	}
	model, err := NewModel(context.Background(), fakeClient{snapshot: snapshot}, bridge, snapshot)
	if err != nil {
		t.Fatalf("create model: %v", err)
	}
	model.width, model.height = 100, 14
	view := model.View().Content
	for _, expected := range []string{"Sensitive read", ".env", "remain redacted", "1. Allow once", "2. Deny", "3. Cancel action"} {
		if !strings.Contains(view, expected) {
			t.Fatalf("sensitive-read view does not contain %q: %q", expected, view)
		}
	}
	if strings.Contains(view, "Allow for this session") {
		t.Fatalf("sensitive-read approval offered a reusable grant: %q", view)
	}
}

func TestPendingLanguageServerApprovalShowsAllowlistedCommand(t *testing.T) {
	bridge, _ := NewEventBridge(1)
	snapshot := codingagent.Snapshot{
		Session: codingagent.Session{ID: "session", Title: "repo"},
		PendingInterrupts: []codingagent.PendingInterrupt{{
			TurnID: "turn", InterruptID: "approval", Kind: "approval", Summary: "Start allowlisted gopls language server", CanGrantSession: true,
			Proposed: &codingagent.ProposedChange{Kind: "lsp", Language: "go", Command: "gopls serve"},
		}},
	}
	model, err := NewModel(context.Background(), fakeClient{snapshot: snapshot}, bridge, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	model.width, model.height = 100, 14
	view := model.View().Content
	for _, expected := range []string{"Language server", "go", "gopls serve", "1. Allow once", "2. Allow for this session"} {
		if !strings.Contains(view, expected) {
			t.Fatalf("language-server approval view does not contain %q: %q", expected, view)
		}
	}
}

func TestProviderIssueOpensPickerAndCredentialIsMasked(t *testing.T) {
	bridge, _ := NewEventBridge(1)
	snapshot := codingagent.Snapshot{Session: codingagent.Session{ID: "session", Title: "repo", ProviderProfileID: "openai", ModelID: "model"}}
	model, err := NewModel(context.Background(), fakeClient{snapshot: snapshot}, bridge, snapshot, WithProviderIssue(&codingagent.ProviderIssue{Code: "credential_missing", Message: "Configure an API key."}))
	if err != nil {
		t.Fatalf("create model: %v", err)
	}
	if !model.picker.active || !strings.Contains(model.View().Content, "Provider & model setup") {
		t.Fatal("Provider issue did not open the picker")
	}
	model.beginProviderForm(codingagent.ProviderProfile{ID: "openai", Kind: "openai", DisplayName: "OpenAI", DefaultModel: "model"})
	model.picker.form.field = 4
	model.picker.form.credential = []rune("top-secret")
	view := model.View().Content
	if strings.Contains(view, "top-secret") || !strings.Contains(view, "••••") {
		t.Fatalf("credential was not masked: %q", view)
	}
	if len(model.history) != 0 {
		t.Fatalf("credential form changed conversation history: %#v", model.history)
	}
}

func TestProviderEnterRequestsOnlyMissingAPIKeyBeforeLoadingModels(t *testing.T) {
	profile := codingagent.ProviderProfile{
		ID: "openai", Kind: "openai", DisplayName: "OpenAI", DefaultModel: "configured-model",
		RequiresCredential: true, CredentialConfigured: false,
	}
	client := &providerPickerClient{}
	model := Model{client: client, ctx: context.Background(), picker: newProviderPicker("", false)}
	model.picker.profiles = []codingagent.ProviderProfile{profile}

	if command := model.handleProviderProfilesKey(tea.Key{Code: tea.KeyEnter}); command != nil {
		t.Fatal("unconfigured Provider attempted to load models before collecting an API key")
	}
	if model.picker.stage != providerCredential || client.listModelsCalls != 0 {
		t.Fatalf("stage = %v, model calls = %d", model.picker.stage, client.listModelsCalls)
	}
	view := model.providerView(100, 18).Content
	for _, hidden := range []string{"Base URL", "Default model", "Display name"} {
		if strings.Contains(view, hidden) {
			t.Fatalf("credential-only page exposed %q: %q", hidden, view)
		}
	}
	if !strings.Contains(view, "API key for OpenAI") {
		t.Fatalf("credential-only page = %q", view)
	}
}

func TestConfiguredProviderEnterLoadsModelsAndAPIKeyCanBeReplaced(t *testing.T) {
	profile := codingagent.ProviderProfile{
		ID: "openai", Kind: "openai", DisplayName: "OpenAI", DefaultModel: "configured-model",
		RequiresCredential: true, CredentialConfigured: true,
	}
	client := &providerPickerClient{
		configuredProfile: profile,
		models:            []codingagent.ProviderModel{{ID: "configured-model", DisplayName: "Configured model"}},
	}
	model := Model{client: client, ctx: context.Background(), picker: newProviderPicker("", false)}
	model.picker.profiles = []codingagent.ProviderProfile{profile}

	command := model.handleProviderProfilesKey(tea.Key{Code: tea.KeyEnter})
	if command == nil {
		t.Fatal("configured Provider did not load models")
	}
	model.applyProviderModels(command().(providerModelsMsg))
	if model.picker.stage != providerModels || client.listModelsCalls != 1 {
		t.Fatalf("stage = %v, model calls = %d", model.picker.stage, client.listModelsCalls)
	}

	model.handleProviderModelsKey(tea.Key{Text: "k"})
	if model.picker.stage != providerCredential {
		t.Fatalf("k did not open API key replacement page: %v", model.picker.stage)
	}
	model.picker.form.credential = []rune("replacement-secret")
	model.picker.form.cursor = len(model.picker.form.credential)
	command = model.saveProviderCredential()
	if command == nil {
		t.Fatal("non-empty replacement API key was not saved")
	}
	next := model.applyProviderSaved(command().(providerSavedMsg))
	if client.credentialSeen != "replacement-secret" || client.configureCalls != 1 {
		t.Fatalf("credential captured = %q, configure calls = %d", client.credentialSeen, client.configureCalls)
	}
	if next == nil {
		t.Fatal("successful API key save did not load models")
	}
	model.applyProviderModels(next().(providerModelsMsg))
	if model.picker.stage != providerModels || client.listModelsCalls != 2 {
		t.Fatalf("stage = %v, model calls = %d", model.picker.stage, client.listModelsCalls)
	}
}

func TestSavedModelsRemainVisibleWhenProviderDiscoveryFails(t *testing.T) {
	profile := codingagent.ProviderProfile{
		ID: "openai", Kind: "openai", DisplayName: "OpenAI", DefaultModel: "profile-default",
		RequiresCredential: true, CredentialConfigured: true,
	}
	model := Model{snapshot: codingagent.Snapshot{Session: codingagent.Session{
		ProviderProfileID: "openai", ModelID: "session-model",
	}}, picker: newProviderPicker("", false)}
	model.applyProviderModels(providerModelsMsg{profile: profile, err: errors.New("catalog unavailable")})

	if len(model.picker.models) != 2 {
		t.Fatalf("saved model count = %d: %#v", len(model.picker.models), model.picker.models)
	}
	if !model.picker.models[0].Current || model.picker.models[0].ID != "session-model" {
		t.Fatalf("current model was not preserved first: %#v", model.picker.models)
	}
	if !model.picker.models[1].Configured || model.picker.models[1].ID != "profile-default" {
		t.Fatalf("profile default was not preserved: %#v", model.picker.models)
	}
	view := model.providerView(100, 18).Content
	for _, expected := range []string{"session-model", "current", "profile-default", "configured", "Saved model choices are still shown"} {
		if !strings.Contains(view, expected) {
			t.Fatalf("saved model page does not contain %q: %q", expected, view)
		}
	}
}

func TestDiscoveredConfiguredModelIsDeduplicatedAndMarkedAvailable(t *testing.T) {
	profile := codingagent.ProviderProfile{ID: "deepseek", DisplayName: "DeepSeek", DefaultModel: "deepseek-chat"}
	model := Model{snapshot: codingagent.Snapshot{Session: codingagent.Session{
		ProviderProfileID: "deepseek", ModelID: "deepseek-chat",
	}}}
	models := model.mergeProviderModels(profile, []codingagent.ProviderModel{
		{ID: "deepseek-chat", DisplayName: "DeepSeek Chat"},
		{ID: "deepseek-reasoner", DisplayName: "DeepSeek Reasoner", Reasoning: true},
	})
	if len(models) != 2 || !models[0].Current || !models[0].Configured || !models[0].Available {
		t.Fatalf("configured discovered model = %#v", models)
	}
}

func TestRecoveryActionBlocksPromptAndExposesOnlyProductDecisions(t *testing.T) {
	bridge, _ := NewEventBridge(1)
	snapshot := codingagent.Snapshot{
		Session:      codingagent.Session{ID: "session", Title: "repo", ProviderProfileID: "openai", ModelID: "model"},
		RuntimeState: codingagent.RuntimeInterrupted,
		RecoveryActions: []codingagent.RecoveryAction{{
			ID: "turn:call", TurnID: "turn", Kind: "decide_tool", ToolCallID: "call", ToolName: "apply_patch",
			ReplayPolicy: "never", Summary: "Tool execution may have produced an external side effect",
			Decisions: []codingagent.RecoveryDecision{
				codingagent.RecoveryConfirmExecuted, codingagent.RecoveryMarkFailed, codingagent.RecoveryRetry, codingagent.RecoveryAbandonTurn,
			},
		}},
	}
	client := &recoveryClient{fakeClient: fakeClient{snapshot: snapshot}, result: codingagent.TurnResult{TurnID: "turn", Status: "completed"}}
	model, err := NewModel(context.Background(), client, bridge, snapshot)
	if err != nil {
		t.Fatalf("create recovery model: %v", err)
	}
	model.width, model.height = 110, 20
	view := model.View().Content
	for _, expected := range []string{"Crash recovery required", "apply_patch", "[x] confirm executed", "[f] mark failed", "[r] retry/continue", "[a] abandon turn"} {
		if !strings.Contains(view, expected) {
			t.Fatalf("recovery view does not contain %q: %q", expected, view)
		}
	}
	if strings.Contains(model.promptLine(), "Ask CodePilot") {
		t.Fatalf("composer was active during recovery: %q", model.promptLine())
	}
	_, command := model.Update(tea.KeyPressMsg(tea.Key{Text: "x"}))
	if command == nil {
		t.Fatal("confirm-executed recovery key did not create a command")
	}
	model.Update(command())
	if client.calls != 1 || client.request.ActionID != "turn:call" || client.request.Decision != codingagent.RecoveryConfirmExecuted {
		t.Fatalf("recovery request = %#v, calls = %d", client.request, client.calls)
	}
}

func TestRecoveryActionIsHiddenWhileTurnIsActive(t *testing.T) {
	action := codingagent.RecoveryAction{ID: "turn:continue", TurnID: "turn", Kind: "continue_run"}
	tests := []struct {
		name  string
		busy  bool
		state codingagent.RuntimeState
	}{
		{name: "locally busy", busy: true, state: codingagent.RuntimeIdle},
		{name: "running snapshot", state: codingagent.RuntimeRunning},
		{name: "cancelling snapshot", state: codingagent.RuntimeCancelling},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			model := Model{busy: test.busy, snapshot: codingagent.Snapshot{
				RuntimeState: test.state, RecoveryActions: []codingagent.RecoveryAction{action},
			}}
			if recovery := model.pendingRecovery(); recovery != nil {
				t.Fatalf("active turn exposed recovery action: %#v", recovery)
			}
		})
	}

	model := Model{snapshot: codingagent.Snapshot{
		RuntimeState: codingagent.RuntimeInterrupted, RecoveryActions: []codingagent.RecoveryAction{action},
	}}
	if recovery := model.pendingRecovery(); recovery == nil {
		t.Fatal("interrupted turn did not expose recovery action")
	}
}

func TestPendingApprovalCanAbandonWholeRecoveredTurn(t *testing.T) {
	bridge, _ := NewEventBridge(1)
	snapshot := codingagent.Snapshot{
		Session:           codingagent.Session{ID: "session", Title: "repo", ProviderProfileID: "openai", ModelID: "model"},
		PendingInterrupts: []codingagent.PendingInterrupt{{TurnID: "turn", InterruptID: "approval", Kind: "approval", Summary: "Apply changes"}},
		RecoveryActions: []codingagent.RecoveryAction{{
			ID: "turn:call", TurnID: "turn", Kind: "resolve_interrupt", Decisions: []codingagent.RecoveryDecision{codingagent.RecoveryAbandonTurn},
		}},
	}
	client := &recoveryClient{fakeClient: fakeClient{snapshot: snapshot}, result: codingagent.TurnResult{TurnID: "turn", Status: "aborted"}}
	model, err := NewModel(context.Background(), client, bridge, snapshot)
	if err != nil {
		t.Fatalf("create approval recovery model: %v", err)
	}
	if view := model.View().Content; !strings.Contains(view, "Abandon turn") {
		t.Fatalf("approval recovery choices = %q", view)
	}
	_, command := model.Update(tea.KeyPressMsg(tea.Key{Text: "4"}))
	if command == nil {
		t.Fatal("abandon-turn key did not create a command")
	}
	model.Update(command())
	if client.calls != 1 || client.request.Decision != codingagent.RecoveryAbandonTurn {
		t.Fatalf("abandon request = %#v, calls = %d", client.request, client.calls)
	}
}
