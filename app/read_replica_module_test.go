package app

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/sqldb"
	"github.com/bds421/rho-kit/infra/sqldb/gormdb/gormpostgres"
)

func TestNewReadReplicaModule_PanicsOnNilDriver(t *testing.T) {
	defer func() {
		r := recover()
		require.NotNil(t, r, "expected panic for nil Driver")
		assert.Contains(t, r, "requires a Driver")
	}()
	NewReadReplicaModule(nil, sqldb.Config{}, sqldb.PoolConfig{})
}

func TestNewReadReplicaModule_Name(t *testing.T) {
	m := NewReadReplicaModule(
		gormpostgres.PostgresDriver{},
		sqldb.Config{Host: "localhost"},
		sqldb.PoolConfig{},
	)
	assert.Equal(t, "read-replica", m.Name())
}

func TestReadReplicaModule_HealthChecksBeforeInit(t *testing.T) {
	m := NewReadReplicaModule(
		gormpostgres.PostgresDriver{},
		sqldb.Config{Host: "localhost"},
		sqldb.PoolConfig{},
	)
	checks := m.HealthChecks()
	assert.Nil(t, checks, "should return nil health checks before Init")
}

func TestReadReplicaModule_CloseBeforeInit(t *testing.T) {
	m := NewReadReplicaModule(
		gormpostgres.PostgresDriver{},
		sqldb.Config{Host: "localhost"},
		sqldb.PoolConfig{},
	)
	err := m.Close(context.TODO())
	require.NoError(t, err, "Close before Init should not error")
}
