package lsp

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type failingWriteCloser struct{}

func (failingWriteCloser) Write([]byte) (int, error) { return 0, errors.New("stdin closed") }
func (failingWriteCloser) Close() error              { return nil }

func TestClientWriteFailureClosesConnection(t *testing.T) {
	c := &client{
		stdin:   failingWriteCloser{},
		done:    make(chan struct{}),
		maxSize: 1 << 20,
	}

	c.handleServerMessage(rpcEnvelope{ID: json.RawMessage("1"), Method: "workspace/workspaceFolders"})

	if !c.closed() {
		t.Fatal("client should close after failing to write a server response")
	}
	if err := c.failure(); err == nil || !strings.Contains(err.Error(), "write LSP response") {
		t.Fatalf("failure = %v, want a write LSP response error", err)
	}
}
