package httpx

import (
	"context"
	"log/slog"

	"github.com/bds421/rho-kit/observability/logging"
)

const loggerKey contextKey = "logger"

// SetLogger stores a *slog.Logger in the context.
// Used by WithRequestLogger middleware to propagate a pre-configured logger.
//
// Note: this also stores the logger via [logging.WithContext] so that both
// httpx.Logger and logging.FromContext find the same logger. This unifies
// the two logger-in-context mechanisms.
func SetLogger(ctx context.Context, l *slog.Logger) context.Context {
	ctx = context.WithValue(ctx, loggerKey, l)
	ctx = logging.WithContext(ctx, l)
	return ctx
}

// Logger extracts the request-scoped logger from the context.
// It checks the httpx-specific key first, then falls back to
// [logging.FromContext] for interoperability with the logging package.
// Returns the fallback logger if neither was set (never returns nil).
func Logger(ctx context.Context, fallback *slog.Logger) *slog.Logger {
	if l, ok := ctx.Value(loggerKey).(*slog.Logger); ok && l != nil {
		return l
	}
	// Fall back to the logging package's context key for interoperability.
	if l := logging.FromContext(ctx); l != slog.Default() {
		return l
	}
	if fallback != nil {
		return fallback
	}
	return slog.Default()
}
