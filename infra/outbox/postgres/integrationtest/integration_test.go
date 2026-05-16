//go:build integration

package integrationtest

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/core/v2/id"
	"github.com/bds421/rho-kit/infra/sqldb/dbtest/v2"
	outboxpg "github.com/bds421/rho-kit/infra/outbox/postgres/v2"
	"github.com/bds421/rho-kit/infra/v2/outbox"
)

func startPostgres(t *testing.T) string {
	t.Helper()
	cfg := dbtest.StartPostgres(t, "outbox_test")
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

	sub, err := fs.Sub(outboxpg.Migrations, "migrations")
	require.NoError(t, err)
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, sub)
	require.NoError(t, err)
	_, err = provider.Up(ctx)
	require.NoError(t, err)
	return pool
}

func mustEntry() outbox.Entry {
	return outbox.Entry{
		ID:          uuid.UUID(id.NewBytes()),
		Topic:       "events",
		RoutingKey:  "user.created",
		MessageID:   "msg-" + uuid.NewString(),
		MessageType: "UserCreated",
		Payload:     json.RawMessage(`{"id":1}`),
		Status:      outbox.StatusPending,
		CreatedAt:   time.Now().UTC(),
	}
}

// TestInsertWithTx_AtomicWithBusinessTx is the headline transactional
// outbox guarantee: an Insert wrapped in the caller's pgx.Tx must
// commit-or-rollback with the surrounding business writes, not as a
// separate transaction. The test rolls back the tx and confirms the
// row never landed.
func TestInsertWithTx_AtomicWithBusinessTx(t *testing.T) {
	dsn := startPostgres(t)
	pool := openAndMigrate(t, dsn)
	store := outboxpg.New(pool)
	writer := outbox.NewWriter(store, outboxpg.RequireTx)

	ctx := context.Background()
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	require.NoError(t, err)

	txCtx := outboxpg.WithTx(ctx, tx)
	err = writer.Write(txCtx, outbox.WriteParams{
		Topic:       "events",
		RoutingKey:  "user.created",
		MessageID:   "rolled-back",
		MessageType: "UserCreated",
		Payload:     json.RawMessage(`{"id":1}`),
	})
	require.NoError(t, err, "Write into the tx must succeed")

	// Roll back the business transaction. The outbox row must not be
	// observable from the pool afterward.
	require.NoError(t, tx.Rollback(ctx))

	n, err := store.CountPending(ctx)
	require.NoError(t, err)
	assert.Zero(t, n, "outbox row must roll back with the business tx")
}

// TestRequireTx_RejectsCallerWithoutTx is the producer-side fail-fast
// guardrail: NewWriter(..., RequireTx) refuses a Write outside a tx so
// the kit catches the misuse at the call site rather than allowing a
// silent split-brain.
func TestRequireTx_RejectsCallerWithoutTx(t *testing.T) {
	dsn := startPostgres(t)
	pool := openAndMigrate(t, dsn)
	store := outboxpg.New(pool)
	writer := outbox.NewWriter(store, outboxpg.RequireTx)

	err := writer.Write(context.Background(), outbox.WriteParams{
		Topic:       "events",
		RoutingKey:  "user.created",
		MessageID:   "no-tx",
		MessageType: "UserCreated",
		Payload:     json.RawMessage(`{"id":1}`),
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, outboxpg.ErrNoTx)
}

// TestFetchPending_SkipLocked: two concurrent FetchPending calls
// against the same Store must claim disjoint subsets, never the same
// row. This is the relay-coordination contract — without SKIP LOCKED
// the second caller would block until the first commits.
func TestFetchPending_SkipLocked(t *testing.T) {
	dsn := startPostgres(t)
	pool := openAndMigrate(t, dsn)
	store := outboxpg.New(pool)

	ctx := context.Background()
	const total = 20
	for i := 0; i < total; i++ {
		require.NoError(t, store.Insert(ctx, mustEntry()))
	}

	var (
		wg     sync.WaitGroup
		seenMu sync.Mutex
		seen   = map[uuid.UUID]int{}
	)
	wg.Add(2)
	worker := func() {
		defer wg.Done()
		entries, err := store.FetchPending(ctx, total/2)
		require.NoError(t, err)
		seenMu.Lock()
		defer seenMu.Unlock()
		for _, e := range entries {
			seen[e.ID]++
		}
	}
	go worker()
	go worker()
	wg.Wait()

	for id, n := range seen {
		assert.Equal(t, 1, n, "id %s claimed by multiple workers", id)
	}
}

// TestFetchPending_PreservesFIFOByCreatedAt pins the FIFO ordering
// contract documented on [outbox.Relay] (default serial publish
// preserves insertion order). A bare UPDATE ... RETURNING does NOT
// preserve any CTE ORDER BY, so this test would have caught the
// previous implementation silently shuffling rows. The fix carries
// row_number() through the claim CTE and re-orders the final SELECT.
func TestFetchPending_PreservesFIFOByCreatedAt(t *testing.T) {
	dsn := startPostgres(t)
	pool := openAndMigrate(t, dsn)
	store := outboxpg.New(pool)

	ctx := context.Background()
	const total = 50
	wantOrder := make([]uuid.UUID, 0, total)
	base := time.Now().UTC().Add(-time.Hour)
	// Stagger created_at deterministically so the FIFO order is well
	// defined and not subject to clock granularity inside the insert
	// loop.
	for i := 0; i < total; i++ {
		e := mustEntry()
		e.CreatedAt = base.Add(time.Duration(i) * time.Millisecond)
		require.NoError(t, store.Insert(ctx, e))
		// Override created_at because Insert stamps NOW() server-side;
		// we need the staggered values for a strict ordering assertion.
		_, err := pool.Exec(ctx, `UPDATE outbox_entries SET created_at = $1 WHERE id = $2`, e.CreatedAt, e.ID)
		require.NoError(t, err)
		wantOrder = append(wantOrder, e.ID)
	}

	got, err := store.FetchPending(ctx, total)
	require.NoError(t, err)
	require.Len(t, got, total)

	gotOrder := make([]uuid.UUID, len(got))
	for i := range got {
		gotOrder[i] = got[i].ID
	}
	assert.Equal(t, wantOrder, gotOrder, "FetchPending must return entries oldest-first by created_at")
}

// TestMarkPublished_StaleStateAfterReset: if ResetStaleProcessing
// pulls a row back to pending while a publish is in flight, the
// follow-up MarkPublished must surface ErrStaleState so the relay
// drops the attempt rather than recording a successful publish on a
// re-claimed row.
func TestMarkPublished_StaleStateAfterReset(t *testing.T) {
	dsn := startPostgres(t)
	pool := openAndMigrate(t, dsn)
	store := outboxpg.New(pool)

	ctx := context.Background()
	require.NoError(t, store.Insert(ctx, mustEntry()))
	claimed, err := store.FetchPending(ctx, 10)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	id := claimed[0].ID.String()

	// Simulate a stale recovery while the publish is "in flight" by
	// hand-resetting the row.
	_, err = pool.Exec(ctx, `UPDATE outbox_entries SET status = 'pending', updated_at = NOW() - INTERVAL '1 hour' WHERE id = $1`, id)
	require.NoError(t, err)

	err = store.MarkPublished(ctx, id, time.Now())
	require.Error(t, err)
	assert.True(t, errors.Is(err, outbox.ErrStaleState), "want ErrStaleState, got %v", err)
}

// TestMarkPublished_NotFoundOnMissingID: distinguishing ErrNotFound
// vs ErrStaleState lets the relay log differently — a missing row
// is operator-visible drift (manual DELETE?); a stale state is a
// race that the relay can simply skip.
func TestMarkPublished_NotFoundOnMissingID(t *testing.T) {
	dsn := startPostgres(t)
	pool := openAndMigrate(t, dsn)
	store := outboxpg.New(pool)

	err := store.MarkPublished(context.Background(), uuid.NewString(), time.Now())
	require.Error(t, err)
	assert.True(t, errors.Is(err, outbox.ErrNotFound), "want ErrNotFound, got %v", err)
}

// TestIncrementAttempts_BumpsAndSchedulesRetry: a transient failure
// must bump attempts, store last_error, set next_retry_at in the
// future, and return the row to pending. FetchPending must skip the
// row until next_retry_at has elapsed.
func TestIncrementAttempts_BumpsAndSchedulesRetry(t *testing.T) {
	dsn := startPostgres(t)
	pool := openAndMigrate(t, dsn)
	store := outboxpg.New(pool)

	ctx := context.Background()
	require.NoError(t, store.Insert(ctx, mustEntry()))
	claimed, err := store.FetchPending(ctx, 10)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	id := claimed[0].ID.String()

	retryAt := time.Now().Add(10 * time.Minute).UTC()
	require.NoError(t, store.IncrementAttempts(ctx, id, "downstream 503", retryAt))

	// FetchPending must skip — next_retry_at is in the future.
	next, err := store.FetchPending(ctx, 10)
	require.NoError(t, err)
	assert.Empty(t, next, "row must stay invisible until next_retry_at passes")

	// Verify attempts incremented and last_error stored.
	var attempts int
	var lastErr string
	err = pool.QueryRow(ctx, `SELECT attempts, last_error FROM outbox_entries WHERE id = $1`, id).Scan(&attempts, &lastErr)
	require.NoError(t, err)
	assert.Equal(t, 1, attempts)
	assert.Equal(t, "downstream 503", lastErr)
}

// TestResetStaleProcessing_RecoversCrashedRelay: when a relay crashes
// mid-publish the row sits in processing forever without recovery.
// ResetStaleProcessing must pull it back to pending so a fresh relay
// can re-claim it.
func TestResetStaleProcessing_RecoversCrashedRelay(t *testing.T) {
	dsn := startPostgres(t)
	pool := openAndMigrate(t, dsn)
	store := outboxpg.New(pool)

	ctx := context.Background()
	require.NoError(t, store.Insert(ctx, mustEntry()))
	claimed, err := store.FetchPending(ctx, 10)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	id := claimed[0].ID

	// Backdate updated_at so the row qualifies as stale.
	_, err = pool.Exec(ctx, `UPDATE outbox_entries SET updated_at = NOW() - INTERVAL '1 hour' WHERE id = $1`, id)
	require.NoError(t, err)

	n, err := store.ResetStaleProcessing(ctx, 5*time.Minute)
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)

	// Re-claimable on the next FetchPending.
	again, err := store.FetchPending(ctx, 10)
	require.NoError(t, err)
	require.Len(t, again, 1)
	assert.Equal(t, id, again[0].ID)
}

// TestHeartbeat_KeepsFreshClaimAlive: a live relay's Heartbeat must
// bump updated_at so ResetStaleProcessing does not pull the row out
// from under it. Without this contract a slow publish would race the
// stale-recovery loop.
func TestHeartbeat_KeepsFreshClaimAlive(t *testing.T) {
	dsn := startPostgres(t)
	pool := openAndMigrate(t, dsn)
	store := outboxpg.New(pool)

	ctx := context.Background()
	require.NoError(t, store.Insert(ctx, mustEntry()))
	claimed, err := store.FetchPending(ctx, 10)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	id := claimed[0].ID.String()

	// Backdate updated_at, then Heartbeat. After Heartbeat the row
	// should no longer be stale for a 5-minute threshold.
	_, err = pool.Exec(ctx, `UPDATE outbox_entries SET updated_at = NOW() - INTERVAL '1 hour' WHERE id = $1`, id)
	require.NoError(t, err)

	touched, err := store.Heartbeat(ctx, []string{id})
	require.NoError(t, err)
	assert.Equal(t, int64(1), touched)

	n, err := store.ResetStaleProcessing(ctx, 5*time.Minute)
	require.NoError(t, err)
	assert.Zero(t, n, "Heartbeat must reset the stale clock")
}

// TestRetentionDeletes: published / failed rows older than the cutoff
// must be removed; rows newer than the cutoff and rows in
// pending/processing must be untouched.
func TestRetentionDeletes(t *testing.T) {
	dsn := startPostgres(t)
	pool := openAndMigrate(t, dsn)
	store := outboxpg.New(pool)

	ctx := context.Background()
	old := time.Now().Add(-2 * time.Hour).UTC()

	// Two published: one old, one fresh. Same for failed.
	rows := []struct {
		status      outbox.Status
		ts          time.Time
		publishedAt *time.Time
	}{
		{outbox.StatusPublished, old, &old},
		{outbox.StatusPublished, time.Now().UTC(), tptr(time.Now())},
		{outbox.StatusFailed, old, nil},
		{outbox.StatusFailed, time.Now().UTC(), nil},
		{outbox.StatusPending, old, nil}, // must NOT be deleted by either sweep
	}
	for _, r := range rows {
		e := mustEntry()
		e.Status = r.status
		e.CreatedAt = r.ts
		e.PublishedAt = r.publishedAt
		require.NoError(t, store.Insert(ctx, e))
		// updated_at gets created_at by Insert; force published_at into
		// the row for the published case (Insert path uses entry.PublishedAt).
		_, err := pool.Exec(ctx, `UPDATE outbox_entries SET updated_at = $1 WHERE id = $2`, r.ts, e.ID)
		require.NoError(t, err)
	}

	cutoff := time.Now().Add(-1 * time.Hour)
	n, err := store.DeletePublishedBefore(ctx, cutoff)
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)

	n, err = store.DeleteFailedBefore(ctx, cutoff)
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)

	// Pending row still present.
	pending, err := store.CountPending(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), pending)
}

func tptr(t time.Time) *time.Time { return &t }
