# internal/provider/ollama

Built-in local Ollama adapter.

## Purpose

Constructs an Eino Ollama SDK chat model for local models discovered via
`builtin.ListOllamaModels`.

## Key types

- `Adapter` — `New(client *http.Client)`, `Kind()`, `ListModels`, `CreateModel`.
- `Defaults` — base URL `http://127.0.0.1:11434`, default model, no credential.

## Dependencies

- `internal/llm`, `internal/provider`, `internal/provider/internal/builtin`,
  `internal/provider/internal/eino`.

## Design notes

- Credential-free (local service); discovery delegates to `builtin.ListOllamaModels`.
- Rejects thinking-level requests with an actionable message to configure it on
  the profile.

## Tests

- No dedicated test file; exercised via `provider/builtins_test.go` and the
  opt-in `provider/live_integration_test.go`.
