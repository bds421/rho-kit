package app

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// isUnspecifiedHost reports whether host names an "all-interfaces"
// bind. Catches every form net.Listen accepts as the IPv4 wildcard:
//
//   - IPv4 0.0.0.0 in canonical form
//   - IPv4 short / leading-zero forms: "0", "0.0", "0.0.0", "00.00.00.00",
//     "000.000.000.000", "0.00.00.00"
//   - IPv4 hex / octal forms: "0x0", "0X00000000", "0x0.0x0.0x0.0x0",
//     "00", "000" (octal interpretation)
//   - IPv6 [::], ::, 0:0:0:0:0:0:0:0 in canonical and bracket-wrapped form
//   - IPv6-mapped IPv4 wildcard ::ffff:0.0.0.0
//
// Empty host is NOT flagged here because [InternalConfig.Addr] defaults
// empty to "127.0.0.1" — an unset INTERNAL_HOST resolves to loopback
// at listen time.
//
// The fix walks numeric segments with strconv.ParseUint base-0, which
// auto-detects decimal / octal / hex per Go's standard rules. This
// catches the hex-encoded zero forms net.Listen accepts but
// net.ParseIP rejects (audit finding N-7). The audit's recommended
// alternative (net.ResolveTCPAddr) would also work but does DNS
// lookup as a side effect — the numeric-only walk has no I/O.
func isUnspecifiedHost(host string) bool {
	if host == "" {
		return false
	}
	// Strip square brackets that might wrap an IPv6 literal.
	stripped := strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
	if ip := net.ParseIP(stripped); ip != nil && ip.IsUnspecified() {
		return true
	}
	if isAllZeroIPv4Numeric(stripped) {
		return true
	}
	return false
}

// isAllZeroIPv4Numeric reports whether s is an IPv4-style numeric form
// that resolves to all-zero (and thus binds to 0.0.0.0 under
// net.Listen). Each dotted segment must parse as a non-negative integer
// with value zero. Accepts decimal, octal (leading 0), and hex
// (0x/0X prefix) per strconv.ParseUint base 0 conventions:
//
//	"0", "00", "000"          → all decimal/octal zeros
//	"0x0", "0X00", "0x000000" → hex zeros
//	"0.0", "0.0.0", "0.0.0.0" → up to 4 segments
//	"0x0.0", "0X0.0x0.0x0.0x0", mixed forms
//
// 5+ segments are rejected (net.Listen routes those through DNS lookup
// rather than IPv4 parsing — audit finding N-7's "5+ segment" branch).
func isAllZeroIPv4Numeric(s string) bool {
	if s == "" {
		return false
	}
	parts := strings.Split(s, ".")
	if len(parts) > 4 {
		return false
	}
	for _, p := range parts {
		if p == "" {
			return false
		}
		// strconv.ParseUint with base 0 auto-detects:
		//   "0x" / "0X" prefix → base 16
		//   "0" prefix          → base 8
		//   otherwise           → base 10
		// Accept any encoding whose value is zero.
		v, err := strconv.ParseUint(p, 0, 64)
		if err != nil || v != 0 {
			return false
		}
	}
	return true
}

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
	// Binding to any unspecified address (0.0.0.0, [::], "") leaks
	// Prometheus labels (route patterns, tenant IDs) to anyone on the
	// network. Operators with strict network isolation must opt in
	// explicitly via WithInternalNonLoopback.
	if isUnspecifiedHost(b.cfg.Internal.Host) && !b.allowInternalNonLoopback {
		return fmt.Errorf("Internal.Host=%q exposes unauthenticated /metrics on all interfaces; bind to a loopback or internal interface, or call WithInternalNonLoopback when network isolation is enforced", b.cfg.Internal.Host)
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
