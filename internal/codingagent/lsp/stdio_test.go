package lsp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/eaglc/codepilot/internal/codingagent/language"
)

func TestStdioServerFactoryCompletesInitializeSyncCallAndShutdown(t *testing.T) {
	root := lspFixture(t)
	profile := language.GoStrategy{}.Profile()
	profile.Server.Program = os.Args[0]
	profile.Server.Args = []string{"-test.run=^TestLSPHelperProcess$", "--", "lsp-helper"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started, err := (stdioServerFactory{}).Start(ctx, startSpec{Root: root, WorktreeID: "fixture", Language: profile, MaxMessageBytes: 1 << 20})
	if err != nil {
		t.Fatalf("start stdio server: %v", err)
	}
	doc, err := readDocument(root, "main.go", language.GoStrategy{}.Profile(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if err := started.SyncDocument(ctx, doc); err != nil {
		t.Fatalf("sync document: %v", err)
	}
	var raw json.RawMessage
	if err := started.Call(ctx, "textDocument/definition", textPositionParams(doc.uri, Position{Line: 1, Column: 1}), &raw); err != nil {
		t.Fatalf("definition call: %v", err)
	}
	locations, err := decodeDefinitionLocations(raw)
	if err != nil || len(locations) != 1 || locations[0].URI != doc.uri {
		t.Fatalf("definition response=%s locations=%#v err=%v", raw, locations, err)
	}
	closeCtx, closeCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer closeCancel()
	if err := started.Close(closeCtx); err != nil {
		t.Fatalf("close stdio server: %v", err)
	}
}

func TestLanguageServerEnvironmentExcludesUnlistedSecrets(t *testing.T) {
	t.Setenv("CODEPILOT_LSP_SECRET", "top-secret")
	for _, value := range languageServerEnvironment() {
		if strings.Contains(value, "CODEPILOT_LSP_SECRET") || strings.Contains(value, "top-secret") {
			t.Fatalf("unlisted secret entered language-server environment: %q", value)
		}
	}
}

func TestReadRPCFrameEnforcesDeclaredLimit(t *testing.T) {
	payload := []byte(`{"jsonrpc":"2.0","id":1,"result":null}`)
	frame := fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(payload), payload)
	read, err := readRPCFrame(bufio.NewReader(strings.NewReader(frame)), len(payload))
	if err != nil || !bytes.Equal(read, payload) {
		t.Fatalf("read frame=%q err=%v", read, err)
	}
	if _, err := readRPCFrame(bufio.NewReader(strings.NewReader(frame)), len(payload)-1); err == nil {
		t.Fatal("oversized RPC frame was accepted")
	}
}

func TestLSPHelperProcess(t *testing.T) {
	marker := false
	for _, argument := range os.Args {
		if argument == "lsp-helper" {
			marker = true
			break
		}
	}
	if !marker {
		return
	}
	reader := bufio.NewReader(os.Stdin)
	for {
		payload, err := readRPCFrame(reader, 1<<20)
		if err != nil {
			if err != io.EOF {
				os.Exit(2)
			}
			return
		}
		var request rpcEnvelope
		if json.Unmarshal(payload, &request) != nil {
			os.Exit(3)
		}
		if request.Method == "exit" {
			return
		}
		if len(request.ID) == 0 {
			continue
		}
		response := rpcEnvelope{JSONRPC: "2.0", ID: request.ID, Result: json.RawMessage(`null`)}
		switch request.Method {
		case "initialize":
			response.Result = json.RawMessage(`{"capabilities":{}}`)
		case "textDocument/definition":
			var params struct {
				TextDocument struct {
					URI string `json:"uri"`
				} `json:"textDocument"`
			}
			_ = json.Unmarshal(request.Params, &params)
			response.Result, _ = json.Marshal([]protocolLocation{{URI: params.TextDocument.URI, Range: protocolRange{Start: protocolPosition{}, End: protocolPosition{Character: 1}}}})
		case "shutdown":
		default:
			response.Result = nil
			response.Error = &rpcError{Code: -32601, Message: "not found"}
		}
		encoded, _ := json.Marshal(response)
		_, _ = fmt.Fprintf(os.Stdout, "Content-Length: %d\r\n\r\n", len(encoded))
		_, _ = os.Stdout.Write(encoded)
	}
}
