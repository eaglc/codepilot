# internal/provider/deepseek

Built-in DeepSeek adapter.

## Purpose

Constructs an Eino DeepSeek SDK chat model and maps DeepSeek-specific thinking
options behind the provider-neutral contract.

## Key types

- `Adapter` — `New(client *http.Client)`, `Kind()`, `ListModels`, `CreateModel`.
- `Defaults` — base URL `https://api.deepseek.com`, default model, credential required.

## Dependencies

- `internal/llm`, `internal/provider`, `internal/provider/internal/builtin`,
  `internal/provider/internal/eino`.

## Design notes

- Discovery delegates to `builtin.ListOpenAICompatibleModels` plus
  `EnrichDeepSeekModels`; thinking levels are mapped via `WithExtraFields` rather
  than polluting `llm.ChatRequest`.

## Tests

- No dedicated test file; exercised via `provider/builtins_test.go` and the
  opt-in `provider/live_integration_test.go`.
