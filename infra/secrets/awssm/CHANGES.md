# Changes

## Unreleased — v2.0

- Initial release. AWS Secrets Manager backend for `secrets.Loader`.
- `WithVersionStage(string)` option; default `AWSCURRENT`.
- `ResourceNotFoundException` → `ErrSecretNotFound`; other errors →
  `ErrLoaderUnavailable` (wrapped via `redact.WrapSentinel` +
  `redact.WrapError` so SDK error text is redacted on log paths).
- `SecretString` AND `SecretBinary` payload paths both supported.
- Caller-owned `*secretsmanager.Client` lifecycle (the kit doesn't
  open or close AWS sessions).
