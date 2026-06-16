package postgres

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/data/v2/approval"
)

func testCursorSigner(t *testing.T) *approval.CursorSigner {
	t.Helper()
	signer, err := approval.NewCursorSigner([]byte("test-approval-cursor-key-32-bytes"))
	require.NoError(t, err)
	return signer
}

func TestNew_PanicsOnNilPool(t *testing.T) {
	assert.Panics(t, func() { New(nil, testCursorSigner(t)) })
}

func TestNew_PanicsOnNilSigner(t *testing.T) {
	assert.Panics(t, func() { New(&pgxpool.Pool{}, nil) })
}

func TestNew_PanicsOnNilOption(t *testing.T) {
	assert.Panics(t, func() { New(&pgxpool.Pool{}, testCursorSigner(t), nil) })
}

func TestStore_InvalidReceiverReturnsError(t *testing.T) {
	ctx := context.Background()

	for name, store := range map[string]*Store{
		"nil":  nil,
		"zero": {},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := store.Create(ctx, approval.Request{})
			assert.ErrorIs(t, err, approval.ErrInvalidStore)

			_, err = store.Get(ctx, "r")
			assert.ErrorIs(t, err, approval.ErrInvalidStore)

			_, _, err = store.List(ctx, approval.Query{TenantID: "tenant"})
			assert.ErrorIs(t, err, approval.ErrInvalidStore)

			_, err = store.Approve(ctx, "r", "approver", "ok")
			assert.ErrorIs(t, err, approval.ErrInvalidStore)

			_, err = store.MarkExecuted(ctx, "r")
			assert.ErrorIs(t, err, approval.ErrInvalidStore)
		})
	}
}

func TestList_ValidatesQueryScopeBeforeDBUse(t *testing.T) {
	store := &Store{pool: &pgxpool.Pool{}, clock: time.Now, cursorSigner: testCursorSigner(t)}

	_, _, err := store.List(context.Background(), approval.Query{TenantID: "tenant", AllTenants: true})
	assert.ErrorIs(t, err, approval.ErrQueryScopeConflict)
}

func TestCreate_UsesSharedValidationBeforeDBUse(t *testing.T) {
	store := &Store{pool: &pgxpool.Pool{}, clock: time.Now, cursorSigner: testCursorSigner(t)}

	r := approval.Request{
		ID:        strings.Repeat("a", approval.MaxIDLen+1),
		TenantID:  "tenant",
		Actor:     "agent",
		Action:    "user.delete",
		ExpiresAt: time.Now().Add(time.Hour),
	}
	_, err := store.Create(context.Background(), r)
	assert.ErrorIs(t, err, approval.ErrInvalidRequest)

	r.ID = "payload-too-large"
	r.Payload = make([]byte, approval.MaxPayloadSize+1)
	_, err = store.Create(context.Background(), r)
	assert.ErrorIs(t, err, approval.ErrInvalidRequest)

	r.Payload = nil
	r.Actor = strings.Repeat("a", approval.MaxActorLen+1)
	_, err = store.Create(context.Background(), r)
	assert.ErrorIs(t, err, approval.ErrInvalidRequest)
}

func TestDecide_UsesSharedValidationBeforeDBUse(t *testing.T) {
	store := &Store{pool: &pgxpool.Pool{}, clock: time.Now, cursorSigner: testCursorSigner(t)}

	_, err := store.Approve(context.Background(), "r1", strings.Repeat("a", approval.MaxActorLen+1), "ok")
	assert.ErrorIs(t, err, approval.ErrInvalidApprover)
}

// TestToPgTimestamp_TruncatesToMicroseconds locks in the precision
// contract Create relies on: timestamptz only keeps microseconds, so the
// timestamps Create echoes back must already be truncated. Otherwise the
// Request returned by Create would carry sub-microsecond nanoseconds that
// no subsequent Get/List round-trip could reproduce.
func TestToPgTimestamp_TruncatesToMicroseconds(t *testing.T) {
	// 123456789ns has a 789ns tail below microsecond resolution.
	in := time.Date(2026, 6, 16, 10, 30, 0, 123456789, time.UTC)
	got := toPgTimestamp(in)

	want := time.Date(2026, 6, 16, 10, 30, 0, 123456000, time.UTC)
	assert.True(t, got.Equal(want), "got %v, want %v", got, want)
	assert.Equal(t, 0, got.Nanosecond()%1000, "sub-microsecond digits must be zero")
	// A value already at microsecond precision must survive a round-trip
	// through toPgTimestamp unchanged (no double-truncation drift).
	assert.True(t, want.Equal(toPgTimestamp(want)))
}

// TestToPgTimestamp_NormalisesToUTC ensures a non-UTC input is converted
// so it matches the UTC values Get/List scan back out of the DB.
func TestToPgTimestamp_NormalisesToUTC(t *testing.T) {
	loc := time.FixedZone("UTC+2", 2*60*60)
	in := time.Date(2026, 6, 16, 12, 0, 0, 0, loc)
	got := toPgTimestamp(in)

	assert.Equal(t, time.UTC, got.Location())
	assert.True(t, got.Equal(in), "instant must be preserved across the UTC shift")
}

// TestToPgTimestamp_PreservesZero keeps the zero value detectable so
// nullableTime and IsZero branches still recognise an unset timestamp.
func TestToPgTimestamp_PreservesZero(t *testing.T) {
	assert.True(t, toPgTimestamp(time.Time{}).IsZero())
}
