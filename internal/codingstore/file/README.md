# internal/codingstore/file

Durable on-disk backend for Coding product metadata (workspaces, worktrees,
sessions, creation intents) and content-addressed artifacts. Every record is a
versioned JSON envelope in a private directory tree with atomic write-then-rename
and immutable-identity checks.

## Purpose

Implements the `codingagent.WorkspaceRepository`, session repository, and
`ArtifactStore`/`ArtifactReader` contracts over the filesystem. Artifacts are
stored as `sha256:`-addressed blobs separate from the session journal.

## Key types

- `Repository` — `NewRepository`, `OpenRepository`, `SaveWorkspace`/`LoadWorkspace`/
  `ListWorkspaces`, `SaveWorktree`/`RelocateWorktree`/`LoadWorktree`/`ListWorktrees`,
  `CreateSession`/`LoadSession`/`ListSessions`/`SaveSession`,
  `BeginSessionCreation`/`CompleteSessionCreation`/`ListSessionCreationIntents`,
  `SaveArtifact`/`LoadArtifact`.

## Dependencies

- `internal/codingagent` only.

## Design notes

- Content-addressed artifacts: `sha256:` digest, dedupe on re-save, size+digest
  re-verification on load, `0600` permissions.
- Envelope versioning with `DisallowUnknownFields` and a single-JSON-value guard.
- Immutable-identity enforcement on workspace/worktree/session updates, and a
  compare-and-swap `RelocateWorktree`.
- Worktree relocation persists a transaction intent before updating its two
  metadata files; opening the repository verifies and completes interrupted
  relocations idempotently.

## Tests

- `artifact_test.go`, `repository_test.go` — content-addressed artifacts,
  persistence across restart, CAS relocation, and session-creation intents.
