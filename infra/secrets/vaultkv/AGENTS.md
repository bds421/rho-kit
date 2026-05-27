# infra/secrets/vaultkv

## Purpose

HashiCorp Vault KV v2 backend for `infra/secrets.Loader`. Wraps a
caller-constructed `*api.Client`; the kit does NOT manage the Vault
auth lifecycle (AppRole / Kubernetes auth / token renewal).

## Public API

- `New(api API, opts ...Option) *Loader`
- `WithField(name string)` — JSON field to read from each KV entry;
  defaults to `"value"`. Vault KV stores arbitrary JSON, so the secret
  is one named field of that JSON object rather than the whole blob.
- `API` interface: minimal `Get(ctx, path) (*api.KVSecret, error)` —
  satisfied by `vaultClient.KVv2(mount).Get` and test stubs

## Error mapping

| Upstream | Returned |
|---|---|
| `*api.ResponseError` with StatusCode 404 | `secrets.ErrSecretNotFound` |
| Errors matching "secret not found" / "Code: 404" string | `secrets.ErrSecretNotFound` |
| Nil response or nil Data | `secrets.ErrSecretNotFound` |
| Missing field / wrong type | descriptive error (non-retryable misconfig) |
| Other (transport / auth) | `secrets.ErrLoaderUnavailable` (wrapped via `redact.WrapSentinel` + `redact.WrapError`) |

## KV v2 only

This backend targets Vault KV v2 (versioned mounts). KV v1 mounts use a
different path and response shape and are not supported.

## See also

- `infra/secrets` — umbrella + `CachedLoader`.
- `crypto/envelope/vaulttransit` — Vault Transit envelope KEK adapter
  (different concern).
