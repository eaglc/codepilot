# internal/llm

Provider-neutral LLM contracts. This is the leaf foundation layer of the whole
codebase: it defines the canonical message, tool-call, request, stream, usage,
and model-reference types that every higher layer depends on, so no other
package ever has to couple to a specific vendor SDK.

## Purpose

`llm` owns the model-agnostic *protocol* only — no executable behavior beyond
validation, cloning, and stream collection. It has zero internal dependencies
and only imports the standard library, which is enforced by
`internal/architecture` and reflected in the dependency test.

## Key types

- `ModelRef` — identifies a model by `Provider` + `Model`; `Validate()`.
- `Model` — provider-neutral capability metadata (context window, max output,
  reasoning, image input, tokenizer).
- `Message` — canonical conversation message (`user` / `assistant` / `tool`),
  with `Validate()`, `ToolCalls()`, and `Clone()`.
- `Content` — tagged-union content block (`text`, `image`, `thinking`, `tool_call`).
- `ToolDefinition` / `ToolCall` — model-visible tool declarations and calls.
- `ChatRequest` — one complete model request; `Validate()`.
- `ChatModel` / `ModelFactory` / `ModelCatalog` — the interfaces providers implement.
- `Stream` / `StreamEvent` — normalized streaming protocol and envelope.
- `Usage` / `StopReason` — normalized token/cost usage and stop classification.
- `ResponseError` — normalized provider failure with `Temporary()` / `RetryReason()`.
- `CollectStream(stream)` — drains a stream to a terminal message.

## Dependencies

None. Standard library only.

## Design notes

- Defensive `Clone()` methods deep-copy mutable payloads (`Arguments`, `Details`,
  `Data`) to prevent aliasing across package boundaries.
- `Validate()` enforces role-specific content rules and rejects unrelated
  populated fields.

## Tests

- `message_test.go` — JSON round-trip of assistant tool-call and tool-result
  messages, and rejection of invalid content/role combinations.
