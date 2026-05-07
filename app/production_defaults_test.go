package app

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/sqldb"
	"github.com/bds421/rho-kit/infra/sqldb/gormdb/gormpostgres"
	"github.com/bds421/rho-kit/observability/tracing"
	"github.com/bds421/rho-kit/security/netutil"
)

// newSafeBuilder returns a Builder with the always-on production-safety
// validator armed and the TLS / audience opt-outs applied. The TLS,
// internal-host, and audience checks have dedicated tests below; the
// helper isolates each remaining test to a single concern.
func newSafeBuilder() *Builder {
	return New("test", "v1", BaseConfig{}).
		WithoutTLS().
		WithoutJWTAudience()
}

func TestBuilder_Validates_NoOpWithoutJWT(t *testing.T) {
	// A service that doesn't enable JWT must still pass validation.
	require.NoError(t, newSafeBuilder().Validate())
}

func TestBuilder_Validates_RejectsJWTWithoutIssuer(t *testing.T) {
	b := newSafeBuilder().
		WithJWT("https://example.com/.well-known/jwks.json")
	err := b.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "WithJWTIssuer")
}

func TestBuilder_Validates_AcceptsJWTWithIssuer(t *testing.T) {
	b := newSafeBuilder().
		WithJWT("https://example.com/.well-known/jwks.json").
		WithJWTIssuer("https://issuer.example.com")
	require.NoError(t, b.Validate())
}

func TestBuilder_Validates_AcceptsJWTWithoutJWTIssuer(t *testing.T) {
	b := newSafeBuilder().
		WithJWT("https://example.com/.well-known/jwks.json").
		WithoutJWTIssuer()
	require.NoError(t, b.Validate())
}

func TestBuilder_Validates_PostgresMustHaveSSLMode(t *testing.T) {
	cfg := sqldb.Config{
		Host:     "localhost",
		Port:     5432,
		User:     "u",
		Password: "p",
		Name:     "db",
		// No sslmode — production validation must reject.
	}
	b := newSafeBuilder().WithPostgres(cfg, sqldb.DefaultPool())
	err := b.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sslmode")
}

func TestBuilder_Validates_PostgresRejectsLooseSSLMode(t *testing.T) {
	cfg := sqldb.Config{
		Host:     "localhost",
		Port:     5432,
		User:     "u",
		Password: "p",
		Name:     "db",
		Options:  map[string]string{"sslmode": "prefer"},
	}
	b := newSafeBuilder().WithPostgres(cfg, sqldb.DefaultPool())
	err := b.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fail closed")
}

func TestBuilder_Validates_PostgresAcceptsRequire(t *testing.T) {
	cfg := sqldb.Config{
		Host:     "localhost",
		Port:     5432,
		User:     "u",
		Password: "p",
		Name:     "db",
		Options:  map[string]string{"sslmode": "require"},
	}
	b := newSafeBuilder().WithPostgres(cfg, sqldb.DefaultPool())
	require.NoError(t, b.Validate())
}

func TestBuilder_Validates_TracingSampleRateCapped(t *testing.T) {
	b := newSafeBuilder().WithTracing(tracing.Config{ServiceName: "test", SampleRate: 1.0})
	err := b.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SampleRate")
}

func TestBuilder_Validates_TracingAcceptsLowSampleRate(t *testing.T) {
	b := newSafeBuilder().WithTracing(tracing.Config{ServiceName: "test", SampleRate: 0.05})
	require.NoError(t, b.Validate())
}

func TestIsPostgresDriver(t *testing.T) {
	assert.True(t, isPostgresDriver(gormpostgres.PostgresDriver{}))
	assert.False(t, isPostgresDriver(nil))
}

// --- C-1: internal ops port must default to loopback ---

func TestInternalConfig_DefaultsToLoopback(t *testing.T) {
	// LoadBaseConfig uses environment variables; clear INTERNAL_HOST so
	// the default applies regardless of the host environment.
	t.Setenv("INTERNAL_HOST", "")
	cfg, err := LoadBaseConfig(8080)
	require.NoError(t, err)
	assert.NotEqual(t, "0.0.0.0", cfg.Internal.Addr(),
		"internal ops port must not bind to 0.0.0.0 by default — exposes /metrics publicly")
	assert.Contains(t, cfg.Internal.Addr(), "127.0.0.1",
		"internal ops port should default to loopback")
}

func TestBuilder_Validates_RejectsExposedInternal(t *testing.T) {
	cfg := BaseConfig{
		Internal: InternalConfig{Host: "0.0.0.0", Port: 9090},
		TLS:      validTLSForTest(),
	}
	b := New("svc", "v1", cfg).
		WithoutJWTAudience()
	err := b.Validate()
	require.Error(t, err, "exposed internal port must fail validation")
	assert.Contains(t, err.Error(), "Internal.Host")
	assert.Contains(t, err.Error(), "WithInternalNonLoopback")
}

func TestWithInternalNonLoopback_AcceptsOptIn(t *testing.T) {
	cfg := BaseConfig{
		Internal: InternalConfig{Host: "0.0.0.0", Port: 9090},
		TLS:      validTLSForTest(),
	}
	b := New("svc", "v1", cfg).
		WithInternalNonLoopback().
		WithoutJWTAudience()
	require.NoError(t, b.Validate(),
		"WithInternalNonLoopback must allow Internal.Host=0.0.0.0")
}

// TestBuilder_Validates_RejectsIPv6Wildcard pins the M-A audit fix:
// the C-1 check used to compare the literal string "0.0.0.0", missing
// the IPv6 wildcard [::] (and other unspecified-address forms).
// Operators setting INTERNAL_HOST=[::] would have bypassed the check
// and bound /metrics to all IPv6 interfaces.
func TestBuilder_Validates_RejectsIPv6Wildcard(t *testing.T) {
	for _, host := range []string{"::", "[::]", "0:0:0:0:0:0:0:0"} {
		t.Run(host, func(t *testing.T) {
			cfg := BaseConfig{
				Internal: InternalConfig{Host: host, Port: 9090},
				TLS:      validTLSForTest(),
			}
			b := New("svc", "v1", cfg).WithoutJWTAudience()
			err := b.Validate()
			require.Error(t, err, "IPv6 wildcard %q must fail validation", host)
			assert.Contains(t, err.Error(), "exposes unauthenticated /metrics")
		})
	}
}

// --- C-2: validator requires TLS ---

func TestBuilder_Validates_RequiresTLS(t *testing.T) {
	b := New("svc", "v1", BaseConfig{}).
		WithoutJWTAudience()
	err := b.Validate()
	require.Error(t, err, "validator must reject empty TLS config")
	assert.Contains(t, err.Error(), "TLS")
	assert.Contains(t, err.Error(), "WithoutTLS")
}

func TestWithoutTLS_AcceptsOptIn(t *testing.T) {
	b := New("svc", "v1", BaseConfig{}).
		WithoutTLS().
		WithoutJWTAudience()
	require.NoError(t, b.Validate(),
		"WithoutTLS must allow empty TLS config")
}

// --- H-4: WithTenantBudget requires WithMultiTenant ---

func TestBudget_RequiresMultiTenant(t *testing.T) {
	b := New("test", "v1", BaseConfig{}).
		WithTenantBudget(&stubBudget{})
	err := b.Validate()
	require.Error(t, err, "WithTenantBudget without WithMultiTenant must fail")
	assert.Contains(t, err.Error(), "WithMultiTenant")
}

func TestBudget_WithMultiTenant_Passes(t *testing.T) {
	b := New("test", "v1", BaseConfig{}).
		WithoutTLS().
		WithoutJWTAudience().
		WithMultiTenant(nil, true).
		WithTenantBudget(&stubBudget{})
	require.NoError(t, b.Validate(),
		"WithTenantBudget paired with WithMultiTenant must pass validation")
}

// --- H-5: validator requires WithJWTAudience ---

func TestBuilder_Validates_RequiresJWTAudience(t *testing.T) {
	b := New("svc", "v1", BaseConfig{TLS: validTLSForTest()}).
		WithJWT("https://example.com/.well-known/jwks.json").
		WithJWTIssuer("https://issuer.example.com")
	err := b.Validate()
	require.Error(t, err, "validator must require WithJWTAudience to mitigate confused-deputy")
	assert.Contains(t, err.Error(), "WithJWTAudience")
}

func TestBuilder_Validates_AcceptsJWTAudience(t *testing.T) {
	b := New("svc", "v1", BaseConfig{TLS: validTLSForTest()}).
		WithJWT("https://example.com/.well-known/jwks.json").
		WithJWTIssuer("https://issuer.example.com").
		WithJWTAudience("svc")
	require.NoError(t, b.Validate(),
		"WithJWTAudience must satisfy the audience check")
}

func TestBuilder_Validates_AcceptsWithoutJWTAudience(t *testing.T) {
	b := New("svc", "v1", BaseConfig{TLS: validTLSForTest()}).
		WithJWT("https://example.com/.well-known/jwks.json").
		WithJWTIssuer("https://issuer.example.com").
		WithoutJWTAudience()
	require.NoError(t, b.Validate(),
		"WithoutJWTAudience must satisfy the audience check")
}

// validTLSForTest returns a TLSConfig that reports Enabled() == true.
// The paths are placeholders — the production-safety validator only
// inspects Enabled(), it does not load the files.
func validTLSForTest() netutil.TLSConfig {
	return netutil.TLSConfig{
		CACert: "/dev/null/ca.pem",
		Cert:   "/dev/null/cert.pem",
		Key:    "/dev/null/key.pem",
	}
}
