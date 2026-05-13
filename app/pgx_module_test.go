package app

import (
	"context"
	"log/slog"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pgxbackend "github.com/bds421/rho-kit/infra/sqldb/pgx/v2"
	"github.com/bds421/rho-kit/runtime/v2/lifecycle"
)

func TestNewPgxModule_PanicsOnEmptyDSN(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic on empty DSN")
		}
		assert.Contains(t, r, "WithPostgres")
	}()
	newPgxModule(pgxbackend.Config{}, nil)
}

func TestWithPostgres_PanicsOnEmptyDSN(t *testing.T) {
	b := New("test", "v1", BaseConfig{})
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on empty DSN")
		}
	}()
	b.WithPostgres(pgxbackend.Config{})
}

func TestWithPostgres_RegistersOnBuilder(t *testing.T) {
	b := New("test", "v1", BaseConfig{}).WithPostgres(pgxbackend.Config{
		DSN: "postgres://u:p@h/db?sslmode=require",
	})
	require.NotNil(t, b.pgxCfg)
	assert.NotEmpty(t, b.pgxCfg.DSN)
}

func TestValidate_AllowsPostgresAlone(t *testing.T) {
	b := New("test", "v1", validBaseConfig()).
		WithoutTLS().
		WithoutJWTAudience().
		WithPostgres(pgxbackend.Config{DSN: "postgres://u:p@h/db?sslmode=require"})
	require.NoError(t, b.Validate())
}

// TestPgxModule_RegistersPoolStatsCollector verifies the module wires the
// pgxpool collector into the configured registerer at Init() time. The
// collector is reachable as a gathered metric family, which is what proves
// downstream Prometheus dashboards see the gauges without an extra opt-in
// in user code.
func TestPgxModule_RegistersPoolStatsCollector(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := newPgxModule(pgxbackend.Config{
		DSN:                            "postgres://u:p@127.0.0.1:1/db?sslmode=disable",
		AllowPlaintextLoopbackForTests: true,
	}, nil)
	m.registerer = reg

	mc := ModuleContext{
		Logger: slog.Default(),
		Runner: lifecycle.NewRunner(slog.Default()),
	}
	err := m.Init(context.Background(), mc)
	require.NoError(t, err)
	t.Cleanup(func() { _ = m.Stop(context.Background()) })

	families, err := reg.Gather()
	require.NoError(t, err)
	names := make(map[string]bool, len(families))
	for _, mf := range families {
		names[mf.GetName()] = true
	}
	assert.True(t, names["pgx_pool_max_conns"], "pgx_pool_max_conns missing from registry")
	assert.True(t, names["pgx_pool_total_conns"], "pgx_pool_total_conns missing from registry")
	assert.True(t, names["pgx_pool_acquire_count_total"], "pgx_pool_acquire_count_total missing from registry")
}
