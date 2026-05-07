package memory

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/bds421/rho-kit/data/approval"
)

// ErrDuplicateID is returned when [Store.Create] is called with an id
// that already exists in the store.
var ErrDuplicateID = errors.New("approval/memory: duplicate request id")

const defaultLimit = 100

// Store is an in-memory [approval.Store]. Safe for concurrent use.
type Store struct {
	mu       sync.Mutex
	requests map[string]approval.Request
	clock    func() time.Time
}

// Option configures the Store.
type Option func(*Store)

// WithClock overrides the wall-clock used to detect expiry inside
// Decide. Tests use this to make the "approve an expired request"
// branch hermetic.
func WithClock(fn func() time.Time) Option {
	return func(s *Store) { s.clock = fn }
}

// New returns an empty Store.
func New(opts ...Option) *Store {
	s := &Store{
		requests: make(map[string]approval.Request),
		clock:    time.Now,
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Create persists a new request in StatePending. Rejects requests
// without a future ExpiresAt — see validateForCreate.
func (s *Store) Create(_ context.Context, r approval.Request) (approval.Request, error) {
	if err := validateForCreate(r, s.clock()); err != nil {
		return approval.Request{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, dup := s.requests[r.ID]; dup {
		return approval.Request{}, fmt.Errorf("%w: %s", ErrDuplicateID, r.ID)
	}

	r.State = approval.StatePending
	if r.CreatedAt.IsZero() {
		r.CreatedAt = s.clock()
	}
	r.CreatedAt = r.CreatedAt.UTC()
	r.ExpiresAt = r.ExpiresAt.UTC()
	s.requests[r.ID] = r
	return r, nil
}

// Get returns the request by id.
func (s *Store) Get(_ context.Context, id string) (approval.Request, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.requests[id]
	if !ok {
		return approval.Request{}, approval.ErrNotFound
	}
	return r, nil
}

// List returns matching requests newest-first by CreatedAt.
func (s *Store) List(_ context.Context, q approval.Query) ([]approval.Request, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	limit := q.Limit
	if limit <= 0 {
		limit = defaultLimit
	}

	matched := make([]approval.Request, 0, len(s.requests))
	for _, r := range s.requests {
		if !match(r, q) {
			continue
		}
		matched = append(matched, r)
	}
	sort.Slice(matched, func(i, j int) bool {
		ti, tj := matched[i].CreatedAt, matched[j].CreatedAt
		if !ti.Equal(tj) {
			return ti.After(tj)
		}
		return matched[i].ID > matched[j].ID
	})
	if len(matched) > limit {
		matched = matched[:limit]
	}
	return matched, nil
}

// Decide records an approver's decision. See [approval.Store] for the
// full contract.
func (s *Store) Decide(_ context.Context, id, decidedBy, reason string, approve bool) (approval.Request, error) {
	if decidedBy == "" {
		return approval.Request{}, approval.ErrInvalidApprover
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	r, ok := s.requests[id]
	if !ok {
		return approval.Request{}, approval.ErrNotFound
	}

	// Auto-expire on read-then-decide. The expiry check happens before
	// the idempotency / terminal-state checks because an expired
	// pending request should never become approved, even if the
	// approver thinks they're idempotently re-approving.
	if r.State == approval.StatePending && !r.ExpiresAt.IsZero() && s.clock().After(r.ExpiresAt) {
		r.State = approval.StateExpired
		r.DecidedAt = s.clock().UTC()
		s.requests[id] = r
		return approval.Request{}, fmt.Errorf("%w: request expired at %s", approval.ErrInvalidTransition, r.ExpiresAt.UTC().Format(time.RFC3339))
	}

	target := approval.StateApproved
	if !approve {
		target = approval.StateRejected
	}

	// Idempotency: same decision is a no-op (but DecidedBy / Reason
	// take the latest values).
	if r.State == target {
		r.DecidedBy = decidedBy
		r.Reason = reason
		s.requests[id] = r
		return r, nil
	}

	// Terminal state: refuse to move out.
	if r.State.IsTerminal() {
		return approval.Request{}, fmt.Errorf("%w: cannot transition out of %s", approval.ErrInvalidTransition, r.State)
	}

	// approved -> rejected (or vice-versa) is also rejected: a flipped
	// decision is a re-decision and should go through a new request.
	if r.State == approval.StateApproved || r.State == approval.StateRejected {
		return approval.Request{}, fmt.Errorf("%w: cannot flip decision once recorded", approval.ErrInvalidTransition)
	}

	r.State = target
	r.DecidedBy = decidedBy
	r.Reason = reason
	r.DecidedAt = s.clock().UTC()
	s.requests[id] = r
	return r, nil
}

// MarkExecuted moves an approved request to executed.
func (s *Store) MarkExecuted(_ context.Context, id string) (approval.Request, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	r, ok := s.requests[id]
	if !ok {
		return approval.Request{}, approval.ErrNotFound
	}

	if r.State == approval.StateExecuted {
		return r, nil
	}
	if r.State != approval.StateApproved {
		return approval.Request{}, fmt.Errorf("%w: MarkExecuted requires source state %s, got %s", approval.ErrInvalidTransition, approval.StateApproved, r.State)
	}

	r.State = approval.StateExecuted
	s.requests[id] = r
	return r, nil
}

// validateForCreate is a thin wrapper that maps to the package-level
// validation contract. Requires non-zero, future ExpiresAt — direct
// store callers must opt into a deadline because permanent pending
// approvals defeat the kit's bounded-decision-window invariant.
func validateForCreate(r approval.Request, now time.Time) error {
	if r.ID == "" || r.TenantID == "" || r.Actor == "" || r.Action == "" {
		return approval.ErrInvalidRequest
	}
	if r.State != "" && r.State != approval.StatePending {
		return approval.ErrInvalidRequest
	}
	if r.ExpiresAt.IsZero() {
		return approval.ErrInvalidRequest
	}
	if !r.ExpiresAt.After(now) {
		return approval.ErrInvalidRequest
	}
	return nil
}

func match(r approval.Request, q approval.Query) bool {
	if q.TenantID != "" && r.TenantID != q.TenantID {
		return false
	}
	if q.Actor != "" && r.Actor != q.Actor {
		return false
	}
	if q.Action != "" && r.Action != q.Action {
		return false
	}
	if q.State != "" && r.State != q.State {
		return false
	}
	if !q.Since.IsZero() && r.CreatedAt.Before(q.Since) {
		return false
	}
	if !q.Until.IsZero() && r.CreatedAt.After(q.Until) {
		return false
	}
	return true
}
