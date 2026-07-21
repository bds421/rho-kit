// Package ratelimit is the lazy app-module wrapper for the kit's
// rate-limiter middleware ([github.com/bds421/rho-kit/httpx/v2/middleware/ratelimit]).
//
// Two flavours:
//   - [IP] caps requests per remote-IP. The middleware is
//     auto-applied to the public mux at [app.PhaseRateLimit] so
//     hostile clients are cheap-rejected before any deeper
//     middleware (signed-request crypto, auth, tenant budget)
//     runs.
//   - [Keyed] caps requests per arbitrary key (api key, user
//     ID, tenant ID). No middleware is auto-installed; consumers
//     reach for it via [KeyedLimiter] inside their RouterFunc and
//     wrap routes individually.
//
// Only [IP] satisfies the [app.RateLimitDeclarer] capability: it
// auto-installs mux-wide middleware. [Keyed] publishes a limiter for
// per-route use but does NOT count as a rate-limit declaration —
// Keyed-only services must also register [IP] or call
// [app.Builder.WithoutRateLimit] so the un-throttled public mux is an
// affirmative choice.
//
// The kit insists on an explicit choice: register [IP], or call
// [app.Builder.WithoutRateLimit] to acknowledge the un-throttled
// posture. Builder.Validate rejects any third option.
package ratelimit

import (
	"context"
	"net/http"
	"time"

	"github.com/bds421/rho-kit/app/v2"
	mwrl "github.com/bds421/rho-kit/httpx/v2/middleware/ratelimit"
	"github.com/bds421/rho-kit/observability/v2/health"
	"github.com/bds421/rho-kit/observability/v2/promutil"
)

// Resource keys.
const (
	// ResourceIPKey is the [app.Infrastructure.Resource] key under
	// which [IP] publishes its *mwrl.Limiter for per-
	// route overrides (tightening /admin while the auto-applied
	// limiter handles the public mux baseline).
	ResourceIPKey = "github.com/bds421/rho-kit/app/ratelimit.ip"
	// ResourceKeyedMapKey is the [app.Infrastructure.Resource] key
	// under which every [Keyed] cooperatively appends its
	// limiter. The value is a map[string]*mwrl.KeyedLimiter.
	ResourceKeyedMapKey = "github.com/bds421/rho-kit/app/ratelimit.keyed"

	// ModuleNameIP is the registered Name() for [IP].
	ModuleNameIP = "ratelimit-ip"
	// ModuleNameKeyedPrefix is the Name() prefix for [Keyed] modules
	// (full name is ModuleNameKeyedPrefix + name).
	ModuleNameKeyedPrefix = "ratelimit-keyed-"
)

// Option configures an [IP] module before Builder.Run executes it.
type Option func(*ipModule)

// WithTrustedProxies forwards CIDRs to the underlying IP limiter so
// X-Forwarded-For / X-Real-IP from those proxies are used for bucketing.
// Without this option the limiter trusts loopback only — behind a
// non-loopback load balancer every client collapses into one bucket.
//
// Panics if any CIDR is invalid (same contract as
// [mwrl.WithTrustedProxies]).
func WithTrustedProxies(cidrs []string) Option {
	// Validate eagerly so misconfiguration panics at Module construction
	// rather than deep inside Init.
	_ = mwrl.WithTrustedProxies(cidrs)
	cloned := append([]string(nil), cidrs...)
	return func(m *ipModule) {
		m.trustedProxies = cloned
	}
}

// IP returns an [app.Module] that registers a per-IP rate
// limiter, attaches its sweeper to the lifecycle runner, and
// contributes [mwrl.Middleware] at [app.PhaseRateLimit] so the
// public mux is auto-protected.
//
// Pass [WithTrustedProxies] when the service sits behind a non-loopback
// ingress so per-client XFF attribution works.
//
// Panics if requests <= 0 or window <= 0, or if an option is nil /
// invalid.
func IP(requests int, window time.Duration, opts ...Option) app.Module {
	if requests <= 0 {
		panic("app/ratelimit: IP requires a positive request limit")
	}
	if window <= 0 {
		panic("app/ratelimit: IP requires a positive window")
	}
	m := &ipModule{requests: requests, window: window}
	for _, opt := range opts {
		if opt == nil {
			panic("app/ratelimit: IP option must not be nil")
		}
		opt(m)
	}
	return m
}

type ipModule struct {
	requests       int
	window         time.Duration
	trustedProxies []string

	limiter *mwrl.Limiter
	// Cached so PublicMiddleware returns a stable function value across
	// repeated reads (matches budget/tenant/signedrequest bridges).
	publicMW func(http.Handler) http.Handler
}

func (m *ipModule) Name() string                           { return ModuleNameIP }
func (m *ipModule) DeclaresRateLimit()                     {}
func (m *ipModule) HealthChecks() []health.DependencyCheck { return nil }
func (m *ipModule) Stop(_ context.Context) error           { return nil }

func (m *ipModule) Init(_ context.Context, mc app.ModuleContext) error {
	metrics := mwrl.NewMetrics()
	opts := []mwrl.LimiterOption{
		mwrl.WithMetrics(metrics),
		mwrl.WithLimiterName("ip"),
	}
	if len(m.trustedProxies) > 0 {
		opts = append(opts, mwrl.WithTrustedProxies(m.trustedProxies))
	}
	m.limiter = mwrl.NewLimiter(m.requests, m.window, opts...)
	m.publicMW = mwrl.Middleware(m.limiter)
	mc.Runner.Add("rate-limiter-cleanup", m.limiter)
	return nil
}

func (m *ipModule) Populate(infra *app.Infrastructure) {
	if m.limiter != nil {
		infra.SetResource(ResourceIPKey, m.limiter)
	}
}

func (m *ipModule) PublicMiddleware() []app.PhasedMiddleware {
	if m.limiter == nil || m.publicMW == nil {
		return nil
	}
	return []app.PhasedMiddleware{{
		Phase: app.PhaseRateLimit,
		Func:  m.publicMW,
	}}
}

// Keyed returns an [app.Module] that registers a keyed rate
// limiter under name and exposes it via [KeyedLimiter]. Unlike
// [IP] no middleware is auto-installed — the keyed limiter
// is for explicit per-route use (e.g., rejecting bursty API keys
// before the request body is read).
//
// Keyed alone does NOT satisfy Builder.Validate's rate-limit
// declaration: register [IP] for mux-wide protection, or call
// [app.Builder.WithoutRateLimit] to acknowledge the un-throttled mux.
//
// Multiple [Keyed] modules with distinct names are supported; the
// underlying resource map indexes them by name. Duplicate names
// panic at registration.
//
// Panics if name is empty or non-metric-safe, or requests <= 0
// or window <= 0.
func Keyed(name string, requests int, window time.Duration) app.Module {
	if name == "" {
		panic("app/ratelimit: Keyed requires a non-empty name")
	}
	if err := promutil.ValidateStaticLabelValue("keyed rate limiter name", name); err != nil {
		panic("app/ratelimit: Keyed requires a metric-safe static name")
	}
	if requests <= 0 {
		panic("app/ratelimit: Keyed requires a positive request limit")
	}
	if window <= 0 {
		panic("app/ratelimit: Keyed requires a positive window")
	}
	return &keyedModule{name: name, requests: requests, window: window}
}

type keyedModule struct {
	name     string
	requests int
	window   time.Duration

	limiter *mwrl.KeyedLimiter
}

func (m *keyedModule) Name() string { return ModuleNameKeyedPrefix + m.name }

// Keyed deliberately does NOT implement DeclaresRateLimit: it installs
// no mux-wide middleware. Pair with [IP] or [app.Builder.WithoutRateLimit].
func (m *keyedModule) HealthChecks() []health.DependencyCheck { return nil }
func (m *keyedModule) Stop(_ context.Context) error           { return nil }

func (m *keyedModule) Init(_ context.Context, mc app.ModuleContext) error {
	metrics := mwrl.NewMetrics()
	m.limiter = mwrl.NewKeyedLimiter(m.requests, m.window,
		mwrl.WithKeyedMetrics(metrics),
		mwrl.WithKeyedLimiterName(m.name),
	)
	mc.Runner.Add("keyed-limiter-"+m.name, m.limiter)
	return nil
}

func (m *keyedModule) Populate(infra *app.Infrastructure) {
	if m.limiter == nil {
		return
	}
	// Cooperatively append to the shared resource map so multiple
	// Keyed registrations stack rather than overwrite each
	// other. Existing map (if any) is read-mutated; absent map
	// path creates a fresh one. Module Populate is called
	// sequentially in registration order, so the read/write race
	// only exists across modules contributing to the same key,
	// not within a single Populate call.
	var m2 map[string]*mwrl.KeyedLimiter
	if existing, ok := infra.Resource(ResourceKeyedMapKey); ok {
		m2, _ = existing.(map[string]*mwrl.KeyedLimiter)
	}
	if m2 == nil {
		m2 = make(map[string]*mwrl.KeyedLimiter)
		infra.SetResource(ResourceKeyedMapKey, m2)
	}
	m2[m.name] = m.limiter
}

// IPLimiter returns the per-IP rate limiter published by
// [IP], or nil if [IP] was not registered.
func IPLimiter(infra app.Infrastructure) *mwrl.Limiter {
	v, ok := infra.Resource(ResourceIPKey)
	if !ok {
		return nil
	}
	l, _ := v.(*mwrl.Limiter)
	return l
}

// KeyedLimiter returns the keyed rate limiter registered under
// name via [Keyed], or nil if no module registered that name.
func KeyedLimiter(infra app.Infrastructure, name string) *mwrl.KeyedLimiter {
	v, ok := infra.Resource(ResourceKeyedMapKey)
	if !ok {
		return nil
	}
	m, _ := v.(map[string]*mwrl.KeyedLimiter)
	if m == nil {
		return nil
	}
	return m[name]
}
