# cmd/codepilot

The CodePilot TUI executable entry point.

## Purpose

Parses flags and subcommands, drives the interactive trust/relocation
confirmation loop against errors returned by `app.New`, and dispatches the
`doctor` and `repair` maintenance commands. `main` wraps `run` with a
signal-based context and `os.Exit`.

## Flags

Main: `--workspace`, `--config-dir`, `--state-dir`, `--provider`, `--model`,
`--permission`, `--sensitive-path` (repeatable), `--version`, `--trust-workspace`,
`--relocate-worktree`, `--skip-relocation`, and the P0 rollback switch
`--disable-product-turns`.

Subcommands: `doctor`/`repair` (`--state-dir`, `--json`).

## Dependencies

- `internal/app`, `internal/codingagent` (a single ID type).

## Design notes

- `run` is separated from `main` with injected stdin/stdout/stderr, so it is
  unit-testable without launching the TUI.
- `--version` short-circuits before any `app.New` call; injected build metadata
  is sanitized and length-capped.
- The trust/relocation retry loop is bounded and treats a declined prompt as a
  clean exit.

## Tests

- `main_test.go` — flag parsing, version sanitization, trust-declined exit, and
  doctor/repair dispatch.
