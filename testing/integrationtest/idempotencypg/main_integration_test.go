//go:build integration

package idempotencypg

import (
	"os"
	"testing"

	"github.com/bds421/rho-kit/infra/sqldb/dbtest/v2"
)

var cluster = dbtest.NewCluster("kit_test")

func TestMain(m *testing.M) { os.Exit(cluster.Run(m)) }
