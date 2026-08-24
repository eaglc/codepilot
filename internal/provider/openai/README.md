# internal/provider/openai

Built-in OpenAI adapter.

## Purpose

Constructs an Eino OpenAI SDK chat model and maps reasoning-effort/thinking levels
for OpenAI models, hiding the SDK behind the `provider.Adapter` contract and
`llm.ChatModel`.

## Key types

- `Adapter` — `New(client *http.Client)`, `Kind()`, `ListModels`, `CreateModel`.
- `Defaults` — base URL `https://api.openai.com/v1`, default model, credential required.

## Dependencies

- `internal/llm`, `internal/provider`, `internal/provider/internal/builtin`,
  `internal/provider/internal/eino`.

## Design notes

- Discovery delegates to `builtin.ListOpenAICompatibleModels` plus
  `EnrichOpenAIModels`; reasoning effort is mapped via `sdk.WithReasoningEffort`
  and `sdk.WithMaxCompletionTokens`.

## Tests

- No dedicated test file; exercised via `provider/builtins_test.go` and the
  opt-in `provider/live_integration_test.go`.
