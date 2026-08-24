# internal/agent/session

Durable, provider-neutral Agent session domain model. It defines the context tree
of `Entry` nodes, the append-only `Record` journal, the `Repository` persistence
contract, and crash-recovery analysis.

## Purpose

Entries form the model context tree; Records store execution facts (operation,
step, tool, approval, usage, compaction, lane) that don't necessarily enter the
model context but are needed for recovery and audit. The package is deliberately
unaware of workspaces, Git, or the TUI — it only knows how to save enough data to
rebuild model context and decide a recovery strategy.

## Key types

- `ID` / `EntryID` / `RecordID` / `RunID` / `Lane` / `MainLane`.
- `Entry` — tagged union (`EntryMessage`, `EntryModelChange`, `EntryCompaction`,
  `EntryBranchSummary`, `EntryCustomMessage`, …) with `Validate()`.
- `Record` — tagged union of 14 record kinds with `Validate()`.
- `Repository` — `Create`, `Load`, `List`, `SetArchived`, `AppendEntry`, `AppendRecord`, `ForkLane`.
- `Snapshot` / `Metadata` — the rebuildable in-memory view and session metadata.
- `MemoryRepository` — in-memory `Repository` for tests.
- `AnalyzeRecovery` / `BuildRecoveryPlan` / `RecoveryPlan` / `RecoveryAction` —
  pure, restart-safe recovery analysis over a `Snapshot`.
- `BranchEntries` / `JournalArchiver` — branch walk and immutable archive contract.

## Dependencies

- `internal/llm` only.

## Design notes

- `Validate()` requires storage-assigned fields (`Sequence`, `Timestamp`, `Lane`,
  `ParentID`) to be empty before append, preventing repository misuse.
- `MemoryRepository` shares a single monotonic `Sequence` across entries and
  records and deep-clones on read/write.
- Recovery analysis is a pure function over `Snapshot`, rebuilt after every
  persisted action, so it never executes tools itself.

## Tests

- `memory_test.go` — sequence/parent assignment, recovery analysis, replay-policy
  planning, and durable-write-gap classification.
