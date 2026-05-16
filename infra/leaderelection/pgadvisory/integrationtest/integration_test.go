//go:build integration

package integrationtest

import (
	"context"
	"database/sql"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/leaderelection/pgadvisory/v2"
	"github.com/bds421/rho-kit/infra/sqldb/dbtest/v2"
	"github.com/bds421/rho-kit/infra/v2/leaderelection"
)

func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	cfg := dbtest.StartPostgres(t, "pgadvisory_le_test")
	dsn := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=disable",
		cfg.Host, cfg.Port, cfg.User, cfg.Password, cfg.Name)
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.Ping())
	return db
}

// One Elector acquires; OnAcquired fires; cancelling the context returns
// the ctx error and OnLost fires.
func TestElector_AcquiresAndShutsDownOnCtxCancel(t *testing.T) {
	db := newTestDB(t)
	key := fmt.Sprintf("le-%d", time.Now().UnixNano())

	e := pgadvisory.New(db, key,
		pgadvisory.WithRetryInterval(50*time.Millisecond),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	acquired := make(chan struct{}, 1)
	lost := atomic.Int64{}
	runErr := make(chan error, 1)

	go func() {
		runErr <- e.Run(ctx, leaderelection.Callbacks{
			OnAcquired: func(_ context.Context) {
				select {
				case acquired <- struct{}{}:
				default:
				}
			},
			OnLost: func() { lost.Add(1) },
		})
	}()

	select {
	case <-acquired:
	case <-time.After(5 * time.Second):
		t.Fatal("OnAcquired was never invoked within 5s")
	}

	assert.True(t, e.IsLeader())

	cancel()
	select {
	case err := <-runErr:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return within 10s after ctx cancel")
	}
	assert.Equal(t, int64(1), lost.Load(),
		"OnLost must fire exactly once for the acquired term on shutdown")
	assert.False(t, e.IsLeader())
}

// A second elector on the same key, against the SAME database, is blocked
// until the first relinquishes — Postgres advisory locks are session-scoped,
// so we need two separate *sql.DB connection pools to model two replicas.
func TestElector_TwoCompetingElectorsOnSameKey(t *testing.T) {
	db1 := newTestDB(t)
	db2 := newTestDB(t)
	key := fmt.Sprintf("le-compete-%d", time.Now().UnixNano())

	e1 := pgadvisory.New(db1, key, pgadvisory.WithRetryInterval(100*time.Millisecond))
	e2 := pgadvisory.New(db2, key, pgadvisory.WithRetryInterval(100*time.Millisecond))

	ctx1, cancel1 := context.WithCancel(context.Background())
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()

	e1Acquired := make(chan struct{}, 1)
	e2Acquired := make(chan struct{}, 1)

	go func() {
		_ = e1.Run(ctx1, leaderelection.Callbacks{
			OnAcquired: func(_ context.Context) { e1Acquired <- struct{}{} },
		})
	}()
	select {
	case <-e1Acquired:
	case <-time.After(5 * time.Second):
		t.Fatal("first elector never acquired")
	}

	go func() {
		_ = e2.Run(ctx2, leaderelection.Callbacks{
			OnAcquired: func(_ context.Context) { e2Acquired <- struct{}{} },
		})
	}()

	// e2 must not acquire while e1 is leader, AND e1 must still report
	// leadership.
	select {
	case <-e2Acquired:
		t.Fatal("second elector acquired while first held the advisory lock")
	case <-time.After(500 * time.Millisecond):
	}
	assert.True(t, e1.IsLeader(), "first elector must still report leadership while holding the advisory lock")
	assert.False(t, e2.IsLeader(), "second elector must not report leadership while waiting")

	cancel1()

	// After e1 releases, e2 must eventually acquire AND e1 must transition
	// off leadership.
	select {
	case <-e2Acquired:
	case <-time.After(10 * time.Second):
		t.Fatal("second elector never acquired after first relinquished")
	}
	assert.True(t, e2.IsLeader(), "second elector must report leadership after acquiring")
	assert.Eventually(t, func() bool { return !e1.IsLeader() }, 5*time.Second, 20*time.Millisecond,
		"cancelled first elector must transition IsLeader to false")
}
