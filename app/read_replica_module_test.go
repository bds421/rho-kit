package app

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/bds421/rho-kit/infra/sqldb"
	"github.com/bds421/rho-kit/infra/sqldb/gormdb"
)

func TestNewReadReplicaModule_Name(t *testing.T) {
	m := NewReadReplicaModule(gormdb.ReplicaConfig{
		Postgres: &sqldb.PostgresConfig{Host: "replica-host"},
	})
	assert.Equal(t, "read-replica", m.Name())
}

func TestNewReadReplicaModule_PopulateBeforeInit(t *testing.T) {
	m := NewReadReplicaModule(gormdb.ReplicaConfig{
		Postgres: &sqldb.PostgresConfig{Host: "replica-host"},
	})
	infra := &Infrastructure{}
	m.Populate(infra)
	assert.Nil(t, infra.DBReader, "DBReader should be nil before Init")
}

func TestNewReadReplicaModule_CloseBeforeInit(t *testing.T) {
	m := NewReadReplicaModule(gormdb.ReplicaConfig{
		Postgres: &sqldb.PostgresConfig{Host: "replica-host"},
	})
	err := m.Close(context.TODO())
	require.NoError(t, err, "Close before Init should not error")
}

func TestNewReadReplicaModule_HealthChecksBeforeInit(t *testing.T) {
	m := NewReadReplicaModule(gormdb.ReplicaConfig{
		Postgres: &sqldb.PostgresConfig{Host: "replica-host"},
	})
	checks := m.HealthChecks()
	assert.Nil(t, checks, "should return nil health checks before Init (replicaDB is nil)")
}

func TestNewReadReplicaModule_HealthCheckName(t *testing.T) {
	m := NewReadReplicaModule(gormdb.ReplicaConfig{
		Postgres: &sqldb.PostgresConfig{Host: "replica-host"},
	})
	// Access the concrete type to simulate post-Init state.
	rm := m.(*readReplicaModule)
	rm.replicaDB = &gorm.DB{}
	checks := rm.HealthChecks()
	require.Len(t, checks, 1)
	assert.Equal(t, "database-replica", checks[0].Name)
	assert.False(t, checks[0].Critical, "replica health check should be non-critical")
}

func TestNewReadReplicaModule_InitFailsWithoutDatabaseModule(t *testing.T) {
	m := NewReadReplicaModule(gormdb.ReplicaConfig{
		Postgres: &sqldb.PostgresConfig{Host: "replica-host"},
	})

	defer func() {
		r := recover()
		require.NotNil(t, r, "expected panic for missing database module")
		assert.Contains(t, r, "database")
	}()

	mc := ModuleContext{
		Logger:  slog.Default(),
		modules: map[string]Module{},
		Config:  BaseConfig{},
	}
	_ = m.Init(context.Background(), mc)
}

func TestNewReadReplicaModule_InitFailsOnInvalidConfig(t *testing.T) {
	m := NewReadReplicaModule(gormdb.ReplicaConfig{
		// No MySQL or Postgres set -> validation error.
	})

	mc := ModuleContext{
		Logger: slog.Default(),
		modules: map[string]Module{
			"database": newDatabaseModule(databaseModuleConfig{
				pgCfg:   &sqldb.PostgresConfig{Host: "primary"},
				poolCfg: sqldb.PoolConfig{},
			}),
		},
		Config: BaseConfig{},
	}

	err := m.Init(context.Background(), mc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires MySQL or Postgres")
}

func TestNewReadReplicaModule_WithModuleRegistration(t *testing.T) {
	// Verify the module can be registered via WithModule without panics.
	b := New("test", "v1", BaseConfig{}).
		WithPostgres(sqldb.PostgresConfig{Host: "primary"}, sqldb.PoolConfig{}).
		WithModule(NewReadReplicaModule(gormdb.ReplicaConfig{
			Postgres: &sqldb.PostgresConfig{Host: "replica"},
		}))

	// The module is registered; verify it shows up.
	require.Len(t, b.modules, 1)
	assert.Equal(t, "read-replica", b.modules[0].Name())
}

func TestDBReaderFallback_NilDB(t *testing.T) {
	infra := TestInfrastructure()
	assert.Nil(t, infra.DB, "DB should be nil in test infrastructure")
	assert.Nil(t, infra.DBReader, "DBReader should be nil when DB is nil")
}

func TestDBReaderFallback_FallsThroughToDB(t *testing.T) {
	// Simulate the fallback logic from builder.go.
	infra := Infrastructure{}
	infra.DB = &gorm.DB{}
	if infra.DBReader == nil && infra.DB != nil {
		infra.DBReader = infra.DB
	}
	assert.Same(t, infra.DB, infra.DBReader)
}
