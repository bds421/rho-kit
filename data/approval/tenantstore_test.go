package approval

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTenantStore_InvalidReceiverReturnsError(t *testing.T) {
	ctx := context.Background()

	for name, store := range map[string]*TenantStore{
		"nil":  nil,
		"zero": {},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := store.Create(ctx, Request{})
			assert.ErrorIs(t, err, ErrInvalidStore)

			_, err = store.Get(ctx, "r")
			assert.ErrorIs(t, err, ErrInvalidStore)

			_, _, err = store.List(ctx, Query{TenantID: "tenant"})
			assert.ErrorIs(t, err, ErrInvalidStore)

			_, err = store.Approve(ctx, "r", "approver", "ok")
			assert.ErrorIs(t, err, ErrInvalidStore)

			_, err = store.Reject(ctx, "r", "approver", "no")
			assert.ErrorIs(t, err, ErrInvalidStore)

			_, err = store.MarkExecuted(ctx, "r")
			assert.ErrorIs(t, err, ErrInvalidStore)
		})
	}
}

func TestNewTenantStore_PanicsWithoutTenantScopedMutator(t *testing.T) {
	// bareStore implements Store only — no ForTenant methods.
	assert.PanicsWithValue(t,
		"approval: NewTenantStore requires a Store implementing TenantScopedMutator (ApproveForTenant/RejectForTenant/MarkExecutedForTenant)",
		func() {
			NewTenantStore(&bareStore{}, "tenant-a")
		},
	)
}

func TestNewTenantStore_PanicsOnNilInnerOrEmptyTenant(t *testing.T) {
	assert.Panics(t, func() { NewTenantStore(nil, "tenant-a") })
	assert.Panics(t, func() { NewTenantStore(newTenantStoreTestStore(), "") })
}

func TestTenantStore_ScopesOperations(t *testing.T) {
	inner := newTenantStoreTestStore()
	store := NewTenantStore(inner, "tenant-a")
	ctx := context.Background()

	req, err := store.Create(ctx, Request{
		ID:        "r1",
		TenantID:  "attacker-supplied",
		Actor:     "agent",
		Action:    "user.delete",
		ExpiresAt: time.Now().Add(time.Hour),
	})
	assert.NoError(t, err)
	assert.Equal(t, "tenant-a", req.TenantID)

	_, err = store.Get(ctx, "tenant-b-request")
	assert.ErrorIs(t, err, ErrNotFound)
}

// TestTenantStore_CrossTenantMutationsReturnNotFound asserts the IDOR-fix
// paths (FR-054): Approve / Reject / MarkExecuted against another tenant's
// request must return ErrNotFound (not ErrTenantMismatch, which would leak
// existence) and must NOT mutate the sibling tenant's record.
func TestTenantStore_CrossTenantMutationsReturnNotFound(t *testing.T) {
	ctx := context.Background()

	mutations := map[string]func(*TenantStore) (Request, error){
		"Approve":      func(s *TenantStore) (Request, error) { return s.Approve(ctx, "tenant-b-request", "approver", "ok") },
		"Reject":       func(s *TenantStore) (Request, error) { return s.Reject(ctx, "tenant-b-request", "approver", "no") },
		"MarkExecuted": func(s *TenantStore) (Request, error) { return s.MarkExecuted(ctx, "tenant-b-request") },
	}

	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			inner := newTenantStoreTestStore()
			store := NewTenantStore(inner, "tenant-a")

			_, err := mutate(store)
			assert.ErrorIs(t, err, ErrNotFound)
			assert.NotErrorIs(t, err, ErrTenantMismatch)

			// The sibling tenant's record must be untouched.
			assert.Equal(t, StatePending, inner.requests["tenant-b-request"].State)
		})
	}
}

// TestTenantStore_ListRescopesQuery asserts List substitutes the wrapper's
// TenantID and clears AllTenants, so a caller cannot widen the scope to see
// other tenants' requests.
func TestTenantStore_ListRescopesQuery(t *testing.T) {
	ctx := context.Background()
	inner := newTenantStoreTestStore()
	inner.requests["tenant-a-request"] = Request{
		ID:        "tenant-a-request",
		TenantID:  "tenant-a",
		Actor:     "agent",
		Action:    "user.delete",
		State:     StatePending,
		ExpiresAt: time.Now().Add(time.Hour),
	}
	store := NewTenantStore(inner, "tenant-a")

	out, _, err := store.List(ctx, Query{TenantID: "tenant-b", AllTenants: true})
	assert.NoError(t, err)
	assert.Len(t, out, 1)
	assert.Equal(t, "tenant-a-request", out[0].ID)
	assert.Equal(t, "tenant-a", inner.lastQuery.TenantID)
	assert.False(t, inner.lastQuery.AllTenants)
}

// TestTenantStore_UsesAtomicForTenantMethods pins that mutations always go
// through ApproveForTenant / RejectForTenant / MarkExecutedForTenant (no
// check-then-act fallback; review-11).
func TestTenantStore_UsesAtomicForTenantMethods(t *testing.T) {
	ctx := context.Background()
	inner := newTenantStoreTestStore()
	inner.requests["r1"] = Request{
		ID:        "r1",
		TenantID:  "tenant-a",
		Actor:     "agent",
		Action:    "user.delete",
		State:     StatePending,
		ExpiresAt: time.Now().Add(time.Hour),
	}
	store := NewTenantStore(inner, "tenant-a")

	_, err := store.Approve(ctx, "r1", "approver", "ok")
	require.NoError(t, err)
	assert.True(t, inner.approveForTenantCalled, "must use ApproveForTenant")
	assert.False(t, inner.approveCalled, "must not fall back to id-only Approve")

	// Cross-tenant via atomic path returns not found without committing.
	inner.requests["other"] = Request{
		ID:       "other",
		TenantID: "tenant-b",
		State:    StatePending,
	}
	_, err = store.Approve(ctx, "other", "approver", "ok")
	assert.ErrorIs(t, err, ErrNotFound)
	assert.Equal(t, StatePending, inner.requests["other"].State)

	// Reject + MarkExecuted also use ForTenant methods.
	inner.requests["r2"] = Request{
		ID: "r2", TenantID: "tenant-a", State: StatePending, ExpiresAt: time.Now().Add(time.Hour),
	}
	_, err = store.Reject(ctx, "r2", "approver", "no")
	require.NoError(t, err)
	assert.True(t, inner.rejectForTenantCalled)

	inner.requests["r3"] = Request{
		ID: "r3", TenantID: "tenant-a", State: StateApproved, ExpiresAt: time.Now().Add(time.Hour),
	}
	_, err = store.MarkExecuted(ctx, "r3")
	require.NoError(t, err)
	assert.True(t, inner.markExecutedForTenantCalled)
}

// bareStore implements Store without TenantScopedMutator.
type bareStore struct{}

func (bareStore) Create(context.Context, Request) (Request, error) { return Request{}, nil }
func (bareStore) Get(context.Context, string) (Request, error)     { return Request{}, ErrNotFound }
func (bareStore) List(context.Context, Query) ([]Request, string, error) {
	return nil, "", nil
}
func (bareStore) Approve(context.Context, string, string, string) (Request, error) {
	return Request{}, ErrNotFound
}
func (bareStore) Reject(context.Context, string, string, string) (Request, error) {
	return Request{}, ErrNotFound
}
func (bareStore) MarkExecuted(context.Context, string) (Request, error) {
	return Request{}, ErrNotFound
}

// tenantStoreTestStore is a TenantScopedMutator used by TenantStore tests.
type tenantStoreTestStore struct {
	requests map[string]Request
	// lastQuery records the Query the wrapper forwarded to List.
	lastQuery Query

	approveCalled               bool
	approveForTenantCalled      bool
	rejectForTenantCalled       bool
	markExecutedForTenantCalled bool
}

func newTenantStoreTestStore() *tenantStoreTestStore {
	return &tenantStoreTestStore{
		requests: map[string]Request{
			"tenant-b-request": {
				ID:        "tenant-b-request",
				TenantID:  "tenant-b",
				Actor:     "agent",
				Action:    "user.delete",
				State:     StatePending,
				ExpiresAt: time.Now().Add(time.Hour),
			},
		},
	}
}

func (s *tenantStoreTestStore) Create(_ context.Context, r Request) (Request, error) {
	s.requests[r.ID] = r
	return r, nil
}

func (s *tenantStoreTestStore) Get(_ context.Context, id string) (Request, error) {
	r, ok := s.requests[id]
	if !ok {
		return Request{}, ErrNotFound
	}
	return r, nil
}

func (s *tenantStoreTestStore) List(_ context.Context, q Query) ([]Request, string, error) {
	s.lastQuery = q
	out := make([]Request, 0, len(s.requests))
	for _, r := range s.requests {
		if q.TenantID != "" && r.TenantID != q.TenantID {
			continue
		}
		out = append(out, r)
	}
	return out, "", nil
}

func (s *tenantStoreTestStore) Approve(_ context.Context, id, decidedBy, reason string) (Request, error) {
	s.approveCalled = true
	return s.decide(id, decidedBy, reason, StateApproved)
}

func (s *tenantStoreTestStore) Reject(_ context.Context, id, decidedBy, reason string) (Request, error) {
	return s.decide(id, decidedBy, reason, StateRejected)
}

func (s *tenantStoreTestStore) decide(id, decidedBy, reason string, target State) (Request, error) {
	r, ok := s.requests[id]
	if !ok {
		return Request{}, ErrNotFound
	}
	r.State = target
	r.DecidedBy = decidedBy
	r.Reason = reason
	s.requests[id] = r
	return r, nil
}

func (s *tenantStoreTestStore) MarkExecuted(_ context.Context, id string) (Request, error) {
	r, ok := s.requests[id]
	if !ok {
		return Request{}, ErrNotFound
	}
	r.State = StateExecuted
	s.requests[id] = r
	return r, nil
}

func (s *tenantStoreTestStore) ApproveForTenant(_ context.Context, tenantID, id, decidedBy, reason string) (Request, error) {
	s.approveForTenantCalled = true
	r, ok := s.requests[id]
	if !ok || r.TenantID != tenantID {
		return Request{}, ErrNotFound
	}
	r.State = StateApproved
	r.DecidedBy = decidedBy
	r.Reason = reason
	s.requests[id] = r
	return r, nil
}

func (s *tenantStoreTestStore) RejectForTenant(_ context.Context, tenantID, id, decidedBy, reason string) (Request, error) {
	s.rejectForTenantCalled = true
	r, ok := s.requests[id]
	if !ok || r.TenantID != tenantID {
		return Request{}, ErrNotFound
	}
	r.State = StateRejected
	r.DecidedBy = decidedBy
	r.Reason = reason
	s.requests[id] = r
	return r, nil
}

func (s *tenantStoreTestStore) MarkExecutedForTenant(_ context.Context, tenantID, id string) (Request, error) {
	s.markExecutedForTenantCalled = true
	r, ok := s.requests[id]
	if !ok || r.TenantID != tenantID {
		return Request{}, ErrNotFound
	}
	r.State = StateExecuted
	s.requests[id] = r
	return r, nil
}
