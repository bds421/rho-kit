package gormdb_test

import (
	"crypto/tls"
	"fmt"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/bds421/rho-kit/infra/sqldb"
	"github.com/bds421/rho-kit/infra/sqldb/gormdb"
)

// fakeDriver implements DriverConfig for testing.
type fakeDriver struct {
	name    string
	openErr error
	openDB  *gorm.DB
}

func (d *fakeDriver) DriverName() string { return d.name }

func (d *fakeDriver) Open(_ sqldb.PoolConfig, _ *slog.Logger, _ *tls.Config) (*gorm.DB, error) {
	if d.openErr != nil {
		return nil, d.openErr
	}
	return d.openDB, nil
}

func TestReplicaConfig_Validate_NilDriver(t *testing.T) {
	cfg := gormdb.ReplicaConfig{}
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Driver is required")
}

func TestReplicaConfig_Validate_Valid(t *testing.T) {
	cfg := gormdb.ReplicaConfig{
		Driver: &fakeDriver{name: "mysql"},
		Pool:   sqldb.DefaultPool(),
	}
	err := cfg.Validate()
	require.NoError(t, err)
}

func TestRegisterReplica_NilPrimary(t *testing.T) {
	cfg := gormdb.ReplicaConfig{
		Driver: &fakeDriver{name: "postgres"},
	}
	_, err := gormdb.RegisterReplica(nil, cfg, slog.Default(), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "primary database is nil")
}

func TestRegisterReplica_InvalidConfig(t *testing.T) {
	cfg := gormdb.ReplicaConfig{} // no driver
	_, err := gormdb.RegisterReplica(&gorm.DB{}, cfg, slog.Default(), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Driver is required")
}

func TestRegisterReplica_OpenError(t *testing.T) {
	cfg := gormdb.ReplicaConfig{
		Driver: &fakeDriver{
			name:    "mysql",
			openErr: fmt.Errorf("connection refused"),
		},
		Pool: sqldb.DefaultPool(),
	}
	_, err := gormdb.RegisterReplica(&gorm.DB{}, cfg, slog.Default(), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "open read replica")
	assert.Contains(t, err.Error(), "connection refused")
}

func TestCloseDB_Nil(t *testing.T) {
	err := gormdb.CloseDB(nil)
	require.NoError(t, err)
}
