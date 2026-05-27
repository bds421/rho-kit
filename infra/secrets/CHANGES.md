# Changes

## Unreleased — v2.0

- Initial release of the `secrets` umbrella + three backends.
- Loader interface, CachedLoader (TTL + stale-while-revalidate +
  single-flight + stale-on-error), RotatingProvider callback adapter.
- Backends: `infra/secrets/awssm` (AWS Secrets Manager),
  `infra/secrets/gcpsm` (GCP Secret Manager),
  `infra/secrets/vaultkv` (HashiCorp Vault KV v2).
- Cache metrics: hits, misses, refreshes, refresh-errors,
  stale-fallbacks, stale-exceeded.
- Distinct from `crypto/envelope/*` (KEK wrapping); this is for
  secrets-as-values (DB passwords, API tokens, signing keys).
