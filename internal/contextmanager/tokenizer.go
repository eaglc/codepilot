package contextmanager

import (
	"encoding/json"

	"github.com/eaglc/codepilot/internal/llm"
)

// Tokenizer estimates provider context usage without coupling to a Provider implementation.
type Tokenizer interface {
	CountText(value string) int
	CountMessage(message llm.Message) int
	CountTool(definition llm.ToolDefinition) int
}

// ByteTokenizer provides a conservative deterministic approximation for bootstrapping and tests.
type ByteTokenizer struct{}

// CountText estimates one token per four bytes and counts an empty value as zero.
func (ByteTokenizer) CountText(value string) int {
	if value == "" {
		return 0
	}
	return (len(value) + 3) / 4
}

// CountMessage includes structured fields such as tool parameters and tool-result metadata.
func (ByteTokenizer) CountMessage(message llm.Message) int {
	encoded, err := json.Marshal(message)
	if err != nil || len(encoded) == 0 {
		return 0
	}
	return (len(encoded) + 3) / 4
}

// CountTool includes the name, description, and complete JSON input schema.
func (ByteTokenizer) CountTool(definition llm.ToolDefinition) int {
	encoded, err := json.Marshal(definition)
	if err != nil || len(encoded) == 0 {
		return 0
	}
	return (len(encoded) + 3) / 4
}
