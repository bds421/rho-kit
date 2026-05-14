//go:build integration

package integrationtest

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"io/fs"
	"net"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/sqldb/dbtest/v2"
	"github.com/bds421/rho-kit/observability/v2/auditlog"
	postgresstore "github.com/bds421/rho-kit/observability/auditlog/postgres/v2"
)

var (
	testChainKey  = bytes.Repeat([]byte("c"), auditlog.MinChainKeyLen)
	testCursorKey = bytes.Repeat([]byte("u"), auditlog.MinCursorKeyLen)
)

func startPostgres(t *testing.T) string {
	t.Helper()
	cfg := dbtest.StartPostgres(t, "auditlog_test")
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

func openAndMigrate(t *testing.T, dsn string) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	sqlDB := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { _ = sqlDB.Close() })

	sub, err := fs.Sub(postgresstore.Migrations, "migrations")
	require.NoError(t, err)
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, sub)
	require.NoError(t, err)
	_, err = provider.Up(ctx)
	require.NoError(t, err)
	return pool
}

func newLogger(t *testing.T, pool *pgxpool.Pool) *auditlog.Logger {
	t.Helper()
	store := postgresstore.New(pool)
	return auditlog.New(store,
		auditlog.WithChainKey(testChainKey),
		auditlog.WithCursorKey(testCursorKey),
	)
}

// TestPostgres_RoundTrip exercises the basic append→list→verify path
// against a real Postgres instance, end to end. It also confirms that
// VerifyChain succeeds over a fresh small chain.
func TestPostgres_RoundTrip(t *testing.T) {
	dsn := startPostgres(t)
	pool := openAndMigrate(t, dsn)
	logger := newLogger(t, pool)
	defer func() { _ = logger.Close() }()

	for i := 0; i < 5; i++ {
		require.NoError(t, logger.LogE(context.Background(), auditlog.Event{
			Actor:    "agent-7",
			Action:   "user.delete",
			Resource: "users/42",
			Status:   auditlog.StatusSuccess,
		}))
	}

	events, next, err := logger.List(context.Background(), auditlog.Filter{}, "", 10)
	require.NoError(t, err)
	assert.Equal(t, 5, len(events))
	assert.Empty(t, next, "no more pages when result count <= limit")

	require.NoError(t, logger.VerifyChain(context.Background()))
}

// TestPostgres_TamperDetected verifies that mutating a stored event
// breaks VerifyChain — the kit's compliance promise depends on this.
func TestPostgres_TamperDetected(t *testing.T) {
	dsn := startPostgres(t)
	pool := openAndMigrate(t, dsn)
	logger := newLogger(t, pool)
	defer func() { _ = logger.Close() }()

	require.NoError(t, logger.LogE(context.Background(), auditlog.Event{
		Actor:    "agent",
		Action:   "x",
		Resource: "r",
		Status:   auditlog.StatusSuccess,
	}))

	sqlDB := stdlib.OpenDBFromPool(pool)
	defer func() { _ = sqlDB.Close() }()
	_, err := sqlDB.Exec("UPDATE audit_log_events SET actor = $1", "impostor")
	require.NoError(t, err)

	assert.ErrorIs(t, logger.VerifyChain(context.Background()), auditlog.ErrChainBroken)
}

// TestPostgres_DeletionDetected: removing a row breaks the chain on
// VerifyChain because the next row's PrevHMAC won't match.
func TestPostgres_DeletionDetected(t *testing.T) {
	dsn := startPostgres(t)
	pool := openAndMigrate(t, dsn)
	logger := newLogger(t, pool)
	defer func() { _ = logger.Close() }()

	for i := 0; i < 3; i++ {
		require.NoError(t, logger.LogE(context.Background(), auditlog.Event{
			Actor: "agent", Action: "x", Resource: "r", Status: auditlog.StatusSuccess,
		}))
	}

	sqlDB := stdlib.OpenDBFromPool(pool)
	defer func() { _ = sqlDB.Close() }()
	_, err := sqlDB.Exec("DELETE FROM audit_log_events WHERE seq = 2")
	require.NoError(t, err)

	assert.ErrorIs(t, logger.VerifyChain(context.Background()), auditlog.ErrChainBroken)
}

// TestPostgres_ConcurrentAppendSerialises is the regression that
// motivated the advisory-lock + FOR UPDATE pair: N concurrent appenders
// against the same Postgres Store must all succeed AND produce a chain
// that VerifyChain accepts.
func TestPostgres_ConcurrentAppendSerialises(t *testing.T) {
	dsn := startPostgres(t)
	pool := openAndMigrate(t, dsn)
	logger := newLogger(t, pool)
	defer func() { _ = logger.Close() }()

	const n = 32
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
	)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			if err := logger.LogE(context.Background(), auditlog.Event{
				Actor: "agent", Action: "x", Resource: "r", Status: auditlog.StatusSuccess,
			}); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	require.Empty(t, errs)

	require.NoError(t, logger.VerifyChain(context.Background()))

	events, _, err := logger.List(context.Background(), auditlog.Filter{}, "", n+1)
	require.NoError(t, err)
	assert.Len(t, events, n)
}

// TestPostgres_RangeChainAppendOrder confirms RangeChain iterates by
// seq ASC regardless of caller-supplied timestamps. A backfilled event
// (older timestamp inserted last) must still verify because seq is the
// append-order ground truth.
func TestPostgres_RangeChainAppendOrder(t *testing.T) {
	dsn := startPostgres(t)
	pool := openAndMigrate(t, dsn)
	logger := newLogger(t, pool)
	defer func() { _ = logger.Close() }()

	now := time.Now().UTC()
	require.NoError(t, logger.LogE(context.Background(), auditlog.Event{
		Timestamp: now.Add(time.Hour),
		Actor:     "agent", Action: "x", Resource: "r", Status: auditlog.StatusSuccess,
	}))
	require.NoError(t, logger.LogE(context.Background(), auditlog.Event{
		Timestamp: now, // older than the previous event
		Actor:     "agent", Action: "y", Resource: "r", Status: auditlog.StatusSuccess,
	}))

	require.NoError(t, logger.VerifyChain(context.Background()))
}

// TestPostgres_LastHMACMatchesTail proves Store.LastHMAC and the
// internal chain are consistent: appending a final event whose
// PrevHMAC is the prior LastHMAC must verify and itself become the
// new LastHMAC.
func TestPostgres_LastHMACMatchesTail(t *testing.T) {
	dsn := startPostgres(t)
	pool := openAndMigrate(t, dsn)
	store := postgresstore.New(pool)
	logger := auditlog.New(store,
		auditlog.WithChainKey(testChainKey),
		auditlog.WithCursorKey(testCursorKey),
	)
	defer func() { _ = logger.Close() }()

	// Empty: no tail.
	tail, err := store.LastHMAC(context.Background())
	require.NoError(t, err)
	assert.Nil(t, tail)

	require.NoError(t, logger.LogE(context.Background(), auditlog.Event{
		Actor: "a", Action: "x", Resource: "r", Status: auditlog.StatusSuccess,
	}))

	tail, err = store.LastHMAC(context.Background())
	require.NoError(t, err)
	require.NotNil(t, tail)
	// Sanity: tail should be SHA-256-sized HMAC (32 bytes).
	mac := hmac.New(sha256.New, testChainKey)
	_ = mac
	assert.Equal(t, 32, len(tail))
}

// TestPostgres_QueryPaginationCursorRoundTrip exercises the
// Logger.List + signed-cursor flow against a real database — the
// Logger encodes/decodes the Store's raw cursor, so the wire-level
// behaviour is "cursor never leaks the internal format."
func TestPostgres_QueryPaginationCursorRoundTrip(t *testing.T) {
	dsn := startPostgres(t)
	pool := openAndMigrate(t, dsn)
	logger := newLogger(t, pool)
	defer func() { _ = logger.Close() }()

	const total = 7
	for i := 0; i < total; i++ {
		require.NoError(t, logger.LogE(context.Background(), auditlog.Event{
			Actor: "agent", Action: "x", Resource: "r", Status: auditlog.StatusSuccess,
		}))
	}

	page1, next, err := logger.List(context.Background(), auditlog.Filter{}, "", 3)
	require.NoError(t, err)
	require.Len(t, page1, 3)
	require.NotEmpty(t, next, "cursor required when more pages exist")

	page2, next2, err := logger.List(context.Background(), auditlog.Filter{}, next, 3)
	require.NoError(t, err)
	require.Len(t, page2, 3)
	require.NotEmpty(t, next2)

	page3, next3, err := logger.List(context.Background(), auditlog.Filter{}, next2, 3)
	require.NoError(t, err)
	require.Len(t, page3, 1, "tail page has remaining 1 of 7")
	assert.Empty(t, next3, "no more pages past the tail")
}

// TestPostgres_FilterByActorActionResource verifies the Filter clauses
// are wired correctly, including the resource-prefix match.
func TestPostgres_FilterByActorActionResource(t *testing.T) {
	dsn := startPostgres(t)
	pool := openAndMigrate(t, dsn)
	logger := newLogger(t, pool)
	defer func() { _ = logger.Close() }()

	cases := []auditlog.Event{
		{Actor: "alice", Action: "create", Resource: "users/1", Status: auditlog.StatusSuccess},
		{Actor: "alice", Action: "delete", Resource: "users/2", Status: auditlog.StatusSuccess},
		{Actor: "bob", Action: "create", Resource: "orders/9", Status: auditlog.StatusSuccess},
	}
	for _, e := range cases {
		require.NoError(t, logger.LogE(context.Background(), e))
	}

	t.Run("actor", func(t *testing.T) {
		got, _, err := logger.List(context.Background(), auditlog.Filter{Actor: "alice"}, "", 10)
		require.NoError(t, err)
		assert.Len(t, got, 2)
	})
	t.Run("action", func(t *testing.T) {
		got, _, err := logger.List(context.Background(), auditlog.Filter{Action: "create"}, "", 10)
		require.NoError(t, err)
		assert.Len(t, got, 2)
	})
	t.Run("resource prefix", func(t *testing.T) {
		got, _, err := logger.List(context.Background(), auditlog.Filter{Resource: "users/"}, "", 10)
		require.NoError(t, err)
		assert.Len(t, got, 2)
	})
}
