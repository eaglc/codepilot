package codingtools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/eaglc/codepilot/internal/codingagent"
	"github.com/eaglc/codepilot/internal/codingagent/language"
	"github.com/eaglc/codepilot/internal/codingagent/lsp"
	"github.com/eaglc/codepilot/internal/llm"
	"github.com/eaglc/codepilot/internal/tool"
)

type navigationOperation string

const (
	navigationDefinition      navigationOperation = "definition"
	navigationReferences      navigationOperation = "references"
	navigationDiagnostics     navigationOperation = "diagnostics"
	navigationDocumentSymbols navigationOperation = "document_symbols"
)

type navigationContext struct {
	root       string
	worktreeID string
	security   *codingagent.SecurityPolicy
	profile    language.WorkspaceProfile
	navigator  lsp.Navigator
}

type navigationTool struct {
	context   navigationContext
	operation navigationOperation
}

type navigationArguments struct {
	Path               string `json:"path"`
	Line               int    `json:"line"`
	Column             int    `json:"column"`
	IncludeDeclaration bool   `json:"include_declaration"`
	Limit              int    `json:"limit"`
}

type lspStartApprovalPayload struct {
	Kind          string   `json:"kind"`
	Version       int      `json:"version"`
	GrantToolName string   `json:"grant_tool_name"`
	RequestedTool string   `json:"requested_tool"`
	Language      string   `json:"language"`
	Program       string   `json:"program"`
	Arguments     []string `json:"arguments"`
	Summary       string   `json:"summary"`
	Digest        string   `json:"digest"`
}

func (t *navigationTool) Definition() llm.ToolDefinition {
	switch t.operation {
	case navigationDefinition:
		return llm.ToolDefinition{Name: "find_definition", Description: "Find bounded in-worktree definitions for a source position using the detected language server.", InputSchema: positionNavigationSchema(false)}
	case navigationReferences:
		return llm.ToolDefinition{Name: "find_references", Description: "Find bounded in-worktree references for a source position using the detected language server.", InputSchema: positionNavigationSchema(true)}
	case navigationDiagnostics:
		return llm.ToolDefinition{Name: "get_diagnostics", Description: "Get bounded language-server diagnostics for one worktree source file.", InputSchema: fileNavigationSchema()}
	default:
		return llm.ToolDefinition{Name: "document_symbols", Description: "List bounded language-server symbols for one worktree source file.", InputSchema: fileNavigationSchema()}
	}
}

func (*navigationTool) ReplayPolicy() tool.ReplayPolicy { return tool.ReplaySafe }

func (t *navigationTool) Execute(ctx context.Context, call tool.Call, _ tool.ProgressSink) (tool.Result, error) {
	arguments, _, scope, result := t.prepare(call)
	if result.Status != "" {
		return result, nil
	}
	return t.query(ctx, call.Name, arguments, scope)
}

func (t *navigationTool) PermissionRequirement(_ context.Context, call tool.Call) (permissionRequirement, tool.Result, error) {
	_, profile, scope, result := t.prepare(call)
	if result.Status != "" {
		return permissionRequirement{}, result, nil
	}
	if t.context.navigator.Ready(scope) {
		return permissionRequirement{}, tool.Result{}, nil
	}
	return permissionRequirement{
		required: true,
		request: codingagent.PermissionRequest{
			ToolName: "language_server", Action: codingagent.PermissionStartLanguageServerAction(string(profile.ID)),
		},
		readOnlyMessage: "The read-only permission mode does not allow a language-server process to start.",
		approval:        t.interrupt(call, profile),
	}, tool.Result{}, nil
}

func (t *navigationTool) Resume(ctx context.Context, call tool.Call, interrupt tool.Interrupt, resolution tool.Result, _ tool.ProgressSink) (tool.Result, error) {
	if resolution.Status != tool.ResultCompleted {
		return resolution.Clone(), nil
	}
	arguments, profile, scope, invalid := t.prepare(call)
	if invalid.Status != "" {
		return invalid, nil
	}
	var payload lspStartApprovalPayload
	if interrupt.Kind != "approval" || json.Unmarshal(interrupt.Payload, &payload) != nil || payload.Kind != "coding_lsp_start_approval_v1" || payload.Version != 1 || payload.GrantToolName != "language_server" || payload.RequestedTool != call.Name || payload.Language != string(profile.ID) || payload.Program != profile.Server.Program || !equalStringSlices(payload.Arguments, profile.Server.Args) || payload.Digest == "" || payload.Digest != lspApprovalDigest(payload, call) || interrupt.ID != approvalID(call, payload.Digest) {
		return failedResult("The saved language-server approval failed its integrity check."), nil
	}
	return t.query(ctx, call.Name, arguments, scope)
}

func (t *navigationTool) prepare(call tool.Call) (navigationArguments, language.Profile, lsp.Scope, tool.Result) {
	var arguments navigationArguments
	if err := decodeArguments(call.Arguments, &arguments); err != nil {
		return arguments, language.Profile{}, lsp.Scope{}, invalidResult(err.Error())
	}
	arguments.Path = strings.TrimSpace(strings.ReplaceAll(arguments.Path, "\\", "/"))
	if arguments.Path == "" {
		return arguments, language.Profile{}, lsp.Scope{}, invalidResult("A worktree-relative source path is required.")
	}
	if t.context.security.IsSensitivePath(arguments.Path) {
		return arguments, language.Profile{}, lsp.Scope{}, deniedResult("Sensitive paths are excluded from language navigation.")
	}
	profile, found := t.context.profile.ResolvePath(arguments.Path)
	if !found {
		return arguments, language.Profile{}, lsp.Scope{}, deniedResult("No detected language server supports the requested file.")
	}
	if arguments.Limit < 0 || arguments.Limit > 200 {
		return arguments, language.Profile{}, lsp.Scope{}, invalidResult("The result limit must be between 0 and 200.")
	}
	if (t.operation == navigationDefinition || t.operation == navigationReferences) && (arguments.Line < 1 || arguments.Column < 1) {
		return arguments, language.Profile{}, lsp.Scope{}, invalidResult("A one-based line and column are required.")
	}
	scope := lsp.Scope{WorktreeID: t.context.worktreeID, Root: t.context.root, Language: profile}
	return arguments, profile, scope, tool.Result{}
}

func (t *navigationTool) interrupt(call tool.Call, profile language.Profile) tool.Result {
	payload := lspStartApprovalPayload{
		Kind: "coding_lsp_start_approval_v1", Version: 1, GrantToolName: "language_server", RequestedTool: call.Name,
		Language: string(profile.ID), Program: profile.Server.Program, Arguments: append([]string(nil), profile.Server.Args...),
		Summary: "Start allowlisted " + profile.Server.Program + " language server for this worktree",
	}
	payload.Digest = lspApprovalDigest(payload, call)
	encoded, _ := json.Marshal(payload)
	return tool.Result{
		Status: tool.ResultInterrupted, Content: []llm.Content{{Type: llm.ContentText, Text: "Approval is required before starting the worktree language-server process."}},
		Interrupt: &tool.Interrupt{ID: approvalID(call, payload.Digest), Kind: "approval", Payload: encoded},
	}
}

func (t *navigationTool) query(ctx context.Context, toolName string, arguments navigationArguments, scope lsp.Scope) (tool.Result, error) {
	var value any
	var lines []string
	switch t.operation {
	case navigationDefinition:
		locations, err := t.context.navigator.Definition(ctx, scope, arguments.Path, lsp.Position{Line: arguments.Line, Column: arguments.Column}, arguments.Limit)
		if err != nil {
			return navigationFailure(scope.Language, err), nil
		}
		value, lines = locations, formatLocations(locations)
	case navigationReferences:
		locations, err := t.context.navigator.References(ctx, scope, arguments.Path, lsp.Position{Line: arguments.Line, Column: arguments.Column}, arguments.IncludeDeclaration, arguments.Limit)
		if err != nil {
			return navigationFailure(scope.Language, err), nil
		}
		value, lines = locations, formatLocations(locations)
	case navigationDiagnostics:
		diagnostics, err := t.context.navigator.Diagnostics(ctx, scope, arguments.Path, arguments.Limit)
		if err != nil {
			return navigationFailure(scope.Language, err), nil
		}
		value, lines = diagnostics, formatDiagnostics(diagnostics)
	case navigationDocumentSymbols:
		symbols, err := t.context.navigator.DocumentSymbols(ctx, scope, arguments.Path, arguments.Limit)
		if err != nil {
			return navigationFailure(scope.Language, err), nil
		}
		value, lines = symbols, formatSymbols(symbols)
	}
	if len(lines) == 0 {
		lines = []string{"No language-server results were found."}
	}
	details, _ := json.Marshal(map[string]any{"kind": "coding_lsp_v1", "operation": t.operation, "language": scope.Language.ID, "results": value})
	_ = toolName
	return completedResult(strings.Join(lines, "\n"), details), nil
}

func navigationFailure(profile language.Profile, err error) tool.Result {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return failedResult("The language-server request was cancelled or timed out.")
	}
	return failedResult("Language navigation is unavailable. Ensure the allowlisted " + profile.Server.Program + " server is installed, then retry.")
}

func formatLocations(values []lsp.Location) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = fmt.Sprintf("%s:%d:%d-%d:%d", value.Path, value.Range.Start.Line, value.Range.Start.Column, value.Range.End.Line, value.Range.End.Column)
	}
	return result
}

func formatDiagnostics(values []lsp.Diagnostic) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = fmt.Sprintf("%s:%d:%d [%s] %s", value.Path, value.Range.Start.Line, value.Range.Start.Column, value.Severity, value.Message)
	}
	return result
}

func formatSymbols(values []lsp.Symbol) []string {
	result := make([]string, len(values))
	for index, value := range values {
		container := ""
		if value.Container != "" {
			container = " in " + value.Container
		}
		result[index] = fmt.Sprintf("%s %s%s at %s:%d:%d", value.Kind, value.Name, container, value.Location.Path, value.Location.Range.Start.Line, value.Location.Range.Start.Column)
	}
	return result
}

func lspApprovalDigest(payload lspStartApprovalPayload, call tool.Call) string {
	copy := payload
	copy.Digest = ""
	encoded, _ := json.Marshal(copy)
	seed := append(encoded, 0)
	seed = append(seed, []byte(call.ID+"\x00"+call.IdempotencyKey+"\x00"+string(call.Arguments))...)
	digest := sha256.Sum256(seed)
	return hex.EncodeToString(digest[:])
}

func equalStringSlices(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func positionNavigationSchema(references bool) json.RawMessage {
	properties := `"path":{"type":"string"},"line":{"type":"integer","minimum":1},"column":{"type":"integer","minimum":1},"limit":{"type":"integer","minimum":1,"maximum":200}`
	if references {
		properties += `,"include_declaration":{"type":"boolean"}`
	}
	return json.RawMessage(`{"type":"object","properties":{` + properties + `},"required":["path","line","column"],"additionalProperties":false}`)
}

func fileNavigationSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"limit":{"type":"integer","minimum":1,"maximum":200}},"required":["path"],"additionalProperties":false}`)
}

var _ tool.ResumableTool = (*navigationTool)(nil)
