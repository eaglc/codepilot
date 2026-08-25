# internal/contextmanager

Context selection and compaction. It selects and compacts provider-neutral model
context to fit token budgets, summarizing old complete turns into a rolling
summary while keeping a recent full tail, with hard-limit trimming and safe
fallbacks.

## Purpose

A `Manager` runs an ordered pipeline of `Strategy` implementations. The concrete
`CompactionStrategy` produces a `rolling-summary/v5` compaction; a
`ModelSummarizer` generates summaries via a (fixed or primary) model; and
`ByteTokenizer` provides a conservative bootstrap token estimate. All of it is
provider-neutral — it depends only on `llm` and injects `Summarizer` /
`SummaryStore` / `Tokenizer` / `TextSanitizer` seams.

## Key types

- `Manager` / `Strategy` — pipeline and its stages.
- `CompactionStrategy` — rolling summary + tail retention + hard-limit trim.
- `Policy` / `Budget` / `BudgetForModel` — token budgets derived from model capability.
- `Summarizer` / `ModelSummarizer` — summary generation seam.
- `SummaryStore` / `MemorySummaryStore` — summary cache seam (persisted by `sessionstore/file`).
- `Summary` / `SummaryFact` — durable, provider-neutral summary and its facts.
- `ExtractSummaryFacts` / `ValidateSummaryFacts` / `FormatSummaryInput` — summary fact safety.
- `Tokenizer` / `ByteTokenizer` — token estimation seam.
- `CurrentTurnTooLargeError` — returned when the current turn alone exceeds the budget.

## Dependencies

- `internal/llm` only.

## Design notes

- Summaries are re-injected as `RoleUser` "untrusted derived context", never into
  the trusted system prompt, to defend against prompt injection.
- Fact-consistency checks require tool names, artifact `sha256:` refs, and changed
  file paths to survive verbatim before a summary is reused.
- Durable `EntryCompaction` summaries seed the next rolling generation; only
  messages after their coverage boundary are reconsidered.
- Oversized old history is split on complete-turn boundaries, summarized in
  cached chunks, and merged hierarchically before the final summary is durable.
- Request-scoped ephemeral context remains visible to the primary model but is
  excluded from durable summary sources.
- Any failure (summary generation, sanitizer, cache) falls back to whole-turn
  `safeTrim` with a recorded `Degradation`, never a hard error.

## Tests

- `compaction_test.go` — tool-aware summaries reused across model switch,
  sanitization, hard-limit whole-turn trimming, budget override, fact
  consistency, and golden `FormatSummaryInput` fixtures.
