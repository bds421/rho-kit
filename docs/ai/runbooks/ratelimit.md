# Rate-Limit Alerts

## Alerts

- `RhoKitRateLimitSpike`
- `RhoKitRateLimitDegraded`
- `RhoKitRateLimitUnavailable`

## First Checks

1. Open the HTTP rate limits dashboard and filter to the alerting
   `namespace`, `service`, and `limiter`.
2. Compare `limited` against `allowed` in
   `http_ratelimit_decisions_total`.
3. Check whether the limiter is `ip` or `keyed`. Keyed limiters often point
   to API keys, user IDs, or application-level subjects; do not add the raw
   key value as a metric label.
4. Check `degraded_passthrough`, `degraded_rejected`, and `unavailable`
   outcomes before tuning limits. Dependency or key-extractor failures can
   look like traffic spikes.

## Mitigation

- For real abuse, keep the limit and block the offending source at the edge.
- For a shared-key accident, fix the key extractor so each actor has a stable
  low-cardinality application key.
- For `degraded_passthrough`, restore the backing health dependency; requests
  are bypassing rate limiting.
- For `degraded_rejected` or `unavailable`, check limiter construction,
  cleanup-loop errors, and key-extractor panics. User-visible 503s are likely.

## Metric Contract

- `http_ratelimit_decisions_total{limiter,kind,outcome}`
- `http_ratelimit_retry_after_seconds{limiter,kind}`

`kind` is `ip` or `keyed`. Valid outcomes are `allowed`, `limited`,
`invalid_client_ip`, `invalid_key`, `unavailable`,
`degraded_passthrough`, and `degraded_rejected`.
