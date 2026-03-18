// Package retry provides a shared backoff policy for transient failures.
//
// It wraps github.com/cenkalti/backoff to keep retry behavior consistent across
// services, with helpers for single-shot retries (Do/DoWith) and resilient loops (Loop).
// Use RetryIfNotPermanent to avoid retrying apperror.Permanent failures, and
// WithOnRetry to emit metrics or logs on each retry.
package retry
