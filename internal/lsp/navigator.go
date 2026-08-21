package lsp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/eaglc/codepilot/internal/agent"
	"github.com/eaglc/codepilot/internal/session"
	"github.com/eaglc/codepilot/internal/workspace"
)

const (
	defaultInitializationTimeout = 15 * time.Second
	defaultDiagnosticWait        = 300 * time.Millisecond
	defaultMaxMessageBytes       = 8 << 20
	defaultMaxDocumentBytes      = 2 << 20
	defaultMaxResults            = 100
)

var _ agent.CodeNavigator = (*Navigator)(nil)

// ActionAuthorizer approves a fully structured language-server startup action.
type ActionAuthorizer interface {
	Authorize(ctx context.Context, mode session.PermissionMode, action session.Action) (session.Authorization, error)
}

// ServerConfig is one trusted language-server executable and argument list.
type ServerConfig struct {
	Program string
	Args    []string
}

// Options contains explicit process, approval, protocol, and resource limits.
type Options struct {
	Executor              workspace.CommandExecutor
	Authorizer            ActionAuthorizer
	Servers               map[agent.LanguageID]ServerConfig
	InitializationTimeout time.Duration
	DiagnosticWait        time.Duration
	MaxMessageBytes       int
	MaxDocumentBytes      int
	MaxResults            int
}

type serverSlot struct {
	ready    chan struct{}
	root     string
	language agent.LanguageID
	client   *client
	err      error
}

// Navigator owns at most one lazily initialized language-server client per
// active WorktreeID. Failed starts are not cached so a later approved retry or
// newly installed binary can recover without restarting CodePilot.
type Navigator struct {
	mu      sync.Mutex
	options Options
	servers map[session.WorktreeID]*serverSlot
	closed  bool
}

// NewNavigator validates options without locating or starting any language server.
func NewNavigator(options Options) (*Navigator, error) {
	if isNilExecutor(options.Executor) || isNilAuthorizer(options.Authorizer) {
		return nil, errors.New("create LSP navigator: executor and authorizer are required")
	}
	if options.Servers == nil {
		options.Servers = defaultServers()
	} else {
		options.Servers = cloneServers(options.Servers)
	}
	if options.InitializationTimeout == 0 {
		options.InitializationTimeout = defaultInitializationTimeout
	}
	if options.DiagnosticWait == 0 {
		options.DiagnosticWait = defaultDiagnosticWait
	}
	if options.MaxMessageBytes == 0 {
		options.MaxMessageBytes = defaultMaxMessageBytes
	}
	if options.MaxDocumentBytes == 0 {
		options.MaxDocumentBytes = defaultMaxDocumentBytes
	}
	if options.MaxResults == 0 {
		options.MaxResults = defaultMaxResults
	}
	if err := validateOptions(options); err != nil {
		return nil, err
	}
	return &Navigator{options: options, servers: make(map[session.WorktreeID]*serverSlot)}, nil
}

// Definition returns safe worktree-relative definition locations.
func (n *Navigator) Definition(ctx context.Context, request agent.DefinitionRequest) ([]agent.Location, error) {
	root, err := validateNavigationScope(request.Scope)
	if err != nil {
		return nil, err
	}
	if err := validatePosition(request.Position); err != nil {
		return nil, err
	}
	doc, err := readDocument(root, request.Path, n.options.MaxDocumentBytes)
	if err != nil {
		return nil, err
	}
	server, err := n.serverFor(ctx, request.Scope, root)
	if err != nil {
		return nil, err
	}
	if err := server.syncDocument(ctx, doc, string(request.Scope.Language)); err != nil {
		return nil, unavailable(ctx, "sync definition document", err)
	}
	var raw json.RawMessage
	err = server.call(ctx, "textDocument/definition", textPositionParams(doc.uri, request.Position), &raw)
	if err != nil {
		return nil, unavailable(ctx, "request definition", err)
	}
	locations, err := decodeDefinitionLocations(raw)
	if err != nil {
		return nil, unavailable(ctx, "decode definition", err)
	}
	return n.safeLocations(root, locations, n.options.MaxResults), nil
}

// References returns bounded safe references for one source position.
func (n *Navigator) References(ctx context.Context, request agent.ReferencesRequest) ([]agent.Location, error) {
	root, err := validateNavigationScope(request.Scope)
	if err != nil {
		return nil, err
	}
	if err := validatePosition(request.Position); err != nil {
		return nil, err
	}
	limit, err := n.resultLimit(request.Limit)
	if err != nil {
		return nil, err
	}
	doc, err := readDocument(root, request.Path, n.options.MaxDocumentBytes)
	if err != nil {
		return nil, err
	}
	server, err := n.serverFor(ctx, request.Scope, root)
	if err != nil {
		return nil, err
	}
	if err := server.syncDocument(ctx, doc, string(request.Scope.Language)); err != nil {
		return nil, unavailable(ctx, "sync reference document", err)
	}
	params := textPositionParams(doc.uri, request.Position)
	params["context"] = map[string]bool{"includeDeclaration": request.IncludeDeclaration}
	var locations []protocolLocation
	if err := server.call(ctx, "textDocument/references", params, &locations); err != nil {
		return nil, unavailable(ctx, "request references", err)
	}
	return n.safeLocations(root, locations, limit), nil
}

// Symbols returns bounded safe workspace symbols matching a non-empty query.
func (n *Navigator) Symbols(ctx context.Context, request agent.SymbolsRequest) ([]agent.Symbol, error) {
	root, err := validateNavigationScope(request.Scope)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(request.Query) == "" || len(request.Query) > 256 {
		return nil, errors.New("query LSP symbols: query is invalid")
	}
	limit, err := n.resultLimit(request.Limit)
	if err != nil {
		return nil, err
	}
	server, err := n.serverFor(ctx, request.Scope, root)
	if err != nil {
		return nil, err
	}
	var values []protocolSymbol
	if err := server.call(ctx, "workspace/symbol", map[string]string{"query": request.Query}, &values); err != nil {
		return nil, unavailable(ctx, "request symbols", err)
	}
	result := make([]agent.Symbol, 0, min(len(values), limit))
	for _, value := range values {
		location, safe := safeLocation(root, value.Location)
		if !safe {
			continue
		}
		result = append(result, agent.Symbol{Name: boundedString(value.Name, 512), Kind: symbolKindName(value.Kind), Location: location})
		if len(result) == limit {
			break
		}
	}
	return result, nil
}

// Diagnostics returns pull diagnostics when supported and otherwise waits
// briefly for the server's bounded publishDiagnostics notification.
func (n *Navigator) Diagnostics(ctx context.Context, request agent.DiagnosticsRequest) ([]agent.Diagnostic, error) {
	root, err := validateNavigationScope(request.Scope)
	if err != nil {
		return nil, err
	}
	limit, err := n.resultLimit(request.Limit)
	if err != nil {
		return nil, err
	}
	doc, err := readDocument(root, request.Path, n.options.MaxDocumentBytes)
	if err != nil {
		return nil, err
	}
	server, err := n.serverFor(ctx, request.Scope, root)
	if err != nil {
		return nil, err
	}
	if err := server.syncDocument(ctx, doc, string(request.Scope.Language)); err != nil {
		return nil, unavailable(ctx, "sync diagnostic document", err)
	}
	var response struct {
		Items []protocolDiagnostic `json:"items"`
	}
	err = server.call(ctx, "textDocument/diagnostic", map[string]any{
		"textDocument": map[string]string{"uri": doc.uri},
	}, &response)
	values := response.Items
	if isMethodNotFound(err) {
		values = server.waitForDiagnostics(ctx, doc.uri, n.options.DiagnosticWait)
	} else if err != nil {
		return nil, unavailable(ctx, "request diagnostics", err)
	}
	result := make([]agent.Diagnostic, 0, min(len(values), limit))
	for _, value := range values {
		if !validProtocolRange(value.Range) || strings.TrimSpace(value.Message) == "" {
			continue
		}
		result = append(result, agent.Diagnostic{
			Path: doc.relative,
			Range: agent.CodeRange{
				Start: agent.CodePosition{Line: value.Range.Start.Line + 1, Column: value.Range.Start.Character + 1},
				End:   agent.CodePosition{Line: value.Range.End.Line + 1, Column: value.Range.End.Character + 1},
			},
			Severity: diagnosticSeverity(value.Severity),
			Message:  boundedString(value.Message, 4096),
			Source:   boundedString(value.Source, 256),
			Code:     diagnosticCode(value.Code),
		})
		if len(result) == limit {
			break
		}
	}
	return result, nil
}

// CloseWorktree closes and forgets the language server for one worktree.
func (n *Navigator) CloseWorktree(ctx context.Context, worktreeID session.WorktreeID) error {
	if n == nil || worktreeID == "" {
		return nil
	}
	n.mu.Lock()
	slot := n.servers[worktreeID]
	delete(n.servers, worktreeID)
	n.mu.Unlock()
	if slot == nil {
		return nil
	}
	select {
	case <-slot.ready:
		if slot.client == nil {
			return nil
		}
		return slot.client.close(ctx)
	default:
	}
	select {
	case <-ctx.Done():
		// The removed slot cannot be reached again. Its initializer detects that
		// removal and closes any process that finishes starting later.
		return ctx.Err()
	case <-slot.ready:
	}
	if slot.client == nil {
		return nil
	}
	return slot.client.close(ctx)
}

// Close shuts down every language-server process and prevents future starts.
func (n *Navigator) Close() error {
	if n == nil {
		return nil
	}
	n.mu.Lock()
	if n.closed {
		n.mu.Unlock()
		return nil
	}
	n.closed = true
	ids := make([]session.WorktreeID, 0, len(n.servers))
	for id := range n.servers {
		ids = append(ids, id)
	}
	n.mu.Unlock()
	sort.Slice(ids, func(left int, right int) bool { return ids[left] < ids[right] })
	var closeErrors []error
	for _, id := range ids {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		closeErrors = append(closeErrors, n.CloseWorktree(ctx, id))
		cancel()
	}
	return errors.Join(closeErrors...)
}

func (n *Navigator) serverFor(ctx context.Context, scope agent.NavigationScope, root string) (*client, error) {
	n.mu.Lock()
	if n.closed {
		n.mu.Unlock()
		return nil, fmt.Errorf("%w: navigator is closed", agent.ErrCodeNavigationUnavailable)
	}
	if slot := n.servers[scope.WorktreeID]; slot != nil {
		if !samePath(slot.root, root) || slot.language != scope.Language {
			n.mu.Unlock()
			return nil, fmt.Errorf("%w: worktree language-server binding changed", agent.ErrCodeNavigationUnavailable)
		}
		n.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-slot.ready:
			if slot.err != nil || slot.client == nil || !slot.client.closed() {
				return slot.client, slot.err
			}
			n.mu.Lock()
			if n.servers[scope.WorktreeID] == slot {
				delete(n.servers, scope.WorktreeID)
			}
			n.mu.Unlock()
			return n.serverFor(ctx, scope, root)
		}
	}
	slot := &serverSlot{ready: make(chan struct{}), root: root, language: scope.Language}
	n.servers[scope.WorktreeID] = slot
	n.mu.Unlock()

	server, err := n.startServer(ctx, scope, root)
	n.mu.Lock()
	current := n.servers[scope.WorktreeID]
	if current == slot && !n.closed {
		slot.client = server
		slot.err = err
		if err != nil {
			delete(n.servers, scope.WorktreeID)
		}
		close(slot.ready)
		n.mu.Unlock()
		return server, err
	}
	slot.err = fmt.Errorf("%w: worktree was closed during startup", agent.ErrCodeNavigationUnavailable)
	close(slot.ready)
	n.mu.Unlock()
	if server != nil {
		closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = server.close(closeCtx)
		cancel()
	}
	return nil, slot.err
}

func (n *Navigator) startServer(ctx context.Context, scope agent.NavigationScope, root string) (*client, error) {
	config, exists := n.options.Servers[scope.Language]
	if !exists {
		return nil, fmt.Errorf("%w: no server is configured for %s", agent.ErrCodeNavigationUnavailable, scope.Language)
	}
	action := languageServerAction(scope, root, config, n.options.InitializationTimeout)
	authorization, err := n.options.Authorizer.Authorize(ctx, scope.PermissionMode, action)
	if err != nil {
		return nil, err
	}
	switch authorization.Outcome {
	case session.AuthorizationPrompt:
		if authorization.Request == nil {
			return nil, errors.New("start language server: approval request is missing")
		}
		return nil, &session.ApprovalRequiredError{Request: *authorization.Request}
	case session.AuthorizationDeny:
		return nil, &session.AppError{Code: session.ErrPermissionDenied, Operation: "lsp.start", UserMessage: "Language server startup was denied."}
	case session.AuthorizationAllow:
	default:
		return nil, errors.New("start language server: approval outcome is invalid")
	}
	process, err := n.options.Executor.Start(ctx, workspace.ProcessSpec{
		ID: "language-server-" + string(scope.Language), Program: config.Program,
		Args: append([]string(nil), config.Args...), Dir: root,
	})
	if err != nil {
		return nil, unavailable(ctx, "start language server", err)
	}
	server := newClient(process, fileURI(root), n.options.MaxMessageBytes)
	initializeCtx, cancel := context.WithTimeout(ctx, n.options.InitializationTimeout)
	err = server.initialize(initializeCtx, os.Getpid())
	cancel()
	if err != nil {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = server.close(closeCtx)
		closeCancel()
		return nil, unavailable(ctx, "initialize language server", err)
	}
	return server, nil
}

func (n *Navigator) safeLocations(root string, values []protocolLocation, limit int) []agent.Location {
	result := make([]agent.Location, 0, min(len(values), limit))
	seen := make(map[string]struct{})
	for _, value := range values {
		location, safe := safeLocation(root, value)
		if !safe {
			continue
		}
		key := fmt.Sprintf("%s:%d:%d:%d:%d", location.Path, location.Range.Start.Line, location.Range.Start.Column, location.Range.End.Line, location.Range.End.Column)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, location)
		if len(result) == limit {
			break
		}
	}
	sort.Slice(result, func(left int, right int) bool {
		if result[left].Path != result[right].Path {
			return result[left].Path < result[right].Path
		}
		if result[left].Range.Start.Line != result[right].Range.Start.Line {
			return result[left].Range.Start.Line < result[right].Range.Start.Line
		}
		return result[left].Range.Start.Column < result[right].Range.Start.Column
	})
	return result
}

func (n *Navigator) resultLimit(value int) (int, error) {
	if value < 0 || value > 200 {
		return 0, errors.New("query LSP: result limit is invalid")
	}
	if value == 0 || value > n.options.MaxResults {
		return n.options.MaxResults, nil
	}
	return value, nil
}

func validateOptions(options Options) error {
	if len(options.Servers) == 0 {
		return errors.New("create LSP navigator: at least one server is required")
	}
	for language, config := range options.Servers {
		if language != agent.LanguageGo && language != agent.LanguagePython {
			return fmt.Errorf("create LSP navigator: language %q is unsupported", language)
		}
		if strings.TrimSpace(config.Program) == "" || filepath.Base(config.Program) != config.Program || len(config.Args) > 16 {
			return fmt.Errorf("create LSP navigator: server for %q is invalid", language)
		}
		program := strings.TrimSuffix(strings.ToLower(config.Program), ".exe")
		valid := language == agent.LanguageGo && program == "gopls" && reflect.DeepEqual(config.Args, []string{"serve"})
		valid = valid || language == agent.LanguagePython && (program == "pyright-langserver" || program == "basedpyright-langserver") && reflect.DeepEqual(config.Args, []string{"--stdio"})
		if !valid {
			return fmt.Errorf("create LSP navigator: server for %q is outside the allowlist", language)
		}
	}
	if options.InitializationTimeout <= 0 || options.InitializationTimeout > time.Minute || options.DiagnosticWait < 0 || options.DiagnosticWait > 5*time.Second {
		return errors.New("create LSP navigator: time limits are invalid")
	}
	if options.MaxMessageBytes < 1024 || options.MaxMessageBytes > 32<<20 || options.MaxDocumentBytes < 1024 || options.MaxDocumentBytes > 16<<20 || options.MaxDocumentBytes >= options.MaxMessageBytes || options.MaxResults <= 0 || options.MaxResults > 200 {
		return errors.New("create LSP navigator: resource limits are invalid")
	}
	return nil
}

func validateNavigationScope(scope agent.NavigationScope) (string, error) {
	if scope.SessionID == "" || scope.TurnID == "" || scope.WorktreeID == "" || strings.TrimSpace(scope.WorktreeRoot) == "" {
		return "", errors.New("query LSP: navigation scope is incomplete")
	}
	if scope.Language != agent.LanguageGo && scope.Language != agent.LanguagePython {
		return "", fmt.Errorf("%w: language %q has no server", agent.ErrCodeNavigationUnavailable, scope.Language)
	}
	switch scope.PermissionMode {
	case session.PermissionReadOnly, session.PermissionAsk, session.PermissionAutoEdit:
	default:
		return "", errors.New("query LSP: permission mode is invalid")
	}
	if !filepath.IsAbs(scope.WorktreeRoot) || filepath.Clean(scope.WorktreeRoot) != scope.WorktreeRoot {
		return "", errors.New("query LSP: worktree root is invalid")
	}
	root, err := filepath.EvalSymlinks(scope.WorktreeRoot)
	if err != nil {
		return "", fmt.Errorf("%w: worktree root is unavailable: %v", agent.ErrCodeNavigationUnavailable, err)
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("%w: worktree root is unavailable", agent.ErrCodeNavigationUnavailable)
	}
	return filepath.Clean(root), nil
}

func validatePosition(value agent.CodePosition) error {
	if value.Line < 1 || value.Line > 10_000_000 || value.Column < 1 || value.Column > 1_000_000 {
		return errors.New("query LSP: source position is invalid")
	}
	return nil
}

func defaultServers() map[agent.LanguageID]ServerConfig {
	return map[agent.LanguageID]ServerConfig{
		agent.LanguageGo:     {Program: "gopls", Args: []string{"serve"}},
		agent.LanguagePython: {Program: "pyright-langserver", Args: []string{"--stdio"}},
	}
}

func cloneServers(values map[agent.LanguageID]ServerConfig) map[agent.LanguageID]ServerConfig {
	cloned := make(map[agent.LanguageID]ServerConfig, len(values))
	for language, config := range values {
		config.Args = append([]string(nil), config.Args...)
		cloned[language] = config
	}
	return cloned
}

func languageServerAction(scope agent.NavigationScope, root string, config ServerConfig, timeout time.Duration) session.Action {
	digest := sha256.New()
	for _, value := range append([]string{"language-server", string(scope.WorktreeID), root, string(scope.Language), config.Program}, config.Args...) {
		_, _ = digest.Write([]byte(value))
		_, _ = digest.Write([]byte{0})
	}
	fingerprint := hex.EncodeToString(digest.Sum(nil))
	return session.Action{
		ID: "action_lsp_" + fingerprint[:16], SessionID: scope.SessionID, TurnID: scope.TurnID,
		Kind: session.ActionStartLanguageServer, WorktreeRoot: root,
		Summary: "Start language server: " + config.Program, Fingerprint: fingerprint,
		Command: &session.CommandAction{Program: config.Program, Args: append([]string(nil), config.Args...), Timeout: timeout},
	}
}

func textPositionParams(uri string, position agent.CodePosition) map[string]any {
	return map[string]any{
		"textDocument": map[string]string{"uri": uri},
		"position":     protocolPosition{Line: position.Line - 1, Character: position.Column - 1},
	}
}

func decodeDefinitionLocations(raw json.RawMessage) ([]protocolLocation, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil, nil
	}
	if strings.HasPrefix(trimmed, "[") {
		var values []struct {
			URI         string        `json:"uri"`
			Range       protocolRange `json:"range"`
			TargetURI   string        `json:"targetUri"`
			TargetRange protocolRange `json:"targetRange"`
		}
		if err := json.Unmarshal(raw, &values); err != nil {
			return nil, err
		}
		locations := make([]protocolLocation, 0, len(values))
		for _, value := range values {
			if value.TargetURI != "" {
				locations = append(locations, protocolLocation{URI: value.TargetURI, Range: value.TargetRange})
			} else {
				locations = append(locations, protocolLocation{URI: value.URI, Range: value.Range})
			}
		}
		return locations, nil
	}
	var value struct {
		URI         string        `json:"uri"`
		Range       protocolRange `json:"range"`
		TargetURI   string        `json:"targetUri"`
		TargetRange protocolRange `json:"targetRange"`
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	if value.TargetURI != "" {
		return []protocolLocation{{URI: value.TargetURI, Range: value.TargetRange}}, nil
	}
	return []protocolLocation{{URI: value.URI, Range: value.Range}}, nil
}

func diagnosticSeverity(value int) agent.DiagnosticSeverity {
	switch value {
	case 1:
		return agent.DiagnosticError
	case 2:
		return agent.DiagnosticWarning
	case 3:
		return agent.DiagnosticInformation
	case 4:
		return agent.DiagnosticHint
	default:
		return agent.DiagnosticInformation
	}
}

func diagnosticCode(value json.RawMessage) string {
	if len(value) == 0 || string(value) == "null" {
		return ""
	}
	var text string
	if json.Unmarshal(value, &text) == nil {
		return boundedString(text, 256)
	}
	var number json.Number
	if json.Unmarshal(value, &number) == nil {
		return boundedString(number.String(), 256)
	}
	return ""
}

func boundedString(value string, maximum int) string {
	value = strings.TrimSpace(value)
	if len(value) <= maximum {
		return value
	}
	value = value[:maximum]
	for len(value) > 0 && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func unavailable(ctx context.Context, operation string, err error) error {
	if contextError := ctx.Err(); contextError != nil {
		return contextError
	}
	var approvalRequired *session.ApprovalRequiredError
	if errors.As(err, &approvalRequired) {
		return err
	}
	return fmt.Errorf("%w: %s: %w", agent.ErrCodeNavigationUnavailable, operation, err)
}

func isNilExecutor(value workspace.CommandExecutor) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func isNilAuthorizer(value ActionAuthorizer) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
