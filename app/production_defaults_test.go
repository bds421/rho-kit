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

func newProdBuilder() *Builder {
	// The base prod builder used by the legacy tests targets the JWT,
	// Postgres, and tracing tightenings. The TLS / internal-host /
	// audience checks are exercised in dedicated tests, so opt out of
	// them here to keep each test single-purpose.
	return New("test", "v1", BaseConfig{}).
		WithProductionDefaults().
		WithProductionAllowPlaintext().
		WithJWTAllowAnyAudience()
}

func TestProductionDefaults_NoOpWithoutJWT(t *testing.T) {
	// A service that doesn't enable JWT must still pass validation.
	require.NoError(t, newProdBuilder().Validate())
}

func TestProductionDefaults_RejectsJWTWithoutIssuer(t *testing.T) {
	b := newProdBuilder().
		WithJWT("https://example.com/.well-known/jwks.json")
	err := b.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "WithJWTIssuer")
}

func TestProductionDefaults_AcceptsJWTWithIssuer(t *testing.T) {
	b := newProdBuilder().
		WithJWT("https://example.com/.well-known/jwks.json").
		WithJWTIssuer("https://issuer.example.com")
	require.NoError(t, b.Validate())
}

func TestProductionDefaults_AcceptsJWTWithAllowAnyIssuer(t *testing.T) {
	b := newProdBuilder().
		WithJWT("https://example.com/.well-known/jwks.json").
		WithJWTAllowAnyIssuer()
	require.NoError(t, b.Validate())
}

func TestProductionDefaults_PostgresMustHaveSSLMode(t *testing.T) {
	cfg := sqldb.Config{
		Host:     "localhost",
		Port:     5432,
		User:     "u",
		Password: "p",
		Name:     "db",
		// No sslmode — production validation must reject.
	}
	b := newProdBuilder().WithPostgres(cfg, sqldb.DefaultPool())
	err := b.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sslmode")
}

func TestProductionDefaults_PostgresRejectsLooseSSLMode(t *testing.T) {
	cfg := sqldb.Config{
		Host:     "localhost",
		Port:     5432,
		User:     "u",
		Password: "p",
		Name:     "db",
		Options:  map[string]string{"sslmode": "prefer"},
	}
	b := newProdBuilder().WithPostgres(cfg, sqldb.DefaultPool())
	err := b.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fail closed")
}

func TestProductionDefaults_PostgresAcceptsRequire(t *testing.T) {
	cfg := sqldb.Config{
		Host:     "localhost",
		Port:     5432,
		User:     "u",
		Password: "p",
		Name:     "db",
		Options:  map[string]string{"sslmode": "require"},
	}
	b := newProdBuilder().WithPostgres(cfg, sqldb.DefaultPool())
	require.NoError(t, b.Validate())
}

func TestProductionDefaults_TracingSampleRateCapped(t *testing.T) {
	b := newProdBuilder().WithTracing(tracing.Config{ServiceName: "test", SampleRate: 1.0})
	err := b.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SampleRate")
}

func TestProductionDefaults_TracingAcceptsLowSampleRate(t *testing.T) {
	b := newProdBuilder().WithTracing(tracing.Config{ServiceName: "test", SampleRate: 0.05})
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

func TestProductionDefaults_RejectsExposedInternal(t *testing.T) {
	cfg := BaseConfig{
		Internal: InternalConfig{Host: "0.0.0.0", Port: 9090},
		TLS:      validTLSForTest(),
	}
	b := New("svc", "v1", cfg).
		WithProductionDefaults().
		WithJWTAllowAnyAudience()
	err := b.Validate()
	require.Error(t, err, "exposed internal port must fail validation")
	assert.Contains(t, err.Error(), "Internal.Host")
	assert.Contains(t, err.Error(), "WithProductionInternalExposed")
}

func TestWithProductionInternalExposed_AcceptsOptIn(t *testing.T) {
	cfg := BaseConfig{
		Internal: InternalConfig{Host: "0.0.0.0", Port: 9090},
		TLS:      validTLSForTest(),
	}
	b := New("svc", "v1", cfg).
		WithProductionDefaults().
		WithProductionInternalExposed().
		WithJWTAllowAnyAudience()
	require.NoError(t, b.Validate(),
		"WithProductionInternalExposed must allow Internal.Host=0.0.0.0")
}

// --- C-2: WithProductionDefaults requires TLS ---

func TestProductionDefaults_RequiresTLS(t *testing.T) {
	b := New("svc", "v1", BaseConfig{}).
		WithProductionDefaults().
		WithJWTAllowAnyAudience()
	err := b.Validate()
	require.Error(t, err, "production validator must reject empty TLS config")
	assert.Contains(t, err.Error(), "TLS")
	assert.Contains(t, err.Error(), "WithProductionAllowPlaintext")
}

func TestWithProductionAllowPlaintext_AcceptsOptIn(t *testing.T) {
	b := New("svc", "v1", BaseConfig{}).
		WithProductionDefaults().
		WithProductionAllowPlaintext().
		WithJWTAllowAnyAudience()
	require.NoError(t, b.Validate(),
		"WithProductionAllowPlaintext must allow empty TLS config")
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
		WithMultiTenant(nil, true).
		WithTenantBudget(&stubBudget{})
	require.NoError(t, b.Validate(),
		"WithTenantBudget paired with WithMultiTenant must pass validation")
}

// --- H-5: WithProductionDefaults requires WithJWTAudience ---

func TestProductionDefaults_RequiresJWTAudience(t *testing.T) {
	b := New("svc", "v1", BaseConfig{TLS: validTLSForTest()}).
		WithProductionDefaults().
		WithJWT("https://example.com/.well-known/jwks.json").
		WithJWTIssuer("https://issuer.example.com")
	err := b.Validate()
	require.Error(t, err, "production must require WithJWTAudience to mitigate confused-deputy")
	assert.Contains(t, err.Error(), "WithJWTAudience")
}

func TestProductionDefaults_AcceptsJWTAudience(t *testing.T) {
	b := New("svc", "v1", BaseConfig{TLS: validTLSForTest()}).
		WithProductionDefaults().
		WithJWT("https://example.com/.well-known/jwks.json").
		WithJWTIssuer("https://issuer.example.com").
		WithJWTAudience("svc")
	require.NoError(t, b.Validate(),
		"WithJWTAudience must satisfy the production audience check")
}

func TestProductionDefaults_AcceptsAllowAnyAudienceOptIn(t *testing.T) {
	b := New("svc", "v1", BaseConfig{TLS: validTLSForTest()}).
		WithProductionDefaults().
		WithJWT("https://example.com/.well-known/jwks.json").
		WithJWTIssuer("https://issuer.example.com").
		WithJWTAllowAnyAudience()
	require.NoError(t, b.Validate(),
		"WithJWTAllowAnyAudience must satisfy the production audience check")
}

// validTLSForTest returns a TLSConfig that reports Enabled() == true.
// The paths are placeholders — the production-defaults validator only
// inspects Enabled(), it does not load the files.
func validTLSForTest() netutil.TLSConfig {
	return netutil.TLSConfig{
		CACert: "/dev/null/ca.pem",
		Cert:   "/dev/null/cert.pem",
		Key:    "/dev/null/key.pem",
	}
}
