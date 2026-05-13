package postgres

import (
	"context"
	"log/slog"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/app/v2"
	pgxbackend "github.com/bds421/rho-kit/infra/sqldb/pgx/v2"
	"github.com/bds421/rho-kit/runtime/v2/lifecycle"
)

func TestModule_PanicsOnEmptyDSN(t *testing.T) {
	defer func() {
		r := recover()
		require.NotNil(t, r, "expected panic on empty DSN")
		assert.Contains(t, r, "non-empty DSN")
	}()
	_ = Module(pgxbackend.Config{})
}

func TestModule_PanicsOnNilOption(t *testing.T) {
	assert.Panics(t, func() {
		_ = Module(
			pgxbackend.Config{DSN: "postgres://u:p@h/db?sslmode=require"},
			nil,
		)
	})
}

func TestWithMigrations_PanicsOnNilFS(t *testing.T) {
	assert.Panics(t, func() {
		_ = WithMigrations(nil)
	})
}

func TestWithInstance_PanicsOnEmptyName(t *testing.T) {
	assert.Panics(t, func() {
		_ = WithInstance("")
	})
}

func TestModule_Name(t *testing.T) {
	m := Module(pgxbackend.Config{DSN: "postgres://u:p@h/db?sslmode=require"})
	assert.Equal(t, "postgres", m.Name())
}

// TestPostgresModule_RegistersPoolStatsCollector verifies the module wires the
// pgxpool collector into the configured registerer at Init() time.
func TestPostgresModule_RegistersPoolStatsCollector(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := Module(pgxbackend.Config{
		DSN:                            "postgres://u:p@127.0.0.1:1/db?sslmode=disable",
		AllowPlaintextLoopbackForTests: true,
	}, WithRegisterer(reg)).(*pgxModule)

	mc := app.ModuleContext{
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

func TestPool_NilWhenAdapterNotRegistered(t *testing.T) {
	infra := app.TestInfrastructure()
	assert.Nil(t, Pool(infra), "Pool() should return nil when no postgres adapter was registered")
}
