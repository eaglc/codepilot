# internal/sessionstore/file

Durable on-disk persistence for Agent sessions, context summaries, and state
locking. Each session is a versioned `session.json` metadata file plus a single
append-only `journal.jsonl` of entries/records, rebuilt into a snapshot on load.

## Purpose

The file backend implements `agent/session.Repository` and
`contextmanager.SummaryStore`, and provides a cross-process exclusive state lease.
It also produces deterministic, content-addressed cold archives of the live
journal for audit purposes.

## Key types

- `Repository` — `NewRepository`, `OpenRepository`, `Create`, `Load`, `List`,
  `SetArchived`, `AppendEntry`, `AppendRecord`, `ForkLane`,
  `CreateJournalArchive` / `ListJournalArchives` / `LoadJournalArchive`,
  `LoadSummary` / `SaveSummary`.
- `StateLease` / `StateInspectionLease` — exclusive writer / read-only inspection lease.
- `LeaseOwner` / `LeaseInUseError` / `ErrStateInUse` — bounded, secret-free lock diagnostics.

## Dependencies

- `internal/agent/session`, `internal/contextmanager`.
- External: `github.com/gofrs/flock` for the OS file lock.

## Design notes

- Atomic JSON writes (temp file + `chmod 0600` + `Sync` + `os.Rename`) throughout.
- Content-addressed archives use `sha256` filenames and pinned gzip/tar metadata,
  so cold copies are deterministic and idempotent.
- A truncated final journal line is ignored and surfaced as a `RecoveryWarning`;
  `prepareJournal` repairs the tail before the next append.
- Journal appends are authoritative and synced before the separate metadata
  timestamp update. A crash in that narrow window can leave `UpdatedAt` stale,
  but cannot lose the entry or record; the next successful append refreshes it.
- Lock files are never deleted or treated as proof of ownership; a stale lock is
  left for the OS to release.

## Tests

- `repository_test.go`, `lease_test.go` — shared-sequence rebuild, truncated-tail
  repair, subprocess crash recovery, immutable archives, summary persistence, and
  cross-process lease exclusivity.
