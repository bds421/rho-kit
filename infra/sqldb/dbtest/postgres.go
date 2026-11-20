//go:build integration

package dbtest

import (
	"context"
	"fmt"
	"testing"

	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/bds421/rho-kit/infra/v2/sqldb"
)

const postgresImage = "postgres:18.1-alpine3.23"

// StartPostgres launches a PostgreSQL container and returns a [sqldb.Config]
// for connecting. The container is automatically terminated when the test
// completes. A package whose tests isolate by database shares one container
// through a [Cluster] instead.
func StartPostgres(t *testing.T, dbName string) sqldb.Config {
	t.Helper()

	ctx := context.Background()

	container, err := tcpostgres.Run(ctx, postgresImage,
		tcpostgres.WithDatabase(dbName),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("start postgres container: %v", err)
	}
	t.Cleanup(func() {
		if err := testcontainers.TerminateContainer(container); err != nil {
			t.Logf("terminate postgres container: %v", err)
		}
	})

	cfg, err := configOf(ctx, container, dbName)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func configOf(ctx context.Context, container *tcpostgres.PostgresContainer, dbName string) (sqldb.Config, error) {
	host, err := container.Host(ctx)
	if err != nil {
		return sqldb.Config{}, fmt.Errorf("get postgres host: %w", err)
	}
	port, err := container.MappedPort(ctx, "5432/tcp")
	if err != nil {
		return sqldb.Config{}, fmt.Errorf("get postgres port: %w", err)
	}
	return sqldb.Config{
		Host:     host,
		Port:     int(port.Num()),
		User:     "test",
		Password: "test",
		Name:     dbName,
		Options:  map[string]string{"sslmode": "disable"},
	}, nil
}
