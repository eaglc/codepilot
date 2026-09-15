# CodePilot

CodePilot is a terminal AI coding agent for working with a local Git repository. It provides persistent conversations, streaming responses, bounded workspace tools, change previews, and approval controls without exposing an unrestricted shell to the model.

## Features

- Full-screen terminal conversation UI with Markdown or plain-text responses.
- Persistent sessions with create, switch, fork, rename, archive, and restore support.
- OpenAI, DeepSeek, Ollama, and custom OpenAI-compatible providers.
- Safe file reading, search, exact or whole-file editing, multi-file patches, Git inspection, and approved checks.
- Explicit `/plan` tasks plus Agent-suggested Plan entry with a user confirmation boundary and read-only planning tools.
- Bounded parallel read-only child Agents for independent Plan explorations and approved Explore/Validate/Review Workflows.
- Isolated parallel Implement child Agents with exact ChangeSet approval, serial integration, and combined validation/review.
- Trusted adaptive execution recommendations with visible rationale, expected Agent count, user override, and versioned quality gates.
- `read-only`, `ask`, and `auto-edit` permission modes.
- Optional Go and Python language-server navigation.

## Requirements

- Go 1.26 or newer
- Git on `PATH`
- A local Git worktree
- A supported provider or a reachable Ollama installation

Language servers such as `gopls` and Pyright are optional.

## Build and run

```powershell
go test ./...
go build -o codepilot.exe ./cmd/codepilot
.\codepilot.exe
```

Open another repository explicitly:

```powershell
.\codepilot.exe --workspace H:\path\to\repository
```

On first use, confirm that the worktree is trusted and select a provider and model. Credentials are stored in the operating-system keyring when available; otherwise they remain in process memory only.

Project documentation is indexed in [docs/README.md](docs/README.md). Release packaging and upgrade instructions are in [docs/release-and-upgrade.md](docs/release-and-upgrade.md).

## TUI basics

- Enter sends a prompt; Alt+Enter inserts a newline.
- Ctrl+C cancels the active turn; Ctrl+D saves and exits while idle.
- Type `/` to open and filter the command menu.
- Tab selects messages and tool results when the input is empty; `Y` copies the selection.
- Alt+M or `/md` switches between Markdown and plain text.
- Approval choices are displayed inline in the conversation.

Common commands:

```text
/provider
/permissions
/session
/workspace
/plan [request]
/rename <title>
/fork
/clear
/md [on|off]
/help
/exit
```

`/fork` opens the conversation history so no internal entry ID is required. `/clear` starts a new persisted session without deleting the previous session or changing worktree files.

Plan mode is scoped to one task. An Agent suggestion offers **Enter Plan mode**, **Continue Direct**, or **Cancel task**; it never switches modes or grants write permission without the user's choice. Every submitted Plan is an immutable version bound to an exact workspace baseline and digest. Before approval and execution, CodePilot distinguishes unrelated workspace drift from changes to Plan-relevant files or worktree identity; material drift returns to read-only planning and requires a new version. During execution, a material assumption, scope, risk, strategy, or workspace deviation pauses at an explicit **Return to Plan mode / Continue approved Plan / Cancel task** boundary. `--disable-plan-suggestions` disables new Agent suggestions while preserving explicit `/plan`; `--disable-plan-mode` disables both new Plan entry paths while keeping previously persisted Plan decisions recoverable.

Parallel read-only child Agents use the shared worktree only for Explore, Validate, and Review. An approved isolated-write Workflow may instead run independent Implement nodes in CodePilot-managed Git worktrees pinned to the Plan commit. Each child produces a content-addressed ChangeSet; the coordinator verifies target digests, shows the exact diff for one-time approval, integrates ChangeSets serially, and then runs combined Validate and Review nodes on the active worktree. Target drift or conflicts stop integration without deleting the child artifact or managed worktree.

`--max-parallel-agents` sets the process-wide child concurrency cap (default 8), while each compiled parallel Workflow currently uses at most two concurrent nodes. `--disable-parallel-subagents` blocks all new parallel delegation; `--disable-parallel-write-subagents` independently blocks new isolated-write Workflows while preserving durable recovery. `--disable-subagents` disables all new child delegation.

For each new executable Plan, CodePilot records the model proposal but independently evaluates it against a versioned task-shape baseline, enabled capabilities, dependency and path-conflict rules, the clean Git baseline requirement, and hard Agent limits. The Plan and approval card show the resulting recommendation, concise reason, expected Agent count, and independent workstreams; users can always choose Direct single-Agent execution. `--prefer-single-agent` makes that the product recommendation for new Plans, and `--disable-adaptive-strategy` restores proposal-only selection while leaving existing Plan data readable. Snapshot metrics compare recommendations and approved choices, and group completion, retries, replans, elapsed time, token use, and cost by execution strategy.

## Permissions and safety

- `read-only` allows inspection only.
- `ask` requests approval before edits and project checks.
- `auto-edit` allows validated edits but still asks before checks.

File operations are confined to the trusted worktree. Sensitive paths and recognized secret values are protected, tool output is bounded, and project checks use fixed detected commands with time and output limits.

## Local data

Configuration and session state use platform-specific user directories. For isolated development:

```powershell
.\codepilot.exe --config-dir H:\temp\codepilot-config --state-dir H:\temp\codepilot-state
```

Only one CodePilot process may own a state directory at a time.
