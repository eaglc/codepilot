# internal/codingagent

The product layer that turns the generic Agent/LLM/tool stack into the "Coding
Agent". It owns the `Service` that manages Coding session lifecycle, builds
trusted prompts and untrusted repository context, applies patches, and projects a
secret-free, typed view of the durable Agent journal to the UI.

## Purpose

`codingagent` is the outermost business module. It binds a Coding session to an
Agent session, a workspace/worktree, a provider/model, and a permission mode; it
enforces security end-to-end (sensitive-path classification, deterministic secret
redaction, append-only session-scoped permission grants); and it projects a
`Snapshot` that is the UI's single authoritative state. It is isolated behind
ports (`AgentRunner`, `ToolFactory`, `PromptBuilder`, `SessionRepository`,
`WorkspaceController`, `EventSink`, `ProviderManager`) so generic Agent packages
never see paths, credentials, or runtime objects.

## Key types

- `Service` — `NewService`, `CreateSession`, `StartTurn`, `ResumeTurn`,
  `RecoverTurn`, `RecoverAutomatically`, `CancelTurn`, `Snapshot`, `ListSessions`,
  `SwitchSession`, `RenameSession`, `SetPermissionMode`, `ArchiveSession`,
  `ForkLane`, plus provider and workspace management methods.
- `SecurityPolicy` — sensitive paths, `ContainsSecret`, and text/JSON redaction.
- `WorkspaceManager` / `ConsistencyManager` — worktree validation/relocation and
  cross-repository diagnosis/repair.
- `AgentEventAdapter` — maps generic Agent events to the product event protocol.
- `ProjectSnapshot` / `Snapshot` / `TranscriptItem` — the secret-free product view.
- `Session`, `Workspace`, `Worktree`, `Event`, `PermissionGrant`, `PermissionMode`.

## Dependencies

- `internal/agent`, `internal/agent/session`, `internal/llm`, `internal/tool`,
  `internal/codingagent/workspace`.

## Design notes

- Defense-in-depth security: layered redaction, exact-scope grants with TTL and
  revocation, and automatic grant revocation on permission-mode change.
- Two-phase session creation via a durable `SessionCreationIntent`, and
  non-destructive `ConsistencyManager` repair.
- The package never forwards generic runtime objects across its API — everything
  is mapped through typed, secret-free DTOs.

## Tests

- `coding_e2e_test.go`, `consistency_test.go`, `event_test.go`,
  `permission_test.go`, `projection_test.go`, `provider_test.go`,
  `security_test.go`, `service_test.go`, `session_management_test.go`,
  `workspace_manager_test.go`.

## Subpackages

- `language` — read-only Go/Python/Node detection and allowlisted server profiles.
- `prompt` — trusted system prompt + untrusted AGENTS.md guidance.
- `workspace` — Git worktree resolution and ignore-aware file indexing.
- `lsp` — bounded, worktree-isolated language-server navigation.
- `tools` — the workspace-scoped tool set with security/artifact boundaries.
