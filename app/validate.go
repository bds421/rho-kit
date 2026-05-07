package app

import (
	"fmt"
	"strings"
)

// Validate checks for common configuration mistakes before startup.
// Callers may use this directly, but Run calls it automatically.
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

	if b.productionDefaults {
		if err := b.validateProductionDefaults(); err != nil {
			return err
		}
	}
	return nil
}

// validateProductionDefaults runs the [Builder.WithProductionDefaults]
// tightenings. Returns nil when every required knob has been set.
func (b *Builder) validateProductionDefaults() error {
	// JWT: must specify issuer or explicitly opt-out.
	if b.jwksURL != "" && b.jwtIssuer == "" && !b.jwtAllowAnyIssue {
		return fmt.Errorf("production: WithJWT requires WithJWTIssuer or the explicit WithJWTAllowAnyIssuer opt-out")
	}

	// Postgres: sslmode must be a TLS-enforcing mode.
	if b.dbDriver != nil && b.dbCfg != nil && isPostgresDriver(b.dbDriver) {
		mode := strings.ToLower(b.dbCfg.Option("sslmode", ""))
		switch mode {
		case "require", "verify-ca", "verify-full":
			// ok
		case "":
			return fmt.Errorf("production: Postgres sslmode must be set (require/verify-ca/verify-full); none configured")
		case "allow", "prefer", "disable":
			return fmt.Errorf("production: Postgres sslmode=%q does not fail closed on TLS handshake error; use require/verify-ca/verify-full", mode)
		default:
			return fmt.Errorf("production: Postgres sslmode=%q is unrecognized", mode)
		}
	}

	// Tracing: full sampling is a collector-cost foot-gun in prod.
	if b.tracingCfg != nil && b.tracingCfg.SampleRate > 0.1 {
		return fmt.Errorf("production: tracing SampleRate=%.2f exceeds 0.1; set lower or pass WithProductionDefaults() before WithTracing(...) and override per-trace via the OTel SDK", b.tracingCfg.SampleRate)
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
