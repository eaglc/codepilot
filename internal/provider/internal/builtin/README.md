# internal/provider/internal/builtin

Shared validation, discovery, and capability enrichment for the concrete built-in
adapters.

## Purpose

Centralizes the code the OpenAI, DeepSeek, and Ollama adapters share: config
validation, OpenAI-compatible `/models` and Ollama `/api/tags` catalog fetching,
and model-capability enrichment.

## Key types

- `Defaults` — `BaseURL`, `ModelID`, `NeedsCredential`.
- `ValidateConfig`, `HTTPClient`.
- `ListOpenAICompatibleModels`, `ListOllamaModels`.
- `EnrichOpenAIModels`, `EnrichDeepSeekModels`.

## Dependencies

- `internal/llm`, `internal/provider`.

## Design notes

- Catalog decoding is size-bounded and rejects trailing JSON; Ollama enrichment
  pulls context window/tokenizer from `/api/show` with a heuristic max-output.

## Tests

- No dedicated test file; exercised via `provider/builtins_test.go`.
