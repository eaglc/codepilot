# internal/codingstore/memory

Ephemeral in-memory implementation of the Coding product repository surface,
backed by maps guarded by a `sync.RWMutex`, with defensive deep-copying of session
data.

## Purpose

The lightweight backend for tests and short-lived processes. Method signatures
mirror the `file` backend (except artifacts) so the two are interchangeable at
call sites.

## Key types

- `Repository` — `NewRepository`, workspace/worktree/session CRUD,
  `RelocateWorktree`, and session-creation-intent lifecycle.

## Dependencies

- `internal/codingagent` only.

## Design notes

- `cloneSession` deep-copies permission grants and sensitive paths on every
  write and read, preventing aliasing of stored state.
- Does not implement artifact storage (`SaveArtifact`/`LoadArtifact`), so it is
  only a partial parallel of the `file` backend.

## Tests

- `repository_test.go` — defensive copying of the session permission audit.
