//go:build integration && nativepostgres

package outboxpg

import (
	"context"
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
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	writer := outboxpg.New(pool)
	routes := outboxpg.WithClaimRoutes(
		outboxpg.ClaimRoute{Topic: "events", RoutingKey: "web"},
		outboxpg.ClaimRoute{Topic: "changes", RoutingKey: "audit"},
	)
	selected := outboxpg.New(pool, routes)
	base := time.Now().UTC().Add(-time.Hour)
	insert := func(topic, key string, order int) outbox.Entry {
		e := mustEntry()
		e.Topic, e.RoutingKey, e.CreatedAt = topic, key, base.Add(time.Duration(order)*time.Second)
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
	require.NoError(t, writer.Insert(ctx, future))
	excluded := []uuid.UUID{deferred.ID, crossedA.ID, crossedB.ID, future.ID}
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
			entries, err := outboxpg.New(pool, routes).FetchPending(ctx, 1)
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
	_ = tx.Rollback(ctx)
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
}
