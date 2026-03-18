//go:build integration

package dbtest

import (
	"context"
	"testing"

	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/bds421/rho-kit/infra/sqldb"
)

// StartPostgres launches a PostgreSQL container and returns a PostgresConfig for connecting.
// The container is automatically terminated when the test completes.
func StartPostgres(t *testing.T, dbName string) sqldb.PostgresConfig {
	t.Helper()

	ctx := context.Background()

	container, err := tcpostgres.Run(ctx, "postgres:18.1-alpine3.23",
		tcpostgres.WithDatabase(dbName),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
	)
	if err != nil {
		t.Fatalf("start postgres container: %v", err)
	}
	t.Cleanup(func() {
		if err := testcontainers.TerminateContainer(container); err != nil {
			t.Logf("terminate postgres container: %v", err)
		}
	})

	host, err := container.Host(ctx)
	if err != nil {
		t.Fatalf("get postgres host: %v", err)
	}

	port, err := container.MappedPort(ctx, "5432/tcp")
	if err != nil {
		t.Fatalf("get postgres port: %v", err)
	}

	return sqldb.PostgresConfig{
		Host:     host,
		Port:     port.Int(),
		User:     "test",
		Password: "test",
		Name:     dbName,
		SSLMode:  "disable",
	}
}
