// Package ratelimit is the lazy app-module wrapper for the kit's
// rate-limiter middleware ([github.com/bds421/rho-kit/httpx/v2/middleware/ratelimit]).
//
// Two flavours:
//   - [IPModule] caps requests per remote-IP. The middleware is
//     auto-applied to the public mux at [app.PhaseRateLimit] so
//     hostile clients are cheap-rejected before any deeper
//     middleware (signed-request crypto, auth, tenant budget)
//     runs.
//   - [KeyedModule] caps requests per arbitrary key (api key, user
//     ID, tenant ID). No middleware is auto-installed; consumers
//     reach for it via [KeyedLimiter] inside their RouterFunc and
//     wrap routes individually.
//
// Both modules satisfy the [app.RateLimitDeclarer] capability so
// the always-on Builder validator counts them as an explicit
// "yes, we ARE rate-limiting" declaration.
//
// The kit insists on an explicit choice: register [IPModule]
// and/or [KeyedModule], or call [app.Builder.WithoutRateLimit]
// to acknowledge the un-throttled posture. Builder.Validate
// rejects any third option.
package ratelimit

import (
	"context"
	"time"

	"github.com/bds421/rho-kit/app/v2"
	mwrl "github.com/bds421/rho-kit/httpx/v2/middleware/ratelimit"
	"github.com/bds421/rho-kit/observability/v2/health"
	"github.com/bds421/rho-kit/observability/v2/promutil"
)

// Resource keys.
const (
	// ResourceIPKey is the [app.Infrastructure.Resource] key under
	// which [IPModule] publishes its *mwrl.RateLimiter for per-
	// route overrides (tightening /admin while the auto-applied
	// limiter handles the public mux baseline).
	ResourceIPKey = "github.com/bds421/rho-kit/app/ratelimit.ip"
	// ResourceKeyedMapKey is the [app.Infrastructure.Resource] key
	// under which every [KeyedModule] cooperatively appends its
	// limiter. The value is a map[string]*mwrl.KeyedRateLimiter.
	ResourceKeyedMapKey = "github.com/bds421/rho-kit/app/ratelimit.keyed"
)

// IPModule returns an [app.Module] that registers a per-IP rate
// limiter, attaches its sweeper to the lifecycle runner, and
// contributes [mwrl.Middleware] at [app.PhaseRateLimit] so the
// public mux is auto-protected.
//
// Panics if requests <= 0 or window <= 0.
func IPModule(requests int, window time.Duration) app.Module {
	if requests <= 0 {
		panic("app/ratelimit: IPModule requires a positive request limit")
	}
	if window <= 0 {
		panic("app/ratelimit: IPModule requires a positive window")
	}
	return &ipModule{requests: requests, window: window}
}

type ipModule struct {
	requests int
	window   time.Duration

	limiter *mwrl.RateLimiter
}

func (m *ipModule) Name() string                                  { return "ratelimit-ip" }
func (m *ipModule) DeclaresRateLimit()                            {}
func (m *ipModule) HealthChecks() []health.DependencyCheck        { return nil }
func (m *ipModule) Stop(_ context.Context) error                  { return nil }

func (m *ipModule) Init(_ context.Context, mc app.ModuleContext) error {
	metrics := mwrl.NewMetrics()
	m.limiter = mwrl.NewRateLimiter(m.requests, m.window,
		mwrl.WithMetrics(metrics),
		mwrl.WithLimiterName("ip"),
	)
	mc.Runner.Add("rate-limiter-cleanup", m.limiter)
	return nil
}

func (m *ipModule) Populate(infra *app.Infrastructure) {
	if m.limiter != nil {
		infra.SetResource(ResourceIPKey, m.limiter)
	}
}

func (m *ipModule) PublicMiddleware() []app.PhasedMiddleware {
	if m.limiter == nil {
		return nil
	}
	return []app.PhasedMiddleware{{
		Phase: app.PhaseRateLimit,
		Func:  mwrl.Middleware(m.limiter),
	}}
}

// KeyedModule returns an [app.Module] that registers a keyed rate
// limiter under name and exposes it via [KeyedLimiter]. Unlike
// [IPModule] no middleware is auto-installed — the keyed limiter
// is for explicit per-route use (e.g., rejecting bursty API keys
// before the request body is read).
//
// Multiple [KeyedModule]s with distinct names are supported; the
// underlying resource map indexes them by name. Duplicate names
// panic at registration.
//
// Panics if name is empty or non-metric-safe, or requests <= 0
// or window <= 0.
func KeyedModule(name string, requests int, window time.Duration) app.Module {
	if name == "" {
		panic("app/ratelimit: KeyedModule requires a non-empty name")
	}
	if err := promutil.ValidateStaticLabelValue("keyed rate limiter name", name); err != nil {
		panic("app/ratelimit: KeyedModule requires a metric-safe static name")
	}
	if requests <= 0 {
		panic("app/ratelimit: KeyedModule requires a positive request limit")
	}
	if window <= 0 {
		panic("app/ratelimit: KeyedModule requires a positive window")
	}
	return &keyedModule{name: name, requests: requests, window: window}
}

type keyedModule struct {
	name     string
	requests int
	window   time.Duration

	limiter *mwrl.KeyedRateLimiter
}

func (m *keyedModule) Name() string                                  { return "ratelimit-keyed-" + m.name }
func (m *keyedModule) DeclaresRateLimit()                            {}
func (m *keyedModule) HealthChecks() []health.DependencyCheck        { return nil }
func (m *keyedModule) Stop(_ context.Context) error                  { return nil }

func (m *keyedModule) Init(_ context.Context, mc app.ModuleContext) error {
	metrics := mwrl.NewMetrics()
	m.limiter = mwrl.NewKeyedRateLimiter(m.requests, m.window,
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
	// KeyedModule registrations stack rather than overwrite each
	// other. Existing map (if any) is read-mutated; absent map
	// path creates a fresh one. Module Populate is called
	// sequentially in registration order, so the read/write race
	// only exists across modules contributing to the same key,
	// not within a single Populate call.
	var m2 map[string]*mwrl.KeyedRateLimiter
	if existing, ok := infra.Resource(ResourceKeyedMapKey); ok {
		m2, _ = existing.(map[string]*mwrl.KeyedRateLimiter)
	}
	if m2 == nil {
		m2 = make(map[string]*mwrl.KeyedRateLimiter)
		infra.SetResource(ResourceKeyedMapKey, m2)
	}
	m2[m.name] = m.limiter
}

// IPLimiter returns the per-IP rate limiter published by
// [IPModule], or nil if [IPModule] was not registered.
func IPLimiter(infra app.Infrastructure) *mwrl.RateLimiter {
	v, ok := infra.Resource(ResourceIPKey)
	if !ok {
		return nil
	}
	l, _ := v.(*mwrl.RateLimiter)
	return l
}

// KeyedLimiter returns the keyed rate limiter registered under
// name via [KeyedModule], or nil if no module registered that
// name.
func KeyedLimiter(infra app.Infrastructure, name string) *mwrl.KeyedRateLimiter {
	v, ok := infra.Resource(ResourceKeyedMapKey)
	if !ok {
		return nil
	}
	m, _ := v.(map[string]*mwrl.KeyedRateLimiter)
	if m == nil {
		return nil
	}
	return m[name]
}

