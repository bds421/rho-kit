package memory

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

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
	store := New(testCursorSigner(t))
	r, err := store.Create(context.Background(), newReq("r1"))
	require.NoError(t, err)
	assert.Equal(t, approval.StatePending, r.State)
}

func TestStore_CopiesPayloadOnPublicBoundaries(t *testing.T) {
	store := New(testCursorSigner(t))
	r := newReq("copy-payload")
	r.Payload = []byte("original")

	created, err := store.Create(context.Background(), r)
	require.NoError(t, err)

	r.Payload[0] = 'i'
	created.Payload[0] = 'c'
	got, err := store.Get(context.Background(), "copy-payload")
	require.NoError(t, err)
	assert.Equal(t, "original", string(got.Payload))

	got.Payload[0] = 'g'
	gotAgain, err := store.Get(context.Background(), "copy-payload")
	require.NoError(t, err)
	assert.Equal(t, "original", string(gotAgain.Payload))

	listed, _, err := store.List(context.Background(), approval.Query{TenantID: "tenant"})
	require.NoError(t, err)
	require.Len(t, listed, 1)
	listed[0].Payload[0] = 'l'
	gotAgain, err = store.Get(context.Background(), "copy-payload")
	require.NoError(t, err)
	assert.Equal(t, "original", string(gotAgain.Payload))

	decided, err := store.Approve(context.Background(), "copy-payload", "approver", "ok")
	require.NoError(t, err)
	decided.Payload[0] = 'd'
	gotAgain, err = store.Get(context.Background(), "copy-payload")
	require.NoError(t, err)
	assert.Equal(t, "original", string(gotAgain.Payload))

	executed, err := store.MarkExecuted(context.Background(), "copy-payload")
	require.NoError(t, err)
	executed.Payload[0] = 'e'
	gotAgain, err = store.Get(context.Background(), "copy-payload")
	require.NoError(t, err)
	assert.Equal(t, "original", string(gotAgain.Payload))
}

func TestCreate_RejectsDuplicate(t *testing.T) {
	store := New(testCursorSigner(t))
	_, err := store.Create(context.Background(), newReq("secret-token"))
	require.NoError(t, err)
	_, err = store.Create(context.Background(), newReq("secret-token"))
	assert.ErrorIs(t, err, ErrDuplicateID)
	assert.NotContains(t, strings.ToLower(err.Error()), "secret-token")
}

func TestCreate_RejectsMissingFields(t *testing.T) {
	store := New(testCursorSigner(t))
	_, err := store.Create(context.Background(), approval.Request{ID: "r"})
	assert.ErrorIs(t, err, approval.ErrInvalidRequest)
}

func TestCreate_RejectsZeroExpiresAt(t *testing.T) {
	store := New(testCursorSigner(t))
	r := newReq("r-zero")
	r.ExpiresAt = time.Time{}
	_, err := store.Create(context.Background(), r)
	assert.ErrorIs(t, err, approval.ErrInvalidRequest)
}

func TestCreate_RejectsPastExpiresAt(t *testing.T) {
	store := New(testCursorSigner(t))
	r := newReq("r-past")
	r.ExpiresAt = time.Now().Add(-time.Hour)
	_, err := store.Create(context.Background(), r)
	assert.ErrorIs(t, err, approval.ErrInvalidRequest)
}

func TestCreate_UsesSharedValidation(t *testing.T) {
	store := New(testCursorSigner(t))

	r := newReq(strings.Repeat("a", approval.MaxIDLen+1))
	_, err := store.Create(context.Background(), r)
	assert.ErrorIs(t, err, approval.ErrInvalidRequest)

	r = newReq("actor-too-long")
	r.Actor = strings.Repeat("a", approval.MaxActorLen+1)
	_, err = store.Create(context.Background(), r)
	assert.ErrorIs(t, err, approval.ErrInvalidRequest)

	r = newReq("payload-too-large")
	r.Payload = make([]byte, approval.MaxPayloadSize+1)
	_, err = store.Create(context.Background(), r)
	assert.ErrorIs(t, err, approval.ErrInvalidRequest)
}

func TestStore_InvalidReceiverReturnsError(t *testing.T) {
	ctx := context.Background()

	for name, store := range map[string]*Store{
		"nil":  nil,
		"zero": {},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := store.Create(ctx, newReq("r"))
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

func TestDecide_RejectsEmptyDecidedBy(t *testing.T) {
	store := New(testCursorSigner(t))
	_, err := store.Create(context.Background(), newReq("r1"))
	require.NoError(t, err)
	_, err = store.Approve(context.Background(), "r1", "", "ok")
	assert.ErrorIs(t, err, approval.ErrInvalidApprover)
}

func TestDecide_RejectsInvalidDecidedBy(t *testing.T) {
	store := New(testCursorSigner(t))
	_, err := store.Create(context.Background(), newReq("r1"))
	require.NoError(t, err)
	_, err = store.Approve(context.Background(), "r1", strings.Repeat("a", approval.MaxActorLen+1), "ok")
	assert.ErrorIs(t, err, approval.ErrInvalidApprover)
}

func TestDecide_RejectsInvalidReason(t *testing.T) {
	store := New(testCursorSigner(t))
	_, err := store.Create(context.Background(), newReq("r1"))
	require.NoError(t, err)
	_, err = store.Approve(context.Background(), "r1", "approver-1", strings.Repeat("r", approval.MaxReasonLen+1))
	assert.ErrorIs(t, err, approval.ErrInvalidReason)
}

func TestDecide_ApprovesPending(t *testing.T) {
	store := New(testCursorSigner(t))
	_, err := store.Create(context.Background(), newReq("r1"))
	require.NoError(t, err)

	r, err := store.Approve(context.Background(), "r1", "approver-1", "looks legit")
	require.NoError(t, err)
	assert.Equal(t, approval.StateApproved, r.State)
	assert.Equal(t, "approver-1", r.DecidedBy)
	assert.Equal(t, "looks legit", r.Reason)
	assert.False(t, r.DecidedAt.IsZero())
}

func TestDecide_RejectsPending(t *testing.T) {
	store := New(testCursorSigner(t))
	_, err := store.Create(context.Background(), newReq("r1"))
	require.NoError(t, err)

	r, err := store.Reject(context.Background(), "r1", "approver-1", "denied")
	require.NoError(t, err)
	assert.Equal(t, approval.StateRejected, r.State)
}

func TestDecide_IdempotentApproveApprove(t *testing.T) {
	store := New(testCursorSigner(t))
	_, err := store.Create(context.Background(), newReq("r1"))
	require.NoError(t, err)
	r1, err := store.Approve(context.Background(), "r1", "approver-1", "ok")
	require.NoError(t, err)

	r2, err := store.Approve(context.Background(), "r1", "approver-2", "still ok")
	require.NoError(t, err)
	assert.Equal(t, approval.StateApproved, r2.State)
	// The whole decision record is idempotent; a replay must not rewrite
	// the original audit metadata.
	assert.Equal(t, "approver-1", r2.DecidedBy)
	assert.Equal(t, "ok", r2.Reason)
	assert.Equal(t, r1.DecidedAt, r2.DecidedAt) // unchanged on idempotent re-decide
}

func TestDecide_RefusesFlip(t *testing.T) {
	store := New(testCursorSigner(t))
	_, err := store.Create(context.Background(), newReq("r1"))
	require.NoError(t, err)
	_, err = store.Approve(context.Background(), "r1", "approver-1", "ok")
	require.NoError(t, err)

	_, err = store.Reject(context.Background(), "r1", "approver-2", "changed mind")
	assert.ErrorIs(t, err, approval.ErrInvalidTransition)
}

func TestDecide_RefusesAfterExecution(t *testing.T) {
	store := New(testCursorSigner(t))
	_, err := store.Create(context.Background(), newReq("r1"))
	require.NoError(t, err)
	_, err = store.Approve(context.Background(), "r1", "approver-1", "ok")
	require.NoError(t, err)
	_, err = store.MarkExecuted(context.Background(), "r1")
	require.NoError(t, err)

	_, err = store.Reject(context.Background(), "r1", "approver-2", "too late")
	assert.ErrorIs(t, err, approval.ErrInvalidTransition)
}

func TestDecide_AutoExpiresPastDeadline(t *testing.T) {
	now := time.Now().UTC()
	clock := now
	store := New(testCursorSigner(t), WithClock(func() time.Time { return clock }))

	r := newReq("r1")
	r.ExpiresAt = now.Add(time.Minute)
	_, err := store.Create(context.Background(), r)
	require.NoError(t, err)

	// Jump past the deadline.
	clock = now.Add(2 * time.Minute)

	_, err = store.Approve(context.Background(), "r1", "approver-1", "late")
	assert.ErrorIs(t, err, approval.ErrInvalidTransition)

	got, err := store.Get(context.Background(), "r1")
	require.NoError(t, err)
	assert.Equal(t, approval.StateExpired, got.State)
}

func TestMarkExecuted_RequiresApproved(t *testing.T) {
	store := New(testCursorSigner(t))
	_, err := store.Create(context.Background(), newReq("r1"))
	require.NoError(t, err)

	_, err = store.MarkExecuted(context.Background(), "r1")
	assert.ErrorIs(t, err, approval.ErrInvalidTransition)
}

func TestMarkExecuted_Idempotent(t *testing.T) {
	store := New(testCursorSigner(t))
	_, err := store.Create(context.Background(), newReq("r1"))
	require.NoError(t, err)
	_, err = store.Approve(context.Background(), "r1", "approver-1", "ok")
	require.NoError(t, err)
	r1, err := store.MarkExecuted(context.Background(), "r1")
	require.NoError(t, err)

	r2, err := store.MarkExecuted(context.Background(), "r1")
	require.NoError(t, err)
	assert.Equal(t, r1, r2)
}

func TestList_Filters(t *testing.T) {
	store := New(testCursorSigner(t))
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

	t1, _, err := store.List(context.Background(), approval.Query{TenantID: "t1"})
	require.NoError(t, err)
	assert.Len(t, t1, 2)

	// Cross-tenant listings (no TenantID) need AllTenants=true after FR-053.
	pending, _, err := store.List(context.Background(), approval.Query{AllTenants: true, State: approval.StatePending})
	require.NoError(t, err)
	assert.Len(t, pending, 3)

	_, err = store.Approve(context.Background(), "r1", "approver-1", "ok")
	require.NoError(t, err)
	pending, _, err = store.List(context.Background(), approval.Query{AllTenants: true, State: approval.StatePending})
	require.NoError(t, err)
	assert.Len(t, pending, 2)

	approved, _, err := store.List(context.Background(), approval.Query{AllTenants: true, State: approval.StateApproved})
	require.NoError(t, err)
	assert.Len(t, approved, 1)
}

func TestList_RejectsEmptyTenantWithoutAllTenants(t *testing.T) {
	// FR-053 [HIGH]: a handler that forgets to set TenantID must NOT
	// silently leak across tenants. The store rejects ambiguous queries.
	store := New(testCursorSigner(t))
	_, _, err := store.List(context.Background(), approval.Query{State: approval.StatePending})
	require.ErrorIs(t, err, approval.ErrQueryTenantRequired)
}

func TestList_RejectsTenantAndAllTenantsConflict(t *testing.T) {
	store := New(testCursorSigner(t))
	_, _, err := store.List(context.Background(), approval.Query{TenantID: "t1", AllTenants: true})
	require.ErrorIs(t, err, approval.ErrQueryScopeConflict)
}

// TestList_CursorPaginatesAllRows locks in the invariant that drove
// the cursor work: every created request is reachable by following
// the next cursor. Before this change, Store.List capped output at
// Limit silently, so a tenant with >Limit requests silently lost the
// tail.
func TestList_CursorPaginatesAllRows(t *testing.T) {
	store := New(testCursorSigner(t))
	now := time.Now().UTC()
	const total = 25
	for i := 0; i < total; i++ {
		_, err := store.Create(context.Background(), approval.Request{
			ID:        fmt.Sprintf("r%02d", i),
			TenantID:  "t",
			Actor:     "a",
			Action:    "user.delete",
			CreatedAt: now.Add(time.Duration(i) * time.Second),
			ExpiresAt: now.Add(time.Hour),
		})
		require.NoError(t, err)
	}

	seen := make(map[string]struct{}, total)
	cursor := ""
	pages := 0
	for {
		reqs, next, err := store.List(context.Background(), approval.Query{
			TenantID: "t", Limit: 7, Cursor: cursor,
		})
		require.NoError(t, err)
		for _, r := range reqs {
			if _, dup := seen[r.ID]; dup {
				t.Fatalf("cursor produced a duplicate id %q", r.ID)
			}
			seen[r.ID] = struct{}{}
		}
		pages++
		if next == "" {
			break
		}
		cursor = next
		require.LessOrEqual(t, pages, 10, "pagination did not converge")
	}
	assert.Len(t, seen, total)
}

func TestList_RejectsMalformedCursor(t *testing.T) {
	store := New(testCursorSigner(t))
	_, _, err := store.List(context.Background(), approval.Query{
		TenantID: "t", Cursor: "not-a-valid-cursor!!!",
	})
	assert.ErrorIs(t, err, approval.ErrInvalidCursor)
}

func TestQuery_Validate(t *testing.T) {
	assert.ErrorIs(t, (approval.Query{}).Validate(), approval.ErrQueryTenantRequired)
	assert.ErrorIs(t, (approval.Query{State: approval.StatePending}).Validate(), approval.ErrQueryTenantRequired)
	assert.NoError(t, (approval.Query{TenantID: "t1"}).Validate())
	assert.NoError(t, (approval.Query{AllTenants: true}).Validate())
	assert.ErrorIs(t, (approval.Query{TenantID: "t1", AllTenants: true}).Validate(), approval.ErrQueryScopeConflict)
}

func TestGet_NotFound(t *testing.T) {
	store := New(testCursorSigner(t))
	_, err := store.Get(context.Background(), "missing")
	assert.ErrorIs(t, err, approval.ErrNotFound)
}

func TestWithClock_PanicsOnNil(t *testing.T) {
	assert.Panics(t, func() { WithClock(nil) })
}

func TestNew_PanicsOnNilOption(t *testing.T) {
	// A nil Option (not a nil signer) must trip the per-option guard in
	// New. Pass a valid signer so the only thing that can panic is the
	// nil entry in opts, exercising New's "option must not be nil"
	// branch rather than the nil-signer branch.
	assert.Panics(t, func() { New(testCursorSigner(t), nil) })
}

// TestNew_PanicsOnNilCursorSigner pins the cursor-signer-required
// contract: a nil signer would let clients forge cursors and skip
// pending-approval pages.
func TestNew_PanicsOnNilCursorSigner(t *testing.T) {
	assert.Panics(t, func() { New(nil) })
}

// TestList_RejectsForgedCursor pins M-006: a client cannot construct
// a cursor that this store will accept — every malformed or
// foreign-signed input must surface as ErrInvalidCursor so HTTP
// handlers can map cleanly to 400 Bad Request.
func TestList_RejectsForgedCursor(t *testing.T) {
	store := New(testCursorSigner(t))

	// Seed a request so List has something to potentially paginate.
	_, err := store.Create(context.Background(), newReq("r-seed"))
	require.NoError(t, err)

	otherSigner, err := approval.NewCursorSigner([]byte("attacker-cursor-key-32-bytes-pad"))
	require.NoError(t, err)
	forged := otherSigner.Encode(time.Now().UTC(), "fake-id")

	_, _, err = store.List(context.Background(), approval.Query{
		TenantID: "tenant",
		Cursor:   forged,
	})
	require.ErrorIs(t, err, approval.ErrInvalidCursor)

	_, _, err = store.List(context.Background(), approval.Query{
		TenantID: "tenant",
		Cursor:   "aGVsbG8td29ybGQ", // base64 without signature separator
	})
	require.ErrorIs(t, err, approval.ErrInvalidCursor)
}

func TestDecide_ExpiresAtTheInstant(t *testing.T) {
	now := time.Now().UTC()
	clock := now
	store := New(testCursorSigner(t), WithClock(func() time.Time { return clock }))

	r := newReq("r-instant")
	r.ExpiresAt = now.Add(time.Minute)
	_, err := store.Create(context.Background(), r)
	require.NoError(t, err)

	clock = r.ExpiresAt

	_, err = store.Approve(context.Background(), "r-instant", "approver-1", "right at expiry")
	assert.ErrorIs(t, err, approval.ErrInvalidTransition)

	got, err := store.Get(context.Background(), "r-instant")
	require.NoError(t, err)
	assert.Equal(t, approval.StateExpired, got.State)
}
