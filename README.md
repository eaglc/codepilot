# CodePilot

CodePilot is a terminal AI coding agent for working with a local Git repository. It provides persistent conversations, streaming responses, bounded workspace tools, change previews, and approval controls without exposing an unrestricted shell to the model.

## Features

- Full-screen terminal conversation UI with Markdown or plain-text responses.
- Persistent sessions with create, switch, fork, rename, archive, and restore support.
- OpenAI, DeepSeek, Ollama, and custom OpenAI-compatible providers.
- Safe file reading, search, exact or whole-file editing, multi-file patches, Git inspection, and approved checks.
- Explicit `/plan` tasks plus Agent-suggested Plan entry with a user confirmation boundary and read-only planning tools.
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

Plan mode is scoped to one task. An Agent suggestion offers **Enter Plan mode**, **Continue Direct**, or **Cancel task**; it never switches modes or grants write permission without the user's choice. `--disable-plan-suggestions` disables new Agent suggestions while preserving explicit `/plan`; `--disable-plan-mode` disables both new Plan entry paths while keeping previously persisted Plan decisions recoverable.

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
