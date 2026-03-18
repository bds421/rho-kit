// Package retry provides a [storage.Storage] wrapper that retries
// transient errors with configurable exponential backoff and jitter.
//
// Internally delegates to [kit/retry.DoWith] for backoff computation,
// avoiding duplicated backoff math.
//
// Usage:
//
//	retried := retry.New(s3Backend, retry.WithMaxAttempts(3))
//	err := retried.Put(ctx, key, reader, meta)
package retry
