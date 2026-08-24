# internal/agent

Provider-neutral agent runtime. `Runtime` drives model turns in a loop, journals
every tool/step/operation boundary through a session repository, publishes
normalized events, and implements crash recovery and interrupt resolution by
rebuilding durable state from the journal.

## Purpose

One `RunID` is one user-triggered turn; each model request is a step; a turn can
contain many assistant messages, tool calls/results, and steps. `agent` owns the
generic turn/step/tool state machine, retry, abort, interrupt/resume, and event
normalization — but *not* coding concerns (paths, credentials, Git, LSP, patch,
permission modes). Those are injected as `DataPolicy` and `ContextProcessor`
interfaces so the package stays ignorant of the product.

## Key types

- `Runtime` — `NewRuntime(deps Dependencies)`, `Run`, `Resume`, `Recover`.
- `RunRequest` / `ResumeRequest` / `RecoverRequest` / `RunLimits` / `RunResult` / `RunStatus`.
- `Event` / `EventKind` — normalized agent events (`assistant_text_delta`,
  `tool_started`, `run_finished`, …) with typed payloads.
- `EventSink` / `NopEventSink` — event delivery seam.
- `ContextProcessor`, `IDGenerator`, `RetryWaiter`, `DataPolicy` — injected capabilities.

## Dependencies

- `internal/agent/session`, `internal/contextmanager`, `internal/llm`, `internal/tool`.

## Design notes

- Durable-first: `Run`/`Resume`/`Recover` validate and append records *before* any
  model or tool side effect; terminal records use `context.WithoutCancel` so a
  canceled context still persists the outcome.
- `DataPolicy` is threaded through every boundary (messages, tool args, results,
  free text) and a `safeError` wrapper redacts failure diagnostics.
- Budget enforcement (`runBudgetReason`, `toolBudgetReason`, `noProgressReason`)
  is centralized and derived from the durable journal.

## Tests

- `runtime_test.go` — 14 tests covering retry policy, budgets, no-progress
  detection, central tool-activity persistence, data-policy redaction,
  interrupt/resume, and safe/idempotent/never replay recovery.
