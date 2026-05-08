// Package budget provides HTTP middleware that charges a fixed
// amount against a [budget.Budget] per request.
//
// Use this for endpoints that should consume a tenant's per-period
// allotment of an arbitrary unit — LLM tokens, embedding calls,
// "expensive operations". When the budget is exhausted the
// middleware returns 429 with advisory headers operators can read.
//
// The default key function reads [tenant.FromContext]; supply a
// custom key when budgets are scoped to something other than the
// tenant ID (API key, user ID, organisation slug).
//
// asvs: V11.1.1
package budget

import (
	"fmt"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/bds421/rho-kit/core/tenant"
	"github.com/bds421/rho-kit/data/budget"
)

// Header names attached to rejection responses. Kept exported so
// upstream proxies / dashboards can read them by name.
const (
	HeaderScope     = "X-Budget-Scope"
	HeaderRemaining = "X-Budget-Remaining"
	HeaderRetry     = "Retry-After"
)

// KeyFunc derives the budget bucket key from the request. Returns
// ("", false) when no budget should be charged (the middleware
// passes through with no headers in that case).
type KeyFunc func(*http.Request) (string, bool)

// TenantKeyFunc reads the tenant ID from the request context (set by
// httpx/middleware/tenant). Requests without a tenant pass through
// unchanged: enforce required-tenant in a separate middleware so
// pre-auth health endpoints aren't billed against a phantom budget.
func TenantKeyFunc() KeyFunc {
	return func(r *http.Request) (string, bool) {
		id, ok := tenant.FromContext(r.Context())
		if !ok {
			return "", false
		}
		return id.String(), true
	}
}

// Option configures the [Middleware].
type Option func(*config)

type config struct {
	key    KeyFunc
	amount int64
	scope  string
}

// WithKeyFunc overrides the default tenant-context key function.
func WithKeyFunc(fn KeyFunc) Option {
	return func(c *config) { c.key = fn }
}

// WithAmount sets the per-request charge. Default: 1.
//
// Use this when an endpoint costs a fixed multiple of the per-token
// unit (e.g. "this generate-image endpoint costs 100 token-equivalents").
// For dynamic per-request costs use the outbound RoundTripper
// (httpx/budget) which can read costs from response headers.
func WithAmount(n int64) Option {
	return func(c *config) { c.amount = n }
}

// WithScope sets the value of the X-Budget-Scope response header on
// rejection (and only on rejection — successful requests don't pay
// for the header). Use a short label that maps to your operator
// dashboard ("tokens-per-hour", "dollars-per-day"). Empty is
// allowed; the header is omitted when blank.
func WithScope(s string) Option {
	return func(c *config) { c.scope = s }
}

// Middleware returns an HTTP middleware that charges the configured
// amount against the resolved key on every request.
//
// Panics on misconfiguration (nil budget, amount < 0).
func Middleware(b budget.Budget, opts ...Option) func(http.Handler) http.Handler {
	if b == nil {
		panic("middleware/budget: budget must not be nil")
	}
	cfg := config{
		key:    TenantKeyFunc(),
		amount: 1,
		// Default scope label: matches httpx/middleware/ratelimit/tenant
		// and stays meaningful in operator dashboards even when a service
		// forgets to call WithScope. Anonymous 429s reading
		// "X-Budget-Scope: tenant" are easier to triage than 429s with
		// no scope header at all.
		scope: "tenant",
	}
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.key == nil {
		panic("middleware/budget: keyFunc must not be nil")
	}
	if cfg.amount < 0 {
		panic("middleware/budget: amount must be >= 0")
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key, ok := cfg.key(r)
			if !ok {
				next.ServeHTTP(w, r)
				return
			}
			allowed, remaining, retryAfter, err := b.Consume(r.Context(), key, cfg.amount)
			if err != nil {
				// Backend errors are surfaced as 503; we don't fail
				// open silently because that would defeat the budget.
				http.Error(w, "budget unavailable", http.StatusServiceUnavailable)
				return
			}
			if !allowed {
				writeRejected(w, cfg.scope, remaining, retryAfter)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func writeRejected(w http.ResponseWriter, scope string, remaining int64, retryAfter time.Duration) {
	if scope != "" {
		w.Header().Set(HeaderScope, scope)
	}
	w.Header().Set(HeaderRemaining, strconv.FormatInt(remaining, 10))
	secs := int64(math.Ceil(retryAfter.Seconds()))
	if secs < 1 {
		secs = 1
	}
	w.Header().Set(HeaderRetry, strconv.FormatInt(secs, 10))
	// JSON body kept tiny on purpose — the meaningful information is
	// in the headers, the body is human-readable companion text.
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusTooManyRequests)
	_, _ = fmt.Fprintf(w, `{"error":"budget exceeded","code":"BUDGET_EXCEEDED","remaining":%d}`, remaining)
}
