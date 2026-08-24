package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/eaglc/codepilot/internal/codingagent/language"
)

const (
	defaultInitializationTimeout = 20 * time.Second
	defaultRequestTimeout        = 10 * time.Second
	defaultDiagnosticWait        = 300 * time.Millisecond
	defaultMaxDocumentBytes      = 2 << 20
	defaultMaxMessageBytes       = 8 << 20
	defaultMaxResults            = 100
)

type server interface {
	SyncDocument(ctx context.Context, document document) error
	Call(ctx context.Context, method string, params any, result any) error
	PublishedDiagnostics(uri string) []protocolDiagnostic
	Alive() bool
	Close(ctx context.Context) error
}

type startSpec struct {
	Root            string
	WorktreeID      string
	Language        language.Profile
	MaxMessageBytes int
}

type serverFactory interface {
	Start(ctx context.Context, spec startSpec) (server, error)
}

// Options bounds process startup, requests, documents and result sets.
type Options struct {
	InitializationTimeout time.Duration
	RequestTimeout        time.Duration
	DiagnosticWait        time.Duration
	MaxDocumentBytes      int64
	MaxMessageBytes       int
	MaxResults            int
	factory               serverFactory
}

// Manager owns one language-server process per worktree/language pair.
type Manager struct {
	options Options
	mu      sync.Mutex
	servers map[serverKey]*serverSlot
	closed  bool
}

type serverKey struct {
	worktree string
	language language.ID
}

type serverSlot struct {
	root    string
	binding string
	ready   chan struct{}
	server  server
	err     error
}

// NewManager creates a process-backed LSP manager with safe defaults.
func NewManager(options Options) (*Manager, error) {
	if options.InitializationTimeout <= 0 {
		options.InitializationTimeout = defaultInitializationTimeout
	}
	if options.RequestTimeout <= 0 {
		options.RequestTimeout = defaultRequestTimeout
	}
	if options.DiagnosticWait < 0 {
		return nil, errors.New("create LSP manager: diagnostic wait is invalid")
	}
	if options.DiagnosticWait == 0 {
		options.DiagnosticWait = defaultDiagnosticWait
	}
	if options.MaxDocumentBytes <= 0 {
		options.MaxDocumentBytes = defaultMaxDocumentBytes
	}
	if options.MaxMessageBytes <= 0 {
		options.MaxMessageBytes = defaultMaxMessageBytes
	}
	if options.MaxResults <= 0 {
		options.MaxResults = defaultMaxResults
	}
	if options.InitializationTimeout > time.Minute || options.RequestTimeout > time.Minute || options.DiagnosticWait > 5*time.Second || options.MaxDocumentBytes > 16<<20 || options.MaxMessageBytes < 1024 || options.MaxMessageBytes > 32<<20 || options.MaxResults > 200 {
		return nil, errors.New("create LSP manager: resource limits are invalid")
	}
	if options.factory == nil {
		options.factory = stdioServerFactory{}
	}
	return &Manager{options: options, servers: make(map[serverKey]*serverSlot)}, nil
}

// Ready reports whether an alive server already exists for this exact binding.
func (m *Manager) Ready(scope Scope) bool {
	if m == nil {
		return false
	}
	root, err := validateScope(scope)
	if err != nil {
		return false
	}
	key := serverKey{worktree: scope.WorktreeID, language: scope.Language.ID}
	m.mu.Lock()
	defer m.mu.Unlock()
	slot := m.servers[key]
	if slot == nil || !samePath(slot.root, root) || slot.binding != language.ServerBinding(scope.Language) {
		return false
	}
	select {
	case <-slot.ready:
		return slot.err == nil && slot.server != nil && slot.server.Alive()
	default:
		return false
	}
}

// Definition resolves bounded in-worktree definitions.
func (m *Manager) Definition(ctx context.Context, scope Scope, path string, position Position, limit int) ([]Location, error) {
	root, doc, limit, err := m.prepare(scope, path, position, true, limit)
	if err != nil {
		return nil, err
	}
	var raw json.RawMessage
	err = m.query(ctx, scope, doc, func(callCtx context.Context, server server) error {
		return server.Call(callCtx, "textDocument/definition", textPositionParams(doc.uri, position), &raw)
	})
	if err != nil {
		return nil, err
	}
	values, err := decodeDefinitionLocations(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: definition response was invalid", ErrUnavailable)
	}
	return safeLocations(root, values, limit), nil
}

// References resolves bounded in-worktree references.
func (m *Manager) References(ctx context.Context, scope Scope, path string, position Position, includeDeclaration bool, limit int) ([]Location, error) {
	root, doc, limit, err := m.prepare(scope, path, position, true, limit)
	if err != nil {
		return nil, err
	}
	params := textPositionParams(doc.uri, position)
	params["context"] = map[string]bool{"includeDeclaration": includeDeclaration}
	var values []protocolLocation
	err = m.query(ctx, scope, doc, func(callCtx context.Context, server server) error {
		return server.Call(callCtx, "textDocument/references", params, &values)
	})
	if err != nil {
		return nil, err
	}
	return safeLocations(root, values, limit), nil
}

// Diagnostics returns pull or recently published diagnostics for one document.
func (m *Manager) Diagnostics(ctx context.Context, scope Scope, path string, limit int) ([]Diagnostic, error) {
	root, doc, limit, err := m.prepare(scope, path, Position{}, false, limit)
	if err != nil {
		return nil, err
	}
	var values []protocolDiagnostic
	err = m.query(ctx, scope, doc, func(callCtx context.Context, server server) error {
		var response struct {
			Items []protocolDiagnostic `json:"items"`
		}
		callErr := server.Call(callCtx, "textDocument/diagnostic", map[string]any{"textDocument": map[string]string{"uri": doc.uri}}, &response)
		if methodNotFound(callErr) {
			timer := time.NewTimer(m.options.DiagnosticWait)
			defer timer.Stop()
			select {
			case <-callCtx.Done():
				return callCtx.Err()
			case <-timer.C:
			}
			values = server.PublishedDiagnostics(doc.uri)
			return nil
		}
		values = response.Items
		return callErr
	})
	if err != nil {
		return nil, err
	}
	result := make([]Diagnostic, 0, min(len(values), limit))
	for _, value := range values {
		if !validProtocolRange(value.Range) || strings.TrimSpace(value.Message) == "" {
			continue
		}
		result = append(result, Diagnostic{
			Path: doc.relative, Range: productRange(value.Range), Severity: diagnosticSeverity(value.Severity),
			Message: bounded(value.Message, 4096), Source: bounded(value.Source, 256), Code: diagnosticCode(value.Code),
		})
		if len(result) == limit {
			break
		}
	}
	_ = root
	return result, nil
}

// DocumentSymbols returns flat, bounded symbols for one document.
func (m *Manager) DocumentSymbols(ctx context.Context, scope Scope, path string, limit int) ([]Symbol, error) {
	root, doc, limit, err := m.prepare(scope, path, Position{}, false, limit)
	if err != nil {
		return nil, err
	}
	var raw json.RawMessage
	err = m.query(ctx, scope, doc, func(callCtx context.Context, server server) error {
		return server.Call(callCtx, "textDocument/documentSymbol", map[string]any{"textDocument": map[string]string{"uri": doc.uri}}, &raw)
	})
	if err != nil {
		return nil, err
	}
	return decodeDocumentSymbols(root, doc.relative, raw, limit), nil
}

func (m *Manager) prepare(scope Scope, requested string, position Position, needsPosition bool, limit int) (string, document, int, error) {
	if m == nil {
		return "", document{}, 0, errors.New("query language server: manager is nil")
	}
	root, err := validateScope(scope)
	if err != nil {
		return "", document{}, 0, err
	}
	if needsPosition && (position.Line < 1 || position.Line > 10_000_000 || position.Column < 1 || position.Column > 1_000_000) {
		return "", document{}, 0, errors.New("query language server: source position is invalid")
	}
	limit, err = m.resultLimit(limit)
	if err != nil {
		return "", document{}, 0, err
	}
	doc, err := readDocument(root, requested, scope.Language, m.options.MaxDocumentBytes)
	return root, doc, limit, err
}

func (m *Manager) query(ctx context.Context, scope Scope, doc document, operation func(context.Context, server) error) error {
	for attempt := 0; attempt < 2; attempt++ {
		server, key, err := m.serverFor(ctx, scope)
		if err != nil {
			return err
		}
		requestCtx, cancel := context.WithTimeout(ctx, m.options.RequestTimeout)
		err = server.SyncDocument(requestCtx, doc)
		if err == nil {
			err = operation(requestCtx, server)
		}
		cancel()
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if server.Alive() || attempt == 1 {
			return fmt.Errorf("%w: request failed: %v", ErrUnavailable, safeRPCError(err))
		}
		m.invalidate(key, server)
	}
	return ErrUnavailable
}

func (m *Manager) serverFor(ctx context.Context, scope Scope) (server, serverKey, error) {
	root, err := validateScope(scope)
	if err != nil {
		return nil, serverKey{}, err
	}
	key := serverKey{worktree: scope.WorktreeID, language: scope.Language.ID}
	binding := language.ServerBinding(scope.Language)
	for {
		m.mu.Lock()
		if m.closed {
			m.mu.Unlock()
			return nil, key, fmt.Errorf("%w: manager is closed", ErrUnavailable)
		}
		if existing := m.servers[key]; existing != nil {
			if !samePath(existing.root, root) || existing.binding != binding {
				m.mu.Unlock()
				return nil, key, fmt.Errorf("%w: worktree binding changed", ErrUnavailable)
			}
			ready := existing.ready
			m.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, key, ctx.Err()
			case <-ready:
			}
			if existing.err != nil {
				return nil, key, existing.err
			}
			if existing.server != nil && existing.server.Alive() {
				return existing.server, key, nil
			}
			m.invalidate(key, existing.server)
			continue
		}
		slot := &serverSlot{root: root, binding: binding, ready: make(chan struct{})}
		m.servers[key] = slot
		m.mu.Unlock()

		startCtx, cancel := context.WithTimeout(ctx, m.options.InitializationTimeout)
		started, startErr := m.options.factory.Start(startCtx, startSpec{Root: root, WorktreeID: scope.WorktreeID, Language: scope.Language, MaxMessageBytes: m.options.MaxMessageBytes})
		if startErr == nil && (started == nil || !started.Alive()) {
			startErr = errors.New("language server did not become ready")
		}
		cancel()
		m.mu.Lock()
		if m.servers[key] == slot && !m.closed {
			slot.server, slot.err = started, normalizeStartError(startErr)
			if slot.err != nil {
				delete(m.servers, key)
			}
			close(slot.ready)
			m.mu.Unlock()
			if slot.err != nil {
				return nil, key, slot.err
			}
			return started, key, nil
		}
		slot.err = fmt.Errorf("%w: worktree closed during startup", ErrUnavailable)
		close(slot.ready)
		m.mu.Unlock()
		if started != nil {
			closeCtx, closeCancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = started.Close(closeCtx)
			closeCancel()
		}
		return nil, key, slot.err
	}
}

func (m *Manager) invalidate(key serverKey, expected server) {
	m.mu.Lock()
	slot := m.servers[key]
	removed := false
	if slot != nil && (expected == nil || slot.server == expected) {
		delete(m.servers, key)
		removed = true
	}
	m.mu.Unlock()
	if removed && expected != nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_ = expected.Close(ctx)
		cancel()
	}
}

// CloseWorktree closes every language server for exactly one worktree.
func (m *Manager) CloseWorktree(ctx context.Context, worktreeID string) error {
	if m == nil || strings.TrimSpace(worktreeID) == "" {
		return nil
	}
	m.mu.Lock()
	var slots []*serverSlot
	for key, slot := range m.servers {
		if key.worktree == worktreeID {
			delete(m.servers, key)
			slots = append(slots, slot)
		}
	}
	m.mu.Unlock()
	return closeSlots(ctx, slots)
}

// Close shuts down every server and rejects future starts.
func (m *Manager) Close() error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	slots := make([]*serverSlot, 0, len(m.servers))
	for _, slot := range m.servers {
		slots = append(slots, slot)
	}
	m.servers = make(map[serverKey]*serverSlot)
	m.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return closeSlots(ctx, slots)
}

func closeSlots(ctx context.Context, slots []*serverSlot) error {
	var values []error
	for _, slot := range slots {
		select {
		case <-ctx.Done():
			return errors.Join(append(values, ctx.Err())...)
		case <-slot.ready:
		}
		if slot.server != nil {
			values = append(values, slot.server.Close(ctx))
		}
	}
	return errors.Join(values...)
}

func (m *Manager) resultLimit(value int) (int, error) {
	if value < 0 || value > 200 {
		return 0, errors.New("query language server: result limit is invalid")
	}
	if value == 0 || value > m.options.MaxResults {
		return m.options.MaxResults, nil
	}
	return value, nil
}

func validateScope(scope Scope) (string, error) {
	if strings.TrimSpace(scope.WorktreeID) == "" || strings.TrimSpace(scope.Root) == "" || scope.Language.ID == "" {
		return "", errors.New("query language server: scope is incomplete")
	}
	if strings.ContainsAny(scope.WorktreeID, "\x00\r\n") || len(scope.WorktreeID) > 256 || !filepath.IsAbs(scope.Root) || filepath.Clean(scope.Root) != scope.Root {
		return "", errors.New("query language server: scope is invalid")
	}
	if err := validateLanguageServer(scope.Language); err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(scope.Root)
	if err != nil {
		return "", fmt.Errorf("%w: worktree root is unavailable", ErrUnavailable)
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("%w: worktree root is unavailable", ErrUnavailable)
	}
	return filepath.Clean(resolved), nil
}

func validateLanguageServer(profile language.Profile) error {
	if err := language.ValidateServer(profile); err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return nil
}

func normalizeStartError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return fmt.Errorf("%w: language server could not start", ErrUnavailable)
}

func textPositionParams(uri string, position Position) map[string]any {
	return map[string]any{"textDocument": map[string]string{"uri": uri}, "position": protocolPosition{Line: position.Line - 1, Character: position.Column - 1}}
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
		result := make([]protocolLocation, 0, len(values))
		for _, value := range values {
			if value.TargetURI != "" {
				result = append(result, protocolLocation{URI: value.TargetURI, Range: value.TargetRange})
			} else {
				result = append(result, protocolLocation{URI: value.URI, Range: value.Range})
			}
		}
		return result, nil
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

func safeLocations(root string, values []protocolLocation, limit int) []Location {
	result := make([]Location, 0, min(len(values), limit))
	seen := make(map[string]struct{})
	for _, value := range values {
		path, ok := pathFromFileURI(root, value.URI)
		if !ok || !validProtocolRange(value.Range) {
			continue
		}
		location := Location{Path: path, Range: productRange(value.Range)}
		key := fmt.Sprintf("%s:%d:%d:%d:%d", path, location.Range.Start.Line, location.Range.Start.Column, location.Range.End.Line, location.Range.End.Column)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, location)
		if len(result) == limit {
			break
		}
	}
	sort.Slice(result, func(left, right int) bool {
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

func decodeDocumentSymbols(root, relative string, raw json.RawMessage, limit int) []Symbol {
	var documents []protocolDocumentSymbol
	if json.Unmarshal(raw, &documents) == nil && len(documents) != 0 {
		result := make([]Symbol, 0, min(len(documents), limit))
		var appendSymbols func([]protocolDocumentSymbol, string)
		appendSymbols = func(values []protocolDocumentSymbol, container string) {
			for _, value := range values {
				if len(result) == limit {
					return
				}
				if strings.TrimSpace(value.Name) != "" && validProtocolRange(value.Range) {
					result = append(result, Symbol{Name: bounded(value.Name, 512), Kind: symbolKind(value.Kind), Container: bounded(container, 512), Location: Location{Path: relative, Range: productRange(value.Range)}})
				}
				appendSymbols(value.Children, value.Name)
			}
		}
		appendSymbols(documents, "")
		return result
	}
	var information []protocolSymbolInformation
	if json.Unmarshal(raw, &information) != nil {
		return nil
	}
	result := make([]Symbol, 0, min(len(information), limit))
	for _, value := range information {
		locations := safeLocations(root, []protocolLocation{value.Location}, 1)
		if len(locations) == 0 || strings.TrimSpace(value.Name) == "" {
			continue
		}
		result = append(result, Symbol{Name: bounded(value.Name, 512), Kind: symbolKind(value.Kind), Container: bounded(value.ContainerName, 512), Location: locations[0]})
		if len(result) == limit {
			break
		}
	}
	return result
}

func validProtocolRange(value protocolRange) bool {
	if value.Start.Line < 0 || value.Start.Character < 0 || value.End.Line < 0 || value.End.Character < 0 {
		return false
	}
	return value.End.Line > value.Start.Line || value.End.Line == value.Start.Line && value.End.Character >= value.Start.Character
}

func productRange(value protocolRange) Range {
	return Range{Start: Position{Line: value.Start.Line + 1, Column: value.Start.Character + 1}, End: Position{Line: value.End.Line + 1, Column: value.End.Character + 1}}
}

func diagnosticSeverity(value int) string {
	switch value {
	case 1:
		return "error"
	case 2:
		return "warning"
	case 3:
		return "information"
	case 4:
		return "hint"
	default:
		return "information"
	}
}

func diagnosticCode(value json.RawMessage) string {
	var text string
	if json.Unmarshal(value, &text) == nil {
		return bounded(text, 256)
	}
	var number json.Number
	if json.Unmarshal(value, &number) == nil {
		return bounded(number.String(), 256)
	}
	return ""
}

func bounded(value string, maximum int) string {
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

func methodNotFound(err error) bool {
	var rpc *rpcError
	return errors.As(err, &rpc) && rpc.Code == -32601
}

func safeRPCError(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "request timed out"
	}
	if errors.Is(err, context.Canceled) {
		return "request was cancelled"
	}
	var rpc *rpcError
	if errors.As(err, &rpc) {
		return fmt.Sprintf("server error %d", rpc.Code)
	}
	return "language server connection failed"
}
