package app

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/sqldb"
)

func TestNewDatabaseModule_PanicsOnNoConfig(t *testing.T) {
	defer func() {
		r := recover()
		require.NotNil(t, r, "expected panic for missing DB config")
		assert.Contains(t, r, "requires MySQL or Postgres config")
	}()
	newDatabaseModule(databaseModuleConfig{})
}

func TestDatabaseModule_Name(t *testing.T) {
	m := newDatabaseModule(databaseModuleConfig{
		pgCfg:   &sqldb.PostgresConfig{Host: "localhost"},
		poolCfg: sqldb.PoolConfig{},
	})
	assert.Equal(t, "database", m.Name())
}

func TestDatabaseModule_DriverMySQL(t *testing.T) {
	m := newDatabaseModule(databaseModuleConfig{
		mysqlCfg: &sqldb.MySQLConfig{Host: "localhost"},
		poolCfg:  sqldb.PoolConfig{},
	})
	assert.Equal(t, "mysql", m.driver())
}

func TestDatabaseModule_DriverPostgres(t *testing.T) {
	m := newDatabaseModule(databaseModuleConfig{
		pgCfg:   &sqldb.PostgresConfig{Host: "localhost"},
		poolCfg: sqldb.PoolConfig{},
	})
	assert.Equal(t, "postgres", m.driver())
}

func TestDatabaseModule_HealthChecksBeforeInit(t *testing.T) {
	m := newDatabaseModule(databaseModuleConfig{
		pgCfg:   &sqldb.PostgresConfig{Host: "localhost"},
		poolCfg: sqldb.PoolConfig{},
	})
	checks := m.HealthChecks()
	assert.Nil(t, checks, "should return nil health checks before Init")
}

func TestDatabaseModule_CloseBeforeInit(t *testing.T) {
	m := newDatabaseModule(databaseModuleConfig{
		pgCfg:   &sqldb.PostgresConfig{Host: "localhost"},
		poolCfg: sqldb.PoolConfig{},
	})
	err := m.Close(context.TODO())
	require.NoError(t, err, "Close before Init should not error")
}

func TestDatabaseModule_PopulateBeforeInit(t *testing.T) {
	m := newDatabaseModule(databaseModuleConfig{
		pgCfg:   &sqldb.PostgresConfig{Host: "localhost"},
		poolCfg: sqldb.PoolConfig{},
	})
	infra := &Infrastructure{}
	m.Populate(infra)
	assert.Nil(t, infra.DB, "DB should be nil before Init")
}

func TestDatabaseModule_DBBeforeInit(t *testing.T) {
	m := newDatabaseModule(databaseModuleConfig{
		pgCfg:   &sqldb.PostgresConfig{Host: "localhost"},
		poolCfg: sqldb.PoolConfig{},
	})
	assert.Nil(t, m.DB(), "DB() should be nil before Init")
}

func TestDatabaseModule_SeedExitBeforeInit(t *testing.T) {
	m := newDatabaseModule(databaseModuleConfig{
		pgCfg:   &sqldb.PostgresConfig{Host: "localhost"},
		poolCfg: sqldb.PoolConfig{},
	})
	assert.False(t, m.SeedExit(), "SeedExit should be false before Init")
}

func TestBuildIntegrationModules_Database(t *testing.T) {
	b := New("test", "v1", BaseConfig{}).
		WithPostgres(sqldb.PostgresConfig{Host: "localhost"}, sqldb.PoolConfig{})

	modules, dbMod := b.buildIntegrationModules()
	require.Len(t, modules, 1)
	assert.Equal(t, "database", modules[0].Name())
	require.NotNil(t, dbMod, "dbMod should be returned")
	assert.Equal(t, "database", dbMod.Name())
}

func TestBuildIntegrationModules_DatabaseMySQL(t *testing.T) {
	b := New("test", "v1", BaseConfig{}).
		WithMySQL(sqldb.MySQLConfig{Host: "localhost"}, sqldb.PoolConfig{})

	modules, dbMod := b.buildIntegrationModules()
	require.Len(t, modules, 1)
	assert.Equal(t, "database", modules[0].Name())
	require.NotNil(t, dbMod)
	assert.Equal(t, "mysql", dbMod.driver())
}

func TestBuildIntegrationModules_DatabaseWithMetrics(t *testing.T) {
	b := New("test", "v1", BaseConfig{}).
		WithPostgres(sqldb.PostgresConfig{Host: "localhost"}, sqldb.PoolConfig{}).
		WithDBMetrics()

	_, dbMod := b.buildIntegrationModules()
	require.NotNil(t, dbMod)
	assert.True(t, dbMod.metrics, "metrics flag should be set")
}

func TestBuildIntegrationModules_NoDatabase(t *testing.T) {
	b := New("test", "v1", BaseConfig{})
	modules, dbMod := b.buildIntegrationModules()
	assert.Empty(t, modules)
	assert.Nil(t, dbMod, "dbMod should be nil when no DB configured")
}

func TestBuildIntegrationModules_DatabaseOrder(t *testing.T) {
	b := &Builder{
		name:        "test",
		version:     "v1",
		dbPgCfg:     &sqldb.PostgresConfig{Host: "localhost"},
		dbPoolCfg:   &sqldb.PoolConfig{},
		dbNamespace: "test",
		mqURL:       "amqp://localhost",
	}

	modules, dbMod := b.buildIntegrationModules()
	require.Len(t, modules, 2)
	assert.Equal(t, "database", modules[0].Name(), "database should be first")
	assert.Equal(t, "rabbitmq", modules[1].Name(), "rabbitmq should be second")
	require.NotNil(t, dbMod)
}
