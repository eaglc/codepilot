package lsp

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

type stdioServerFactory struct{}

func (stdioServerFactory) Start(ctx context.Context, spec startSpec) (server, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	command := exec.Command(spec.Language.Server.Program, spec.Language.Server.Args...)
	command.Dir = spec.Root
	command.Env = languageServerEnvironment()
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := command.Start(); err != nil {
		return nil, err
	}
	client := &stdioServer{
		command: command, stdin: stdin, reader: bufio.NewReaderSize(stdout, 64<<10), maxMessageBytes: spec.MaxMessageBytes,
		pending: make(map[int64]chan rpcEnvelope), diagnostics: make(map[string][]protocolDiagnostic), documents: make(map[string]documentState), done: make(chan struct{}),
	}
	go func() { _, _ = io.Copy(io.Discard, stderr) }()
	go client.readLoop()
	go func() {
		_ = command.Wait()
		client.markDone()
	}()
	initialize := map[string]any{
		"processId": os.Getpid(), "rootUri": fileURI(spec.Root),
		"workspaceFolders": []map[string]string{{"uri": fileURI(spec.Root), "name": spec.WorktreeID}},
		"capabilities": map[string]any{
			"textDocument": map[string]any{"definition": map[string]any{}, "references": map[string]any{}, "documentSymbol": map[string]any{}, "diagnostic": map[string]any{}},
			"workspace":    map[string]any{"workspaceFolders": true},
		},
	}
	var response json.RawMessage
	if err := client.Call(ctx, "initialize", initialize, &response); err != nil {
		closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = client.Close(closeCtx)
		cancel()
		return nil, err
	}
	if err := client.notify(ctx, "initialized", map[string]any{}); err != nil {
		closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = client.Close(closeCtx)
		cancel()
		return nil, err
	}
	return client, nil
}

type documentState struct {
	hash    [sha256.Size]byte
	version int
}

type stdioServer struct {
	command         *exec.Cmd
	stdin           io.WriteCloser
	reader          *bufio.Reader
	maxMessageBytes int

	writeMu sync.Mutex
	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan rpcEnvelope

	diagnosticMu sync.RWMutex
	diagnostics  map[string][]protocolDiagnostic
	documentMu   sync.Mutex
	documents    map[string]documentState

	done      chan struct{}
	doneOnce  sync.Once
	closeOnce sync.Once
}

func (s *stdioServer) SyncDocument(ctx context.Context, value document) error {
	hash := sha256.Sum256([]byte(value.text))
	s.documentMu.Lock()
	state, exists := s.documents[value.uri]
	if exists && state.hash == hash {
		s.documentMu.Unlock()
		return nil
	}
	version := state.version + 1
	method := "textDocument/didChange"
	params := map[string]any{
		"textDocument":   map[string]any{"uri": value.uri, "version": version},
		"contentChanges": []map[string]string{{"text": value.text}},
	}
	if !exists {
		method = "textDocument/didOpen"
		params = map[string]any{"textDocument": map[string]any{"uri": value.uri, "languageId": value.languageID, "version": version, "text": value.text}}
	}
	err := s.notify(ctx, method, params)
	if err == nil {
		s.documents[value.uri] = documentState{hash: hash, version: version}
	}
	s.documentMu.Unlock()
	return err
}

func (s *stdioServer) Call(ctx context.Context, method string, params any, result any) error {
	if !s.Alive() {
		return ErrUnavailable
	}
	encodedParams, err := json.Marshal(params)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.nextID++
	id := s.nextID
	responses := make(chan rpcEnvelope, 1)
	s.pending[id] = responses
	s.mu.Unlock()
	request := rpcEnvelope{JSONRPC: "2.0", ID: json.RawMessage(strconv.FormatInt(id, 10)), Method: method, Params: encodedParams}
	if err := s.writeEnvelope(request); err != nil {
		s.removePending(id)
		return err
	}
	select {
	case <-ctx.Done():
		s.removePending(id)
		_ = s.notify(context.Background(), "$/cancelRequest", map[string]int64{"id": id})
		return ctx.Err()
	case <-s.done:
		s.removePending(id)
		return ErrUnavailable
	case response := <-responses:
		if response.Error != nil {
			return response.Error
		}
		if result == nil || len(response.Result) == 0 || string(response.Result) == "null" {
			return nil
		}
		if err := json.Unmarshal(response.Result, result); err != nil {
			return errors.New("language server returned an invalid response")
		}
		return nil
	}
}

func (s *stdioServer) PublishedDiagnostics(uri string) []protocolDiagnostic {
	s.diagnosticMu.RLock()
	defer s.diagnosticMu.RUnlock()
	return append([]protocolDiagnostic(nil), s.diagnostics[uri]...)
}

func (s *stdioServer) Alive() bool {
	if s == nil {
		return false
	}
	select {
	case <-s.done:
		return false
	default:
		return true
	}
}

func (s *stdioServer) Close(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		if s.Alive() {
			shutdownCtx, cancel := context.WithTimeout(ctx, time.Second)
			_ = s.Call(shutdownCtx, "shutdown", map[string]any{}, nil)
			cancel()
			_ = s.notify(context.Background(), "exit", map[string]any{})
		}
		_ = s.stdin.Close()
	})
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		if s.command != nil && s.command.Process != nil {
			_ = s.command.Process.Kill()
		}
		return ctx.Err()
	}
}

func (s *stdioServer) notify(ctx context.Context, method string, params any) error {
	if !s.Alive() {
		return ErrUnavailable
	}
	encoded, err := json.Marshal(params)
	if err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	return s.writeEnvelope(rpcEnvelope{JSONRPC: "2.0", Method: method, Params: encoded})
}

func (s *stdioServer) writeEnvelope(value rpcEnvelope) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if !s.Alive() {
		return ErrUnavailable
	}
	_, err = fmt.Fprintf(s.stdin, "Content-Length: %d\r\n\r\n", len(encoded))
	if err == nil {
		_, err = s.stdin.Write(encoded)
	}
	return err
}

func (s *stdioServer) readLoop() {
	defer s.markDone()
	for {
		payload, err := readRPCFrame(s.reader, s.maxMessageBytes)
		if err != nil {
			return
		}
		var envelope rpcEnvelope
		if json.Unmarshal(payload, &envelope) != nil || envelope.JSONRPC != "2.0" {
			return
		}
		s.dispatch(envelope)
	}
}

func (s *stdioServer) dispatch(envelope rpcEnvelope) {
	if len(envelope.ID) != 0 && envelope.Method == "" {
		var id int64
		if json.Unmarshal(envelope.ID, &id) != nil {
			return
		}
		s.mu.Lock()
		responses := s.pending[id]
		delete(s.pending, id)
		s.mu.Unlock()
		if responses != nil {
			responses <- envelope
		}
		return
	}
	if envelope.Method == "textDocument/publishDiagnostics" {
		var params struct {
			URI         string               `json:"uri"`
			Diagnostics []protocolDiagnostic `json:"diagnostics"`
		}
		if json.Unmarshal(envelope.Params, &params) == nil && params.URI != "" {
			s.diagnosticMu.Lock()
			s.diagnostics[params.URI] = append([]protocolDiagnostic(nil), params.Diagnostics...)
			s.diagnosticMu.Unlock()
		}
		return
	}
	if len(envelope.ID) != 0 && envelope.Method != "" {
		response := rpcEnvelope{JSONRPC: "2.0", ID: append(json.RawMessage(nil), envelope.ID...), Result: json.RawMessage(`null`)}
		if envelope.Method == "workspace/configuration" {
			response.Result = json.RawMessage(`[]`)
		} else if envelope.Method != "client/registerCapability" && envelope.Method != "window/workDoneProgress/create" {
			response.Result = nil
			response.Error = &rpcError{Code: -32601, Message: "method not supported"}
		}
		_ = s.writeEnvelope(response)
	}
}

func (s *stdioServer) removePending(id int64) {
	s.mu.Lock()
	delete(s.pending, id)
	s.mu.Unlock()
}

func (s *stdioServer) markDone() {
	s.doneOnce.Do(func() { close(s.done) })
}

func readRPCFrame(reader *bufio.Reader, maximum int) ([]byte, error) {
	contentLength := -1
	headerBytes := 0
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		headerBytes += len(line)
		if headerBytes > 16<<10 {
			return nil, errors.New("language server response headers are too large")
		}
		line = strings.TrimSpace(line)
		if line == "" {
			break
		}
		name, value, found := strings.Cut(line, ":")
		if found && strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			parsed, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil {
				return nil, errors.New("language server content length is invalid")
			}
			contentLength = parsed
		}
	}
	if contentLength < 0 || contentLength > maximum {
		return nil, errors.New("language server response exceeds its size limit")
	}
	payload := make([]byte, contentLength)
	_, err := io.ReadFull(reader, payload)
	return payload, err
}

func languageServerEnvironment() []string {
	allow := map[string]struct{}{
		"PATH": {}, "PATHEXT": {}, "SYSTEMROOT": {}, "WINDIR": {}, "TEMP": {}, "TMP": {}, "TMPDIR": {},
		"HOME": {}, "USERPROFILE": {}, "LOCALAPPDATA": {}, "APPDATA": {}, "LANG": {}, "LC_ALL": {},
		"GOROOT": {}, "GOPATH": {}, "GOMODCACHE": {}, "GOCACHE": {}, "GOENV": {}, "GOPROXY": {}, "GOSUMDB": {},
		"VIRTUAL_ENV": {}, "PYTHONUTF8": {}, "NODE_PATH": {},
	}
	result := make([]string, 0, len(allow)+1)
	for _, value := range os.Environ() {
		name, _, found := strings.Cut(value, "=")
		if _, ok := allow[strings.ToUpper(name)]; found && ok {
			result = append(result, value)
		}
	}
	result = append(result, "CODEPILOT_LSP=1")
	return result
}
