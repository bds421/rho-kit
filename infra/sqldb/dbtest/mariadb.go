//go:build integration

package dbtest

import (
	"context"
	"testing"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/mariadb"

	"github.com/bds421/rho-kit/infra/sqldb"
)

// StartMySQL launches a MariaDB container and returns a MySQLConfig for connecting.
// The container is automatically terminated when the test completes.
func StartMySQL(t *testing.T, dbName string) sqldb.MySQLConfig {
	t.Helper()

	ctx := context.Background()

	container, err := mariadb.Run(ctx, "mariadb:12.1.2",
		mariadb.WithDatabase(dbName),
		mariadb.WithUsername("test"),
		mariadb.WithPassword("test"),
	)
	if err != nil {
		t.Fatalf("start mariadb container: %v", err)
	}
	t.Cleanup(func() {
		if err := testcontainers.TerminateContainer(container); err != nil {
			t.Logf("terminate mariadb container: %v", err)
		}
	})

	host, err := container.Host(ctx)
	if err != nil {
		t.Fatalf("get mariadb host: %v", err)
	}

	port, err := container.MappedPort(ctx, "3306/tcp")
	if err != nil {
		t.Fatalf("get mariadb port: %v", err)
	}

	return sqldb.MySQLConfig{
		Host:     host,
		Port:     port.Int(),
		User:     "test",
		Password: "test",
		Name:     dbName,
	}
}
