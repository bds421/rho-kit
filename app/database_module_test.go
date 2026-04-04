package app

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/sqldb"
	"github.com/bds421/rho-kit/infra/sqldb/gormdb/gormpostgres"
)

func TestNewDatabaseModule_PanicsOnNoDriver(t *testing.T) {
	defer func() {
		r := recover()
		require.NotNil(t, r, "expected panic for missing Driver")
		assert.Contains(t, r, "requires a Driver")
	}()
	newDatabaseModule(databaseModuleConfig{})
}

func TestDatabaseModule_Name(t *testing.T) {
	m := newDatabaseModule(databaseModuleConfig{
		driver:  &gormpostgres.PostgresDriver{},
		cfg:     sqldb.Config{Host: "localhost"},
		poolCfg: sqldb.PoolConfig{},
	})
	assert.Equal(t, "database", m.Name())
}

func TestDatabaseModule_HealthChecksBeforeInit(t *testing.T) {
	m := newDatabaseModule(databaseModuleConfig{
		driver:  &gormpostgres.PostgresDriver{},
		cfg:     sqldb.Config{Host: "localhost"},
		poolCfg: sqldb.PoolConfig{},
	})
	checks := m.HealthChecks()
	assert.Nil(t, checks, "should return nil health checks before Init")
}

func TestDatabaseModule_CloseBeforeInit(t *testing.T) {
	m := newDatabaseModule(databaseModuleConfig{
		driver:  &gormpostgres.PostgresDriver{},
		cfg:     sqldb.Config{Host: "localhost"},
		poolCfg: sqldb.PoolConfig{},
	})
	err := m.Close(context.TODO())
	require.NoError(t, err, "Close before Init should not error")
}

func TestDatabaseModule_PopulateBeforeInit(t *testing.T) {
	m := newDatabaseModule(databaseModuleConfig{
		driver:  &gormpostgres.PostgresDriver{},
		cfg:     sqldb.Config{Host: "localhost"},
		poolCfg: sqldb.PoolConfig{},
	})
	infra := &Infrastructure{}
	m.Populate(infra)
	assert.Nil(t, infra.DB, "DB should be nil before Init")
}

func TestDatabaseModule_PopulateSetsDBReader(t *testing.T) {
	m := newDatabaseModule(databaseModuleConfig{
		driver:  &gormpostgres.PostgresDriver{},
		cfg:     sqldb.Config{Host: "localhost"},
		poolCfg: sqldb.PoolConfig{},
	})
	infra := &Infrastructure{}
	m.Populate(infra)
	// Both DB and DBReader should be nil before Init.
	assert.Nil(t, infra.DB)
	assert.Nil(t, infra.DBReader)
}

func TestDatabaseModule_DBBeforeInit(t *testing.T) {
	m := newDatabaseModule(databaseModuleConfig{
		driver:  &gormpostgres.PostgresDriver{},
		cfg:     sqldb.Config{Host: "localhost"},
		poolCfg: sqldb.PoolConfig{},
	})
	assert.Nil(t, m.DB(), "DB() should be nil before Init")
}

func TestDatabaseModule_SeedExitBeforeInit(t *testing.T) {
	m := newDatabaseModule(databaseModuleConfig{
		driver:  &gormpostgres.PostgresDriver{},
		cfg:     sqldb.Config{Host: "localhost"},
		poolCfg: sqldb.PoolConfig{},
	})
	assert.False(t, m.SeedExit(), "SeedExit should be false before Init")
}

func TestBuildIntegrationModules_Database(t *testing.T) {
	b := New("test", "v1", BaseConfig{}).
		WithPostgres(sqldb.Config{Host: "localhost"}, sqldb.PoolConfig{})

	modules, dbMod := b.buildIntegrationModules()
	// httpclient is always present; database is added when configured.
	assert.True(t, hasModule(modules, "httpclient"), "httpclient module should be present")
	assert.True(t, hasModule(modules, "database"), "database module should be present")
	require.NotNil(t, dbMod, "dbMod should be returned")
	assert.Equal(t, "database", dbMod.Name())
}

func TestBuildIntegrationModules_DatabaseMySQL(t *testing.T) {
	b := New("test", "v1", BaseConfig{}).
		WithMySQL(sqldb.Config{Host: "localhost"}, sqldb.PoolConfig{})

	modules, dbMod := b.buildIntegrationModules()
	assert.True(t, hasModule(modules, "database"), "database module should be present")
	require.NotNil(t, dbMod)
	assert.Equal(t, "mysql", dbMod.driver.Name())
}

func TestBuildIntegrationModules_DatabaseWithMetrics(t *testing.T) {
	b := New("test", "v1", BaseConfig{}).
		WithPostgres(sqldb.Config{Host: "localhost"}, sqldb.PoolConfig{}).
		WithDBMetrics()

	_, dbMod := b.buildIntegrationModules()
	require.NotNil(t, dbMod)
	assert.True(t, dbMod.metrics, "metrics flag should be set")
}

func TestBuildIntegrationModules_NoDatabase(t *testing.T) {
	b := New("test", "v1", BaseConfig{})
	modules, dbMod := b.buildIntegrationModules()
	// httpclient is always present even without DB.
	assert.True(t, hasModule(modules, "httpclient"), "httpclient module should always be present")
	assert.False(t, hasModule(modules, "database"), "database module should not be present")
	assert.Nil(t, dbMod, "dbMod should be nil when no DB configured")
}

func TestBuildIntegrationModules_DatabaseOrder(t *testing.T) {
	b := &Builder{
		name:        "test",
		version:     "v1",
		dbDriver:    &gormpostgres.PostgresDriver{},
		dbCfg:       &sqldb.Config{Host: "localhost"},
		dbPoolCfg:   &sqldb.PoolConfig{},
		dbNamespace: "test",
		mqURL:       "amqp://localhost",
	}

	modules, dbMod := b.buildIntegrationModules()
	// Order: httpclient -> database -> rabbitmq
	names := moduleNames(modules)
	assert.Equal(t, []string{"httpclient", "database", "rabbitmq"}, names)
	require.NotNil(t, dbMod)
}

// hasModule reports whether the module list contains a module with the given name.
func hasModule(modules []Module, name string) bool {
	for _, m := range modules {
		if m.Name() == name {
			return true
		}
	}
	return false
}

// moduleNames returns the names of all modules in order.
func moduleNames(modules []Module) []string {
	names := make([]string, len(modules))
	for i, m := range modules {
		names[i] = m.Name()
	}
	return names
}
