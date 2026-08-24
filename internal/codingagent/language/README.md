# internal/codingagent/language

Read-only language detection and allowlisted language-server profiles for the
Coding Agent.

## Purpose

Detects which supported languages (Go, Python, Node/TypeScript) are present in a
Git worktree using only filesystem metadata — never invoking the toolchain or
project code. It freezes validated detection strategies and exposes immutable
language-server profiles plus prompt hints consumed by the Coding tools.

## Key types

- `ID` — `Go`, `Python`, `Node`.
- `Detection` / `Profile` / `WorkspaceProfile` — bounded evidence, immutable
  per-language capability, and polyglot result.
- `Strategy` / `Registry` — detection seam and frozen strategy set.
- `GoStrategy` / `PythonStrategy` / `NodeStrategy` — concrete strategies.
- `ResolvePath` / `DocumentLanguage` — extension-based language and LSP `languageId` mapping.

## Dependencies

- `internal/codingagent/workspace` (Git-aware file index).

## Design notes

- Detection is strictly read-only: markers first, then the Git index, then a
  bounded root fallback.
- Server commands are pinned to exact program basename and args (`gopls serve`,
  `pyright-langserver --stdio`, `typescript-language-server --stdio`).

## Tests

- `registry_test.go` — deterministic polyglot detection, read-only sources,
  defensive profile copies, and Git-aware indexing.
