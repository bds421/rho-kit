//go:build integration

package sagapg

import (
	"context"
	"database/sql"
	"encoding/json"
	"net"
	"net/url"
	"strconv"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/data/saga/pgstore/v2"
	"github.com/bds421/rho-kit/infra/sqldb/dbtest/v2"
	"github.com/bds421/rho-kit/infra/v2/sqldb"
	"github.com/bds421/rho-kit/runtime/v2/saga"
)

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	cfg := dbtest.StartPostgres(t, "kit_test")
	db, err := sql.Open("pgx", postgresDSN(cfg))
	require.NoError(t, err, "open postgres")
	waitForPostgres(t, db)
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS saga_instances (
			id            VARCHAR(64)  PRIMARY KEY,
			definition    VARCHAR(128) NOT NULL,
			state         VARCHAR(32)  NOT NULL,
			current_step  INT          NOT NULL DEFAULT 0,
			compensated   JSONB        NOT NULL DEFAULT '[]'::jsonb,
			input         BYTEA,
			step_results  JSONB        NOT NULL DEFAULT '[]'::jsonb,
			last_error    TEXT         NOT NULL DEFAULT '',
			created_at    TIMESTAMPTZ  NOT NULL DEFAULT now(),
			updated_at    TIMESTAMPTZ  NOT NULL DEFAULT now()
		)`)
	require.NoError(t, err, "create table")
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_saga_instances_resumable
		ON saga_instances (state, updated_at)
		WHERE state IN ('pending', 'running', 'compensating')`)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func waitForPostgres(t *testing.T, db *sql.DB) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		lastErr = db.PingContext(ctx)
		cancel()
		if lastErr == nil {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("ping postgres: %v", lastErr)
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

func clearTable(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.Exec(`DELETE FROM saga_instances`)
	require.NoError(t, err, "truncate")
}

func TestStore_FirstWriteAndRead(t *testing.T) {
	db := testDB(t)
	clearTable(t, db)
	s := pgstore.New(db)
	ctx := context.Background()

	inst := saga.Instance{
		ID:          "inst-1",
		Definition:  "test-saga",
		State:       saga.StatePending,
		Input:       json.RawMessage(`{"k":"v"}`),
		Compensated: []int{},
		StepResults: []json.RawMessage{},
	}
	require.NoError(t, s.Put(ctx, inst), "first Put (UpdatedAt zero) takes INSERT path")

	got, err := s.Get(ctx, "inst-1")
	require.NoError(t, err)
	require.Equal(t, saga.StatePending, got.State)
	require.False(t, got.UpdatedAt.IsZero())
}

func TestStore_FirstWriteDuplicateIDSurfacesErrConcurrentUpdate(t *testing.T) {
	db := testDB(t)
	clearTable(t, db)
	s := pgstore.New(db)
	ctx := context.Background()
	inst := saga.Instance{ID: "dup", Definition: "test", State: saga.StatePending}
	require.NoError(t, s.Put(ctx, inst))

	// Second first-write with the same ID and zero UpdatedAt MUST NOT
	// clobber the existing row. The B2 IS-NULL escape used to allow
	// this; the split-path implementation closes the hole.
	err := s.Put(ctx, inst)
	require.ErrorIs(t, err, pgstore.ErrConcurrentUpdate,
		"duplicate first-write must surface ErrConcurrentUpdate, NOT silently clobber")
}

func TestStore_OptimisticUpdateAdvancesOnMatch(t *testing.T) {
	db := testDB(t)
	clearTable(t, db)
	s := pgstore.New(db)
	ctx := context.Background()

	require.NoError(t, s.Put(ctx, saga.Instance{
		ID: "inst-2", Definition: "t", State: saga.StatePending,
	}))
	got, err := s.Get(ctx, "inst-2")
	require.NoError(t, err)

	got.State = saga.StateRunning
	got.CurrentStep = 1
	require.NoError(t, s.Put(ctx, got), "matching updated_at → UPDATE succeeds")

	after, err := s.Get(ctx, "inst-2")
	require.NoError(t, err)
	require.Equal(t, saga.StateRunning, after.State)
	require.Equal(t, 1, after.CurrentStep)
	require.True(t, after.UpdatedAt.After(got.UpdatedAt))
}

func TestStore_OptimisticUpdateRejectsStale(t *testing.T) {
	db := testDB(t)
	clearTable(t, db)
	s := pgstore.New(db)
	ctx := context.Background()

	require.NoError(t, s.Put(ctx, saga.Instance{
		ID: "stale", Definition: "t", State: saga.StatePending,
	}))
	v1, _ := s.Get(ctx, "stale")
	v2, _ := s.Get(ctx, "stale")
	require.Equal(t, v1.UpdatedAt, v2.UpdatedAt)

	// Replica A writes first → wins.
	v1.State = saga.StateRunning
	require.NoError(t, s.Put(ctx, v1))

	// Replica B tries with the now-stale UpdatedAt → must reject.
	v2.State = saga.StateCompensating
	err := s.Put(ctx, v2)
	require.ErrorIs(t, err, pgstore.ErrConcurrentUpdate,
		"stale UpdatedAt MUST surface ErrConcurrentUpdate (no IS NULL escape)")

	// The state-of-record reflects replica A's write.
	after, _ := s.Get(ctx, "stale")
	require.Equal(t, saga.StateRunning, after.State)
}

func TestStore_GetUnknownReturnsErrInstanceNotFound(t *testing.T) {
	db := testDB(t)
	clearTable(t, db)
	s := pgstore.New(db)
	_, err := s.Get(context.Background(), "ghost")
	require.ErrorIs(t, err, saga.ErrInstanceNotFound)
}

func TestStore_ListResumableExcludesTerminal(t *testing.T) {
	db := testDB(t)
	clearTable(t, db)
	s := pgstore.New(db)
	ctx := context.Background()

	require.NoError(t, s.Put(ctx, saga.Instance{ID: "a", Definition: "t", State: saga.StatePending}))
	require.NoError(t, s.Put(ctx, saga.Instance{ID: "b", Definition: "t", State: saga.StateRunning}))
	require.NoError(t, s.Put(ctx, saga.Instance{ID: "c", Definition: "t", State: saga.StateCompensating}))
	require.NoError(t, s.Put(ctx, saga.Instance{ID: "d", Definition: "t", State: saga.StateCompleted}))
	require.NoError(t, s.Put(ctx, saga.Instance{ID: "e", Definition: "t", State: saga.StateFailed}))

	got, err := s.ListResumable(ctx, 0)
	require.NoError(t, err)
	ids := map[string]bool{}
	for _, i := range got {
		ids[i.ID] = true
	}
	require.True(t, ids["a"])
	require.True(t, ids["b"])
	require.True(t, ids["c"])
	require.False(t, ids["d"], "completed instance must not be resumable")
	require.False(t, ids["e"], "failed instance must not be resumable")
}

func TestStore_DeleteIsIdempotent(t *testing.T) {
	db := testDB(t)
	clearTable(t, db)
	s := pgstore.New(db)
	ctx := context.Background()
	require.NoError(t, s.Put(ctx, saga.Instance{ID: "x", Definition: "t", State: saga.StatePending}))
	require.NoError(t, s.Delete(ctx, "x"))
	require.NoError(t, s.Delete(ctx, "x"), "second Delete is a no-op")
	require.NoError(t, s.Delete(ctx, "never"))
	_, err := s.Get(ctx, "x")
	require.ErrorIs(t, err, saga.ErrInstanceNotFound)
}

func TestStore_StepResultsRoundTrip(t *testing.T) {
	db := testDB(t)
	clearTable(t, db)
	s := pgstore.New(db)
	ctx := context.Background()

	inst := saga.Instance{
		ID: "results", Definition: "t", State: saga.StatePending,
		StepResults: []json.RawMessage{
			json.RawMessage(`"a-output"`),
			json.RawMessage(`{"b":"output"}`),
		},
		Compensated: []int{1},
	}
	require.NoError(t, s.Put(ctx, inst))
	got, err := s.Get(ctx, "results")
	require.NoError(t, err)
	require.Len(t, got.StepResults, 2)
	require.Equal(t, `"a-output"`, string(got.StepResults[0]))
	require.Equal(t, `{"b":"output"}`, string(got.StepResults[1]))
	require.Equal(t, []int{1}, got.Compensated)
}
