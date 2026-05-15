// asvs: V14.1.1, V14.4.1, V9.1.1
package app

import (
	"fmt"
	"net"
	"strings"
)

// isLoopbackHost reports whether host resolves exclusively to
// loopback (127.0.0.0/8 or ::1). Used by the production-safety
// validator to enforce that the internal ops port (which serves
// /metrics, /healthz, /ready without authentication) binds only to
// the loopback interface unless [Builder.AllowInternalNonLoopback]
// has been called.
//
// FR-010 [HIGH]: pre-2.0 the validator only rejected unspecified
// (wildcard) hosts, so INTERNAL_HOST=10.0.0.5 (or any other reachable
// interface) passed silently and exposed /metrics on the network.
// The new contract is: only loopback binds pass the default check;
// everything else — wildcard, private-network, public IP, or
// hostname that resolves outside loopback — requires
// AllowInternalNonLoopback.
//
// Empty host counts as loopback because [InternalConfig.Addr]
// defaults empty to "127.0.0.1" at listen time. Bracket-only IPv6
// forms ("[]", "[", "]") collapse to empty after stripping but DO
// resolve to the IPv6 wildcard at listen time, so they're flagged
// as non-loopback.
//
// net.ResolveTCPAddr does NOT do DNS lookup for numeric host
// strings; it only invokes the resolver when the input is a
// hostname, in which case the validator (run once at boot) is the
// right place to do it — a hostname that resolves to a non-loopback
// IS exposing /metrics on the network and should be flagged.
func isLoopbackHost(host string) bool {
	if host == "" {
		// Empty defaults to 127.0.0.1 in the listener config.
		return true
	}
	// Strip square brackets that may wrap an IPv6 literal —
	// net.JoinHostPort docs explicitly say "host does not contain
	// square brackets", and passing "[::]" produces "[[::]]:0"
	// which fails to parse.
	stripped := strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
	if stripped == "" {
		// Bracket-only forms ("[]", "[", "]") strip to empty BUT
		// net.Listen accepts "[]:port" as the IPv6 wildcard and binds
		// [::]:port — that's a non-loopback exposure (audit finding
		// N-10). Treat post-strip empty as non-loopback so the
		// default validator rejects it.
		return false
	}
	addr, err := net.ResolveTCPAddr("tcp", net.JoinHostPort(stripped, "0"))
	if err != nil {
		// Resolution failed (typo, unresolvable hostname) — fail
		// closed: not provably loopback.
		return false
	}
	return addr.IP != nil && addr.IP.IsLoopback()
}

// Validate checks for common configuration mistakes before startup.
// Callers may use this directly, but Run calls it automatically.
//
// All checks run unconditionally. The kit does not have a development
// mode — production safety is the only mode. Each tightening can be
// individually relaxed via an explicit Without*() opt-out (e.g.
// [Builder.WithoutTLS], [Builder.AllowInternalNonLoopback],
// [Builder.WithoutJWTIssuer], [Builder.WithoutJWTAudience]). Those
// opt-outs are deliberate, documented declarations — they are not
// gated on KIT_ENV.
func (b *Builder) Validate() error {
	if b == nil {
		return fmt.Errorf("builder is nil")
	}

	if err := b.cfg.ValidateBase(); err != nil {
		return err
	}

	// Rate-limit per-module argument validation is enforced at
	// app/ratelimit.IPModule / KeyedModule construction (panics on
	// invalid requests / window / name).

	// TLSReloadOnSignal only makes sense alongside the reloading
	// TLS source — without the source there is nothing to reload.
	// Reject at construction rather than discover the misconfiguration
	// at the first signal delivery.
	if len(b.tlsReloadSignals) > 0 && !b.tlsReloadActive {
		return fmt.Errorf("TLSReloadOnSignal requires ReloadingTLS")
	}

	// H-4: TenantBudget without MultiTenant cannot derive the
	// default tenant key. Keep this as a startup error instead of
	// letting every request fail later at the budget middleware.
	if b.budgetSpec != nil && b.tenantSpec == nil {
		return fmt.Errorf("TenantBudget requires MultiTenant(..., required=true) so the default budget key can be derived from the tenant context")
	}

	// R3-H: TenantBudget enforces a per-tenant budget that keys on
	// the tenant ID. Pin the strict combination at construction so
	// callers do not discover missing tenant context only after the
	// request reaches the budget middleware.
	if b.budgetSpec != nil && b.tenantSpec != nil {
		if !b.tenantSpec.required {
			return fmt.Errorf("TenantBudget requires MultiTenant(..., required=true) because budget enforcement needs a tenant key on every charged request")
		}
		if b.tenantSpec.allowMissingTenantOnSafeMethods {
			return fmt.Errorf("TenantBudget is incompatible with AllowMissingTenantOnSafeMethods because budget enforcement needs a tenant key on every charged request")
		}
	}

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

	// C-2: TLS must be configured. Partial TLSConfig silently falls back
	// to plaintext HTTP (see netutil.TLSConfig.Enabled). Operators who
	// terminate TLS at an external proxy must opt in explicitly.
	if !b.cfg.TLS.Enabled() && !b.allowPlaintext {
		return fmt.Errorf("TLS must be configured (TLS_CA_CERT, TLS_CERT, TLS_KEY) or call WithoutTLS for services fronted by an external TLS terminator — partial configuration silently falls back to plaintext HTTP")
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
		return fmt.Errorf("rate limiting must be declared explicitly: register a ratelimit.IPModule / ratelimit.KeyedModule from app/ratelimit, or call WithoutRateLimit for services whose traffic is bounded by another control (mTLS peer set, upstream gateway limit, internal cron worker)")
	}

	// C-1 + FR-010 [HIGH]: the internal ops port exposes /metrics,
	// /healthz, /ready without authentication. Pre-fix, the validator
	// only rejected wildcard binds (0.0.0.0, [::]), so any specific
	// non-loopback IP (10.0.0.5, a hostname that resolves to a
	// routable interface, etc.) silently passed. Now the default
	// requires loopback; non-loopback binds — wildcard, private,
	// public — all need explicit AllowInternalNonLoopback.
	if !isLoopbackHost(b.cfg.Internal.Host) && !b.allowInternalNonLoopback {
		return fmt.Errorf("Internal.Host is not loopback — exposes unauthenticated /metrics on a routable interface; bind to 127.0.0.1 / localhost / ::1, or call AllowInternalNonLoopback when network isolation is enforced")
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
