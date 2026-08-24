# internal/provider

Provider-neutral model service. `Service` resolves configured `Profile` values
plus their credentials into SDK-backed `llm.ChatModel` instances, hiding every
adapter SDK type behind `internal/llm`. It also owns provider error
classification and in-memory profile/credential stores.

## Purpose

The root package holds the shared provider contracts and the `Service` that
implements the `llm.ModelFactory` / `llm.ModelCatalog` roles. Concrete adapters
live in the `openai`, `deepseek`, and `ollama` subpackages; the root package must
not import them (the `app` composition root registers adapters to avoid a parent/
child cycle). Eino SDK types are confined to `internal/provider/internal/eino`.

## Key types

- `Profile` — secret-free provider config (ID, kind, base URL, default model,
  credential reference, validation time).
- `Adapter` — `Kind()`, `ListModels(...)`, `CreateModel(...)` implemented by each vendor.
- `Service` — `NewService`, `Preflight`, `ListModels`, `DescribeModel`, `CreateModel`,
  `SaveCredential`, `DeleteCredential`, `CredentialStatus`.
- `ProfileRepository` / `CredentialStore` / `CredentialRepository` — persistence contracts.
- `ProductError` — safe error classification (`ErrorCode` + `Temporary()` / `RetryReason()`).
- `PreflightResult` / `ModelConfig` — preflight and model-construction data.
- `MemoryProfileRepository` / `MemoryCredentialStore` — in-memory implementations.

## Dependencies

- `internal/llm` only (root package).

## Design notes

- `ProductError.Error()` never echoes SDK/HTTP cause text; retryability is exposed
  through `Temporary()` so the Agent layer needs no knowledge of this package.
- Credentials are defensively copied and wiped after use; `Profile` never carries
  secret material.
- A model-capability cache avoids network discovery on every agent step.

## Tests

- `provider_test.go`, `error_test.go`, `builtins_test.go`, and an opt-in
  `live_integration_test.go` gated by `CODEPILOT_LIVE_*` env switches.

## Subpackages

- `credential` — keyring / environment / chained credential resolution.
- `file` — versioned, atomic, secret-free profile persistence.
- `openai` / `deepseek` / `ollama` — vendor adapters.
- `internal/builtin` — shared validation, discovery, and capability enrichment.
- `internal/eino` — the single Eino SDK ↔ `llm` translation boundary.
