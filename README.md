# CodePilot

CodePilot is a terminal-based AI software-engineering agent for understanding and improving a local Git worktree. It keeps persistent sessions, shows code changes, asks before sensitive actions, and uses bounded workspace tools instead of an unrestricted shell.

## Current capabilities

- Full-screen Conversation and Diff interface with narrow-terminal support.
- Persistent sessions: create, list, switch, rename, archive, and restore.
- OpenAI, DeepSeek, Ollama, and custom OpenAI-compatible providers.
- Go and Python project detection, safe file/search/diff tools, patch application, and approved checks.
- Permission modes: `read-only`, `ask`, and `auto-edit`.
- Streaming model output, cancellation, approval prompts, and session recovery warnings.
- Optional `gopls`, `pyright-langserver`, or `basedpyright-langserver` navigation with safe text-tool fallback.

## Prerequisites

- Go 1.26 or newer.
- Git available on `PATH`.
- A Git worktree to open.
- For real agent turns, one supported provider:
  - an OpenAI or DeepSeek API key;
  - a reachable Ollama installation; or
  - an OpenAI-compatible endpoint, model ID, and API key.

`gopls` and Pyright are optional. CodePilot starts them only when navigation is requested, after applying its approval policy. Missing language servers do not prevent normal search and file-reading tools from working.

## Build

From the repository root:

```powershell
go test ./... -count=1 -timeout=120s
go build -o codepilot.exe ./cmd/codepilot
```

On macOS or Linux, the equivalent binary command is:

```bash
go build -o codepilot ./cmd/codepilot
```

To embed a release version:

```powershell
go build -ldflags "-X main.version=0.1.0" -o codepilot.exe ./cmd/codepilot
```

## Run

Start CodePilot from a Git worktree:

```powershell
H:\path\to\codepilot.exe
```

Or select a worktree explicitly:

```powershell
H:\path\to\codepilot.exe --workspace H:\path\to\repository
```

On first use, CodePilot displays the normalized worktree path and asks for trust confirmation before creating a session. For an explicitly controlled non-interactive environment, `--trust-workspace` skips that prompt.

If the active session has no provider, the Provider Picker opens automatically. A brand-new worktree reuses the most recently validated model when its credential is still available; otherwise the picker opens:

1. Use Up/Down and Enter to select a configured model or add a provider. Configured models and provider setup choices are shown in separate sections, duplicate provider/model entries are collapsed, and the active model is preselected. Selecting a configured model switches it directly without opening a second model list.
2. Enter the requested endpoint, model, or API key. API keys are masked.
3. CodePilot checks endpoint and credential access through the model catalog without spending model tokens.
4. Select a returned model; CodePilot then runs one minimal tool-calling probe and activates it only after that model passes.

DeepSeek V4 is probed in its default thinking mode without sending `tool_choice`, because that mode supports tool calls but rejects the OpenAI-compatible tool-selection field.

Credentials are stored in the operating-system keyring when available. If the keyring is unavailable, the credential remains only in process memory and must be entered again after restart.

## Using the TUI

- Type a request and press Enter to start an Agent turn.
- Use Alt+Enter to insert a newline.
- Use Ctrl+C to cancel an active turn.
- Use Ctrl+D while idle to save and exit.
- Use Tab to switch Conversation and Diff focus.
- Approval prompts accept `Y` for once, `S` for the matching action during this session, and `N` or Esc to deny.
- Type `/` to open the command menu. Commands with subcommands are expanded into executable choices such as `/session list` and `/permissions ask`; continue typing to filter, then use Up/Down and Enter or Tab to insert a command.
- Type `@` at the start of the current input token to search Git-visible workspace files and inferred directories. Selecting a directory keeps completion open for deeper navigation.

Available commands:

```text
/model
/permissions [read-only|ask|auto-edit]
/session create [TITLE]
/session list [--all]
/session switch ID
/session rename NAME
/session archive
/workspace open PATH
/workspace list
/status
/diff [proposed|session|workspace]
/clear
/help
/exit
```

`/clear` starts a new persisted session with an empty conversation while preserving the previous session and all worktree files.

`/session new` remains available as an alias for `/session create`.

## Permission behavior

- `read-only`: permits inspection and rejects patches and project execution.
- `ask`: prompts before patches and checks.
- `auto-edit`: permits validated patches but still prompts before running project checks.

CodePilot never exposes an arbitrary shell tool to the model. Check commands come from fixed language strategies, use explicit executables and arguments, run in the active worktree, and have time and output limits.

## Local data

Configuration and session state use platform-specific user directories. Override them for development or isolated testing:

```powershell
.\codepilot.exe --config-dir H:\temp\codepilot-config --state-dir H:\temp\codepilot-state
```

Only one CodePilot process may own a state directory at a time.

## Verification scope

The normal test suite is offline and uses scripted models; it verifies the Session, Agent, tools, persistence, TUI interaction, application composition, and startup/exit lifecycle without sending real provider requests. Provider availability and answer quality still depend on the provider, selected model, network, repository, and the clarity of the user request.
