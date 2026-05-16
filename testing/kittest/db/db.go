//go:build integration

package db

import (
	"github.com/bds421/rho-kit/infra/sqldb/dbtest/v2"
)

// StartPostgres launches a PostgreSQL testcontainer and returns a
// [sqldb.Config] for connecting. The container is automatically terminated
// when the test completes.
//
// This is a zero-cost re-export of [dbtest.StartPostgres].
var StartPostgres = dbtest.StartPostgres
