// Package app wires the api-gateway EXAMPLE.
//
// Composition shown:
//
//	ratelimit.Middleware            (IP-keyed throttle)
//	  → jwtAuthMiddleware           (bearer-token validation; STUB)
//	    → downstream-fanout handler
//	         → circuitbreaker.ExecuteCtx (fast-fail on broken downstream)
//	           → retry.DoWith             (transient blip recovery)
//	             → real downstream call
//
// The order is the canonical kit pattern:
//   - Rate-limit is OUTERMOST so DDoS shedding happens before any
//     auth or downstream work.
//   - JWT auth is SECOND so unauthenticated requests do not consume
//     downstream budget.
//   - The downstream fan-out wraps breaker(retry(call)) so a broken
//     downstream rejects fast WITHOUT burning retries.
//
// SECURITY: this example STUBS JWT validation against a static
// demo bearer token so the smoke test can stand up without a JWKS
// endpoint. Production deployments wire `security/jwtutil` (or the
// `app/jwt` bridge module under `app.Builder.With(jwt.Module(...))`)
// which fetches keys from the issuer's JWKS and validates issuer,
// audience, expiry, and signature. The Builder also runs the
// always-on production-safety validator at startup — TLS,
// internal-host loopback, sslmode, tracing sample rate. The
// example skips both for clarity but the canonical recipe in
// examples/README.md shows the Builder shape.
package app

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/bds421/rho-kit/app/v2"
	apphttp "github.com/bds421/rho-kit/app/http/v2"
	"github.com/bds421/rho-kit/httpx/v2/middleware/ratelimit"
	"github.com/bds421/rho-kit/resilience/v2/circuitbreaker"
	"github.com/bds421/rho-kit/resilience/v2/retry"
)

const demoTokenEnv = "API_GATEWAY_DEMO_TOKEN"

// Run starts the gateway via the kit's app.Builder so the
// example demonstrates the SAME production wiring shape every
// real service uses. The Builder's always-on validator
// (TLS / JWT issuer-audience / internal-host loopback / sslmode)
// would reject the example's stubbed configuration at startup;
// we opt out per-policy via the kit's documented `Without*`
// helpers so the relaxation is auditable line-by-line:
//
//   - apphttp.WithoutTLS() — example listens on plain http so
//     curl doesn't need a cert. Production wires real TLS.
//   - Builder.WithoutRateLimit() — the example's rate limit is
//     applied inline by the handler (httpx/middleware/ratelimit
//     directly) so callers see EXACTLY where it lives. Production
//     can either keep that pattern OR register
//     ratelimit.IP(...) through app/ratelimit at the Builder
//     level; both are valid.
//
// kit-doctor will flag each of these in production code,
// confirming the discipline.
func Run(ctx context.Context) error {
	logger := slog.Default()
	demoToken, err := demoBearerTokenFromEnv()
	if err != nil {
		return err
	}
	gw := newGateway(demoToken, callRealDownstream)

	cfg := app.BaseConfig{
		Server:      app.ServerConfig{Host: "127.0.0.1", Port: 8095},
		Internal:    app.InternalConfig{Host: "127.0.0.1", Port: 9095},
		Environment: "example",
		LogLevel:    "info",
	}
	return app.New("api-gateway", "0.0.0-example", cfg).
		Logger(logger).
		WithoutRateLimit().
		// Example listens on plain http for curl/test convenience.
		// kit-doctor:allow apphttp-without-tls
		With(apphttp.Module(apphttp.WithoutTLS())).
		Router(func(_ app.Infrastructure) http.Handler {
			mux := http.NewServeMux()
			mux.Handle("GET /api/orders", gw.buildHandler(logger))
			return mux
		}).
		RunContext(ctx)
}

// gateway groups the example's composition state. In a real
// service this would hold downstream-service clients, the JWT
// verifier, and connection pools.
type gateway struct {
	bearerToken string
	downstream  downstreamFn
	breaker     *circuitbreaker.CircuitBreaker
	retryPolicy retry.Policy
}

// downstreamFn is the stub shape the example fans out to. A real
// gateway calls a gRPC client, an HTTP client, or a queue
// publisher here.
type downstreamFn func(ctx context.Context, tenant string) (string, error)

// newGateway constructs the example gateway with a configurable
// downstream callable so the smoke test can inject deterministic
// failure shapes.
func newGateway(token string, downstream downstreamFn) *gateway {
	return &gateway{
		bearerToken: token,
		downstream:  downstream,
		breaker: circuitbreaker.NewCircuitBreaker(
			5 /* trip after 5 consecutive failures */,
			500*time.Millisecond,
			circuitbreaker.WithName("orders-downstream"),
		),
		retryPolicy: retry.Policy{
			MaxRetries: 2,
			BaseDelay:  50 * time.Millisecond,
			MaxDelay:   200 * time.Millisecond,
			Factor:     2.0,
			Jitter:     0.25,
		},
	}
}

// buildHandler wires the canonical rate-limit → auth → fan-out
// chain.
func (g *gateway) buildHandler(_ *slog.Logger) http.Handler {
	limiter := ratelimit.NewLimiter(60, time.Minute,
		ratelimit.WithLimiterName("public_orders"),
	)

	core := http.HandlerFunc(g.handleListOrders)
	withAuth := g.jwtAuthMiddleware(core)
	withLimit := ratelimit.Middleware(limiter)(withAuth)
	return withLimit
}

// jwtAuthMiddleware is the example's STUB validator. Production
// deployments swap this for security/jwtutil or app/jwt — both
// validate issuer, audience, signature, and expiry against a
// JWKS endpoint. The stub uses crypto/subtle.ConstantTimeCompare
// so the example doesn't model the wrong shape for token
// comparison.
func (g *gateway) jwtAuthMiddleware(next http.Handler) http.HandlerFunc {
	tokenBytes := []byte(g.bearerToken)
	return func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			http.Error(w, "missing bearer token", http.StatusUnauthorized)
			return
		}
		got := []byte(strings.TrimPrefix(auth, "Bearer "))
		if subtle.ConstantTimeCompare(got, tokenBytes) != 1 {
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	}
}

// handleListOrders fans out to the downstream service wrapped with
// the canonical breaker(retry(call)) chain.
func (g *gateway) handleListOrders(w http.ResponseWriter, r *http.Request) {
	tenant := r.Header.Get("X-Tenant-Id")
	if tenant == "" {
		http.Error(w, "X-Tenant-Id header is required", http.StatusBadRequest)
		return
	}

	var result string
	err := g.breaker.ExecuteCtx(r.Context(), func(ctx context.Context) error {
		return retry.DoWith(ctx, g.retryPolicy, func(ctx context.Context) error {
			out, err := g.downstream(ctx, tenant)
			if err != nil {
				return err
			}
			result = out
			return nil
		})
	})
	if err != nil {
		if errors.Is(err, circuitbreaker.ErrCircuitOpen) {
			// Distinct status code so operators can tell "down" from
			// "rejected by breaker for protection".
			http.Error(w, "downstream unavailable (circuit open)", http.StatusServiceUnavailable)
			return
		}
		http.Error(w, "downstream call failed", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"tenant": tenant,
		"orders": result,
	})
}

// callRealDownstream is the production stand-in. In a real gateway
// this would invoke a gRPC client, an HTTP client wrapped with
// httpx.NewHTTPClient, or a queue publisher.
func callRealDownstream(_ context.Context, tenant string) (string, error) {
	return "orders for " + tenant, nil
}

// failingDownstream is a deterministic transient-failure shape used
// by smoke tests. The first N calls fail; subsequent calls succeed.
// It is intentionally exported (package-internal) so the test
// package can inject it; production code never imports this.
type failingDownstream struct {
	failuresRemaining atomic.Int32
}

func (f *failingDownstream) call(_ context.Context, tenant string) (string, error) {
	if f.failuresRemaining.Add(-1) >= 0 {
		return "", errors.New("transient downstream failure")
	}
	return "orders for " + tenant, nil
}

// alwaysFailDownstream returns an error on every call. Used to
// drive the breaker into open state in the smoke test.
func alwaysFailDownstream(_ context.Context, _ string) (string, error) {
	return "", errors.New("downstream is broken")
}

func demoBearerTokenFromEnv() (string, error) {
	tok := os.Getenv(demoTokenEnv)
	if len(tok) < 16 {
		return "", fmt.Errorf("%s must be set to a 16+ char demo token; generate with: openssl rand -hex 16", demoTokenEnv)
	}
	return tok, nil
}
