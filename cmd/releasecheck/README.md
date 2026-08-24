# cmd/releasecheck

A minimal CLI wrapper around `internal/releasecheck`.

## Purpose

Parses release-metadata flags, invokes `releasecheck.Verify`, and prints a
human-readable summary (with per-artifact SHA-256 hashes) or a machine-readable
JSON report.

## Flags

`--root`, `--version`, `--commit`, `--date`, `--require-clean`,
`--require-changelog`, `--json`.

## Dependencies

- `internal/releasecheck` only.

## Design notes

- `run` is testable via injected arguments and uses the conventional 0/1/2 exit
  codes; context is signal-driven, matching the main command.
- `--version` here means the semantic release version, not "print and exit".

## Tests

- No dedicated test file; verification logic is covered by
  `internal/releasecheck`'s tests.
