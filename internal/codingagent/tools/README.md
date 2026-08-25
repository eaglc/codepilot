# internal/codingagent/tools

The workspace-scoped Coding Agent tool set (package `codingtools`).

## Purpose

`Factory` builds a per-worktree `tool.Registry` of read-only file/Git inspection
tools, exact-match `edit_file`, whole-file `replace_file`, and validated `apply_patch` edit tools, trusted check-plan execution, and
LSP-backed navigation tools. Every tool is uniformly wrapped by a security
boundary (sensitive-path approval/redaction); result-producing tools also use
an artifact boundary for large-result externalization.

## Tool set

`read_file`, `read_tool_result`, `list_files`, `search_code`, `git_status`, `git_diff`, `git_log`,
`git_branches`, `git_show_commit`, `edit_file`, `replace_file`, `apply_patch`, `list_check_plans`, `run_checks`,
`find_definition`, `find_references`, `get_diagnostics`, `document_symbols`.

## Key types

- `Factory` — `NewFactory(options)`, `CreateTools(ctx, scope)`.
- `Options` — bounds plus `Artifacts`, `Security`, `Languages`, `Navigator`.

## Dependencies

- `internal/codingagent`, `internal/codingagent/language`,
  `internal/codingagent/lsp`, `internal/codingagent/workspace`,
  `internal/llm`, `internal/tool`.

## Design notes

| Operation | Tools | `read-only` | `ask` | `auto-edit` |
|---|---|---|---|---|
| Inspect worktree | read/search/Git/list tools | Allow | Allow | Allow |
| Modify files | `edit_file`, `replace_file`, `apply_patch` | Deny | Approve or exact session grant | Allow bounded safe edits; approve guarded edits |
| Run project checks | `run_checks` | Deny | Approve or exact plan grant | Approve or exact plan grant |
| Start a language server | navigation tools, only when not already ready | Deny | Approve or language grant | Approve or language grant |

Sensitive-path reads use the separate security boundary: explicit reads require
one-time approval and remain redacted, while recursive search is denied.
Large tool results expose a content-addressed reference; `read_tool_result`
resolves that reference through the artifact reader in bounded UTF-8 chunks.

- `apply_patch` validates via `git apply --check` plus before/after digests to
  reject worktree drift, and binds approval to an integrity digest.
- `edit_file` replaces one exact `old_text` occurrence, generates the unified
  diff in-process, and reuses the same validation, approval, and drift checks.
- `replace_file` safely rewrites one existing text file, preserves its line
  endings by default, and generates the preview rather than accepting a diff.
- A shared permission boundary applies read-only, session-grant, auto-edit, and
  approval decisions to modifying and process-starting tools.
- `run_checks` only runs fixed allowlisted plans (go test/vet/build,
  python compile/test, node test/build) and never accepts executable/shell text.
- Argument decoding uses `DisallowUnknownFields` and rejects trailing JSON.
- `process_unix.go` / `process_windows.go` isolate the single cross-platform
  check-command concern behind build tags.

## Tests

- `artifact_boundary_test.go`, `checks_test.go`, `factory_test.go`,
  `git_read_test.go` — traversal rejection, sensitive-read approval/redaction,
  patch approval/drift/session-grant, LSP navigation approval, and process-group
  timeout cancellation.
