package memory

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/data/approval"
)

func newReq(id string) approval.Request {
	return approval.Request{
		ID:        id,
		TenantID:  "tenant",
		Actor:     "agent",
		Action:    "user.delete",
		Resource:  "users/42",
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}
}

func TestCreate_StartsPending(t *testing.T) {
	store := New()
	r, err := store.Create(context.Background(), newReq("r1"))
	require.NoError(t, err)
	assert.Equal(t, approval.StatePending, r.State)
}

func TestCreate_RejectsDuplicate(t *testing.T) {
	store := New()
	_, err := store.Create(context.Background(), newReq("r1"))
	require.NoError(t, err)
	_, err = store.Create(context.Background(), newReq("r1"))
	assert.ErrorIs(t, err, ErrDuplicateID)
}

func TestCreate_RejectsMissingFields(t *testing.T) {
	store := New()
	_, err := store.Create(context.Background(), approval.Request{ID: "r"})
	assert.ErrorIs(t, err, approval.ErrInvalidRequest)
}

func TestDecide_ApprovesPending(t *testing.T) {
	store := New()
	_, err := store.Create(context.Background(), newReq("r1"))
	require.NoError(t, err)

	r, err := store.Decide(context.Background(), "r1", "approver-1", "looks legit", true)
	require.NoError(t, err)
	assert.Equal(t, approval.StateApproved, r.State)
	assert.Equal(t, "approver-1", r.DecidedBy)
	assert.Equal(t, "looks legit", r.Reason)
	assert.False(t, r.DecidedAt.IsZero())
}

func TestDecide_RejectsPending(t *testing.T) {
	store := New()
	_, err := store.Create(context.Background(), newReq("r1"))
	require.NoError(t, err)

	r, err := store.Decide(context.Background(), "r1", "approver-1", "denied", false)
	require.NoError(t, err)
	assert.Equal(t, approval.StateRejected, r.State)
}

func TestDecide_IdempotentApproveApprove(t *testing.T) {
	store := New()
	_, err := store.Create(context.Background(), newReq("r1"))
	require.NoError(t, err)
	r1, err := store.Decide(context.Background(), "r1", "approver-1", "ok", true)
	require.NoError(t, err)

	r2, err := store.Decide(context.Background(), "r1", "approver-2", "still ok", true)
	require.NoError(t, err)
	assert.Equal(t, approval.StateApproved, r2.State)
	// Latest decider wins (the contract: the *decision* is idempotent,
	// not the metadata).
	assert.Equal(t, "approver-2", r2.DecidedBy)
	assert.Equal(t, r1.DecidedAt, r2.DecidedAt) // unchanged on idempotent re-decide
}

func TestDecide_RefusesFlip(t *testing.T) {
	store := New()
	_, err := store.Create(context.Background(), newReq("r1"))
	require.NoError(t, err)
	_, err = store.Decide(context.Background(), "r1", "approver-1", "ok", true)
	require.NoError(t, err)

	_, err = store.Decide(context.Background(), "r1", "approver-2", "changed mind", false)
	assert.ErrorIs(t, err, approval.ErrInvalidTransition)
}

func TestDecide_RefusesAfterExecution(t *testing.T) {
	store := New()
	_, err := store.Create(context.Background(), newReq("r1"))
	require.NoError(t, err)
	_, err = store.Decide(context.Background(), "r1", "approver-1", "ok", true)
	require.NoError(t, err)
	_, err = store.MarkExecuted(context.Background(), "r1")
	require.NoError(t, err)

	_, err = store.Decide(context.Background(), "r1", "approver-2", "too late", false)
	assert.ErrorIs(t, err, approval.ErrInvalidTransition)
}

func TestDecide_AutoExpiresPastDeadline(t *testing.T) {
	now := time.Now().UTC()
	clock := now
	store := New(WithClock(func() time.Time { return clock }))

	r := newReq("r1")
	r.ExpiresAt = now.Add(time.Minute)
	_, err := store.Create(context.Background(), r)
	require.NoError(t, err)

	// Jump past the deadline.
	clock = now.Add(2 * time.Minute)

	_, err = store.Decide(context.Background(), "r1", "approver-1", "late", true)
	assert.ErrorIs(t, err, approval.ErrInvalidTransition)

	got, err := store.Get(context.Background(), "r1")
	require.NoError(t, err)
	assert.Equal(t, approval.StateExpired, got.State)
}

func TestMarkExecuted_RequiresApproved(t *testing.T) {
	store := New()
	_, err := store.Create(context.Background(), newReq("r1"))
	require.NoError(t, err)

	_, err = store.MarkExecuted(context.Background(), "r1")
	assert.ErrorIs(t, err, approval.ErrInvalidTransition)
}

func TestMarkExecuted_Idempotent(t *testing.T) {
	store := New()
	_, err := store.Create(context.Background(), newReq("r1"))
	require.NoError(t, err)
	_, err = store.Decide(context.Background(), "r1", "approver-1", "ok", true)
	require.NoError(t, err)
	r1, err := store.MarkExecuted(context.Background(), "r1")
	require.NoError(t, err)

	r2, err := store.MarkExecuted(context.Background(), "r1")
	require.NoError(t, err)
	assert.Equal(t, r1, r2)
}

func TestList_Filters(t *testing.T) {
	store := New()
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

	pending, err := store.List(context.Background(), approval.Query{State: approval.StatePending})
	require.NoError(t, err)
	assert.Len(t, pending, 3)

	_, err = store.Decide(context.Background(), "r1", "approver-1", "ok", true)
	require.NoError(t, err)
	pending, err = store.List(context.Background(), approval.Query{State: approval.StatePending})
	require.NoError(t, err)
	assert.Len(t, pending, 2)

	approved, err := store.List(context.Background(), approval.Query{State: approval.StateApproved})
	require.NoError(t, err)
	assert.Len(t, approved, 1)
}

func TestGet_NotFound(t *testing.T) {
	store := New()
	_, err := store.Get(context.Background(), "missing")
	assert.ErrorIs(t, err, approval.ErrNotFound)
}
