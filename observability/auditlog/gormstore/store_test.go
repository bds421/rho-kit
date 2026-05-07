package gormstore

import (
	"context"
	"encoding/base64"
	"fmt"
	"io/fs"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/sqldb/memdb"
	"github.com/bds421/rho-kit/observability/auditlog"
)

func setupStore(t *testing.T) *GormStore {
	t.Helper()
	sub, err := fs.Sub(Migrations, "migrations")
	require.NoError(t, err)
	db := memdb.New(t, sub)
	s := New(db)
	return s
}

func TestAppendAndQuery(t *testing.T) {
	s := setupStore(t)
	ctx := context.Background()

	event := auditlog.Event{
		ID:        "evt-1",
		Timestamp: time.Now(),
		Actor:     "alice",
		Action:    "create",
		Resource:  "orders/1",
		Status:    "success",
	}
	require.NoError(t, s.Append(ctx, event))

	events, cursor, err := s.Query(ctx, auditlog.Filter{}, "", 10)
	require.NoError(t, err)
	assert.Len(t, events, 1)
	assert.Empty(t, cursor)
	assert.Equal(t, "evt-1", events[0].ID)
	assert.Equal(t, "alice", events[0].Actor)
}

func TestQuery_Filters(t *testing.T) {
	s := setupStore(t)
	ctx := context.Background()

	now := time.Now()
	events := []auditlog.Event{
		{ID: "1", Timestamp: now.Add(-2 * time.Hour), Actor: "alice", Action: "create", Resource: "orders/1", Status: "success"},
		{ID: "2", Timestamp: now.Add(-1 * time.Hour), Actor: "bob", Action: "update", Resource: "orders/1", Status: "success"},
		{ID: "3", Timestamp: now, Actor: "alice", Action: "delete", Resource: "users/1", Status: "failure"},
	}
	for _, e := range events {
		require.NoError(t, s.Append(ctx, e))
	}

	// Filter by actor.
	result, _, err := s.Query(ctx, auditlog.Filter{Actor: "alice"}, "", 10)
	require.NoError(t, err)
	assert.Len(t, result, 2)

	// Filter by action.
	result, _, err = s.Query(ctx, auditlog.Filter{Action: "update"}, "", 10)
	require.NoError(t, err)
	assert.Len(t, result, 1)

	// Filter by resource prefix.
	result, _, err = s.Query(ctx, auditlog.Filter{Resource: "orders"}, "", 10)
	require.NoError(t, err)
	assert.Len(t, result, 2)

	// Filter by time range.
	result, _, err = s.Query(ctx, auditlog.Filter{Since: now.Add(-90 * time.Minute)}, "", 10)
	require.NoError(t, err)
	assert.Len(t, result, 2)
}

func TestDeleteBefore(t *testing.T) {
	s := setupStore(t)
	ctx := context.Background()

	now := time.Now()
	events := []auditlog.Event{
		{ID: "old-1", Timestamp: now.Add(-48 * time.Hour), Actor: "a", Action: "x", Resource: "r", Status: "s"},
		{ID: "old-2", Timestamp: now.Add(-25 * time.Hour), Actor: "a", Action: "x", Resource: "r", Status: "s"},
		{ID: "new-1", Timestamp: now, Actor: "a", Action: "x", Resource: "r", Status: "s"},
	}
	for _, e := range events {
		require.NoError(t, s.Append(ctx, e))
	}

	deleted, err := s.DeleteBefore(ctx, now.Add(-24*time.Hour))
	require.NoError(t, err)
	assert.Equal(t, int64(2), deleted)

	remaining, _, err := s.Query(ctx, auditlog.Filter{}, "", 10)
	require.NoError(t, err)
	assert.Len(t, remaining, 1)
	assert.Equal(t, "new-1", remaining[0].ID)
}

// TestDeleteBefore_BatchesAcrossDialects pins the MEDIUM finding: the prior
// `Limit(N).Delete(...)` form is not portable (PostgreSQL rejects DELETE
// LIMIT, GORM may silently drop the limit). The fix selects primary keys
// per batch, then deletes by id IN (...). With more than retentionBatchSize
// expired rows, the loop must complete and remove every old row.
func TestDeleteBefore_BatchesAcrossDialects(t *testing.T) {
	s := setupStore(t)
	ctx := context.Background()

	now := time.Now()
	const oldRows = retentionBatchSize + 5
	for i := 0; i < oldRows; i++ {
		require.NoError(t, s.Append(ctx, auditlog.Event{
			ID:        fmt.Sprintf("old-%05d", i),
			Timestamp: now.Add(-48 * time.Hour),
			Actor:     "a", Action: "x", Resource: "r", Status: "s",
		}))
	}
	require.NoError(t, s.Append(ctx, auditlog.Event{
		ID:        "new-1",
		Timestamp: now,
		Actor:     "a", Action: "x", Resource: "r", Status: "s",
	}))

	deleted, err := s.DeleteBefore(ctx, now.Add(-24*time.Hour))
	require.NoError(t, err)
	assert.Equal(t, int64(oldRows), deleted)

	remaining, _, err := s.Query(ctx, auditlog.Filter{}, "", oldRows+10)
	require.NoError(t, err)
	require.Len(t, remaining, 1)
	assert.Equal(t, "new-1", remaining[0].ID)
}

func TestNew_PanicsOnNilDB(t *testing.T) {
	assert.Panics(t, func() { New(nil) })
}

func TestWithCursorSecret_PanicsOnShortKey(t *testing.T) {
	assert.Panics(t, func() { WithCursorSecret([]byte("too-short")) })
}

func TestCursor_RoundTripVerifies(t *testing.T) {
	s := setupStore(t)
	ts := time.Now().UTC()
	cursor := s.encodeCursor(ts, "evt-42")
	gotTS, gotID, err := s.decodeCursor(cursor)
	require.NoError(t, err)
	assert.Equal(t, ts.UnixNano(), gotTS.UnixNano())
	assert.Equal(t, "evt-42", gotID)
}

func TestCursor_RejectsTamperedTimestamp(t *testing.T) {
	s := setupStore(t)
	cursor := s.encodeCursor(time.Now().UTC(), "evt-1")

	// Decode the payload, change the timestamp to year 3000, re-encode WITHOUT
	// touching the signature — the verify step must reject it.
	parts := splitCursor(t, cursor)
	body, err := base64Decode(parts[0])
	require.NoError(t, err)
	tampered := []byte("99999999999999999999|evt-1") // far-future nanos
	_ = body
	tamperedCursor := base64Encode(tampered) + "." + parts[1]

	_, _, err = s.decodeCursor(tamperedCursor)
	require.ErrorIs(t, err, ErrCursorInvalid)
}

func TestCursor_RejectsCrossSecretCursor(t *testing.T) {
	// A cursor minted by store-A (one secret) must be rejected by store-B
	// (different secret). Mirrors the production scenario where two
	// replicas with different lazily-generated secrets reject each other's
	// cursors — the warning to set WithCursorSecret in production exists
	// precisely for this.
	storeA := setupStore(t)
	storeB := setupStore(t)
	cursor := storeA.encodeCursor(time.Now().UTC(), "evt-1")
	_, _, err := storeB.decodeCursor(cursor)
	require.ErrorIs(t, err, ErrCursorInvalid)
}

func TestCursor_RejectsMalformed(t *testing.T) {
	s := setupStore(t)
	cases := []string{
		"",
		"no-dot",
		"!!.!!",
		"YWJj.YWJj", // base64-valid but signature won't match
	}
	for _, c := range cases {
		_, _, err := s.decodeCursor(c)
		assert.ErrorIs(t, err, ErrCursorInvalid, "input %q", c)
	}
}

// splitCursor isolates the payload and signature halves of a signed cursor
// so the tamper test can rewrite the payload without touching the signature
// — the exact attack the HMAC defends against.
func splitCursor(t *testing.T, cursor string) [2]string {
	t.Helper()
	for i := 0; i < len(cursor); i++ {
		if cursor[i] == '.' {
			return [2]string{cursor[:i], cursor[i+1:]}
		}
	}
	t.Fatalf("malformed cursor %q has no '.'", cursor)
	return [2]string{}
}

func base64Encode(b []byte) string          { return base64.RawURLEncoding.EncodeToString(b) }
func base64Decode(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }
