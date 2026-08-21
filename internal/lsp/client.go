package lsp

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/eaglc/codepilot/internal/workspace"
)

type rpcResponse struct {
	result json.RawMessage
	err    error
}

type documentState struct {
	version int
	digest  [sha256.Size]byte
}

// client owns one language-server process, its read loop, and all pending RPC
// calls. Only the read loop consumes stdout; concurrent callers share pending.
type client struct {
	process workspace.CommandProcess
	stdin   io.WriteCloser
	stdout  io.ReadCloser
	stderr  io.ReadCloser
	rootURI string
	maxSize int

	writeMu sync.Mutex
	docMu   sync.Mutex
	stateMu sync.Mutex
	nextID  int64
	pending map[int64]chan rpcResponse
	docs    map[string]documentState
	diags   map[string][]protocolDiagnostic

	diagnosticChanged chan struct{}
	done              chan struct{}
	processDone       chan struct{}
	finishOnce        sync.Once
	closeOnce         sync.Once
	terminalErr       error
}

func newClient(process workspace.CommandProcess, rootURI string, maxSize int) *client {
	value := &client{
		process:           process,
		stdin:             process.Stdin(),
		stdout:            process.Stdout(),
		stderr:            process.Stderr(),
		rootURI:           rootURI,
		maxSize:           maxSize,
		pending:           make(map[int64]chan rpcResponse),
		docs:              make(map[string]documentState),
		diags:             make(map[string][]protocolDiagnostic),
		diagnosticChanged: make(chan struct{}, 1),
		done:              make(chan struct{}),
		processDone:       make(chan struct{}),
	}
	go value.readLoop()
	go value.waitProcess()
	go func() {
		// stderr is drained without retaining its potentially sensitive content.
		_, _ = io.Copy(io.Discard, value.stderr)
	}()
	return value
}

func (c *client) initialize(ctx context.Context, processID int) error {
	params := map[string]any{
		"processId":        processID,
		"clientInfo":       map[string]string{"name": "codepilot"},
		"rootUri":          c.rootURI,
		"workspaceFolders": []map[string]string{{"uri": c.rootURI, "name": "workspace"}},
		"capabilities": map[string]any{
			"workspace": map[string]any{"workspaceFolders": true, "symbol": map[string]any{}},
			"textDocument": map[string]any{
				"definition": map[string]any{}, "references": map[string]any{}, "publishDiagnostics": map[string]any{},
			},
		},
	}
	var result json.RawMessage
	if err := c.call(ctx, "initialize", params, &result); err != nil {
		return err
	}
	return c.notifyContext(ctx, "initialized", map[string]any{})
}

func (c *client) call(ctx context.Context, method string, params any, destination any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.stateMu.Lock()
	c.nextID++
	id := c.nextID
	response := make(chan rpcResponse, 1)
	c.pending[id] = response
	c.stateMu.Unlock()

	message := map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}
	if err := c.writeMessageContext(ctx, message); err != nil {
		c.removePending(id)
		return err
	}
	select {
	case <-ctx.Done():
		c.removePending(id)
		cancelCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		_ = c.notifyContext(cancelCtx, "$/cancelRequest", map[string]any{"id": id})
		cancel()
		return ctx.Err()
	case <-c.done:
		c.removePending(id)
		return c.failure()
	case value := <-response:
		if value.err != nil {
			return value.err
		}
		if destination == nil || len(value.result) == 0 || bytes.Equal(value.result, []byte("null")) {
			return nil
		}
		if err := json.Unmarshal(value.result, destination); err != nil {
			return fmt.Errorf("decode LSP response for %s: %w", method, err)
		}
		return nil
	}
}

func (c *client) notify(method string, params any) error {
	return c.writeMessage(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func (c *client) notifyContext(ctx context.Context, method string, params any) error {
	return c.writeMessageContext(ctx, map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func (c *client) syncDocument(ctx context.Context, value document, language string) error {
	c.docMu.Lock()
	defer c.docMu.Unlock()
	digest := sha256.Sum256([]byte(value.text))
	state, exists := c.docs[value.uri]
	if exists && state.digest == digest {
		return nil
	}
	state.version++
	state.digest = digest
	if !exists {
		if err := c.notifyContext(ctx, "textDocument/didOpen", map[string]any{"textDocument": map[string]any{
			"uri": value.uri, "languageId": language, "version": state.version, "text": value.text,
		}}); err != nil {
			return err
		}
		c.docs[value.uri] = state
		return nil
	}
	if err := c.notifyContext(ctx, "textDocument/didChange", map[string]any{
		"textDocument":   map[string]any{"uri": value.uri, "version": state.version},
		"contentChanges": []map[string]string{{"text": value.text}},
	}); err != nil {
		return err
	}
	c.docs[value.uri] = state
	return nil
}

func (c *client) cachedDiagnostics(uri string) []protocolDiagnostic {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return append([]protocolDiagnostic(nil), c.diags[uri]...)
}

func (c *client) waitForDiagnostics(ctx context.Context, uri string, wait time.Duration) []protocolDiagnostic {
	if values := c.cachedDiagnostics(uri); len(values) > 0 || wait <= 0 {
		return values
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-c.done:
	case <-timer.C:
	case <-c.diagnosticChanged:
	}
	return c.cachedDiagnostics(uri)
}

func (c *client) readLoop() {
	reader := bufio.NewReader(c.stdout)
	for {
		content, err := readFrame(reader, c.maxSize)
		if err != nil {
			c.finish(fmt.Errorf("read LSP message: %w", err))
			return
		}
		var envelope rpcEnvelope
		if err := json.Unmarshal(content, &envelope); err != nil || envelope.JSONRPC != "2.0" {
			c.finish(errors.New("read LSP message: invalid JSON-RPC envelope"))
			return
		}
		if envelope.Method != "" {
			c.handleServerMessage(envelope)
			continue
		}
		id, err := decodeResponseID(envelope.ID)
		if err != nil {
			continue
		}
		c.stateMu.Lock()
		pending := c.pending[id]
		delete(c.pending, id)
		c.stateMu.Unlock()
		if pending == nil {
			continue
		}
		if envelope.Error != nil {
			pending <- rpcResponse{err: envelope.Error}
		} else {
			pending <- rpcResponse{result: append(json.RawMessage(nil), envelope.Result...)}
		}
	}
}

func (c *client) handleServerMessage(envelope rpcEnvelope) {
	if envelope.Method == "textDocument/publishDiagnostics" {
		var params struct {
			URI         string               `json:"uri"`
			Diagnostics []protocolDiagnostic `json:"diagnostics"`
		}
		if json.Unmarshal(envelope.Params, &params) == nil && params.URI != "" {
			c.stateMu.Lock()
			c.diags[params.URI] = append([]protocolDiagnostic(nil), params.Diagnostics...)
			c.stateMu.Unlock()
			select {
			case c.diagnosticChanged <- struct{}{}:
			default:
			}
		}
		return
	}
	if len(envelope.ID) == 0 {
		return
	}
	var result any
	var responseError *rpcError
	switch envelope.Method {
	case "workspace/configuration":
		var params struct {
			Items []json.RawMessage `json:"items"`
		}
		// A decode failure only yields an empty configuration list, which is a
		// safe fallback for workspace/configuration queries.
		_ = json.Unmarshal(envelope.Params, &params)
		configuration := make([]map[string]any, len(params.Items))
		for index := range configuration {
			configuration[index] = map[string]any{}
		}
		result = configuration
	case "workspace/workspaceFolders":
		result = []map[string]string{{"uri": c.rootURI, "name": "workspace"}}
	case "client/registerCapability", "client/unregisterCapability", "window/workDoneProgress/create":
		result = nil
	case "workspace/applyEdit":
		result = map[string]any{"applied": false, "failureReason": "CodePilot does not accept language-server edits."}
	default:
		responseError = &rpcError{Code: -32601, Message: "Method not supported by CodePilot."}
	}
	response := map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(envelope.ID)}
	if responseError != nil {
		response["error"] = responseError
	} else {
		response["result"] = result
	}
	if err := c.writeMessage(response); err != nil {
		c.finish(fmt.Errorf("write LSP response: %w", err))
	}
}

func (c *client) writeMessage(value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode LSP message: %w", err)
	}
	if len(encoded) > c.maxSize {
		return errors.New("encode LSP message: payload exceeds limit")
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	select {
	case <-c.done:
		return c.failure()
	default:
	}
	if _, err := fmt.Fprintf(c.stdin, "Content-Length: %d\r\n\r\n", len(encoded)); err != nil {
		return fmt.Errorf("write LSP header: %w", err)
	}
	if _, err := c.stdin.Write(encoded); err != nil {
		return fmt.Errorf("write LSP body: %w", err)
	}
	return nil
}

func (c *client) writeMessageContext(ctx context.Context, value any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	written := make(chan error, 1)
	go func() {
		written <- c.writeMessage(value)
	}()
	select {
	case err := <-written:
		return err
	case <-ctx.Done():
		// Closing a blocked process write is the only portable way to release the
		// owning goroutine because anonymous pipes do not expose deadlines.
		_ = c.process.Kill()
		return ctx.Err()
	case <-c.done:
		return c.failure()
	}
}

func (c *client) waitProcess() {
	err := c.process.Wait()
	close(c.processDone)
	if err == nil {
		err = io.EOF
	}
	c.finish(fmt.Errorf("language server exited: %w", err))
}

func (c *client) finish(err error) {
	c.finishOnce.Do(func() {
		c.stateMu.Lock()
		c.terminalErr = err
		pending := c.pending
		c.pending = make(map[int64]chan rpcResponse)
		c.stateMu.Unlock()
		close(c.done)
		for _, response := range pending {
			response <- rpcResponse{err: err}
		}
	})
}

func (c *client) failure() error {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if c.terminalErr != nil {
		return c.terminalErr
	}
	return errors.New("language server connection is closed")
}

func (c *client) closed() bool {
	select {
	case <-c.done:
		return true
	default:
		return false
	}
}

func (c *client) removePending(id int64) {
	c.stateMu.Lock()
	delete(c.pending, id)
	c.stateMu.Unlock()
}

func (c *client) close(ctx context.Context) error {
	var closeErr error
	c.closeOnce.Do(func() {
		select {
		case <-c.done:
		default:
			var ignored json.RawMessage
			if err := c.call(ctx, "shutdown", map[string]any{}, &ignored); err != nil && ctx.Err() == nil {
				closeErr = errors.Join(closeErr, err)
			}
			if err := c.notifyContext(ctx, "exit", map[string]any{}); err != nil && !c.closed() {
				closeErr = errors.Join(closeErr, err)
			}
		}
		_ = c.stdin.Close()
		select {
		case <-c.processDone:
		case <-ctx.Done():
			closeErr = errors.Join(closeErr, ctx.Err(), c.process.Kill())
			select {
			case <-c.processDone:
			case <-time.After(time.Second):
				closeErr = errors.Join(closeErr, errors.New("language server did not exit after it was killed"))
			}
		}
		_ = c.stdout.Close()
		_ = c.stderr.Close()
	})
	return closeErr
}

func readFrame(reader *bufio.Reader, maxSize int) ([]byte, error) {
	contentLength := -1
	headerBytes := 0
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		headerBytes += len(line)
		if headerBytes > 8<<10 {
			return nil, errors.New("LSP header exceeds limit")
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		name, value, found := strings.Cut(line, ":")
		if !found || !strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			continue
		}
		length, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return nil, errors.New("LSP content length is invalid")
		}
		contentLength = length
	}
	if contentLength < 0 || contentLength > maxSize {
		return nil, errors.New("LSP content length is outside the allowed range")
	}
	content := make([]byte, contentLength)
	if _, err := io.ReadFull(reader, content); err != nil {
		return nil, err
	}
	return content, nil
}

func decodeResponseID(value json.RawMessage) (int64, error) {
	var id int64
	if len(value) == 0 || json.Unmarshal(value, &id) != nil || id <= 0 {
		return 0, errors.New("LSP response ID is invalid")
	}
	return id, nil
}

func isMethodNotFound(err error) bool {
	var rpcFailure *rpcError
	return errors.As(err, &rpcFailure) && rpcFailure.Code == -32601
}
