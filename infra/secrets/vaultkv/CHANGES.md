# Changes

## Unreleased — v2.0

- Initial release. HashiCorp Vault KV v2 backend for `secrets.Loader`.
- `WithField(name)` option; default `"value"`.
- 404 / "secret not found" / nil-data → `ErrSecretNotFound`; other
  errors → `ErrLoaderUnavailable` (wrapped via `redact.WrapSentinel`
  + `redact.WrapError`).
- Missing field / non-string field → descriptive error (non-retryable
  misconfig).
- KV v2 only (versioned mounts). KV v1 is out of scope.
- Caller-owned `*api.Client` lifecycle (kit doesn't manage AppRole /
  Kubernetes-auth / token-renewal).
