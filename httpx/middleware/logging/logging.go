package logging

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/bds421/rho-kit/httpx/middleware"
	"github.com/bds421/rho-kit/httpx/middleware/clientip"
)

// Logger returns middleware that logs each HTTP request with method, path,
// status, and duration.
//
// Paths in quietPaths are logged at Debug level to reduce noise from health
// checks. Trailing slashes are normalized so "/health" and "/health/" match
// the same entry. Each function in extraAttrs is called per request to add
// additional slog attributes (e.g. request ID).
func Logger(logger *slog.Logger, quietPaths []string, extraAttrs ...func(r *http.Request) slog.Attr) func(http.Handler) http.Handler {
	quiet := make(map[string]bool, len(quietPaths))
	for _, p := range quietPaths {
		quiet[strings.TrimRight(p, "/")] = true
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			wrapped := middleware.NewResponseRecorder(w)

			next.ServeHTTP(wrapped, r)

			level := slog.LevelInfo
			if quiet[strings.TrimRight(r.URL.Path, "/")] {
				level = slog.LevelDebug
			}

			attrs := []slog.Attr{
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", wrapped.Status()),
				slog.Duration("duration", time.Since(start)),
				slog.String("remote", clientip.ClientIP(r)),
			}
			for _, fn := range extraAttrs {
				attrs = append(attrs, fn(r))
			}

			logger.LogAttrs(r.Context(), level, "request", attrs...)
		})
	}
}
