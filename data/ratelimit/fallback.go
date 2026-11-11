package ratelimit

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/bds421/rho-kit/core/v2/redact"
)

// Fallback asks Primary and, while it fails, Secondary: a shared limiter
// (Redis) that is unreachable narrows the budget to one instance instead
// of refusing every request. A key both reject as invalid, and a request
// whose client has gone, are not limiter failures. Primary's own timeouts
// bound each call; the fallback adds none.
type Fallback struct {
	Primary, Secondary Limiter
	// Logger reports when Primary starts failing and when it answers again;
	// nil logs through slog.Default.
	Logger *slog.Logger

	down atomic.Bool
}

func (f *Fallback) Allow(ctx context.Context, key string) (bool, time.Duration, error) {
	allowed, retryAfter, err := f.Primary.Allow(ctx, key)
	if err == nil {
		if f.down.CompareAndSwap(true, false) {
			f.logger().InfoContext(ctx, "shared rate limit reachable again; the budget is shared")
		}
		return allowed, retryAfter, nil
	}
	if ctx.Err() != nil {
		return false, 0, ctx.Err()
	}
	if errors.Is(err, ErrInvalidKey) {
		return false, 0, err
	}
	if f.down.CompareAndSwap(false, true) {
		f.logger().WarnContext(ctx, "shared rate limit unavailable; this instance limits alone",
			redact.Error(err), redact.ErrorMessage(err))
	}
	return f.Secondary.Allow(ctx, key)
}

func (f *Fallback) logger() *slog.Logger {
	return cmp.Or(f.Logger, slog.Default())
}
