//go:build integration && nativepostgres

package outboxpg

import (
	"context"
	"errors"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	outboxpg "github.com/bds421/rho-kit/infra/outbox/postgres/v2"
	"github.com/bds421/rho-kit/infra/v2/outbox"
)

// This opt-in test uses a preprovisioned disposable native database. It never
// starts Docker or alters cluster roles; the caller owns database cleanup.
func TestFetchPending_ClaimRoutesNative(t *testing.T) {
	dsn := os.Getenv("RHO_OUTBOX_NATIVE_DATABASE_URL")
	u, err := url.Parse(dsn)
	if err != nil || u.Hostname() != "127.0.0.1" || !strings.HasPrefix(u.Path, "/rho_outbox_claims_") ||
		os.Getenv("RHO_OUTBOX_NATIVE_DATABASE_ISOLATED") != "1" {
		t.Fatal("explicit isolated native outbox test database required")
	}
	pool := openAndMigrate(t, dsn)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	writer := outboxpg.New(pool)
	routes := outboxpg.WithClaimRoutes(
		outboxpg.ClaimRoute{Topic: "events", RoutingKey: "web", MessageType: "UserCreated", SchemaVersion: 1},
		outboxpg.ClaimRoute{Topic: "changes", RoutingKey: "audit", MessageType: "UserChanged", SchemaVersion: 2},
	)
	selected := outboxpg.NewWithOptions(pool, routes)
	base := time.Now().UTC().Add(-time.Hour)
	insert := func(topic, key string, order int) outbox.Entry {
		e := mustEntry()
		e.Topic, e.RoutingKey, e.CreatedAt = topic, key, base.Add(time.Duration(order)*time.Second)
		e.MessageType, e.SchemaVersion = "UserCreated", 1
		if topic == "changes" {
			e.MessageType, e.SchemaVersion = "UserChanged", 2
		}
		require.NoError(t, writer.Insert(ctx, e))
		return e
	}
	deferred := insert("events", "deferred", 0)
	crossedA := insert("events", "audit", 1)
	crossedB := insert("changes", "web", 2)
	future := mustEntry()
	future.Topic, future.RoutingKey, future.CreatedAt = "events", "web", base.Add(3*time.Second)
	retry := time.Now().UTC().Add(time.Hour)
	future.NextRetryAt = &retry
	future.SchemaVersion = 1
	require.NoError(t, writer.Insert(ctx, future))
	wrongType := mustEntry()
	wrongType.RoutingKey, wrongType.MessageType, wrongType.SchemaVersion, wrongType.CreatedAt = "web", "UserChanged", 1, base.Add(time.Second)
	require.NoError(t, writer.Insert(ctx, wrongType))
	wrongVersion := wrongType
	wrongVersion.ID, wrongVersion.MessageType, wrongVersion.SchemaVersion = uuid.New(), "UserCreated", 2
	require.NoError(t, writer.Insert(ctx, wrongVersion))
	legacySchema := wrongType
	legacySchema.ID, legacySchema.MessageType, legacySchema.SchemaVersion = uuid.New(), "UserCreated", 0
	require.NoError(t, writer.Insert(ctx, legacySchema))
	excluded := []uuid.UUID{deferred.ID, crossedA.ID, crossedB.ID, future.ID, wrongType.ID, wrongVersion.ID, legacySchema.ID}
	snapshot := func() string {
		var raw string
		require.NoError(t, pool.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(e) ORDER BY id)::text FROM outbox_entries e WHERE id=ANY($1::uuid[])`, excluded).Scan(&raw))
		return raw
	}
	before := snapshot()
	a, b := insert("events", "web", 4), insert("changes", "audit", 5)
	c, d := insert("events", "web", 6), insert("changes", "audit", 7)
	batch, err := selected.FetchPending(ctx, 2)
	require.NoError(t, err)
	require.Len(t, batch, 2)
	require.Equal(t, []uuid.UUID{a.ID, b.ID}, []uuid.UUID{batch[0].ID, batch[1].ID}, "filter must precede limit and preserve selected FIFO")

	// Two replicas must claim disjoint eligible rows using the existing fences.
	type result struct {
		entries []outbox.Entry
		err     error
	}
	results := make(chan result, 2)
	var workers sync.WaitGroup
	for range 2 {
		workers.Go(func() {
			entries, err := outboxpg.NewWithOptions(pool, routes).FetchPending(ctx, 1)
			results <- result{entries, err}
		})
	}
	workers.Wait()
	ids := map[uuid.UUID]bool{}
	for range 2 {
		r := <-results
		require.NoError(t, r.err)
		require.Len(t, r.entries, 1)
		ids[r.entries[0].ID] = true
	}
	require.Equal(t, map[uuid.UUID]bool{c.ID: true, d.ID: true}, ids)
	require.Equal(t, before, snapshot(), "excluded whole rows, including tokens/attempts/timestamps, must remain identical")
	require.NoError(t, selected.MarkPublished(ctx, a.ID.String(), time.Now()))

	// Omitting the option retains legacy claim-all behavior.
	legacy, err := writer.FetchPending(ctx, 1)
	require.NoError(t, err)
	require.Len(t, legacy, 1)
	require.Equal(t, deferred.ID, legacy[0].ID)

	// Keep one selected UPDATE in flight behind an advisory barrier, then prove
	// a deferred row can still be locked independently with NOWAIT.
	lockExcluded := insert("events", "later-deferred", 8)
	held := insert("events", "web", 9)
	_, err = pool.Exec(ctx, `CREATE FUNCTION hold_selected_claim() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.routing_key = 'web' THEN PERFORM pg_advisory_xact_lock(81314, 1); END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER hold_selected_claim BEFORE UPDATE ON outbox_entries FOR EACH ROW EXECUTE FUNCTION hold_selected_claim()`)
	require.NoError(t, err)
	blocker, err := pool.Acquire(ctx)
	require.NoError(t, err)
	defer blocker.Release()
	_, err = blocker.Exec(ctx, `SELECT pg_advisory_lock(81314,1)`)
	require.NoError(t, err)
	unlock := sync.OnceFunc(func() {
		cleanup, done := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer done()
		_, _ = blocker.Exec(cleanup, `SELECT pg_advisory_unlock(81314,1)`)
	})
	var claimant sync.WaitGroup
	defer claimant.Wait()
	defer unlock()
	claimant.Go(func() {
		entries, err := selected.FetchPending(ctx, 1)
		results <- result{entries, err}
	})
	require.Eventually(t, func() bool {
		var waiting bool
		err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND classid=81314 AND objid=1 AND NOT granted AND database=(SELECT oid FROM pg_database WHERE datname=current_database()))`).Scan(&waiting)
		return err == nil && waiting
	}, time.Second, time.Millisecond)
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	var id uuid.UUID
	err = tx.QueryRow(ctx, `SELECT id FROM outbox_entries WHERE id=$1 FOR UPDATE NOWAIT`, lockExcluded.ID).Scan(&id)
	rollbackErr := tx.Rollback(ctx)
	require.NoError(t, rollbackErr)
	require.NoError(t, err, "filtered-out recipient must not hold a row lock during a claim")
	require.Equal(t, lockExcluded.ID, id)
	unlock()
	r := <-results
	require.NoError(t, r.err)
	require.Len(t, r.entries, 1)
	require.Equal(t, held.ID, r.entries[0].ID)
	var status string
	var attempts int
	var token *uuid.UUID
	require.NoError(t, pool.QueryRow(ctx, `SELECT status,attempts,claim_token FROM outbox_entries WHERE id=$1`, lockExcluded.ID).Scan(&status, &attempts, &token))
	require.Equal(t, string(outbox.StatusPending), status)
	require.Zero(t, attempts)
	require.Nil(t, token)
	// Cancellation while an eligible UPDATE is blocked must release its row lock
	// and keep every excluded whole row unchanged. No publisher is called.
	cancelEntry := insert("events", "web", 10)
	cancelForeignBefore := snapshot()
	_, err = blocker.Exec(ctx, `SELECT pg_advisory_lock(81314,1)`)
	require.NoError(t, err)
	cancelCtx, cancelClaim := context.WithCancel(ctx)
	defer cancelClaim()
	var cancellation sync.WaitGroup
	cancellation.Go(func() { entries, err := selected.FetchPending(cancelCtx, 1); results <- result{entries, err} })
	defer cancellation.Wait()
	// Release the session lock before joining, including assertion-failure paths.
	defer func() {
		cleanup, done := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer done()
		_, _ = blocker.Exec(cleanup, `SELECT pg_advisory_unlock(81314,1)`)
	}()
	require.Eventually(t, func() bool {
		var waiting bool
		err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND classid=81314 AND objid=1 AND NOT granted AND database=(SELECT oid FROM pg_database WHERE datname=current_database()))`).Scan(&waiting)
		return err == nil && waiting
	}, time.Second, time.Millisecond)
	cancelClaim()
	canceled := <-results
	require.True(t, errors.Is(canceled.err, context.Canceled))
	cancellation.Wait()
	_, err = blocker.Exec(ctx, `SELECT pg_advisory_unlock(81314,1)`)
	require.NoError(t, err)
	lockTx, err := pool.Begin(ctx)
	require.NoError(t, err)
	err = lockTx.QueryRow(ctx, `SELECT id FROM outbox_entries WHERE id=$1 FOR UPDATE NOWAIT`, cancelEntry.ID).Scan(&id)
	rollbackErr = lockTx.Rollback(ctx)
	require.NoError(t, err, "canceled statement must release its eligible row lock")
	require.NoError(t, rollbackErr)
	require.Equal(t, cancelForeignBefore, snapshot(), "cancellation must not change excluded complete rows")
	// Claim the same row, stale-reset it, and prove an old token cannot affect the
	// replacement owner even though the route and entry ID are unchanged.
	oldOwner := outboxpg.NewWithOptions(pool, routes)
	claimed, err := oldOwner.FetchPending(ctx, 1)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.Equal(t, cancelEntry.ID, claimed[0].ID)
	_, err = pool.Exec(ctx, `UPDATE outbox_entries SET updated_at=NOW()-interval '2 hours' WHERE id=$1`, cancelEntry.ID)
	require.NoError(t, err)
	// Foreign status variants exercise all maintenance paths, not just claims.
	foreign := []uuid.UUID{wrongType.ID, wrongVersion.ID, legacySchema.ID, lockExcluded.ID}
	for _, status := range []outbox.Status{outbox.StatusProcessing, outbox.StatusPublished, outbox.StatusFailed} {
		for _, template := range []outbox.Entry{deferred, crossedA, crossedB, wrongType, wrongVersion, legacySchema} {
			entry := template
			entry.ID = uuid.New()
			entry.Status = status
			entry.CreatedAt = base.Add(-2 * time.Hour)
			entry.PublishedAt = new(base)
			require.NoError(t, writer.Insert(ctx, entry))
			foreign = append(foreign, entry.ID)
		}
	}
	foreignSnapshot := func() string {
		var raw string
		require.NoError(t, pool.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(e) ORDER BY id)::text FROM outbox_entries e WHERE id=ANY($1::uuid[])`, foreign).Scan(&raw))
		return raw
	}
	foreignBefore := foreignSnapshot()
	reset, err := selected.ResetStaleProcessing(ctx, time.Hour)
	require.NoError(t, err)
	require.EqualValues(t, 1, reset)
	replacement := outboxpg.NewWithOptions(pool, routes)
	reclaimed, err := replacement.FetchPending(ctx, 1)
	require.NoError(t, err)
	require.Len(t, reclaimed, 1)
	require.Equal(t, cancelEntry.ID, reclaimed[0].ID)
	heartbeats, err := oldOwner.Heartbeat(ctx, []string{cancelEntry.ID.String()})
	require.NoError(t, err)
	require.Zero(t, heartbeats)
	require.ErrorIs(t, oldOwner.MarkPublished(ctx, cancelEntry.ID.String(), time.Now()), outbox.ErrStaleState)
	require.NoError(t, replacement.MarkFailed(ctx, cancelEntry.ID.String(), "test failure"))
	count, err := selected.CountPending(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, count, "only the selected future-due entry remains pending")
	// Selected old terminal entries can be pruned; foreign terminal rows cannot.
	_, err = pool.Exec(ctx, `UPDATE outbox_entries SET published_at=$2 WHERE id=$1`, a.ID, base)
	require.NoError(t, err)
	deleted, err := selected.DeletePublishedBefore(ctx, time.Now().Add(-time.Minute))
	require.NoError(t, err)
	require.EqualValues(t, 1, deleted)
	deleted, err = selected.DeleteFailedBefore(ctx, time.Now().Add(time.Minute))
	require.NoError(t, err)
	require.EqualValues(t, 1, deleted)
	require.Equal(t, foreignBefore, foreignSnapshot(), "all scoped maintenance must preserve foreign complete rows")
	// A zero selector means exact legacy schema, never all versions.
	legacyOnly := outboxpg.NewWithOptions(pool, outboxpg.WithClaimRoutes(outboxpg.ClaimRoute{Topic: "events", RoutingKey: "web", MessageType: "UserCreated", SchemaVersion: 0}))
	oldSchema, err := legacyOnly.FetchPending(ctx, 1)
	require.NoError(t, err)
	require.Len(t, oldSchema, 1)
	require.Equal(t, legacySchema.ID, oldSchema[0].ID)

}
