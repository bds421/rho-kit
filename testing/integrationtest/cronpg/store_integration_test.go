//go:build integration

package cronpg

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"net/url"
	"strconv"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/data/cron/pgstore/v2"
	"github.com/bds421/rho-kit/infra/sqldb/dbtest/v2"
	"github.com/bds421/rho-kit/infra/v2/sqldb"
)

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	cfg := dbtest.StartPostgres(t, "kit_test")
	db, err := sql.Open("pgx", postgresDSN(cfg))
	require.NoError(t, err, "open postgres")
	waitForPostgres(t, db)
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS cron_schedules (
			name        VARCHAR(128) PRIMARY KEY,
			spec        VARCHAR(128) NOT NULL,
			enabled     BOOLEAN      NOT NULL DEFAULT TRUE,
			description TEXT,
			created_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
			updated_at  TIMESTAMPTZ  NOT NULL DEFAULT now()
		)`)
	require.NoError(t, err, "create table")
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
	_, err := db.Exec(`DELETE FROM cron_schedules`)
	require.NoError(t, err, "truncate")
}

func TestStore_AddGetList(t *testing.T) {
	db := testDB(t)
	clearTable(t, db)
	s := pgstore.New(db)
	ctx := context.Background()

	require.NoError(t, s.Add(ctx, pgstore.ScheduleRecord{
		Name:    "nightly-cleanup",
		Spec:    "0 3 * * *",
		Enabled: true,
	}))
	require.NoError(t, s.Add(ctx, pgstore.ScheduleRecord{
		Name:        "hourly-report",
		Spec:        "@hourly",
		Enabled:     true,
		Description: "Hourly metric roll-up",
	}))

	got, err := s.Get(ctx, "hourly-report")
	require.NoError(t, err)
	require.Equal(t, "@hourly", got.Spec)
	require.Equal(t, "Hourly metric roll-up", got.Description)
	require.True(t, got.Enabled)
	require.False(t, got.CreatedAt.IsZero())
	require.False(t, got.UpdatedAt.IsZero())

	all, err := s.List(ctx)
	require.NoError(t, err)
	require.Len(t, all, 2)
	require.Equal(t, "hourly-report", all[0].Name, "sorted by name")
	require.Equal(t, "nightly-cleanup", all[1].Name)
}

func TestStore_AddDuplicateFails(t *testing.T) {
	db := testDB(t)
	clearTable(t, db)
	s := pgstore.New(db)
	ctx := context.Background()
	rec := pgstore.ScheduleRecord{Name: "dup", Spec: "@daily"}
	require.NoError(t, s.Add(ctx, rec))
	require.Error(t, s.Add(ctx, rec), "second Add of same name must fail (PK violation)")
}

func TestStore_UpsertReplacesSpec(t *testing.T) {
	db := testDB(t)
	clearTable(t, db)
	s := pgstore.New(db)
	ctx := context.Background()

	require.NoError(t, s.Upsert(ctx, pgstore.ScheduleRecord{Name: "x", Spec: "@daily"}))
	require.NoError(t, s.Upsert(ctx, pgstore.ScheduleRecord{Name: "x", Spec: "@hourly"}))
	got, err := s.Get(ctx, "x")
	require.NoError(t, err)
	require.Equal(t, "@hourly", got.Spec)
}

func TestStore_EnableFlips(t *testing.T) {
	db := testDB(t)
	clearTable(t, db)
	s := pgstore.New(db)
	ctx := context.Background()
	require.NoError(t, s.Add(ctx, pgstore.ScheduleRecord{Name: "x", Spec: "@daily"}))
	require.NoError(t, s.Enable(ctx, "x", false))
	got, err := s.Get(ctx, "x")
	require.NoError(t, err)
	require.False(t, got.Enabled)
	require.NoError(t, s.Enable(ctx, "x", true))
	got, err = s.Get(ctx, "x")
	require.NoError(t, err)
	require.True(t, got.Enabled)
}

func TestStore_EnableUnknownNameErrors(t *testing.T) {
	db := testDB(t)
	clearTable(t, db)
	s := pgstore.New(db)
	err := s.Enable(context.Background(), "ghost", true)
	require.ErrorIs(t, err, pgstore.ErrScheduleNotFound)
}

func TestStore_GetUnknownErrors(t *testing.T) {
	db := testDB(t)
	clearTable(t, db)
	s := pgstore.New(db)
	_, err := s.Get(context.Background(), "ghost")
	require.True(t, errors.Is(err, pgstore.ErrScheduleNotFound))
}

func TestStore_RemoveIdempotent(t *testing.T) {
	db := testDB(t)
	clearTable(t, db)
	s := pgstore.New(db)
	ctx := context.Background()
	require.NoError(t, s.Add(ctx, pgstore.ScheduleRecord{Name: "x", Spec: "@daily"}))
	require.NoError(t, s.Remove(ctx, "x"))
	require.NoError(t, s.Remove(ctx, "x"), "removing again is a no-op")
	require.NoError(t, s.Remove(ctx, "never-existed"))
}
