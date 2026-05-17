// Package retry provides a shared backoff policy for transient failures.
//
// It wraps github.com/cenkalti/backoff to keep retry behavior consistent across
// services, with helpers for single-shot retries (Do/DoWith) and resilient loops (Loop).
// Use RetryIfNotPermanent to avoid retrying apperror.Permanent failures.
//
// Observability:
//   - Policy.Metrics wires built-in Prometheus collectors
//     (retry_outcomes_total counter, retry_attempts histogram) keyed
//     by Policy.Name so dashboards can alert on
//     "retries-exhausted rate by service" without per-call boilerplate.
//   - Policy.OnRetry remains the right tool for per-attempt reactions
//     (custom logging, paging on the 3rd attempt, etc.) — it runs on
//     every retry, while Metrics record once per terminal outcome.
//   - Policy.Logger lets callers inject a *slog.Logger for the
//     callback-panic recovery paths; defaults to slog.Default.
package retry
