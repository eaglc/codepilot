# internal/architecture

Executable architecture guardrails. This is a test-only package with no
non-test source: its tests enforce the layer boundaries and release invariants
documented in `docs/architecture/modular-architecture-migration.md`.

## What it enforces

- `dependencies_test.go` — parses every non-test `.go` file under `internal/` and
  rejects any import of `_legacy`, plus any internal import that violates the
  layered dependency direction (e.g. `llm` importing `agent`, `ui` importing
  `provider`/`llm`/`agent`/`sessionstore`). The composition root `app` is the one
  deliberate exemption.
- `ci_test.go` — asserts the CI workflow keeps the three native platforms, the
  offline/race/vet/build gates, and never enables live Provider tests.
- `release_test.go` — asserts the GoReleaser and release-workflow configuration
  keeps identity, packaging, and supply-chain gates (reproducible flags, SBOM,
  tag-only release, no credential consumption).

## Purpose

The dependency-direction rule lives here as code, so a future refactor that
re-introduces a forbidden import fails `go test` instead of drifting silently.

## Tests

The package is itself the test suite; it has no runtime surface.
