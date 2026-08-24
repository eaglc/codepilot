package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eaglc/codepilot/internal/codingagent/language"
)

type fakeServerFactory struct {
	mu             sync.Mutex
	starts         []startSpec
	servers        []*fakeServer
	crashFirstCall bool
	blockCalls     bool
}

func (f *fakeServerFactory) Start(_ context.Context, spec startSpec) (server, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	value := &fakeServer{spec: spec, alive: true, crashOnCall: f.crashFirstCall && len(f.starts) == 0, blockCalls: f.blockCalls}
	f.starts = append(f.starts, spec)
	f.servers = append(f.servers, value)
	return value, nil
}

type fakeServer struct {
	mu          sync.Mutex
	spec        startSpec
	alive       bool
	crashOnCall bool
	blockCalls  bool
	syncs       []document
	calls       []string
	closes      int
}

func (s *fakeServer) SyncDocument(_ context.Context, value document) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.alive {
		return ErrUnavailable
	}
	s.syncs = append(s.syncs, value)
	return nil
}

func (s *fakeServer) Call(ctx context.Context, method string, _ any, result any) error {
	s.mu.Lock()
	s.calls = append(s.calls, method)
	if s.crashOnCall {
		s.crashOnCall = false
		s.alive = false
		s.mu.Unlock()
		return errors.New("transport closed")
	}
	block := s.blockCalls
	alive := s.alive
	s.mu.Unlock()
	if !alive {
		return ErrUnavailable
	}
	if block {
		<-ctx.Done()
		return ctx.Err()
	}
	mainURI := fileURI(filepath.Join(s.spec.Root, "main.go"))
	outsideURI := fileURI(filepath.Join(filepath.Dir(s.spec.Root), "outside.go"))
	var response any
	switch method {
	case "textDocument/definition":
		response = []protocolLocation{{URI: mainURI, Range: protocolRange{Start: protocolPosition{Line: 1, Character: 2}, End: protocolPosition{Line: 1, Character: 6}}}}
	case "textDocument/references":
		response = []protocolLocation{
			{URI: mainURI, Range: protocolRange{Start: protocolPosition{Line: 3, Character: 1}, End: protocolPosition{Line: 3, Character: 4}}},
			{URI: mainURI, Range: protocolRange{Start: protocolPosition{Line: 3, Character: 1}, End: protocolPosition{Line: 3, Character: 4}}},
			{URI: outsideURI, Range: protocolRange{Start: protocolPosition{}, End: protocolPosition{Character: 1}}},
		}
	case "textDocument/diagnostic":
		response = map[string]any{"items": []protocolDiagnostic{{Range: protocolRange{Start: protocolPosition{}, End: protocolPosition{Character: 3}}, Severity: 1, Message: "undefined: value", Source: "fixture", Code: json.RawMessage(`"E001"`)}}}
	case "textDocument/documentSymbol":
		response = []protocolDocumentSymbol{{Name: "main", Kind: 12, Range: protocolRange{Start: protocolPosition{}, End: protocolPosition{Line: 2}}, Children: []protocolDocumentSymbol{{Name: "value", Kind: 13, Range: protocolRange{Start: protocolPosition{Line: 1}, End: protocolPosition{Line: 1, Character: 5}}}}}}
	default:
		return &rpcError{Code: -32601, Message: "missing"}
	}
	encoded, _ := json.Marshal(response)
	return json.Unmarshal(encoded, result)
}

func (*fakeServer) PublishedDiagnostics(string) []protocolDiagnostic { return nil }
func (s *fakeServer) Alive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.alive
}
func (s *fakeServer) Close(context.Context) error {
	s.mu.Lock()
	s.closes++
	s.alive = false
	s.mu.Unlock()
	return nil
}

func TestManagerNavigationQueriesAreBoundedAndWorktreeSafe(t *testing.T) {
	root := lspFixture(t)
	factory := &fakeServerFactory{}
	manager, err := NewManager(Options{factory: factory, RequestTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	scope := goScope(root, "worktree-1")
	definitions, err := manager.Definition(context.Background(), scope, "main.go", Position{Line: 1, Column: 1}, 10)
	if err != nil || len(definitions) != 1 || definitions[0].Path != "main.go" || definitions[0].Range.Start.Line != 2 || definitions[0].Range.Start.Column != 3 {
		t.Fatalf("definitions=%#v err=%v", definitions, err)
	}
	references, err := manager.References(context.Background(), scope, "main.go", Position{Line: 1, Column: 1}, true, 10)
	if err != nil || len(references) != 1 || references[0].Path != "main.go" {
		t.Fatalf("references=%#v err=%v", references, err)
	}
	diagnostics, err := manager.Diagnostics(context.Background(), scope, "main.go", 10)
	if err != nil || len(diagnostics) != 1 || diagnostics[0].Severity != "error" || diagnostics[0].Code != "E001" {
		t.Fatalf("diagnostics=%#v err=%v", diagnostics, err)
	}
	symbols, err := manager.DocumentSymbols(context.Background(), scope, "main.go", 10)
	if err != nil || len(symbols) != 2 || symbols[0].Name != "main" || symbols[1].Container != "main" {
		t.Fatalf("symbols=%#v err=%v", symbols, err)
	}
	if len(factory.starts) != 1 || len(factory.servers[0].syncs) != 4 {
		t.Fatalf("server reuse: starts=%d syncs=%d", len(factory.starts), len(factory.servers[0].syncs))
	}
}

func TestManagerRestartsOnceAfterCrashAndIsolatesWorktrees(t *testing.T) {
	rootOne := lspFixture(t)
	rootTwo := lspFixture(t)
	factory := &fakeServerFactory{crashFirstCall: true}
	manager, _ := NewManager(Options{factory: factory, RequestTimeout: time.Second})
	defer manager.Close()
	if _, err := manager.Definition(context.Background(), goScope(rootOne, "one"), "main.go", Position{Line: 1, Column: 1}, 10); err != nil {
		t.Fatalf("restart definition: %v", err)
	}
	if len(factory.starts) != 2 || factory.servers[0].closes != 1 {
		t.Fatalf("crash restart: starts=%d first closes=%d", len(factory.starts), factory.servers[0].closes)
	}
	if _, err := manager.Definition(context.Background(), goScope(rootTwo, "two"), "main.go", Position{Line: 1, Column: 1}, 10); err != nil {
		t.Fatalf("second worktree definition: %v", err)
	}
	if len(factory.starts) != 3 || samePath(factory.starts[1].Root, factory.starts[2].Root) {
		t.Fatalf("worktree processes were not isolated: %#v", factory.starts)
	}
	changed := goScope(rootTwo, "one")
	if _, err := manager.Definition(context.Background(), changed, "main.go", Position{Line: 1, Column: 1}, 10); !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "binding changed") {
		t.Fatalf("changed worktree binding error = %v", err)
	}
}

func TestManagerRejectsLanguageServerBindingDrift(t *testing.T) {
	root := lspFixture(t)
	if err := os.WriteFile(filepath.Join(root, "main.py"), []byte("value = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	factory := &fakeServerFactory{}
	manager, _ := NewManager(Options{factory: factory, RequestTimeout: time.Second})
	defer manager.Close()
	profile := language.PythonStrategy{}.Profile()
	scope := Scope{WorktreeID: "worktree", Root: root, Language: profile}
	if _, err := manager.Definition(context.Background(), scope, "main.py", Position{Line: 1, Column: 1}, 10); err != nil {
		t.Fatalf("initial definition: %v", err)
	}
	profile.Server.Program = "basedpyright-langserver"
	scope.Language = profile
	if manager.Ready(scope) {
		t.Fatal("changed server binding reported ready")
	}
	if _, err := manager.Definition(context.Background(), scope, "main.py", Position{Line: 1, Column: 1}, 10); !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "binding changed") {
		t.Fatalf("changed server binding error = %v", err)
	}
	if len(factory.starts) != 1 {
		t.Fatalf("binding drift started another process: starts=%d", len(factory.starts))
	}
}

func TestManagerAppliesRequestTimeoutAndCloseWorktree(t *testing.T) {
	root := lspFixture(t)
	factory := &fakeServerFactory{blockCalls: true}
	manager, _ := NewManager(Options{factory: factory, RequestTimeout: 50 * time.Millisecond})
	started := time.Now()
	_, err := manager.Definition(context.Background(), goScope(root, "worktree"), "main.go", Position{Line: 1, Column: 1}, 10)
	if !errors.Is(err, ErrUnavailable) || time.Since(started) > time.Second {
		t.Fatalf("request timeout error=%v duration=%s", err, time.Since(started))
	}
	if err := manager.CloseWorktree(context.Background(), "worktree"); err != nil || factory.servers[0].closes != 1 {
		t.Fatalf("close worktree: closes=%d err=%v", factory.servers[0].closes, err)
	}
	if _, err := manager.Definition(context.Background(), goScope(root, "worktree"), "main.go", Position{Line: 0, Column: 1}, 10); err == nil || len(factory.starts) != 1 {
		t.Fatalf("invalid request started a server: starts=%d err=%v", len(factory.starts), err)
	}
}

func lspFixture(t *testing.T) string {
	t.Helper()
	root := filepath.Clean(t.TempDir())
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nfunc main() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func goScope(root, worktree string) Scope {
	return Scope{WorktreeID: worktree, Root: root, Language: language.GoStrategy{}.Profile()}
}
