// asvs: V14.1.1, V14.4.1, V9.1.1
package app

import (
	"fmt"

	"github.com/bds421/rho-kit/security/v2/netutil"
)

// isLoopbackHost delegates to [netutil.IsLoopbackHost] so app validators and
// transport-safety checks share one definition of loopback (FR-010).
func isLoopbackHost(host string) bool {
	return netutil.IsLoopbackHost(host)
}

// Validate checks for common configuration mistakes before startup.
// Callers may use this directly, but Run calls it automatically.
//
// All checks run unconditionally. The kit does not have a development
// mode — production safety is the only mode. Each tightening can be
// individually relaxed via an explicit opt-out registered on the
// owning module (e.g. [http.WithoutTLS], [http.WithInternalNonLoopback]
// from app/http, and [jwt.WithoutIssuer], [jwt.WithoutAudience] from
// app/jwt). Those opt-outs are deliberate, documented declarations —
// they are not gated on KIT_ENV.
func (b *Builder) Validate() error {
	if b == nil {
		return fmt.Errorf("builder is nil")
	}

	if err := b.cfg.ValidateBase(); err != nil {
		return err
	}

	// Rate-limit per-module argument validation is enforced at
	// app/ratelimit.IP / Keyed construction (panics on
	// invalid requests / window / name).

	// TLSReloadOnSignal only makes sense alongside the reloading
	// TLS source — without the source there is nothing to reload.
	// Reject at construction rather than discover the misconfiguration
	// at the first signal delivery.
	// Resolve early so TLS-reload coherence checks read the same
	// config as Run.
	tlsHTTPCfg := resolveHTTPConfig(b.modules)
	if len(tlsHTTPCfg.tlsReloadSignals) > 0 && !tlsHTTPCfg.reloadingTLSActive {
		return fmt.Errorf("TLSReloadOnSignal requires ReloadingTLS")
	}

	// H-4 / R3-H: TenantBudget-vs-MultiTenant cross-checks moved to
	// app/budget.Module.Init (it looks up the registered tenant
	// module via ModuleContext.LookupModule and reads policy via
	// the TenantPolicyProvider capability).

	return b.validateProductionSafety()
}

// validateProductionSafety runs the always-on production-shape
// tightenings: JWT issuer/audience pinning, TLS, internal-host loopback,
// Postgres TLS sslmode, and tracing sample-rate cap. Each is
// individually relaxable via an explicit Without*() opt-out.
func (b *Builder) validateProductionSafety() error {
	// JWT issuer/audience pinning is enforced at app/jwt.Module
	// construction (panics when neither WithIssuer/WithoutIssuer nor
	// WithAudience/WithoutAudience is supplied); no Builder check
	// is needed anymore.

	// Resolve the effective HTTP configuration: prefer an
	// [HTTPConfigProvider] module (typically app/http.Module), fall
	// back to the legacy Builder.* fields during the migration
	// window.
	httpCfg := resolveHTTPConfig(b.modules)

	// C-2: TLS must be configured. Partial TLSConfig silently falls back
	// to plaintext HTTP (see netutil.TLSConfig.Enabled). Operators who
	// terminate TLS at an external proxy must opt in explicitly via
	// http.WithoutTLS().
	if !b.cfg.TLS.Enabled() && !httpCfg.allowPlaintext {
		return fmt.Errorf("TLS must be configured (TLS_CA_CERT, TLS_CERT, TLS_KEY) or call http.WithoutTLS() for services fronted by an external TLS terminator — partial configuration silently falls back to plaintext HTTP")
	}

	// Lens F A.5: a Builder.Run() call that declares no rate limiter at
	// all is a silent foot-gun: every other Builder security control
	// (TLS, JWT issuer / audience, internal-host loopback) fails-loud
	// when unconfigured, but the rate limiter used to default to "none"
	// and let a single hostile client saturate the public listener.
	// Pin the affirmative-declaration contract here: callers must pick
	// WithIPRateLimit, WithKeyedRateLimit, or the explicit
	// WithoutRateLimit opt-out for traffic-bounded services.
	if !b.hasRateLimitDeclaration() && !b.allowNoRateLimit {
		return fmt.Errorf("rate limiting must be declared explicitly: register ratelimit.IP from app/ratelimit (mux-wide), or call WithoutRateLimit for services whose traffic is bounded by another control (mTLS peer set, upstream gateway limit, internal cron worker). Note: ratelimit.Keyed alone does not satisfy this gate — it installs no public-mux middleware")
	}

	// C-1 + FR-010 [HIGH]: the internal ops port exposes /metrics,
	// /healthz, /ready without authentication. Pre-fix, the validator
	// only rejected wildcard binds (0.0.0.0, [::]), so any specific
	// non-loopback IP (10.0.0.5, a hostname that resolves to a
	// routable interface, etc.) silently passed. Now the default
	// requires loopback; non-loopback binds — wildcard, private,
	// public — all need explicit AllowInternalNonLoopback.
	if !isLoopbackHost(b.cfg.Internal.Host) && !httpCfg.allowInternalNonLoopback {
		return fmt.Errorf("Internal.Host is not loopback — exposes unauthenticated /metrics on a routable interface; bind to 127.0.0.1 / localhost / ::1, or call http.WithInternalNonLoopback() when network isolation is enforced")
	}

	// Postgres TLS validation lives inside the pgx package's Connect — by
	// the time the pool is opened, the DSN sslmode is checked and the
	// connection fails closed if it would silently degrade. The Builder
	// does not pre-parse the DSN here because pgx is the single source
	// of truth for what counts as a hardened TLS configuration.

	// Tracing sample-rate validation lives in app/tracing.Module's
	// constructor. The Builder no longer holds tracing config directly,
	// so the always-on rate cap is enforced at adapter construction time.

	return nil
}
