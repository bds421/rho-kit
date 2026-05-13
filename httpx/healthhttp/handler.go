// Package healthhttp provides the HTTP handler for the internal ops port
// (liveness, readiness, and Prometheus metrics endpoints).
package healthhttp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/httpx/v2/internal/transportdefaults"
	"github.com/bds421/rho-kit/observability/v2/health"
)

// Handler wraps a [health.Checker] as an [http.Handler].
// It calls Evaluate and maps the health status to an HTTP status code:
//   - StatusUnhealthy → 503 Service Unavailable
//   - everything else → 200 OK
func Handler(checker *health.Checker) http.Handler {
	if err := health.ValidateChecker(checker); err != nil {
		panic("healthhttp: Handler requires a valid *health.Checker")
	}
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
			slog.Error("health: failed to encode response", redact.Error(err))
		}
	})
}

// InternalHandlerOption configures the internal ops handler.
type InternalHandlerOption func(*internalConfig)

type internalConfig struct {
	sloHandler http.Handler
}

var errHTTPCheckRedirectBlocked = errors.New("healthhttp: dependency redirects are disabled")

// WithSLOHandler adds a GET /slo endpoint to the internal ops handler.
// Pass the handler from slohttp.Handler.
func WithSLOHandler(h http.Handler) InternalHandlerOption {
	return func(c *internalConfig) {
		c.sloHandler = h
	}
}

// NewInternalHandler builds the mux for the internal ops port:
//
//	GET /health  -> liveness (version only, no dependency checks)
//	GET /ready   -> readiness handler (caller provides)
//	GET /metrics -> Prometheus metrics
//	GET /slo     -> SLO status (optional, via [WithSLOHandler])
//
// The readiness parameter accepts any http.Handler.
// Use [Handler] to wrap a [health.Checker] as an http.Handler.
func NewInternalHandler(version string, readiness http.Handler, opts ...InternalHandlerOption) http.Handler {
	if readiness == nil {
		panic("healthhttp: NewInternalHandler requires a non-nil readiness handler")
	}
	var cfg internalConfig
	for _, o := range opts {
		if o == nil {
			panic("healthhttp: NewInternalHandler option must not be nil")
		}
		o(&cfg)
	}

	mux := http.NewServeMux()
	liveness := Handler(&health.Checker{Version: health.ResolveVersion(version)})
	mux.Handle("GET /health", liveness)
	mux.Handle("GET /ready", readiness)
	// Wrap promhttp with a Cache-Control: no-store header so a
	// misconfigured CDN/reverse proxy in front of the internal port
	// can't serve stale metrics. Prometheus itself ignores cache
	// headers, but operators occasionally point a CDN at /metrics for
	// "convenience".
	mux.Handle("GET /metrics", noStoreHandler(promhttp.Handler()))
	if cfg.sloHandler != nil {
		mux.Handle("GET /slo", cfg.sloHandler)
	}
	return mux
}

// noStoreHandler wraps h to send Cache-Control: no-store on the response.
func noStoreHandler(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		h.ServeHTTP(w, r)
	})
}

// HTTPCheck returns a non-critical [health.DependencyCheck] that probes
// the given URL with an HTTP GET. Use this for external dependencies
// whose failure should degrade readiness but not flip /ready to 503
// (e.g. an analytics ingest). Use [CriticalHTTPCheck] when the
// dependency's failure should fail readiness outright.
//
// The check applies a 5-second timeout and blocks redirects unless the
// supplied client has an explicit redirect policy.
func HTTPCheck(name, url string, client *http.Client) health.DependencyCheck {
	return httpCheck(name, url, client, false)
}

// CriticalHTTPCheck is [HTTPCheck] with Critical=true — a failure flips
// the service's /ready response to unhealthy. Use only for dependencies
// without which the service cannot serve correctly.
func CriticalHTTPCheck(name, url string, client *http.Client) health.DependencyCheck {
	return httpCheck(name, url, client, true)
}

func httpCheck(name, url string, client *http.Client, critical bool) health.DependencyCheck {
	client = dependencyHTTPClient(client)
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

func dependencyHTTPClient(client *http.Client) *http.Client {
	if client == nil {
		return &http.Client{
			Timeout:       5 * time.Second,
			Transport:     transportdefaults.New(nil, 0, "healthhttp: HTTPCheck"),
			CheckRedirect: blockHTTPCheckRedirect,
		}
	}
	if client.Timeout > 0 && client.Transport != nil && client.CheckRedirect != nil {
		return client
	}
	cloned := *client
	if cloned.Timeout <= 0 {
		cloned.Timeout = 5 * time.Second
	}
	if cloned.Transport == nil {
		cloned.Transport = transportdefaults.New(nil, 0, "healthhttp: HTTPCheck")
	}
	if cloned.CheckRedirect == nil {
		cloned.CheckRedirect = blockHTTPCheckRedirect
	}
	return &cloned
}

func blockHTTPCheckRedirect(_ *http.Request, _ []*http.Request) error {
	return errHTTPCheckRedirectBlocked
}
