# internal/codingagent/lsp

Bounded, worktree-isolated language-server navigation for the Coding tools.

## Purpose

Manages one stdio language-server process per worktree/language pair (gopls,
pyright/basedpyright, typescript-language-server), implements the minimal
JSON-RPC/LSP client (initialize, didOpen/didChange, definition, references,
diagnostics, documentSymbol), and converts results to a product-neutral one-based
representation with strict resource and path containment.

## Key types

- `Navigator` — read-only `Ready`, `Definition`, `References`, `Diagnostics`,
  `DocumentSymbols`, `CloseWorktree`, `Close`.
- `Manager` — process-backed manager; `NewManager`.
- `Scope` — immutable worktree/language query binding.
- `Position` / `Range` / `Location` / `Diagnostic` / `Symbol` — product-facing result types.
- `Options` — process/request/document/result bounds.
- `ErrUnavailable` — sentinel for a missing, crashed, or unsupported server.

## Dependencies

- `internal/codingagent/language`.

## Design notes

- One process per binding with lazy start, single crash-retry, and binding-drift
  detection; every dimension bounded (timeouts, 2 MiB documents, 8 MiB messages,
  capped results, 16 KiB headers).
- Server environment is whitelisted (secrets stripped); definition/reference
  results are re-validated for in-worktree containment.

## Tests

- `manager_test.go`, `stdio_test.go` — bounded/worktree-safe queries, crash
  restart + isolation, binding-drift rejection, timeout, and an RPC-frame limit
  subprocess harness.
