package app

import (
	"fmt"
	"strings"
)

// Validate checks for common configuration mistakes before startup.
// Callers may use this directly, but Run calls it automatically.
//
// All checks run unconditionally. The kit does not have a development
// mode — production safety is the only mode. Each tightening can be
// individually relaxed via an explicit Without*() opt-out (e.g.
// [Builder.WithoutTLS], [Builder.WithInternalNonLoopback],
// [Builder.WithoutJWTIssuer], [Builder.WithoutJWTAudience]). Those
// opt-outs are deliberate, documented declarations — they are not
// gated on KIT_ENV.
func (b *Builder) Validate() error {
	if b == nil {
		return fmt.Errorf("builder is nil")
	}

	if b.dbDriver != nil && b.dbPoolCfg == nil {
		return fmt.Errorf("database pool config is required when a database is configured")
	}
	if b.dbDriver != nil && b.pgxCfg != nil {
		return fmt.Errorf("WithPgx and WithPostgres/WithMySQL are mutually exclusive — pick one DB driver")
	}
	if b.dbMetrics && b.dbDriver == nil {
		return fmt.Errorf("database metrics require a configured database")
	}
	if b.seedFn != nil && b.dbDriver == nil {
		return fmt.Errorf("seed requires a configured database")
	}
	if b.migrationsDir != nil && b.dbDriver == nil {
		return fmt.Errorf("migrations require a configured database (use WithMySQL or WithPostgres)")
	}
	if b.criticalBroker && b.mqURL == "" {
		return fmt.Errorf("critical broker requires a RabbitMQ URL")
	}
	if b.ipRateRequests > 0 && b.ipRateWindow <= 0 {
		return fmt.Errorf("IP rate limit window must be > 0 when rate limiting is enabled")
	}
	if b.ipRateWindow > 0 && b.ipRateRequests < 1 {
		return fmt.Errorf("IP rate limit requests must be > 0 when window is set")
	}
	for _, spec := range b.keyedLimiters {
		if spec.name == "" {
			return fmt.Errorf("keyed rate limiter name is required")
		}
		if spec.requests <= 0 {
			return fmt.Errorf("keyed rate limiter %q must allow at least 1 request", spec.name)
		}
		if spec.window <= 0 {
			return fmt.Errorf("keyed rate limiter %q window must be > 0", spec.name)
		}
	}

	// H-4: WithTenantBudget without WithMultiTenant fails open silently —
	// the default TenantKeyFunc returns no key, the budget middleware
	// short-circuits, and no enforcement happens.
	if b.budgetSpec != nil && b.tenantSpec == nil {
		return fmt.Errorf("WithTenantBudget requires WithMultiTenant — without it the default TenantKeyFunc returns no key and budget enforcement is silently skipped")
	}

	return b.validateProductionSafety()
}

// validateProductionSafety runs the always-on production-shape
// tightenings: JWT issuer/audience pinning, TLS, internal-host loopback,
// Postgres TLS sslmode, and tracing sample-rate cap. Each is
// individually relaxable via an explicit Without*() opt-out.
func (b *Builder) validateProductionSafety() error {
	// JWT: must specify issuer or explicitly opt out via WithoutJWTIssuer.
	if b.jwksURL != "" && b.jwtIssuer == "" && !b.jwtAllowAnyIssue {
		return fmt.Errorf("WithJWT requires WithJWTIssuer or the explicit WithoutJWTIssuer opt-out")
	}

	// H-5: JWT audience pinning is the standard confused-deputy mitigation
	// (RFC 7519 §4.1.3). Without it, a token minted for a sibling service
	// that trusts the same JWKS is silently accepted.
	if b.jwksURL != "" && b.jwtAudience == "" && !b.jwtAllowAnyAudience {
		return fmt.Errorf("WithJWT requires WithJWTAudience or the explicit WithoutJWTAudience opt-out (RFC 7519 confused-deputy mitigation)")
	}

	// C-2: TLS must be configured. Partial TLSConfig silently falls back
	// to plaintext HTTP (see netutil.TLSConfig.Enabled). Operators who
	// terminate TLS at an external proxy must opt in explicitly.
	if !b.cfg.TLS.Enabled() && !b.allowPlaintext {
		return fmt.Errorf("TLS must be configured (TLS_CA_CERT, TLS_CERT, TLS_KEY) or call WithoutTLS for services fronted by an external TLS terminator — partial configuration silently falls back to plaintext HTTP")
	}

	// C-1: the internal ops port exposes /metrics without authentication.
	// Binding to 0.0.0.0 leaks Prometheus labels (route patterns, tenant
	// IDs) to anyone on the network. Operators with strict network
	// isolation must opt in explicitly via WithInternalNonLoopback.
	if b.cfg.Internal.Host == "0.0.0.0" && !b.allowInternalNonLoopback {
		return fmt.Errorf("Internal.Host=\"0.0.0.0\" exposes unauthenticated /metrics; bind to a loopback or internal interface, or call WithInternalNonLoopback when network isolation is enforced")
	}

	// Postgres: sslmode must be a TLS-enforcing mode.
	if b.dbDriver != nil && b.dbCfg != nil && isPostgresDriver(b.dbDriver) {
		mode := strings.ToLower(b.dbCfg.Option("sslmode", ""))
		switch mode {
		case "require", "verify-ca", "verify-full":
			// ok
		case "":
			return fmt.Errorf("Postgres sslmode must be set (require/verify-ca/verify-full); none configured")
		case "allow", "prefer", "disable":
			return fmt.Errorf("Postgres sslmode=%q does not fail closed on TLS handshake error; use require/verify-ca/verify-full", mode)
		default:
			return fmt.Errorf("Postgres sslmode=%q is unrecognized", mode)
		}
	}

	// Tracing: full sampling is a collector-cost foot-gun.
	if b.tracingCfg != nil && b.tracingCfg.SampleRate > 0.1 {
		return fmt.Errorf("tracing SampleRate=%.2f exceeds 0.1; lower the rate and override per-trace via the OTel SDK if you need bursts", b.tracingCfg.SampleRate)
	}

	return nil
}

// isPostgresDriver inspects the driver type without taking a hard
// dependency on the gormpostgres package's type at the validate
// layer. The Driver interface's String/Name signature gives us enough
// disambiguation for both kit-shipped drivers.
func isPostgresDriver(d any) bool {
	if d == nil {
		return false
	}
	type named interface{ Name() string }
	if n, ok := d.(named); ok {
		return strings.Contains(strings.ToLower(n.Name()), "postgres")
	}
	type stringer interface{ String() string }
	if s, ok := d.(stringer); ok {
		return strings.Contains(strings.ToLower(s.String()), "postgres")
	}
	return strings.Contains(strings.ToLower(fmt.Sprintf("%T", d)), "postgres")
}
