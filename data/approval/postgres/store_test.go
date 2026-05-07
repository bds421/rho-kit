package postgres

import (
	"context"
	"io/fs"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/data/approval"
	"github.com/bds421/rho-kit/infra/sqldb/memdb"
)

func setup(t *testing.T) *Store {
	t.Helper()
	sub, err := fs.Sub(Migrations, "migrations")
	require.NoError(t, err)
	db := memdb.New(t, sub)
	return New(db)
}

func newReq(id string) approval.Request {
	return approval.Request{
		ID:        id,
		TenantID:  "tenant",
		Actor:     "agent",
		Action:    "user.delete",
		Resource:  "users/42",
		Payload:   []byte(`{"force":true}`),
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}
}

func TestCreate_ReadsBack(t *testing.T) {
	store := setup(t)
	r, err := store.Create(context.Background(), newReq("r1"))
	require.NoError(t, err)
	assert.Equal(t, approval.StatePending, r.State)

	got, err := store.Get(context.Background(), "r1")
	require.NoError(t, err)
	assert.Equal(t, "tenant", got.TenantID)
	assert.Equal(t, approval.StatePending, got.State)
}

func TestDecide_FullLifecycle(t *testing.T) {
	store := setup(t)
	_, err := store.Create(context.Background(), newReq("r1"))
	require.NoError(t, err)

	r, err := store.Decide(context.Background(), "r1", "approver-1", "ok", true)
	require.NoError(t, err)
	assert.Equal(t, approval.StateApproved, r.State)

	exec, err := store.MarkExecuted(context.Background(), "r1")
	require.NoError(t, err)
	assert.Equal(t, approval.StateExecuted, exec.State)

	_, err = store.Decide(context.Background(), "r1", "approver-2", "too late", false)
	assert.ErrorIs(t, err, approval.ErrInvalidTransition)
}

func TestDecide_Idempotent(t *testing.T) {
	store := setup(t)
	_, err := store.Create(context.Background(), newReq("r1"))
	require.NoError(t, err)

	_, err = store.Decide(context.Background(), "r1", "approver-1", "ok", true)
	require.NoError(t, err)

	r2, err := store.Decide(context.Background(), "r1", "approver-2", "still ok", true)
	require.NoError(t, err)
	assert.Equal(t, approval.StateApproved, r2.State)
	assert.Equal(t, "approver-2", r2.DecidedBy)
}

func TestDecide_FlipRefused(t *testing.T) {
	store := setup(t)
	_, err := store.Create(context.Background(), newReq("r1"))
	require.NoError(t, err)
	_, err = store.Decide(context.Background(), "r1", "approver-1", "ok", true)
	require.NoError(t, err)
	_, err = store.Decide(context.Background(), "r1", "approver-2", "changed mind", false)
	assert.ErrorIs(t, err, approval.ErrInvalidTransition)
}

func TestDecide_Expires(t *testing.T) {
	now := time.Now().UTC()
	clock := now
	sub, err := fs.Sub(Migrations, "migrations")
	require.NoError(t, err)
	db := memdb.New(t, sub)
	store := New(db, WithClock(func() time.Time { return clock }))

	r := newReq("r1")
	r.ExpiresAt = now.Add(time.Minute)
	_, err = store.Create(context.Background(), r)
	require.NoError(t, err)

	clock = now.Add(2 * time.Minute)

	_, err = store.Decide(context.Background(), "r1", "approver-1", "late", true)
	assert.ErrorIs(t, err, approval.ErrInvalidTransition)

	got, err := store.Get(context.Background(), "r1")
	require.NoError(t, err)
	assert.Equal(t, approval.StateExpired, got.State)
}

func TestList_Filters(t *testing.T) {
	store := setup(t)
	now := time.Now().UTC()
	requests := []approval.Request{
		{ID: "r1", TenantID: "t1", Actor: "a", Action: "user.delete", CreatedAt: now.Add(-3 * time.Hour), ExpiresAt: now.Add(time.Hour)},
		{ID: "r2", TenantID: "t1", Actor: "b", Action: "user.delete", CreatedAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(time.Hour)},
		{ID: "r3", TenantID: "t2", Actor: "a", Action: "file.delete", CreatedAt: now.Add(-1 * time.Hour), ExpiresAt: now.Add(time.Hour)},
	}
	for _, r := range requests {
		_, err := store.Create(context.Background(), r)
		require.NoError(t, err)
	}
	t1, err := store.List(context.Background(), approval.Query{TenantID: "t1"})
	require.NoError(t, err)
	assert.Len(t, t1, 2)

	allPending, err := store.List(context.Background(), approval.Query{State: approval.StatePending})
	require.NoError(t, err)
	assert.Len(t, allPending, 3)
}

func TestNew_PanicsOnNilDB(t *testing.T) {
	assert.Panics(t, func() { New(nil) })
}
