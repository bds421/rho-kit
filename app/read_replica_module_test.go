package app

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/bds421/rho-kit/infra/sqldb"
)

func TestNewReadReplicaModule_PanicsOnNoConfig(t *testing.T) {
	defer func() {
		r := recover()
		require.NotNil(t, r, "expected panic for missing config")
		assert.Contains(t, r, "requires MySQL or Postgres config")
	}()
	newReadReplicaModule(readReplicaModuleConfig{})
}

func TestReadReplicaModule_Name(t *testing.T) {
	m := newReadReplicaModule(readReplicaModuleConfig{
		pgCfg:   &sqldb.PostgresConfig{Host: "replica-host"},
		poolCfg: sqldb.PoolConfig{},
	})
	assert.Equal(t, "read-replica", m.Name())
}

func TestReadReplicaModule_DriverMySQL(t *testing.T) {
	m := newReadReplicaModule(readReplicaModuleConfig{
		mysqlCfg: &sqldb.MySQLConfig{Host: "replica-host"},
		poolCfg:  sqldb.PoolConfig{},
	})
	assert.Equal(t, "mysql", m.driver())
}

func TestReadReplicaModule_DriverPostgres(t *testing.T) {
	m := newReadReplicaModule(readReplicaModuleConfig{
		pgCfg:   &sqldb.PostgresConfig{Host: "replica-host"},
		poolCfg: sqldb.PoolConfig{},
	})
	assert.Equal(t, "postgres", m.driver())
}

func TestReadReplicaModule_CloseBeforeInit(t *testing.T) {
	m := newReadReplicaModule(readReplicaModuleConfig{
		pgCfg:   &sqldb.PostgresConfig{Host: "replica-host"},
		poolCfg: sqldb.PoolConfig{},
	})
	err := m.Close(context.TODO())
	require.NoError(t, err, "Close before Init should not error")
}

func TestReadReplicaModule_PopulateBeforeInit(t *testing.T) {
	m := newReadReplicaModule(readReplicaModuleConfig{
		pgCfg:   &sqldb.PostgresConfig{Host: "replica-host"},
		poolCfg: sqldb.PoolConfig{},
	})
	infra := &Infrastructure{}
	m.Populate(infra)
	assert.Nil(t, infra.DBReader, "DBReader should be nil before Init")
}

func TestReadReplicaModule_HealthChecksBeforeInit(t *testing.T) {
	m := newReadReplicaModule(readReplicaModuleConfig{
		pgCfg:   &sqldb.PostgresConfig{Host: "replica-host"},
		poolCfg: sqldb.PoolConfig{},
	})
	checks := m.HealthChecks()
	assert.Nil(t, checks, "should return nil health checks before Init (replicaDB is nil)")
}

func TestReadReplicaModule_HealthCheckName(t *testing.T) {
	m := newReadReplicaModule(readReplicaModuleConfig{
		pgCfg:   &sqldb.PostgresConfig{Host: "replica-host"},
		poolCfg: sqldb.PoolConfig{},
	})
	// Simulate a post-Init state by setting replicaDB to a non-nil value.
	// We cannot actually connect, but HealthChecks only checks for nil.
	m.replicaDB = &gorm.DB{}
	checks := m.HealthChecks()
	require.Len(t, checks, 1)
	assert.Equal(t, "database-replica", checks[0].Name)
	assert.False(t, checks[0].Critical, "replica health check should be non-critical (degraded, not unhealthy)")
}

func TestWithReadReplica_PanicsWithoutPostgres(t *testing.T) {
	defer func() {
		r := recover()
		require.NotNil(t, r, "expected panic without WithPostgres")
		assert.Contains(t, r, "requires WithPostgres")
	}()
	New("test", "v1", BaseConfig{}).
		WithReadReplica(sqldb.PostgresConfig{Host: "replica"}, sqldb.PoolConfig{})
}

func TestWithReadReplicaMySQL_PanicsWithoutMySQL(t *testing.T) {
	defer func() {
		r := recover()
		require.NotNil(t, r, "expected panic without WithMySQL")
		assert.Contains(t, r, "requires WithMySQL")
	}()
	New("test", "v1", BaseConfig{}).
		WithReadReplicaMySQL(sqldb.MySQLConfig{Host: "replica"}, sqldb.PoolConfig{})
}

func TestWithReadReplica_PanicsWhenMySQLReplicaAlreadySet(t *testing.T) {
	defer func() {
		r := recover()
		require.NotNil(t, r, "expected panic for mutual exclusivity")
		assert.Contains(t, r, "mutually exclusive")
	}()
	b := New("test", "v1", BaseConfig{}).
		WithPostgres(sqldb.PostgresConfig{Host: "primary"}, sqldb.PoolConfig{})
	// Simulate a MySQL replica already set by directly setting the field,
	// since WithReadReplicaMySQL requires WithMySQL which conflicts with WithPostgres.
	b.replicaMySQLCfg = &sqldb.MySQLConfig{Host: "replica-mysql"}
	b.WithReadReplica(sqldb.PostgresConfig{Host: "replica"}, sqldb.PoolConfig{})
}

func TestWithReadReplicaMySQL_PanicsWhenPgReplicaAlreadySet(t *testing.T) {
	defer func() {
		r := recover()
		require.NotNil(t, r, "expected panic for mutual exclusivity")
		assert.Contains(t, r, "mutually exclusive")
	}()
	b := New("test", "v1", BaseConfig{}).
		WithMySQL(sqldb.MySQLConfig{Host: "primary"}, sqldb.PoolConfig{})
	// Simulate a Pg replica already set by directly setting the field.
	b.replicaPgCfg = &sqldb.PostgresConfig{Host: "replica-pg"}
	b.WithReadReplicaMySQL(sqldb.MySQLConfig{Host: "replica"}, sqldb.PoolConfig{})
}

func TestWithReadReplica_StoresConfig(t *testing.T) {
	b := New("test", "v1", BaseConfig{}).
		WithPostgres(sqldb.PostgresConfig{Host: "primary"}, sqldb.PoolConfig{}).
		WithReadReplica(sqldb.PostgresConfig{Host: "replica"}, sqldb.PoolConfig{})

	require.NotNil(t, b.replicaPgCfg)
	assert.Equal(t, "replica", b.replicaPgCfg.Host)
	require.NotNil(t, b.replicaPoolCfg)
}

func TestWithReadReplicaMySQL_StoresConfig(t *testing.T) {
	b := New("test", "v1", BaseConfig{}).
		WithMySQL(sqldb.MySQLConfig{Host: "primary"}, sqldb.PoolConfig{}).
		WithReadReplicaMySQL(sqldb.MySQLConfig{Host: "replica"}, sqldb.PoolConfig{})

	require.NotNil(t, b.replicaMySQLCfg)
	assert.Equal(t, "replica", b.replicaMySQLCfg.Host)
	require.NotNil(t, b.replicaPoolCfg)
}

func TestBuildIntegrationModules_WithReadReplica(t *testing.T) {
	b := &Builder{
		name:           "test",
		version:        "v1",
		dbPgCfg:        &sqldb.PostgresConfig{Host: "primary"},
		dbPoolCfg:      &sqldb.PoolConfig{},
		dbNamespace:    "test",
		replicaPgCfg:   &sqldb.PostgresConfig{Host: "replica"},
		replicaPoolCfg: &sqldb.PoolConfig{},
	}

	modules, dbMod := b.buildIntegrationModules()
	require.Len(t, modules, 2)
	assert.Equal(t, "database", modules[0].Name())
	assert.Equal(t, "read-replica", modules[1].Name())
	require.NotNil(t, dbMod)
}

func TestBuildIntegrationModules_WithReadReplicaMySQL(t *testing.T) {
	b := &Builder{
		name:            "test",
		version:         "v1",
		dbMySQLCfg:      &sqldb.MySQLConfig{Host: "primary"},
		dbPoolCfg:       &sqldb.PoolConfig{},
		dbNamespace:     "test",
		replicaMySQLCfg: &sqldb.MySQLConfig{Host: "replica"},
		replicaPoolCfg:  &sqldb.PoolConfig{},
	}

	modules, dbMod := b.buildIntegrationModules()
	require.Len(t, modules, 2)
	assert.Equal(t, "database", modules[0].Name())
	assert.Equal(t, "read-replica", modules[1].Name())
	require.NotNil(t, dbMod)
}

func TestBuildIntegrationModules_NoReplicaWithoutConfig(t *testing.T) {
	b := &Builder{
		name:        "test",
		version:     "v1",
		dbPgCfg:     &sqldb.PostgresConfig{Host: "primary"},
		dbPoolCfg:   &sqldb.PoolConfig{},
		dbNamespace: "test",
	}

	modules, _ := b.buildIntegrationModules()
	require.Len(t, modules, 1)
	assert.Equal(t, "database", modules[0].Name())
}

func TestValidate_ReplicaWithoutPrimaryDB(t *testing.T) {
	b := newTestBuilder()
	b.replicaPgCfg = &sqldb.PostgresConfig{Host: "replica"}
	b.replicaPoolCfg = &sqldb.PoolConfig{}
	err := b.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read replica requires a configured primary database")
}

func TestValidate_ReplicaPgWithoutPostgres(t *testing.T) {
	b := newTestBuilder()
	b.dbMySQLCfg = &sqldb.MySQLConfig{Host: "primary"}
	b.dbPoolCfg = &sqldb.PoolConfig{}
	b.replicaPgCfg = &sqldb.PostgresConfig{Host: "replica"}
	b.replicaPoolCfg = &sqldb.PoolConfig{}
	err := b.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "WithReadReplica requires WithPostgres")
}

func TestValidate_ReplicaMySQLWithoutMySQL(t *testing.T) {
	b := newTestBuilder()
	b.dbPgCfg = &sqldb.PostgresConfig{Host: "primary"}
	b.dbPoolCfg = &sqldb.PoolConfig{}
	b.replicaMySQLCfg = &sqldb.MySQLConfig{Host: "replica"}
	b.replicaPoolCfg = &sqldb.PoolConfig{}
	err := b.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "WithReadReplicaMySQL requires WithMySQL")
}

func TestValidate_ReplicaWithCorrectDriver(t *testing.T) {
	b := newTestBuilder()
	b.dbPgCfg = &sqldb.PostgresConfig{Host: "primary"}
	b.dbPoolCfg = &sqldb.PoolConfig{}
	b.replicaPgCfg = &sqldb.PostgresConfig{Host: "replica"}
	b.replicaPoolCfg = &sqldb.PoolConfig{}
	err := b.Validate()
	require.NoError(t, err)
}

func TestValidate_ReplicaMySQLWithCorrectDriver(t *testing.T) {
	b := newTestBuilder()
	b.dbMySQLCfg = &sqldb.MySQLConfig{Host: "primary"}
	b.dbPoolCfg = &sqldb.PoolConfig{}
	b.replicaMySQLCfg = &sqldb.MySQLConfig{Host: "replica"}
	b.replicaPoolCfg = &sqldb.PoolConfig{}
	err := b.Validate()
	require.NoError(t, err)
}

func TestDBReaderFallbackInTestInfrastructure(t *testing.T) {
	infra := TestInfrastructure()
	assert.Nil(t, infra.DB, "DB should be nil in test infrastructure")
	assert.Nil(t, infra.DBReader, "DBReader should be nil in test infrastructure")
}
