# internal/provider/file

Secret-free Provider profile persistence.

## Purpose

Persists `provider.Profile` values in a versioned JSON file under the config
directory, with atomic writes and strict validation on both read and write.

## Key types

- `Repository` — `NewRepository(root)`.

## Dependencies

- `internal/provider` only.

## Design notes

- Atomic write path (temp file + `Chmod(0600)` + `Sync` + `Close` + `os.Rename`).
- Strict decoding (`DisallowUnknownFields`, size cap, single-JSON-value check,
  format-version check) and full record validation on read and save.
- Credentials are never persisted — profiles carry only a credential reference.

## Tests

- `repository_test.go` — persistence/update/ordering, corrupt/unknown-field
  rejection, and a dedicated assertion that credentials are never stored.
