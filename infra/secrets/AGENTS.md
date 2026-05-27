# infra/secrets

## Purpose

Pluggable secret-loader umbrella. The `Loader` interface, the TTL
cached wrapper (`CachedLoader` with stale-while-revalidate + single-flight
+ stale-on-error fallback), and the `NewRotatingProvider` callback
adapter for SDK credential hooks. Backends live in sibling go modules
so consumers pay only for what they import.

## Public API

- `Loader` interface: `Get(ctx, key) (Secret, error)`
- `Secret{Value *secret.String, Version string, FetchedAt time.Time}`
- `MakeSecret(b []byte, version string) Secret`
- `NewCachedLoader(inner Loader, opts ...CacheOption) (*CachedLoader, error)`
- `CachedLoader.Get` / `.Invalidate(key)`
- `NewRotatingProvider(loader Loader, key string, timeout time.Duration) func() (string, error)`
- Errors: `ErrSecretNotFound`, `ErrLoaderUnavailable`
- Cache options: `WithCacheTTL`, `WithCacheRefreshAfter`, `WithCacheMaxStale`,
  `WithCacheLogger`, `WithCacheMetricsRegisterer`

## Backends (each own go module)

| Backend | Path | Upstream SDK |
|---|---|---|
| AWS Secrets Manager | `infra/secrets/awssm` | aws-sdk-go-v2/service/secretsmanager |
| GCP Secret Manager  | `infra/secrets/gcpsm` | cloud.google.com/go/secretmanager |
| HashiCorp Vault KV2 | `infra/secrets/vaultkv` | hashicorp/vault/api |

## Cache semantics

```
hit, not stale        → return cached, no fetch
hit, refresh-due      → return cached, spawn background refresh
hit, expired          → single-flight foreground fetch;
                        on ErrLoaderUnavailable AND within MaxStale →
                        return stale + warn-log + counter
miss                  → single-flight foreground fetch
```

## Metrics (Prometheus)

- `secrets_cache_hits_total`
- `secrets_cache_misses_total`
- `secrets_cache_background_refreshes_total`
- `secrets_cache_background_refresh_errors_total`
- `secrets_cache_stale_fallbacks_total`
- `secrets_cache_stale_exceeded_total`

## Tests

`go test -race ./...`. Umbrella tests cover: hit deduplication, not-found
surfaced, stale fallback within MaxStale, stale exceeded surfaces error,
RefreshAfter < TTL guard, Invalidate, singleflight coalescing, rotating
provider happy path, panic guards.

Each backend has unit tests against a stubbed upstream API (no live
cloud / Vault dependency for the unit tier).

## See also

- `crypto/envelope` — KEK/DEK wrapping. Distinct concern from
  secret-loading: a KEK might be loaded by THIS package and then
  used by envelope to wrap DEKs.
- `security/jwtutil` — JWKS fetch has a similar caching shape but is
  baked into the JWT verifier rather than going through this Loader.
