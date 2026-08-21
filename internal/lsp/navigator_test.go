package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eaglc/codepilot/internal/agent"
	"github.com/eaglc/codepilot/internal/session"
	"github.com/eaglc/codepilot/internal/workspace"
)

func TestNavigatorLazilyNavigatesAndFiltersLocations(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	mainPath := filepath.Join(root, "main.go")
	if err := os.WriteFile(mainPath, []byte("package main\nfunc answer() int { return 42 }\n"), 0o600); err != nil {
		t.Fatalf("write main document: %v", err)
	}
	secretPath := filepath.Join(root, ".env")
	if err := os.WriteFile(secretPath, []byte("TOKEN=secret\n"), 0o600); err != nil {
		t.Fatalf("write sensitive document: %v", err)
	}
	outsidePath := filepath.Join(t.TempDir(), "outside.go")
	if err := os.WriteFile(outsidePath, []byte("package outside\n"), 0o600); err != nil {
		t.Fatalf("write outside document: %v", err)
	}

	server := &scriptedServer{mainURI: fileURI(mainPath), secretURI: fileURI(secretPath), outsideURI: fileURI(outsidePath), diagnosticPush: true}
	executor := &fakeCommandExecutor{server: server}
	authorizer := &recordingAuthorizer{outcome: session.AuthorizationAllow}
	navigator, err := NewNavigator(Options{Executor: executor, Authorizer: authorizer, DiagnosticWait: time.Millisecond})
	if err != nil {
		t.Fatalf("create navigator: %v", err)
	}
	t.Cleanup(func() { _ = navigator.Close() })
	if executor.startCount() != 0 {
		t.Fatal("navigator eagerly started a language server")
	}

	scope := navigationTestScope(root)
	definitions, err := navigator.Definition(context.Background(), agent.DefinitionRequest{
		Scope: scope, Path: "main.go", Position: agent.CodePosition{Line: 2, Column: 6},
	})
	if err != nil {
		t.Fatalf("definition: %v", err)
	}
	if len(definitions) != 1 || definitions[0].Path != "main.go" || definitions[0].Range.Start.Line != 2 {
		t.Fatalf("filtered definitions = %#v", definitions)
	}
	if executor.startCount() != 1 {
		t.Fatalf("process starts = %d, want one", executor.startCount())
	}
	if authorizer.lastAction.Kind != session.ActionStartLanguageServer || authorizer.lastAction.Command.Program != "gopls" {
		t.Fatalf("startup action = %#v", authorizer.lastAction)
	}
	if got := server.lastPosition(); got != (protocolPosition{Line: 1, Character: 5}) {
		t.Fatalf("protocol position = %#v", got)
	}

	references, err := navigator.References(context.Background(), agent.ReferencesRequest{
		Scope: scope, Path: "main.go", Position: agent.CodePosition{Line: 2, Column: 6}, Limit: 10,
	})
	if err != nil || len(references) != 1 || references[0].Path != "main.go" {
		t.Fatalf("references = %#v, %v", references, err)
	}
	symbols, err := navigator.Symbols(context.Background(), agent.SymbolsRequest{Scope: scope, Query: "answer", Limit: 10})
	if err != nil || len(symbols) != 1 || symbols[0].Name != "answer" || symbols[0].Kind != "function" {
		t.Fatalf("symbols = %#v, %v", symbols, err)
	}
	diagnostics, err := navigator.Diagnostics(context.Background(), agent.DiagnosticsRequest{Scope: scope, Path: "main.go", Limit: 10})
	if err != nil || len(diagnostics) != 1 || diagnostics[0].Severity != agent.DiagnosticWarning || diagnostics[0].Code != "42" {
		t.Fatalf("diagnostics = %#v, %v", diagnostics, err)
	}
	if executor.startCount() != 1 {
		t.Fatalf("shared worktree process starts = %d, want one", executor.startCount())
	}

	closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	if err := navigator.CloseWorktree(closeCtx, scope.WorktreeID); err != nil {
		t.Fatalf("close worktree: %v", err)
	}
	cancel()
	if _, err := navigator.Symbols(context.Background(), agent.SymbolsRequest{Scope: scope, Query: "answer"}); err != nil {
		t.Fatalf("restart symbols: %v", err)
	}
	if executor.startCount() != 2 {
		t.Fatalf("starts after close = %d, want two", executor.startCount())
	}
}

func TestNewNavigatorValidatesServerAndResourceOptions(t *testing.T) {
	executor := &fakeCommandExecutor{}
	authorizer := &recordingAuthorizer{outcome: session.AuthorizationAllow}
	validPython := Options{
		Executor: executor, Authorizer: authorizer,
		Servers: map[agent.LanguageID]ServerConfig{agent.LanguagePython: {Program: "basedpyright-langserver", Args: []string{"--stdio"}}},
	}
	if navigator, err := NewNavigator(validPython); err != nil {
		t.Fatalf("create basedpyright navigator: %v", err)
	} else if err := navigator.Close(); err != nil {
		t.Fatalf("close unused navigator: %v", err)
	}

	tests := []Options{
		{Authorizer: authorizer},
		{Executor: executor},
		{Executor: executor, Authorizer: authorizer, Servers: map[agent.LanguageID]ServerConfig{agent.LanguageGo: {Program: "go", Args: []string{"run", "gopls"}}}},
		{Executor: executor, Authorizer: authorizer, MaxMessageBytes: 1024, MaxDocumentBytes: 1024},
		{Executor: executor, Authorizer: authorizer, MaxResults: 201},
	}
	for index, options := range tests {
		if _, err := NewNavigator(options); err == nil {
			t.Fatalf("invalid options %d were accepted: %#v", index, options)
		}
	}
}

func TestNavigatorRequiresApprovalBeforeStartingProcess(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	authorizer := &recordingAuthorizer{outcome: session.AuthorizationPrompt}
	executor := &fakeCommandExecutor{server: &scriptedServer{}}
	navigator, err := NewNavigator(Options{Executor: executor, Authorizer: authorizer})
	if err != nil {
		t.Fatal(err)
	}

	_, err = navigator.Definition(context.Background(), agent.DefinitionRequest{
		Scope: navigationTestScope(root), Path: "main.go", Position: agent.CodePosition{Line: 1, Column: 1},
	})
	var approvalRequired *session.ApprovalRequiredError
	if !errors.As(err, &approvalRequired) {
		t.Fatalf("definition error = %v, want approval", err)
	}
	if executor.startCount() != 0 || approvalRequired.Request.Action.Kind != session.ActionStartLanguageServer {
		t.Fatalf("process started before approval or wrong request: starts=%d request=%#v", executor.startCount(), approvalRequired.Request)
	}
}

func TestNavigatorDegradesWhenServerCannotStartAndRejectsSensitivePathFirst(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("TOKEN=secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	executor := &fakeCommandExecutor{startErr: errors.New("executable missing")}
	authorizer := &recordingAuthorizer{outcome: session.AuthorizationAllow}
	navigator, err := NewNavigator(Options{Executor: executor, Authorizer: authorizer})
	if err != nil {
		t.Fatal(err)
	}
	scope := navigationTestScope(root)

	_, err = navigator.Definition(context.Background(), agent.DefinitionRequest{
		Scope: scope, Path: ".env", Position: agent.CodePosition{Line: 1, Column: 1},
	})
	var appError *session.AppError
	if !errors.As(err, &appError) || appError.Code != session.ErrPermissionDenied || executor.startCount() != 0 || authorizer.calls != 0 {
		t.Fatalf("sensitive path error=%v starts=%d approvals=%d", err, executor.startCount(), authorizer.calls)
	}

	_, err = navigator.Definition(context.Background(), agent.DefinitionRequest{
		Scope: scope, Path: "main.go", Position: agent.CodePosition{Line: 1, Column: 1},
	})
	if !errors.Is(err, agent.ErrCodeNavigationUnavailable) || executor.startCount() != 1 {
		t.Fatalf("missing server error=%v starts=%d", err, executor.startCount())
	}
}

func TestNavigatorConcurrentFirstUseStartsOneProcess(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := &scriptedServer{mainURI: fileURI(filepath.Join(root, "main.go")), initializeDelay: 20 * time.Millisecond}
	executor := &fakeCommandExecutor{server: server}
	navigator, err := NewNavigator(Options{Executor: executor, Authorizer: &recordingAuthorizer{outcome: session.AuthorizationAllow}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = navigator.Close() })
	scope := navigationTestScope(root)

	var wait sync.WaitGroup
	errorsFound := make(chan error, 8)
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := navigator.Definition(context.Background(), agent.DefinitionRequest{
				Scope: scope, Path: "main.go", Position: agent.CodePosition{Line: 1, Column: 1},
			})
			errorsFound <- err
		}()
	}
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		if err != nil {
			t.Fatalf("concurrent definition: %v", err)
		}
	}
	if executor.startCount() != 1 {
		t.Fatalf("concurrent starts = %d, want one", executor.startCount())
	}
}

func TestNavigatorRejectsWorktreeIDRebinding(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	otherRoot := filepath.Clean(t.TempDir())
	executor := &fakeCommandExecutor{server: &scriptedServer{}}
	navigator, err := NewNavigator(Options{Executor: executor, Authorizer: &recordingAuthorizer{outcome: session.AuthorizationAllow}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = navigator.Close() })
	scope := navigationTestScope(root)
	if _, err := navigator.Symbols(context.Background(), agent.SymbolsRequest{Scope: scope, Query: "answer"}); err != nil {
		t.Fatalf("initial symbols: %v", err)
	}
	scope.WorktreeRoot = otherRoot
	if _, err := navigator.Symbols(context.Background(), agent.SymbolsRequest{Scope: scope, Query: "answer"}); !errors.Is(err, agent.ErrCodeNavigationUnavailable) {
		t.Fatalf("rebound worktree error = %v", err)
	}
	if executor.startCount() != 1 {
		t.Fatalf("rebound worktree starts = %d, want one", executor.startCount())
	}
}

func TestNavigatorCancelledCloseStillTerminatesReadyProcess(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	executor := &fakeCommandExecutor{server: &scriptedServer{}}
	navigator, err := NewNavigator(Options{Executor: executor, Authorizer: &recordingAuthorizer{outcome: session.AuthorizationAllow}})
	if err != nil {
		t.Fatal(err)
	}
	scope := navigationTestScope(root)
	if _, err := navigator.Symbols(context.Background(), agent.SymbolsRequest{Scope: scope, Query: "answer"}); err != nil {
		t.Fatalf("start symbols: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := navigator.CloseWorktree(ctx, scope.WorktreeID); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled close error = %v", err)
	}
	process := executor.lastProcess()
	select {
	case <-process.done:
	case <-time.After(time.Second):
		t.Fatal("cancelled close left the language-server process running")
	}
}

func TestProtocolFrameAndSingleLocationLinkDecoding(t *testing.T) {
	frame := bufio.NewReader(strings.NewReader("Content-Type: application/vscode-jsonrpc; charset=utf-8\r\nContent-Length: 5\r\n\r\nhello"))
	content, err := readFrame(frame, 1024)
	if err != nil || string(content) != "hello" {
		t.Fatalf("read frame = %q, %v", content, err)
	}
	oversized := bufio.NewReader(strings.NewReader("Content-Length: 2048\r\n\r\n"))
	if _, err := readFrame(oversized, 1024); err == nil {
		t.Fatal("oversized protocol frame was accepted")
	}

	target := "file:///workspace/main.go"
	raw, err := json.Marshal(protocolLocationLink{
		TargetURI:   target,
		TargetRange: protocolRange{Start: protocolPosition{Line: 3, Character: 2}, End: protocolPosition{Line: 3, Character: 8}},
	})
	if err != nil {
		t.Fatal(err)
	}
	locations, err := decodeDefinitionLocations(raw)
	if err != nil || len(locations) != 1 || locations[0].URI != target || locations[0].Range.Start.Line != 3 {
		t.Fatalf("location link = %#v, %v", locations, err)
	}
}

func navigationTestScope(root string) agent.NavigationScope {
	return agent.NavigationScope{
		SessionID: "session_test", TurnID: "turn_test", WorktreeID: "worktree_test", WorktreeRoot: root,
		PermissionMode: session.PermissionAsk, Language: agent.LanguageGo,
	}
}

type recordingAuthorizer struct {
	mu         sync.Mutex
	outcome    session.AuthorizationOutcome
	calls      int
	lastAction session.Action
}

func (a *recordingAuthorizer) Authorize(_ context.Context, _ session.PermissionMode, action session.Action) (session.Authorization, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls++
	a.lastAction = action
	authorization := session.Authorization{Outcome: a.outcome}
	if a.outcome == session.AuthorizationPrompt {
		authorization.Request = &session.ApprovalRequest{
			ID: "approval_lsp", SessionID: action.SessionID, TurnID: action.TurnID, Action: action, CreatedAt: time.Now().UTC(),
		}
	}
	return authorization, nil
}

type fakeCommandExecutor struct {
	mu        sync.Mutex
	starts    []workspace.ProcessSpec
	processes []*fakeProcess
	server    *scriptedServer
	startErr  error
}

func (*fakeCommandExecutor) Run(context.Context, workspace.CommandSpec) (workspace.CommandResult, error) {
	return workspace.CommandResult{}, errors.New("unexpected command run")
}

func (e *fakeCommandExecutor) Start(_ context.Context, spec workspace.ProcessSpec) (workspace.CommandProcess, error) {
	e.mu.Lock()
	e.starts = append(e.starts, spec)
	startErr := e.startErr
	server := e.server
	e.mu.Unlock()
	if startErr != nil {
		return nil, startErr
	}
	process := newFakeProcess(spec, server)
	e.mu.Lock()
	e.processes = append(e.processes, process)
	e.mu.Unlock()
	return process, nil
}

func (e *fakeCommandExecutor) startCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.starts)
}

func (e *fakeCommandExecutor) lastProcess() *fakeProcess {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.processes[len(e.processes)-1]
}

type fakeProcess struct {
	stdin      *io.PipeWriter
	stdout     *io.PipeReader
	stderr     *io.PipeReader
	serverIn   *io.PipeReader
	serverOut  *io.PipeWriter
	done       chan struct{}
	finishOnce sync.Once
	err        error
}

func newFakeProcess(spec workspace.ProcessSpec, server *scriptedServer) *fakeProcess {
	serverIn, clientIn := io.Pipe()
	clientOut, serverOut := io.Pipe()
	clientErr, serverErr := io.Pipe()
	_ = serverErr.Close()
	process := &fakeProcess{
		stdin: clientIn, stdout: clientOut, stderr: clientErr,
		serverIn: serverIn, serverOut: serverOut, done: make(chan struct{}),
	}
	go func() {
		err := server.serve(spec, serverIn, serverOut)
		_ = serverIn.Close()
		_ = serverOut.Close()
		process.finish(err)
	}()
	return process
}

func (p *fakeProcess) Stdin() io.WriteCloser { return p.stdin }
func (p *fakeProcess) Stdout() io.ReadCloser { return p.stdout }
func (p *fakeProcess) Stderr() io.ReadCloser { return p.stderr }
func (p *fakeProcess) Wait() error           { <-p.done; return p.err }
func (p *fakeProcess) Kill() error {
	_ = p.serverIn.CloseWithError(errors.New("killed"))
	_ = p.serverOut.CloseWithError(errors.New("killed"))
	p.finish(errors.New("killed"))
	return nil
}

func (p *fakeProcess) finish(err error) {
	p.finishOnce.Do(func() {
		p.err = err
		close(p.done)
	})
}

type scriptedServer struct {
	mu              sync.Mutex
	writeMu         sync.Mutex
	mainURI         string
	secretURI       string
	outsideURI      string
	diagnosticPush  bool
	initializeDelay time.Duration
	position        protocolPosition
}

func (s *scriptedServer) serve(_ workspace.ProcessSpec, input io.Reader, output io.Writer) error {
	reader := bufio.NewReader(input)
	for {
		content, err := readFrame(reader, defaultMaxMessageBytes)
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) {
				return nil
			}
			return err
		}
		var request rpcEnvelope
		if err := json.Unmarshal(content, &request); err != nil {
			return err
		}
		switch request.Method {
		case "initialize":
			if s.initializeDelay > 0 {
				time.Sleep(s.initializeDelay)
			}
			if err := s.respond(output, request.ID, map[string]any{"capabilities": map[string]any{}}); err != nil {
				return err
			}
		case "initialized", "textDocument/didChange", "$/cancelRequest":
		case "textDocument/didOpen":
			if s.diagnosticPush {
				if err := s.write(output, map[string]any{
					"jsonrpc": "2.0", "method": "textDocument/publishDiagnostics",
					"params": map[string]any{"uri": s.mainURI, "diagnostics": []protocolDiagnostic{{
						Range:    protocolRange{Start: protocolPosition{Line: 1, Character: 1}, End: protocolPosition{Line: 1, Character: 3}},
						Severity: 2, Message: "warning", Source: "fake", Code: json.RawMessage(`42`),
					}}},
				}); err != nil {
					return err
				}
			}
		case "textDocument/definition":
			s.capturePosition(request.Params)
			locations := []protocolLocation{
				{URI: s.mainURI, Range: protocolRange{Start: protocolPosition{Line: 1}, End: protocolPosition{Line: 1, Character: 6}}},
				{URI: s.secretURI, Range: protocolRange{Start: protocolPosition{}, End: protocolPosition{Character: 1}}},
				{URI: s.outsideURI, Range: protocolRange{Start: protocolPosition{}, End: protocolPosition{Character: 1}}},
			}
			if err := s.respond(output, request.ID, locations); err != nil {
				return err
			}
		case "textDocument/references":
			if err := s.respond(output, request.ID, []protocolLocation{{
				URI: s.mainURI, Range: protocolRange{Start: protocolPosition{Line: 1}, End: protocolPosition{Line: 1, Character: 6}},
			}}); err != nil {
				return err
			}
		case "workspace/symbol":
			if err := s.respond(output, request.ID, []protocolSymbol{
				{Name: "answer", Kind: 12, Location: protocolLocation{URI: s.mainURI, Range: protocolRange{Start: protocolPosition{Line: 1}, End: protocolPosition{Line: 1, Character: 6}}}},
				{Name: "outside", Kind: 12, Location: protocolLocation{URI: s.outsideURI, Range: protocolRange{Start: protocolPosition{}, End: protocolPosition{Character: 1}}}},
			}); err != nil {
				return err
			}
		case "textDocument/diagnostic":
			if s.diagnosticPush {
				if err := s.respondError(output, request.ID, -32601, "not supported"); err != nil {
					return err
				}
			} else if err := s.respond(output, request.ID, map[string]any{"kind": "full", "items": []protocolDiagnostic{}}); err != nil {
				return err
			}
		case "shutdown":
			if err := s.respond(output, request.ID, nil); err != nil {
				return err
			}
		case "exit":
			return nil
		default:
			if len(request.ID) > 0 {
				if err := s.respondError(output, request.ID, -32601, "not supported"); err != nil {
					return err
				}
			}
		}
	}
}

func (s *scriptedServer) capturePosition(raw json.RawMessage) {
	var params struct {
		Position protocolPosition `json:"position"`
	}
	if json.Unmarshal(raw, &params) == nil {
		s.mu.Lock()
		s.position = params.Position
		s.mu.Unlock()
	}
}

func (s *scriptedServer) lastPosition() protocolPosition {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.position
}

func (s *scriptedServer) respond(output io.Writer, id json.RawMessage, result any) error {
	return s.write(output, map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func (s *scriptedServer) respondError(output io.Writer, id json.RawMessage, code int, message string) error {
	return s.write(output, map[string]any{"jsonrpc": "2.0", "id": id, "error": &rpcError{Code: code, Message: message}})
}

func (s *scriptedServer) write(output io.Writer, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := io.WriteString(output, "Content-Length: "+strconv.Itoa(len(encoded))+"\r\n\r\n"); err != nil {
		return err
	}
	_, err = output.Write(encoded)
	return err
}
