# infra/secrets/awssm

## Purpose

AWS Secrets Manager backend for `infra/secrets.Loader`. Wraps a
caller-constructed `*secretsmanager.Client`; the kit does NOT manage
the AWS session lifecycle.

## Public API

- `New(api API, opts ...Option) *Loader`
- `WithVersionStage(string)` — defaults to `AWSCURRENT`
- `API` interface: minimal `GetSecretValue` surface — `*secretsmanager.Client`
  satisfies it; tests stub it

## Error mapping

| Upstream | Returned |
|---|---|
| `*smtypes.ResourceNotFoundException` | `secrets.ErrSecretNotFound` |
| Other (transport / auth / quota) | `secrets.ErrLoaderUnavailable` (wrapped via `redact.WrapSentinel` + `redact.WrapError`) |
| Empty payload (no SecretString or SecretBinary) | descriptive error (non-retryable misconfig) |

## See also

- `infra/secrets` — umbrella + `CachedLoader` (recommended in front of this backend; Secrets Manager has tight RPS limits).
- `crypto/envelope/awskms` — different concern (KEK wrapping via AWS KMS). Use that for envelope DEK encryption; this for secrets-as-values.
