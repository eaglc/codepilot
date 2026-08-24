# internal/releasecheck

Release-readiness preflight for deterministic builds.

## Purpose

Validates the inputs and repository state required to cut a release: metadata
(semver, commit SHA, build date), a clean Git tree matching HEAD, an optional
CHANGELOG entry, and reproducible builds — compiling every supported target twice
and comparing SHA-256 digests, then running the native binary's `--version`.

## Key types

- `Metadata` — `Version`, `Commit`, `BuildDate`; `Validate()`.
- `Target` / `Targets()` — the six-target release matrix (linux/windows/darwin × amd64/arm64).
- `Artifact` / `Report` — per-target digest and the result of a successful preflight.
- `Options` — `Root`, `Metadata`, `RequireClean`, `RequireChangelog`.
- `Verify(ctx, Options) (Report, error)`.

## Dependencies

- None. Standard library only.

## Design notes

- Strict validation: semver without a leading `v`, full hex commit, RFC3339 date.
- Reproducibility uses deterministic flags (`-trimpath`, `-buildvcs=false`,
  `-buildid=`, `-s -w`, `CGO_ENABLED=0`) and two separate build directories.
- `buildEnvironment` replaces (not appends) `CGO_ENABLED`/`GOOS`/`GOARCH`.

## Tests

- `check_test.go` — metadata validation, the release matrix, and target-variable
  environment construction.
