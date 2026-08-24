# internal/app

The composition root. It wires every layer together into a runnable application.

## Purpose

`New` resolves config/state directories, acquires the exclusive state lease,
resolves/trusts the worktree binding, and then constructs the file-backed
repositories, the provider service, the context manager, the Agent runtime, the
LSP manager, the workspace manager, the coding tools factory, the prompt builder,
the UI event bridge, and finally the `codingagent.Service` and `ui.Model`. It also
hosts one-shot maintenance (`DiagnoseState`/`RepairState`) and the
`ProviderManager` port adapter.

## Key types

- `Application` — `New(ctx, Options)`, `Run`, `Close`.
- `Options` — process paths, provider/model/permission selection, trust flags, streams.
- `TrustRequiredError` / `RelocationRequiredError` — sentinel errors for CLI confirmation.
- `WorkspaceTrustRequired` / `WorktreeRelocationRequired` / `UserMessage` — CLI extractors.
- `DiagnoseState` / `RepairState` — read-only / explicit consistency maintenance.

## Dependencies

- The full stack: `agent`, `agent/session`, `codingagent` (+ subpackages),
  `codingstore/file`, `contextmanager`, `llm`, `provider` (+ subpackages),
  `sessionstore/file`, and `ui`. As the composition root it is
  deliberately exempt from the dependency-direction rule.

## Design notes

- Trust and relocation are typed errors returned from `New`, letting the CLI
  drive an idempotent retry loop instead of the app doing I/O prompts.
- Credential secrets are scrubbed in place and never copied into reports.

## Tests

- `app_test.go`, `maintenance_test.go`, `provider_manager_test.go` — full `New`
  composition, state lease exclusivity, trust/relocation, consistency maintenance,
  and provider port adaptation.
