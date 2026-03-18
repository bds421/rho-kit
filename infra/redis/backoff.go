package redis

import (
	"context"
	"log/slog"

	"github.com/bds421/rho-kit/resilience/retry"
)

// RunWithBackoff runs fn in a loop, restarting with exponential backoff on
// error. If fn runs for longer than 30s before returning an error, the backoff
// resets to base delay. Random jitter is applied to prevent thundering herd
// when multiple consumers restart simultaneously.
// Blocks until ctx is cancelled.
//
// This delegates to the shared retry.Loop with the WorkerPolicy defaults
// (3s base, 60s max, 2x factor, ±25% jitter, 30s stability reset).
func RunWithBackoff(ctx context.Context, logger *slog.Logger, component string, fn func(ctx context.Context) error) {
	retry.Loop(ctx, logger, component, fn)
}
