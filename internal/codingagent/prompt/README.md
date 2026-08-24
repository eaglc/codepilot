# internal/codingagent/prompt

Trusted Coding Agent system prompt and untrusted repository-derived guidance.

## Purpose

`Builder` builds the trusted system prompt and, separately, the untrusted
AGENTS.md-derived guidance delivered as lower-priority user-role context. The
split is the primary prompt-injection defense: repository content can never alter
tool availability, permissions, model selection, or policy.

## Key types

- `Builder` — `NewBuilder`, `BuildSystemPrompt`, `BuildUntrustedContext`.

## Dependencies

- `internal/codingagent` (scope/security types), `internal/llm`,
  `internal/codingagent/workspace`.

## Design notes

- Guidance is structurally separated from the system prompt and explicitly
  de-authorized; hard limits (≤32 files, ≤16 KiB/file, ≤64 KiB total) with
  per-file source/scope/sha256 metadata.
- Symlink-safe resolution and `security.RedactText` applied to guidance content.

## Tests

- `builder_test.go` — trusted scope + stable tool ordering, bounded scoped
  AGENTS.md loading, and oversized-instruction rejection.
