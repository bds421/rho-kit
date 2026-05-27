# infra/secrets/gcpsm

## Purpose

GCP Secret Manager backend for `infra/secrets.Loader`. Wraps a
caller-constructed `secretmanager.Client`; the kit does NOT manage
the GCP credential lifecycle.

## Public API

- `New(api API, opts ...Option) *Loader`
- `WithProject(id string)` — required when Get receives a bare secret
  name; ignored when caller passes a fully-qualified `projects/P/secrets/S/versions/V` path
- `WithVersion(v string)` — defaults to `latest`
- `API` interface: minimal `AccessSecretVersion` surface

## Error mapping

| Upstream | Returned |
|---|---|
| gRPC `codes.NotFound` | `secrets.ErrSecretNotFound` |
| Other gRPC errors | `secrets.ErrLoaderUnavailable` (wrapped via `redact.WrapSentinel` + `redact.WrapError`) |
| Empty payload | descriptive error (non-retryable misconfig) |

## Bare vs fully-qualified names

`Get(ctx, "my-secret")` requires `WithProject` and the configured
default version. `Get(ctx, "projects/P/secrets/S/versions/3")` works
without `WithProject` and targets the explicit version — useful for
cross-project or version-pinned reads from a single Loader.

## See also

- `infra/secrets` — umbrella + `CachedLoader`.
- `crypto/envelope/gcpkms` — KEK wrapping via GCP KMS (different concern).
