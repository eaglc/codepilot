# internal/codingagent/workspace

Git worktree discovery and ignore-aware file indexing for the Coding Agent.

## Purpose

Resolves and validates a user-selected Git worktree without mutating it, derives
a stable repository identity fingerprint, and indexes tracked + untracked
non-ignored files using Git's own exclude engine. It has no internal
dependencies and shells out only to the `git` binary.

## Key types

- `IndexFiles(ctx, root, start, options)` — bounded Git-aware file enumeration.
- `FileIndexOptions` — `MaxFiles` and `Include` filter.
- `ResolvedWorktree` — `Root`, `GitDir`, `GitCommonDir`, `RepositoryFingerprint`.
- `ResolveWorktree(ctx, path)` — validates one worktree and computes its fingerprint.
- `VerifyRepositoryFingerprint(ctx, root, fingerprint)` — confirms the anchor commit exists.

## Dependencies

- None. Standard library + external `git` binary.

## Design notes

- The fingerprint anchors on the root commit object (`git-anchor-v1:…:…`), so new
  commits/branches/merges do not change identity; empty repos get an empty identity.
- `IndexFiles` uses `git ls-files --cached --others --exclude-standard -z` to
  respect `.gitignore` without reimplementing it, and enforces path, timeout, and
  file-count caps.

## Tests

- `files_test.go`, `locator_test.go` — `.gitignore`/dependency-boundary respect,
  truncation reporting, and stable/empty-repo fingerprint behavior.
