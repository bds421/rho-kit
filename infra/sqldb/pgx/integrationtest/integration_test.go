//go:build integration

package integrationtest

import (
	"context"
	"net"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/sqldb/dbtest/v2"
	kitpgx "github.com/bds421/rho-kit/infra/sqldb/pgx/v2"
	"github.com/bds421/rho-kit/infra/v2/sqldb"
)

func startPostgres(t *testing.T) string {
	t.Helper()
	return postgresDSN(dbtest.StartPostgres(t, "pgx_test"))
}

func postgresDSN(cfg sqldb.Config) string {
	q := url.Values{}
	for k, v := range cfg.Options {
		q.Set(k, v)
	}
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(cfg.User, cfg.Password),
		Host:     net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)),
		Path:     cfg.Name,
		RawQuery: q.Encode(),
	}
	return u.String()
}

func TestPing_Live(t *testing.T) {
	dsn := startPostgres(t)
	pool, err := kitpgx.Connect(context.Background(), kitpgx.Config{DSN: dsn, AllowPlaintextLoopbackForTests: true})
	require.NoError(t, err)
	defer pool.Close()
	require.NoError(t, pool.Ping(context.Background()))
}

func TestCopy_LoadsRows(t *testing.T) {
	dsn := startPostgres(t)
	pool, err := kitpgx.Connect(context.Background(), kitpgx.Config{DSN: dsn, AllowPlaintextLoopbackForTests: true})
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
	dsn := startPostgres(t)
	// Cap MaxConns to 5 so the listener pinning one is visible if the
	// pool sizing math goes wrong.
	pool, err := kitpgx.Connect(context.Background(), kitpgx.Config{DSN: dsn, MaxConns: 5, AllowPlaintextLoopbackForTests: true})
	require.NoError(t, err)
	defer pool.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	notif, errs, err := pool.Listen(ctx, "events")
	require.NoError(t, err)

	var got []kitpgx.Notification
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
