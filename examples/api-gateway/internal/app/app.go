// Package app wires the api-gateway EXAMPLE.
//
// Composition shown:
//
//	ratelimit.Middleware            (IP-keyed throttle)
//	  → jwtAuthMiddleware           (bearer-token validation; STUB)
//	    → downstream-fanout handler
//	         → timeoutbudget.New      (request-scoped time budget;
//	                                   reserves 50ms for response-write)
//	           → bulkhead.ExecuteCtx (caps concurrent in-flight
//	                                   to the downstream)
//	             → budget.WithRemaining (derive per-call deadline
//	                                     from remaining budget)
//	               → circuitbreaker.ExecuteCtx (fast-fail on broken
//	                                            downstream)
//	                 → retry.DoWith             (transient blip recovery)
//	                   → real downstream call
//
// The order is the canonical kit pattern:
//   - Rate-limit is OUTERMOST so DDoS shedding happens before any
//     auth or downstream work.
//   - JWT auth is SECOND so unauthenticated requests do not consume
//     downstream budget.
//   - timeoutbudget is OUTERMOST in the downstream chain so the
//     request-total deadline is established before any concurrency
//     control runs. The 50ms reservation guards response-write
//     from being swallowed by a slow downstream that consumes the
//     entire budget.
//   - bulkhead sits OUTSIDE breaker so a flood does not consume
//     breaker slots — the cap on concurrent in-flight applies
//     regardless of breaker state.
//   - budget.WithRemaining derives the per-call ctx INSIDE the
//     bulkhead so each retry attempt inherits the shrinking
//     deadline as the budget burns down.
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

	apphttp "github.com/bds421/rho-kit/app/http/v2"
	"github.com/bds421/rho-kit/app/v2"
	"github.com/bds421/rho-kit/httpx/v2/middleware/ratelimit"
	"github.com/bds421/rho-kit/resilience/v2/bulkhead"
	"github.com/bds421/rho-kit/resilience/v2/circuitbreaker"
	"github.com/bds421/rho-kit/resilience/v2/retry"
	"github.com/bds421/rho-kit/resilience/v2/timeoutbudget"
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

	// API-key-protected demo route (opaque keys via security/apikey +
	// httpx/middleware/apikey), alongside the JWT-stubbed /api/orders route
	// so the example shows both auth styles side by side.
	apiKeyHandler, apiKeyToken, err := newAPIKeyDemoHandler(ctx, logger)
	if err != nil {
		return err
	}
	// EXAMPLE ONLY: never log a plaintext key in production — it is shown to
	// the owner once at issuance and only its hash is ever stored.
	logger.Info("issued demo api key",
		"try", "curl -H 'Authorization: Bearer <token>' localhost:8095/api/keys-demo",
		"token", apiKeyToken,
	)

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
			mux.Handle("GET /api/keys-demo", apiKeyHandler)
			return mux
		}).
		RunContext(ctx)
}

// requestBudget caps the total time any single inbound request
// is allowed to spend in the downstream chain. Production
// services tune this from inbound SLO budgets — a 1s public SLO
// implies an internal budget tighter than 1s so the gateway has
// headroom for response-write and observability emit.
const (
	requestBudget       = 800 * time.Millisecond
	postCallReservation = 50 * time.Millisecond
	bulkheadMaxInFlight = 8
	bulkheadQueueWait   = 100 * time.Millisecond
)

// gateway groups the example's composition state. In a real
// service this would hold downstream-service clients, the JWT
// verifier, and connection pools.
type gateway struct {
	bearerToken         string
	downstream          downstreamFn
	bulkhead            *bulkhead.Bulkhead
	breaker             *circuitbreaker.CircuitBreaker
	retryPolicy         retry.Policy
	requestBudget       time.Duration
	postCallReservation time.Duration
}

// downstreamFn is the stub shape the example fans out to. A real
// gateway calls a gRPC client, an HTTP client, or a queue
// publisher here.
type downstreamFn func(ctx context.Context, tenant string) (string, error)

// newGateway constructs the example gateway with a configurable
// downstream callable so the smoke test can inject deterministic
// failure shapes. The bulkhead, breaker and retry policy are
// constructed with the kit's defaults; production tunes them per
// downstream SLO.
func newGateway(token string, downstream downstreamFn) *gateway {
	return &gateway{
		bearerToken: token,
		downstream:  downstream,
		bulkhead: bulkhead.New("orders-downstream", bulkheadMaxInFlight,
			bulkhead.WithMaxQueueWait(bulkheadQueueWait),
		),
		breaker: circuitbreaker.NewCircuitBreaker(
			5, /* trip after 5 consecutive failures */
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
		requestBudget:       requestBudget,
		postCallReservation: postCallReservation,
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
// the canonical timeoutbudget → bulkhead → breaker(retry(call))
// chain. Each layer translates failure into a distinct status code
// so operators can route incidents correctly:
//
//   - 503 (circuit open)         — breaker rejected fast; downstream broken.
//   - 503 (bulkhead full)        — concurrent in-flight cap exceeded.
//   - 504 (deadline / budget)    — total request time expired before completion.
//   - 502 (other)                — bottom of the chain returned an error.
func (g *gateway) handleListOrders(w http.ResponseWriter, r *http.Request) {
	tenant := r.Header.Get("X-Tenant-Id")
	if tenant == "" {
		http.Error(w, "X-Tenant-Id header is required", http.StatusBadRequest)
		return
	}

	// Request-scoped time budget. The cancel func releases the
	// underlying timer when the handler returns.
	budgetCtx, budget, cancel := timeoutbudget.New(r.Context(), g.requestBudget)
	defer cancel()

	// Hold back a small slice of the budget for the response-write
	// + observability emit so a slow downstream cannot consume the
	// entire allocation and leave the response truncated.
	restore := budget.WithReservation(g.postCallReservation)
	defer restore()

	var result string
	err := g.bulkhead.ExecuteCtx(budgetCtx, func(ctx context.Context) error {
		// Derive the per-call deadline from what is LEFT in the
		// budget after the bulkhead acquisition. Each retry sees
		// the same shrinking deadline.
		callCtx, callCancel, err := budget.WithRemaining(ctx)
		if err != nil {
			return err // timeoutbudget.ErrBudgetExhausted
		}
		defer callCancel()

		return g.breaker.ExecuteCtx(callCtx, func(ctx context.Context) error {
			return retry.DoWith(ctx, g.retryPolicy, func(ctx context.Context) error {
				out, err := g.downstream(ctx, tenant)
				if err != nil {
					return err
				}
				result = out
				return nil
			})
		})
	})
	if err != nil {
		writeDownstreamError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"tenant": tenant,
		"orders": result,
	})
}

// writeDownstreamError translates the inner-chain error into the
// status code that lets operators distinguish protection-driven
// rejection (503) from budget exhaustion (504) from a downstream
// failure (502). Order of checks matters — bulkhead and breaker
// both surface 503 but with different operator semantics, so we
// keep the messages distinct.
func writeDownstreamError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, circuitbreaker.ErrCircuitOpen):
		http.Error(w, "downstream unavailable (circuit open)", http.StatusServiceUnavailable)
	case errors.Is(err, bulkhead.ErrBulkheadFull):
		http.Error(w, "downstream busy (bulkhead full)", http.StatusServiceUnavailable)
	case errors.Is(err, timeoutbudget.ErrBudgetExhausted),
		errors.Is(err, context.DeadlineExceeded):
		http.Error(w, "downstream timed out (budget exhausted)", http.StatusGatewayTimeout)
	default:
		http.Error(w, "downstream call failed", http.StatusBadGateway)
	}
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
