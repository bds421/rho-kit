//go:build integration

package auditlogpg

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	postgresstore "github.com/bds421/rho-kit/observability/auditlog/postgres/v2"
	"github.com/bds421/rho-kit/observability/v2/auditlog"
)

func event(action, status string) auditlog.Event {
	return auditlog.Event{Actor: "a", Action: action, Resource: "r", Status: status}
}

func actions(t *testing.T, logger *auditlog.Logger) []string {
	t.Helper()
	events, _, err := logger.List(context.Background(), auditlog.Filter{}, "", 50)
	require.NoError(t, err)
	out := make([]string, len(events))
	for i, e := range events {
		out[i] = e.Action
	}
	return out
}

// A success event appended with WithTx commits and rolls back with the
// caller's change; a denied or failed one is kept even when the change
// rolls back. The chain verifies throughout.
func TestPostgres_AppendJoinsTheCallersTransaction(t *testing.T) {
	ctx := context.Background()
	pool := openAndMigrate(t, startPostgres(t))
	logger := newLogger(t, pool)
	defer func() { _ = logger.Close() }()

	errLater := errors.New("the change fails after its entries")
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		joined := postgresstore.WithTx(ctx, tx)
		require.NoError(t, logger.LogE(joined, event("change.denied", auditlog.StatusDenied)))
		require.NoError(t, logger.LogE(joined, event("change.failed", auditlog.StatusFailure)))
		require.NoError(t, logger.LogE(joined, event("change.rolled_back", auditlog.StatusSuccess)))
		return errLater
	})
	require.ErrorIs(t, err, errLater)

	require.NoError(t, pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		return logger.LogE(postgresstore.WithTx(ctx, tx), event("change.kept", auditlog.StatusSuccess))
	}))

	assert.ElementsMatch(t, []string{"change.denied", "change.failed", "change.kept"}, actions(t, logger))
	require.NoError(t, logger.VerifyChain(ctx))
}

// A transaction above READ COMMITTED may read a head older than the
// newest, so joining it is refused.
func TestPostgres_AppendRefusesRepeatableRead(t *testing.T) {
	ctx := context.Background()
	pool := openAndMigrate(t, startPostgres(t))
	logger := newLogger(t, pool)
	defer func() { _ = logger.Close() }()

	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, "SELECT count(*) FROM audit_log_events")
	require.NoError(t, err)
	require.NoError(t, logger.LogE(ctx, event("concurrent", auditlog.StatusSuccess)))

	err = logger.LogE(postgresstore.WithTx(ctx, tx), event("stale", auditlog.StatusSuccess))
	require.ErrorIs(t, err, postgresstore.ErrNotReadCommitted)
	require.NoError(t, tx.Rollback(ctx))
	assert.Equal(t, []string{"concurrent"}, actions(t, logger))
	require.NoError(t, logger.VerifyChain(ctx))
}

// Two rows that link to the same predecessor are a fork; the unique
// prev_hmac index refuses the second.
func TestPostgres_AForkFailsTheInsert(t *testing.T) {
	ctx := context.Background()
	pool := openAndMigrate(t, startPostgres(t))
	logger := newLogger(t, pool)
	defer func() { _ = logger.Close() }()
	require.NoError(t, logger.LogE(ctx, event("first", auditlog.StatusSuccess)))
	require.NoError(t, logger.LogE(ctx, event("second", auditlog.StatusSuccess)))

	_, err := pool.Exec(ctx, `INSERT INTO audit_log_events (id, occurred_at, actor, tenant, action, resource, status, ip_address, trace_id, metadata, signed_metadata, prev_hmac, hmac, search_text)
SELECT 'fork', occurred_at, actor, tenant, action, resource, status, ip_address, trace_id, metadata, signed_metadata, prev_hmac, '\x00', search_text
FROM audit_log_events ORDER BY seq DESC LIMIT 1`)
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr)
	assert.Equal(t, "23505", pgErr.Code)
	assert.Equal(t, "idx_audit_log_events_prev_hmac", pgErr.ConstraintName)
}

// A joined transaction holds the chain lock until it ends: another
// appender waits for it, at most its lock timeout, and the chain stays
// whole.
func TestPostgres_AppendInTransactionHoldsTheChainLock(t *testing.T) {
	ctx := context.Background()
	pool := openAndMigrate(t, startPostgres(t))
	logger := newLogger(t, pool)
	defer func() { _ = logger.Close() }()
	impatient := auditlog.New(postgresstore.New(pool, postgresstore.WithLockTimeout(200*time.Millisecond)),
		auditlog.WithChainKey(testChainKey), auditlog.WithCursorKey(testCursorKey))
	defer func() { _ = impatient.Close() }()

	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	require.NoError(t, logger.LogE(postgresstore.WithTx(ctx, tx), event("first", auditlog.StatusSuccess)))

	err = impatient.LogE(ctx, event("gave up", auditlog.StatusSuccess))
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr)
	assert.Equal(t, "55P03", pgErr.Code, "lock_timeout")

	done := make(chan error, 1)
	go func() { done <- logger.LogE(ctx, event("second", auditlog.StatusSuccess)) }()
	waitForAdvisoryWaiter(t, pool)
	require.NoError(t, tx.Commit(ctx))
	require.NoError(t, <-done)

	assert.ElementsMatch(t, []string{"first", "second"}, actions(t, logger))
	require.NoError(t, logger.VerifyChain(ctx))
}

// A joined append puts the caller's lock_timeout back once it holds the
// chain lock.
func TestPostgres_JoinedAppendKeepsTheCallersLockTimeout(t *testing.T) {
	ctx := context.Background()
	pool := openAndMigrate(t, startPostgres(t))
	logger := newLogger(t, pool)
	defer func() { _ = logger.Close() }()

	require.NoError(t, pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SET LOCAL lock_timeout = '42s'"); err != nil {
			return err
		}
		if err := logger.LogE(postgresstore.WithTx(ctx, tx), event("change", auditlog.StatusSuccess)); err != nil {
			return err
		}
		var timeout string
		if err := tx.QueryRow(ctx, "SELECT current_setting('lock_timeout')").Scan(&timeout); err != nil {
			return err
		}
		assert.Equal(t, "42s", timeout)
		return nil
	}))
}

func waitForAdvisoryWaiter(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	require.Eventually(t, func() bool {
		var waiting int
		err := pool.QueryRow(context.Background(), "SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND NOT granted").Scan(&waiting)
		return err == nil && waiting == 1
	}, 4*time.Second, 20*time.Millisecond, "an appender waits for the chain lock")
}
