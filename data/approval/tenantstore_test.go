package approval

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
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

// TestTenantStore_PostWriteMismatchTripwire asserts the best-effort TOCTOU
// tripwire (L057): if the row's TenantID is reassigned between the ownership
// read and the inner write, the wrapper detects the mismatch on the write
// result and returns ErrTenantMismatch.
func TestTenantStore_PostWriteMismatchTripwire(t *testing.T) {
	ctx := context.Background()

	mutations := map[string]func(*TenantStore) (Request, error){
		"Approve":      func(s *TenantStore) (Request, error) { return s.Approve(ctx, "tenant-a-request", "approver", "ok") },
		"Reject":       func(s *TenantStore) (Request, error) { return s.Reject(ctx, "tenant-a-request", "approver", "no") },
		"MarkExecuted": func(s *TenantStore) (Request, error) { return s.MarkExecuted(ctx, "tenant-a-request") },
	}

	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			inner := newTenantStoreTestStore()
			inner.requests["tenant-a-request"] = Request{
				ID:        "tenant-a-request",
				TenantID:  "tenant-a",
				Actor:     "agent",
				Action:    "user.delete",
				State:     StatePending,
				ExpiresAt: time.Now().Add(time.Hour),
			}
			// Reassign the row to another tenant after the ownership read
			// (which the wrapper performs via Get) but before/at the write.
			inner.reassignOnWrite = "tenant-b"
			store := NewTenantStore(inner, "tenant-a")

			_, err := mutate(store)
			assert.ErrorIs(t, err, ErrTenantMismatch)
		})
	}
}

type tenantStoreTestStore struct {
	requests map[string]Request
	// lastQuery records the Query the wrapper forwarded to List.
	lastQuery Query
	// reassignOnWrite, when non-empty, rewrites the TenantID of the row
	// being mutated to simulate a concurrent in-place tenant reassignment
	// happening between the wrapper's ownership read and its write.
	reassignOnWrite string
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
	if s.reassignOnWrite != "" {
		r.TenantID = s.reassignOnWrite
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
	if s.reassignOnWrite != "" {
		r.TenantID = s.reassignOnWrite
	}
	r.State = StateExecuted
	s.requests[id] = r
	return r, nil
}
