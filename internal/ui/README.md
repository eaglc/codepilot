# internal/ui

The full-screen terminal front end (Bubble Tea v2) for CodePilot.

## Purpose

Renders a single-column, command-line-style conversation surface with optional
Markdown, inline tool activity and diffs, a status/metrics line, and a
blinking-cursor composer. It drives the whole product through one `Client`
interface and consumes streaming product events from `EventBridge`, presenting
modal pages (provider, session, workspace, permissions, help) over the
conversation. It owns command completion, terminal clipboard copying, approval,
and crash-recovery prompting.

## Key types

- `Model` — the Bubble Tea model; `NewModel`, `Init`, `Update`, `View`.
- `Client` — the product boundary interface (session, turn, provider, workspace).
- `EventBridge` — bounded product-event queue; `NewEventBridge`, `PublishCodingEvent`, `Events`, `Close`.
- `Run(ctx, model, input, output)` — starts Bubble Tea with process-owned streams.
- `Option` / `WithProviderIssue` — construction options.

## Dependencies

- `internal/codingagent` only. Third-party: Bubble Tea v2, Lipgloss v2, glamour,
  `charmbracelet/x/ansi`.

## Design notes

- The UI depends only on `Client` (an interface) and `codingagent` DTOs; an
  architecture test forbids importing `llm`, `provider`, `agent`, `sessionstore`,
  or Eino.
- `EventBridge` is a bounded queue with backpressure and adjacent-delta merging;
  a generation counter rejects stale async results after session/workspace switches.
- Streaming and durable assistant text share one rendering path, so completing a
  turn does not unexpectedly change its layout. Markdown remains enabled by
  default and can be toggled with Alt+M or `/md`.
- The command registry is the single source for completion, dispatch, and help.
  `/clear` creates the only kind of clean session; its first prompt is silently
  reused as a bounded title while the turn continues independently.
- Clipboard copying uses Bubble Tea's OSC52 support and only accepts the
  product-safe transcript projection. Credentials are zeroed after use and
  masked on screen; error text is redacted and bounded.

## Tests

- `architecture_test.go`, `eventbridge_test.go`, `model_test.go` —
  layout/keybinding/picker/approval/recovery behavior, delta merging,
  backpressure, and the import-boundary guard.
