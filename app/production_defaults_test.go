package app

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/sqldb"
	"github.com/bds421/rho-kit/infra/sqldb/gormdb/gormpostgres"
	"github.com/bds421/rho-kit/observability/tracing"
)

func newProdBuilder() *Builder {
	return New("test", "v1", BaseConfig{}).WithProductionDefaults()
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
