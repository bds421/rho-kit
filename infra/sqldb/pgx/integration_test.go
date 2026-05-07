//go:build integration

package pgx

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

func startPostgres(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	c, err := postgres.Run(ctx, "postgres:17-alpine",
		postgres.WithDatabase("test"),
		postgres.WithUsername("test"),
		postgres.WithPassword("test"),
		postgres.BasicWaitStrategies(),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(c) })

	dsn, err := c.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	return dsn
}

func TestPing_Live(t *testing.T) {
	t.Setenv("KIT_ENV", "development")
	dsn := startPostgres(t)
	pool, err := Connect(context.Background(), Config{DSN: dsn})
	require.NoError(t, err)
	defer pool.Close()
	require.NoError(t, pool.Ping(context.Background()))
}

func TestCopy_LoadsRows(t *testing.T) {
	t.Setenv("KIT_ENV", "development")
	dsn := startPostgres(t)
	pool, err := Connect(context.Background(), Config{DSN: dsn})
	require.NoError(t, err)
	defer pool.Close()

	ctx := context.Background()
	_, err = pool.Pool().Exec(ctx, "CREATE TABLE items (id INT, name TEXT)")
	require.NoError(t, err)

	rows := [][]any{
		{1, "alice"},
		{2, "bob"},
		{3, "carol"},
	}
	n, err := pool.Copy(ctx, "items", []string{"id", "name"}, rows)
	require.NoError(t, err)
	assert.Equal(t, int64(3), n)

	var count int
	require.NoError(t, pool.Pool().QueryRow(ctx, "SELECT COUNT(*) FROM items").Scan(&count))
	assert.Equal(t, 3, count)
}

func TestListenNotify_RoundTrip(t *testing.T) {
	t.Setenv("KIT_ENV", "development")
	dsn := startPostgres(t)
	// Cap MaxConns to 5 so the listener pinning one is visible if the
	// pool sizing math goes wrong.
	pool, err := Connect(context.Background(), Config{DSN: dsn, MaxConns: 5})
	require.NoError(t, err)
	defer pool.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	notif, errs, err := pool.Listen(ctx, "events")
	require.NoError(t, err)

	var got []Notification
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for n := range notif {
			got = append(got, n)
			if len(got) == 2 {
				cancel() // exit listener
			}
		}
	}()

	// Allow the LISTEN to land before NOTIFY.
	time.Sleep(100 * time.Millisecond)

	require.NoError(t, pool.Notify(context.Background(), "events", "first"))
	require.NoError(t, pool.Notify(context.Background(), "events", "second"))

	wg.Wait()
	// Drain errors for the cancellation-induced exit.
	for range errs {
	}

	require.Len(t, got, 2)
	assert.Equal(t, "first", got[0].Payload)
	assert.Equal(t, "second", got[1].Payload)
}
