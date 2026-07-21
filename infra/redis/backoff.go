package redis

import (
	"context"
	"log/slog"

	"github.com/bds421/rho-kit/resilience/v2/retry"
)

// RunWithBackoff runs fn in a loop, restarting with exponential backoff on
// error. If fn runs for longer than 30s before returning an error, the backoff
// resets to base delay. Random jitter is applied to prevent thundering herd
// when multiple consumers restart simultaneously.
//
// Returns ctx.Err() when stopped by cancellation. When the loop exits without
// cancellation (fn returned nil for graceful completion, or a non-retryable
// error stopped the loop), returns the last error from fn (nil on graceful
// completion). Callers that only care about clean shutdown can still use
// errors.Is(err, context.Canceled).
//
// This delegates to the shared retry.Loop with the WorkerPolicy() defaults
// (3s base, 60s max, 2x factor, ±25% jitter, 30s stability reset).
func RunWithBackoff(ctx context.Context, logger *slog.Logger, component string, fn func(ctx context.Context) error) error {
	return runWithBackoff(ctx, logger, component, fn)
}

// runWithBackoff exposes retry timing only inside this package so tests can
// exercise restart semantics without sleeping through the production worker
// policy. The exported API intentionally keeps the hardened defaults.
func runWithBackoff(ctx context.Context, logger *slog.Logger, component string, fn func(ctx context.Context) error, opts ...retry.Option) error {
	var last error
	retry.Loop(ctx, logger, component, func(ctx context.Context) error {
		err := fn(ctx)
		last = err
		return err
	}, opts...)
	if err := ctx.Err(); err != nil {
		return err
	}
	return last
}
