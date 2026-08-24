package lsp

import "encoding/json"

type protocolPosition struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

type protocolRange struct {
	Start protocolPosition `json:"start"`
	End   protocolPosition `json:"end"`
}

type protocolLocation struct {
	URI   string        `json:"uri"`
	Range protocolRange `json:"range"`
}

type protocolDiagnostic struct {
	Range    protocolRange   `json:"range"`
	Severity int             `json:"severity"`
	Message  string          `json:"message"`
	Source   string          `json:"source"`
	Code     json.RawMessage `json:"code"`
}

type protocolDocumentSymbol struct {
	Name           string                   `json:"name"`
	Kind           int                      `json:"kind"`
	Range          protocolRange            `json:"range"`
	SelectionRange protocolRange            `json:"selectionRange"`
	Children       []protocolDocumentSymbol `json:"children"`
}

type protocolSymbolInformation struct {
	Name          string           `json:"name"`
	Kind          int              `json:"kind"`
	ContainerName string           `json:"containerName"`
	Location      protocolLocation `json:"location"`
}

type rpcEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string {
	if e == nil {
		return "language server request failed"
	}
	return e.Message
}

func symbolKind(value int) string {
	names := map[int]string{
		1: "file", 2: "module", 3: "namespace", 4: "package", 5: "class", 6: "method", 7: "property", 8: "field",
		9: "constructor", 10: "enum", 11: "interface", 12: "function", 13: "variable", 14: "constant", 15: "string",
		16: "number", 17: "boolean", 18: "array", 19: "object", 20: "key", 21: "null", 22: "enum-member",
		23: "struct", 24: "event", 25: "operator", 26: "type-parameter",
	}
	if name := names[value]; name != "" {
		return name
	}
	return "unknown"
}
