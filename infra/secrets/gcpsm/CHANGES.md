# Changes

## Unreleased — v2.0

- Initial release. GCP Secret Manager backend for `secrets.Loader`.
- `WithProject(id)` + `WithVersion(v)` options; defaults `latest`.
- Bare names resolve under the configured project; fully-qualified
  `projects/P/secrets/S/versions/V` paths bypass project pinning.
- gRPC `codes.NotFound` → `ErrSecretNotFound`; other gRPC errors →
  `ErrLoaderUnavailable` (wrapped via `redact.WrapSentinel` +
  `redact.WrapError`).
- Caller-owned `secretmanager.Client` lifecycle.
- `Secret.Version` exposes the bare trailing version segment (e.g. `3`),
  not the full `projects/P/secrets/S/versions/N` resource path, matching
  the documented `secrets.Secret.Version` contract and the awssm/vaultkv
  backends.
