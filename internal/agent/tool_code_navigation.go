package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/eaglc/codepilot/internal/session"
	"github.com/eaglc/codepilot/internal/tool"
)

const maxNavigationResults = 200

type definitionTool struct {
	scope           session.TurnScope
	navigationScope NavigationScope
	navigator       CodeNavigator
}

type referencesTool struct {
	scope           session.TurnScope
	navigationScope NavigationScope
	navigator       CodeNavigator
}

type symbolsTool struct {
	scope           session.TurnScope
	navigationScope NavigationScope
	navigator       CodeNavigator
}

type diagnosticsTool struct {
	scope           session.TurnScope
	navigationScope NavigationScope
	navigator       CodeNavigator
}

type positionedNavigationArguments struct {
	Path   string `json:"path"`
	Line   int    `json:"line"`
	Column int    `json:"column"`
}

type referencesArguments struct {
	Path               string `json:"path"`
	Line               int    `json:"line"`
	Column             int    `json:"column"`
	IncludeDeclaration bool   `json:"include_declaration,omitempty"`
	Limit              int    `json:"limit,omitempty"`
}

type symbolsArguments struct {
	Query string `json:"query"`
	Limit int    `json:"limit,omitempty"`
}

type diagnosticsArguments struct {
	Path  string `json:"path"`
	Limit int    `json:"limit,omitempty"`
}

type locationsOutput struct {
	Locations []locationOutput `json:"locations"`
}

type locationOutput struct {
	Path        string `json:"path"`
	StartLine   int    `json:"start_line"`
	StartColumn int    `json:"start_column"`
	EndLine     int    `json:"end_line"`
	EndColumn   int    `json:"end_column"`
}

type symbolsOutput struct {
	Symbols []symbolOutput `json:"symbols"`
}

type symbolOutput struct {
	Name     string         `json:"name"`
	Kind     string         `json:"kind"`
	Location locationOutput `json:"location"`
}

type diagnosticsOutput struct {
	Diagnostics []diagnosticOutput `json:"diagnostics"`
}

type diagnosticOutput struct {
	Path        string             `json:"path"`
	StartLine   int                `json:"start_line"`
	StartColumn int                `json:"start_column"`
	EndLine     int                `json:"end_line"`
	EndColumn   int                `json:"end_column"`
	Severity    DiagnosticSeverity `json:"severity"`
	Message     string             `json:"message"`
	Source      string             `json:"source,omitempty"`
	Code        string             `json:"code,omitempty"`
}

func (t *definitionTool) Definition() tool.Definition {
	return definition("definition", "Find definitions for a one-based source position using the active worktree language server.", positionedNavigationSchema())
}

func (t *definitionTool) Invoke(ctx context.Context, arguments json.RawMessage) (tool.Result, error) {
	var parsed positionedNavigationArguments
	if invalid := decodeToolArguments(arguments, &parsed); invalid != nil {
		return *invalid, nil
	}
	if err := validatePositionedArguments(parsed.Path, parsed.Line, parsed.Column); err != nil {
		return invalidArgument(err.Error())
	}
	locations, err := t.navigator.Definition(ctx, DefinitionRequest{
		Scope: t.navigationScope, Path: parsed.Path,
		Position: CodePosition{Line: parsed.Line, Column: parsed.Column},
	})
	if err != nil {
		return normalizeNavigationError(ctx, err)
	}
	return completedJSONResult(locationsOutput{Locations: locationOutputs(locations)}, t.scope.Limits.ToolResultMaxBytes), nil
}

func (t *referencesTool) Definition() tool.Definition {
	return definition("references", "Find bounded references for a one-based source position using the active worktree language server.", `{
  "type":"object",
  "properties":{
    "path":{"type":"string","minLength":1,"maxLength":4096,"description":"Worktree-relative source path."},
    "line":{"type":"integer","minimum":1,"maximum":10000000},
    "column":{"type":"integer","minimum":1,"maximum":1000000,"description":"One-based UTF-16 column."},
    "include_declaration":{"type":"boolean","default":false},
    "limit":{"type":"integer","minimum":0,"maximum":200,"description":"Zero uses the navigator default."}
  },
  "required":["path","line","column"],
  "additionalProperties":false
}`)
}

func (t *referencesTool) Invoke(ctx context.Context, arguments json.RawMessage) (tool.Result, error) {
	var parsed referencesArguments
	if invalid := decodeToolArguments(arguments, &parsed); invalid != nil {
		return *invalid, nil
	}
	if err := validatePositionedArguments(parsed.Path, parsed.Line, parsed.Column); err != nil || parsed.Limit < 0 || parsed.Limit > maxNavigationResults {
		return invalidArgument("The source position or reference limit is outside the declared bounds.")
	}
	locations, err := t.navigator.References(ctx, ReferencesRequest{
		Scope: t.navigationScope, Path: parsed.Path,
		Position: parsedPosition(parsed.Line, parsed.Column), IncludeDeclaration: parsed.IncludeDeclaration, Limit: parsed.Limit,
	})
	if err != nil {
		return normalizeNavigationError(ctx, err)
	}
	return completedJSONResult(locationsOutput{Locations: locationOutputs(locations)}, t.scope.Limits.ToolResultMaxBytes), nil
}

func (t *symbolsTool) Definition() tool.Definition {
	return definition("symbols", "Find bounded workspace symbols by name using the active worktree language server.", `{
  "type":"object",
  "properties":{
    "query":{"type":"string","minLength":1,"maxLength":256},
    "limit":{"type":"integer","minimum":0,"maximum":200,"description":"Zero uses the navigator default."}
  },
  "required":["query"],
  "additionalProperties":false
}`)
}

func (t *symbolsTool) Invoke(ctx context.Context, arguments json.RawMessage) (tool.Result, error) {
	var parsed symbolsArguments
	if invalid := decodeToolArguments(arguments, &parsed); invalid != nil {
		return *invalid, nil
	}
	if strings.TrimSpace(parsed.Query) == "" || len(parsed.Query) > 256 || parsed.Limit < 0 || parsed.Limit > maxNavigationResults {
		return invalidArgument("The symbol query or limit is outside the declared bounds.")
	}
	symbols, err := t.navigator.Symbols(ctx, SymbolsRequest{Scope: t.navigationScope, Query: parsed.Query, Limit: parsed.Limit})
	if err != nil {
		return normalizeNavigationError(ctx, err)
	}
	output := symbolsOutput{Symbols: make([]symbolOutput, 0, len(symbols))}
	for _, symbol := range symbols {
		output.Symbols = append(output.Symbols, symbolOutput{Name: symbol.Name, Kind: symbol.Kind, Location: newLocationOutput(symbol.Location)})
	}
	return completedJSONResult(output, t.scope.Limits.ToolResultMaxBytes), nil
}

func (t *diagnosticsTool) Definition() tool.Definition {
	return definition("diagnostics", "Read bounded diagnostics for one source document from the active worktree language server.", `{
  "type":"object",
  "properties":{
    "path":{"type":"string","minLength":1,"maxLength":4096,"description":"Worktree-relative source path."},
    "limit":{"type":"integer","minimum":0,"maximum":200,"description":"Zero uses the navigator default."}
  },
  "required":["path"],
  "additionalProperties":false
}`)
}

func (t *diagnosticsTool) Invoke(ctx context.Context, arguments json.RawMessage) (tool.Result, error) {
	var parsed diagnosticsArguments
	if invalid := decodeToolArguments(arguments, &parsed); invalid != nil {
		return *invalid, nil
	}
	if strings.TrimSpace(parsed.Path) == "" || len(parsed.Path) > 4096 || parsed.Limit < 0 || parsed.Limit > maxNavigationResults {
		return invalidArgument("The diagnostic path or limit is outside the declared bounds.")
	}
	diagnostics, err := t.navigator.Diagnostics(ctx, DiagnosticsRequest{Scope: t.navigationScope, Path: parsed.Path, Limit: parsed.Limit})
	if err != nil {
		return normalizeNavigationError(ctx, err)
	}
	output := diagnosticsOutput{Diagnostics: make([]diagnosticOutput, 0, len(diagnostics))}
	for _, diagnostic := range diagnostics {
		output.Diagnostics = append(output.Diagnostics, diagnosticOutput{
			Path: diagnostic.Path, StartLine: diagnostic.Range.Start.Line, StartColumn: diagnostic.Range.Start.Column,
			EndLine: diagnostic.Range.End.Line, EndColumn: diagnostic.Range.End.Column,
			Severity: diagnostic.Severity, Message: diagnostic.Message, Source: diagnostic.Source, Code: diagnostic.Code,
		})
	}
	return completedJSONResult(output, t.scope.Limits.ToolResultMaxBytes), nil
}

func positionedNavigationSchema() string {
	return `{
  "type":"object",
  "properties":{
    "path":{"type":"string","minLength":1,"maxLength":4096,"description":"Worktree-relative source path."},
    "line":{"type":"integer","minimum":1,"maximum":10000000},
    "column":{"type":"integer","minimum":1,"maximum":1000000,"description":"One-based UTF-16 column."}
  },
  "required":["path","line","column"],
  "additionalProperties":false
}`
}

func validatePositionedArguments(path string, line int, column int) error {
	if strings.TrimSpace(path) == "" || len(path) > 4096 || line < 1 || line > 10_000_000 || column < 1 || column > 1_000_000 {
		return errors.New("The source path or position is outside the declared bounds.")
	}
	return nil
}

func parsedPosition(line int, column int) CodePosition {
	return CodePosition{Line: line, Column: column}
}

func locationOutputs(locations []Location) []locationOutput {
	output := make([]locationOutput, 0, len(locations))
	for _, location := range locations {
		output = append(output, newLocationOutput(location))
	}
	return output
}

func newLocationOutput(location Location) locationOutput {
	return locationOutput{
		Path: location.Path, StartLine: location.Range.Start.Line, StartColumn: location.Range.Start.Column,
		EndLine: location.Range.End.Line, EndColumn: location.Range.End.Column,
	}
}

func normalizeNavigationError(ctx context.Context, err error) (tool.Result, error) {
	if errors.Is(err, ErrCodeNavigationUnavailable) {
		return tool.Result{Status: tool.ResultFailed, Content: "Language-server navigation is unavailable; continue with search_code and read_file."}, nil
	}
	return normalizeToolError(ctx, err)
}
