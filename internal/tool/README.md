# internal/tool

Executable tool contracts. One layer above `llm`, this package owns the
`Tool`/`ResumableTool` interfaces, replay policy, call/result/progress types, and
the immutable `Registry` that validates and dispatches execution and resumption.

## Purpose

`tool` deliberately owns *execution only*. It does not journal activity or
publish product events — the Agent layer records tool activity centrally and
the Coding layer projects it to the UI. `llm.ToolDefinition` is what the model
sees; `tool.Tool` is what the Agent can execute. The two are not merged into one
interface.

## Key types

- `Tool` — `Definition()`, `ReplayPolicy()`, `Execute(ctx, Call, ProgressSink) (Result, error)`.
- `ResumableTool` — embeds `Tool` and adds `Resume(...)` for interrupt continuation.
- `Registry` — immutable validated set; `NewRegistry`, `Definitions`, `Lookup`, `Execute`, `Resume`.
- `Call` — a tool invocation (`ID`, `Name`, `Arguments`, `IdempotencyKey`).
- `Result` — execution result with `ResultStatus` and `Interrupt`.
- `ReplayPolicy` — `ReplayNever` / `ReplaySafe` / `ReplayIdempotent` recovery semantics.
- `Progress` / `ProgressSink` — bounded transient progress reporting.

## Dependencies

- `internal/llm` only.

## Design notes

- `Registry` sorts names deterministically and rejects duplicate model-visible names.
- Defensive copies are made of schemas, arguments, and interrupt payloads at every boundary.
- `isNilTool` uses reflection to catch typed-nil interface values.

## Tests

- `registry_test.go` — execution through the registry without activity ownership,
  and duplicate-name rejection.
