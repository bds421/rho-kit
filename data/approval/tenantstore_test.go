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

			_, err = store.List(ctx, Query{TenantID: "tenant"})
			assert.ErrorIs(t, err, ErrInvalidStore)

			_, err = store.Decide(ctx, "r", "approver", "ok", true)
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

type tenantStoreTestStore struct {
	requests map[string]Request
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

func (s *tenantStoreTestStore) List(_ context.Context, q Query) ([]Request, error) {
	out := make([]Request, 0, len(s.requests))
	for _, r := range s.requests {
		if q.TenantID != "" && r.TenantID != q.TenantID {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

func (s *tenantStoreTestStore) Decide(_ context.Context, id, decidedBy, reason string, approve bool) (Request, error) {
	r, ok := s.requests[id]
	if !ok {
		return Request{}, ErrNotFound
	}
	if approve {
		r.State = StateApproved
	} else {
		r.State = StateRejected
	}
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
