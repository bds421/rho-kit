package gormdb_test

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/sqldb"
	"github.com/bds421/rho-kit/infra/sqldb/gormdb"
	"github.com/bds421/rho-kit/infra/sqldb/memdb"
)

func TestReplicaConfig_Validate_NoDriver(t *testing.T) {
	err := gormdb.ReplicaConfig{}.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires MySQL or Postgres")
}

func TestReplicaConfig_Validate_BothDrivers(t *testing.T) {
	err := gormdb.ReplicaConfig{
		MySQL:    &sqldb.MySQLConfig{Host: "a"},
		Postgres: &sqldb.PostgresConfig{Host: "b"},
	}.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not both")
}

func TestReplicaConfig_Validate_MySQL(t *testing.T) {
	err := gormdb.ReplicaConfig{
		MySQL: &sqldb.MySQLConfig{Host: "replica"},
	}.Validate()
	require.NoError(t, err)
}

func TestReplicaConfig_Validate_Postgres(t *testing.T) {
	err := gormdb.ReplicaConfig{
		Postgres: &sqldb.PostgresConfig{Host: "replica"},
	}.Validate()
	require.NoError(t, err)
}

func TestReplicaConfig_Driver(t *testing.T) {
	tests := []struct {
		name   string
		cfg    gormdb.ReplicaConfig
		driver string
	}{
		{
			name:   "mysql",
			cfg:    gormdb.ReplicaConfig{MySQL: &sqldb.MySQLConfig{Host: "a"}},
			driver: "mysql",
		},
		{
			name:   "postgres",
			cfg:    gormdb.ReplicaConfig{Postgres: &sqldb.PostgresConfig{Host: "b"}},
			driver: "postgres",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.driver, tc.cfg.Driver())
		})
	}
}

func TestRegisterReplica_NilPrimary(t *testing.T) {
	err := gormdb.RegisterReplica(nil, nil, slog.Default())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "primary database must not be nil")
}

func TestRegisterReplica_NilReplica(t *testing.T) {
	primary := memdb.New(t, nil)
	err := gormdb.RegisterReplica(primary, nil, slog.Default())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "replica database must not be nil")
}

func TestRegisterReplica_Success(t *testing.T) {
	primary := memdb.New(t, nil)
	replica := memdb.New(t, nil)

	err := gormdb.RegisterReplica(primary, replica, slog.Default())
	require.NoError(t, err)
}

func TestCloseDB_Nil(t *testing.T) {
	err := gormdb.CloseDB(nil)
	require.NoError(t, err)
}

func TestCloseDB_ValidDB(t *testing.T) {
	db := memdb.New(t, nil)
	err := gormdb.CloseDB(db)
	require.NoError(t, err)
}
