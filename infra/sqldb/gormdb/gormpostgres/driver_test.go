package gormpostgres_test

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/bds421/rho-kit/infra/sqldb"
	"github.com/bds421/rho-kit/infra/sqldb/gormdb/gormpostgres"
)

func TestDriver_DriverName(t *testing.T) {
	d := gormpostgres.Driver{Config: sqldb.PostgresConfig{}}
	assert.Equal(t, "postgres", d.DriverName())
}

func TestDriver_OpenFailsWithBadConfig(t *testing.T) {
	d := gormpostgres.Driver{Config: sqldb.PostgresConfig{
		Host: "invalid-host-that-does-not-exist",
		Port: 1,
		User: "test",
		Name: "testdb",
	}}
	_, err := d.Open(sqldb.DefaultPool(), slog.Default(), nil)
	// Connection will fail since there is no PostgreSQL server.
	assert.Error(t, err)
}
