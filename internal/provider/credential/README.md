# internal/provider/credential

Credential resolution from protected or fallback sources, kept separate from
profiles and session data.

## Purpose

Provides an allow-listed environment store, an OS-keyring store, and a chain that
combines them, so provider secrets are never persisted alongside profiles or
session data.

## Key types

- `EnvironmentStore` — read-only, allow-listed environment mapping.
- `KeyringStore` — OS keyring backend.
- `ChainStore` — primary credential repository with fallback stores.

## Dependencies

- `internal/provider`; external `github.com/zalando/go-keyring`.

## Design notes

- The environment fallback is explicitly allow-listed (reference → env-name map)
  and read-only; the keyring backend is abstracted for testability.
- `ChainStore.LoadCredential` tolerates an unavailable keyring when a later
  fallback resolves the secret.

## Tests

- `store_test.go` — environment mapping, keyring save/load/delete, error
  classification, and chain fallback.
