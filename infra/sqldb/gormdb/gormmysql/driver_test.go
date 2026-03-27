package gormmysql_test

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/bds421/rho-kit/infra/sqldb"
	"github.com/bds421/rho-kit/infra/sqldb/gormdb/gormmysql"
)

func TestDriver_DriverName(t *testing.T) {
	d := gormmysql.Driver{Config: sqldb.MySQLConfig{}}
	assert.Equal(t, "mysql", d.DriverName())
}

func TestDriver_OpenFailsWithBadConfig(t *testing.T) {
	d := gormmysql.Driver{Config: sqldb.MySQLConfig{
		Host: "invalid-host-that-does-not-exist",
		Port: 1,
		User: "test",
		Name: "testdb",
	}}
	_, err := d.Open(sqldb.DefaultPool(), slog.Default(), nil)
	// Connection will fail since there is no MySQL server.
	assert.Error(t, err)
}
