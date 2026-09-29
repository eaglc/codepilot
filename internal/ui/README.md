# internal/ui

The full-screen terminal front end (Bubble Tea v2) for CodePilot.

## Purpose

Renders a single-column, command-line-style conversation surface with optional
Markdown, inline tool activity and diffs, a compact product-state Header, a
durable Task/Plan/Workflow/Agent hierarchy, and a 2–8 row
terminal-cell-aware Composer. It drives the whole product through one `Client`
interface and consumes streaming product events from `EventBridge`, presenting
modal pages (provider, session, workspace, permissions, instructions, context, help) over the
conversation. It owns command completion, terminal clipboard copying, approval,
and crash-recovery prompting.

## Key types

- `Model` — the Bubble Tea model; `NewModel`, `Init`, `Update`, `View`.
- `Client` — the product boundary interface (session, turn, provider, workspace).
- `EventBridge` — bounded product-event queue; `NewEventBridge`, `PublishCodingEvent`, `Events`, `Close`.
- `Run(ctx, model, input, output)` — starts Bubble Tea with process-owned streams.
- `ComposerKeymap` / `WithComposerKeymap` — configurable submit and newline bindings; defaults are Enter and Alt+Enter.
- `Option` / `WithProviderIssue` — construction options.

## Dependencies

- `internal/codingagent` only. Third-party: Bubble Tea v2, Lipgloss v2, glamour,
  `charmbracelet/x/ansi`, and `rivo/uniseg`.

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
  `/instructions [path]` loads content-free instruction provenance on demand
  through the product API and computes the effective root-to-leaf scope chain.
  `/context` loads the latest content-free prepared-request observation and
  keeps Provider-exact totals visually distinct from local category estimates.
  `/status` opens a dedicated Task/Plan/Workflow/Child Agent page sourced only
  from `codingagent.Snapshot`; wide terminals show the same nodes inline with
  collapsible scope, evidence, ChangeSet, and check details.
  `/clear` creates the only kind of clean session; its first prompt is silently
  reused as a bounded title while the turn continues independently.
- The Composer lays out Unicode grapheme clusters in terminal cells, soft-wraps
  long input, keeps 2–8 visible rows, and moves vertically within visual lines
  before entering sent-input history. Unsent drafts are isolated by Session and
  use a separate model from durable sent history.
- The Header automatically removes lower-priority badges as width shrinks while
  preserving required user action and execution mode. Narrow terminals move the
  task tree to `/status`; selecting a Diff and pressing `D` opens a full-screen
  Diff that closes with `D`, `q`, or Esc.
- Provider credentials remain in the Provider picker's masked, zeroed buffer;
  that buffer is never shared with Composer drafts or sent-input history.
- Clipboard copying uses Bubble Tea's OSC52 support and only accepts the
  product-safe transcript projection. Credentials are zeroed after use and
  masked on screen; error text is redacted and bounded.

## Tests

- `architecture_test.go`, `eventbridge_test.go`, `model_test.go` —
  layout/keybinding/picker/approval/recovery behavior, delta merging,
  backpressure, and the import-boundary guard.
