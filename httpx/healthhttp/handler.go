// Package healthhttp provides the HTTP handler for the internal ops port
// (liveness, readiness, and Prometheus metrics endpoints).
package healthhttp

import (
	"encoding/json"
	"log/slog"
	"context"
	"io"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/bds421/rho-kit/observability/health"
)

// Handler wraps a [health.Checker] as an [http.Handler].
// It calls Evaluate and maps the health status to an HTTP status code:
//   - StatusUnhealthy → 503 Service Unavailable
//   - everything else → 200 OK
func Handler(checker *health.Checker) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := checker.Evaluate(r.Context())

		code := http.StatusOK
		if resp.Status == health.StatusUnhealthy {
			code = http.StatusServiceUnavailable
		}

		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			slog.Error("health: failed to encode response", "error", err)
		}
	})
}

// NewInternalHandler builds the mux for the internal ops port:
//
//	GET /health  -> liveness (version only, no dependency checks)
//	GET /ready   -> readiness handler (caller provides)
//	GET /metrics -> Prometheus metrics
//
// The readiness parameter accepts any http.Handler.
// Use [Handler] to wrap a [health.Checker] as an http.Handler.
func NewInternalHandler(version string, readiness http.Handler) http.Handler {
	mux := http.NewServeMux()
	liveness := Handler(&health.Checker{Version: health.ResolveVersion(version)})
	mux.Handle("GET /health", liveness)
	mux.Handle("GET /ready", readiness)
	mux.Handle("GET /metrics", promhttp.Handler())
	return mux
}

// HTTPCheck returns a [health.DependencyCheck] that probes the given URL with
// an HTTP GET. Use this to monitor external HTTP dependencies (e.g., upstream
// APIs, auth servers). The check applies a 5-second timeout.
func HTTPCheck(name, url string, client *http.Client, critical bool) health.DependencyCheck {
	return health.DependencyCheck{
		Name: name,
		Check: func(ctx context.Context) string {
			checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			req, err := http.NewRequestWithContext(checkCtx, http.MethodGet, url, nil)
			if err != nil {
				return health.StatusUnhealthy
			}
			resp, err := client.Do(req)
			if err != nil {
				return health.StatusUnhealthy
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				return health.StatusUnhealthy
			}
			return health.StatusHealthy
		},
		Critical: critical,
	}
}
