package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/core/v2/apperror"
)

// execCall records a single Exec invocation so tests can assert on the
// arguments the Store maps onto the SQL placeholders.
type execCall struct {
	sql  string
	args []any
}

// fakeQuerier is a hand-rolled querier seam stand-in. Exec returns a
// caller-supplied CommandTag; QueryRow scans a caller-supplied existence flag
// so the Revoke not-found probe can be driven without a database.
type fakeQuerier struct {
	execCalls []execCall
	execTag   pgconn.CommandTag
	execErr   error

	exists bool
}

func (f *fakeQuerier) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	f.execCalls = append(f.execCalls, execCall{sql: sql, args: args})
	return f.execTag, f.execErr
}

func (f *fakeQuerier) Query(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
	panic("Query not used in these tests")
}

func (f *fakeQuerier) QueryRow(_ context.Context, _ string, _ ...any) pgx.Row {
	return existsRow{exists: f.exists}
}

// existsRow scans a single bool, mimicking the SELECT EXISTS(...) probe.
type existsRow struct{ exists bool }

func (r existsRow) Scan(dest ...any) error {
	if len(dest) != 1 {
		panic("existsRow.Scan expects exactly one destination")
	}
	p, ok := dest[0].(*bool)
	if !ok {
		panic("existsRow.Scan expects a *bool destination")
	}
	*p = r.exists
	return nil
}

// TestRevoke_ZeroAtWritesNull pins the contract that a zero revocation time is
// persisted as NULL rather than the 0001-01-01 sentinel, so the key reads back
// active — matching apikey.MemoryRepository for the same degenerate input.
func TestRevoke_ZeroAtWritesNull(t *testing.T) {
	fq := &fakeQuerier{execTag: pgconn.NewCommandTag("UPDATE 1")}
	s := &Store{pool: fq}

	err := s.Revoke(context.Background(), "k1", time.Time{})
	require.NoError(t, err)

	require.Len(t, fq.execCalls, 1)
	args := fq.execCalls[0].args
	require.Len(t, args, 2)
	require.Equal(t, "k1", args[0])

	// A zero at must map to a nil *time.Time (SQL NULL), never a non-nil
	// 0001-01-01 sentinel that would read back as revoked-forever.
	at, ok := args[1].(*time.Time)
	require.True(t, ok, "revoked_at arg must be a *time.Time")
	require.Nil(t, at, "zero at must persist NULL, not a sentinel timestamp")
}

// TestRevoke_NonZeroAtWritesUTC confirms a real revocation time is forwarded as
// a non-nil UTC pointer.
func TestRevoke_NonZeroAtWritesUTC(t *testing.T) {
	fq := &fakeQuerier{execTag: pgconn.NewCommandTag("UPDATE 1")}
	s := &Store{pool: fq}

	loc := time.FixedZone("UTC+2", 2*3600)
	at := time.Date(2026, 6, 16, 12, 0, 0, 0, loc)

	err := s.Revoke(context.Background(), "k1", at)
	require.NoError(t, err)

	require.Len(t, fq.execCalls, 1)
	got, ok := fq.execCalls[0].args[1].(*time.Time)
	require.True(t, ok, "revoked_at arg must be a *time.Time")
	require.NotNil(t, got)
	require.True(t, got.Equal(at), "revocation instant must be preserved")
	require.Equal(t, time.UTC, got.Location(), "revoked_at must be stored in UTC")
}

// TestRevoke_MissingKeyReturnsNotFound covers the probe path: zero rows updated
// and the existence check reporting absence yields a NotFound error.
func TestRevoke_MissingKeyReturnsNotFound(t *testing.T) {
	fq := &fakeQuerier{execTag: pgconn.NewCommandTag("UPDATE 0"), exists: false}
	s := &Store{pool: fq}

	err := s.Revoke(context.Background(), "missing", time.Now())
	require.Error(t, err)
	require.True(t, apperror.IsNotFound(err), "absent key must yield NotFound, got %v", err)
}

// TestRevoke_AlreadyRevokedIsIdempotent covers the probe path where the key
// exists but no row was updated (already revoked): Revoke returns nil.
func TestRevoke_AlreadyRevokedIsIdempotent(t *testing.T) {
	fq := &fakeQuerier{execTag: pgconn.NewCommandTag("UPDATE 0"), exists: true}
	s := &Store{pool: fq}

	err := s.Revoke(context.Background(), "k1", time.Now())
	require.NoError(t, err, "revoking an already-revoked key must be a no-op")
}
