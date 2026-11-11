package ratelimit

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/bds421/rho-kit/core/v2/redact"
)

// Fallback asks Primary and, while it fails, Secondary: a shared limiter
// (Redis) that is unreachable narrows the budget to one instance instead
// of refusing every request. A key both reject as invalid, and a request
// whose client has gone, are not limiter failures.
type Fallback struct {
	Primary, Secondary Limiter
	// Logger reports each failure of Primary; nil logs through slog.Default.
	Logger *slog.Logger
}

func (f Fallback) Allow(ctx context.Context, key string) (bool, time.Duration, error) {
	allowed, retryAfter, err := f.Primary.Allow(ctx, key)
	if err == nil {
		return allowed, retryAfter, nil
	}
	if ctx.Err() != nil {
		return false, 0, ctx.Err()
	}
	if errors.Is(err, ErrInvalidKey) {
		return false, 0, err
	}
	cmp.Or(f.Logger, slog.Default()).WarnContext(ctx, "shared rate limit unavailable; this instance limits alone",
		redact.Error(err), redact.ErrorMessage(err))
	return f.Secondary.Allow(ctx, key)
}
