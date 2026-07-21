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

// queryCall records a single QueryRow invocation so tests can assert on the
// arguments the Store maps onto the SQL placeholders.
type queryCall struct {
	sql  string
	args []any
}

// fakeQuerier is a hand-rolled querier seam stand-in for the atomic Revoke
// CTE (UPDATE … RETURNING + EXISTS), which runs as a single QueryRow scanning
// (updated, present).
type fakeQuerier struct {
	queryCalls []queryCall
	queryErr   error

	// updated/present are the two bools the Revoke CTE returns.
	updated bool
	present bool
}

func (f *fakeQuerier) Exec(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
	panic("Exec not used by atomic Revoke")
}

func (f *fakeQuerier) Query(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
	panic("Query not used in these tests")
}

func (f *fakeQuerier) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	f.queryCalls = append(f.queryCalls, queryCall{sql: sql, args: args})
	if f.queryErr != nil {
		return errRow{err: f.queryErr}
	}
	return revokeRow{updated: f.updated, present: f.present}
}

type errRow struct{ err error }

func (r errRow) Scan(...any) error { return r.err }

// revokeRow scans (updated, present) from the atomic Revoke CTE.
type revokeRow struct {
	updated bool
	present bool
}

func (r revokeRow) Scan(dest ...any) error {
	if len(dest) != 2 {
		panic("revokeRow.Scan expects exactly two destinations")
	}
	u, ok := dest[0].(*bool)
	if !ok {
		panic("revokeRow.Scan expects *bool for updated")
	}
	p, ok := dest[1].(*bool)
	if !ok {
		panic("revokeRow.Scan expects *bool for present")
	}
	*u = r.updated
	*p = r.present
	return nil
}

// TestRevoke_ZeroAtWritesNull pins the contract that a zero revocation time is
// persisted as NULL rather than the 0001-01-01 sentinel, so the key reads back
// active — matching apikey.MemoryRepository for the same degenerate input.
func TestRevoke_ZeroAtWritesNull(t *testing.T) {
	fq := &fakeQuerier{updated: true, present: true}
	s := &Store{pool: fq}

	err := s.Revoke(context.Background(), "k1", time.Time{})
	require.NoError(t, err)

	require.Len(t, fq.queryCalls, 1)
	args := fq.queryCalls[0].args
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
	fq := &fakeQuerier{updated: true, present: true}
	s := &Store{pool: fq}

	loc := time.FixedZone("UTC+2", 2*3600)
	at := time.Date(2026, 6, 16, 12, 0, 0, 0, loc)

	err := s.Revoke(context.Background(), "k1", at)
	require.NoError(t, err)

	require.Len(t, fq.queryCalls, 1)
	got, ok := fq.queryCalls[0].args[1].(*time.Time)
	require.True(t, ok, "revoked_at arg must be a *time.Time")
	require.NotNil(t, got)
	require.True(t, got.Equal(at), "revocation instant must be preserved")
	require.Equal(t, time.UTC, got.Location(), "revoked_at must be stored in UTC")
}

// TestRevoke_MissingKeyReturnsNotFound covers the CTE path: neither updated
// nor present yields a NotFound error.
func TestRevoke_MissingKeyReturnsNotFound(t *testing.T) {
	fq := &fakeQuerier{updated: false, present: false}
	s := &Store{pool: fq}

	err := s.Revoke(context.Background(), "missing", time.Now())
	require.Error(t, err)
	require.True(t, apperror.IsNotFound(err), "absent key must yield NotFound, got %v", err)
}

// TestRevoke_AlreadyRevokedIsIdempotent covers present && !updated: the key
// exists but was already revoked — Revoke returns nil.
func TestRevoke_AlreadyRevokedIsIdempotent(t *testing.T) {
	fq := &fakeQuerier{updated: false, present: true}
	s := &Store{pool: fq}

	err := s.Revoke(context.Background(), "k1", time.Now())
	require.NoError(t, err, "revoking an already-revoked key must be a no-op")
}
