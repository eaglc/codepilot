# internal/provider/internal/eino

The single translation layer between the CloudWeGo Eino SDK and the
provider-neutral `llm` contract.

## Purpose

Wraps an Eino `ToolCallingChatModel` as an `llm.ChatModel` and normalizes
streaming events, messages, tools, stop reasons, and usage. This is the only place
Eino SDK types are mapped to `llm`, which keeps every adapter thin and prevents
Eino from leaking into `agent`, `codingagent`, or the UI.

## Key types

- `Model` — `New(inner model.ToolCallingChatModel, ref llm.ModelRef, options RequestOptionMapper)`.
- `RequestOptionMapper` — maps `llm.ChatRequest` to Eino model options.

## Dependencies

- `internal/llm`, `internal/provider`; external `github.com/cloudwego/eino/...`.

## Design notes

- Streaming buffers chunks and calls `schema.ConcatMessages` at EOF, emitting a
  full lifecycle of `StreamEvent`s; usage normalization includes cache-read and
  reasoning tokens.
- Tool-result errors are surfaced to the model via a `[tool_error]\n` prefix;
  redacted thinking is dropped.

## Tests

- `model_test.go` — provider error classification, stream normalization, and
  tool-result error visibility.
