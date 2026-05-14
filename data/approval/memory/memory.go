package memory

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/bds421/rho-kit/core/v2/clock"
	"github.com/bds421/rho-kit/data/v2/approval"
)

// ErrDuplicateID is returned when [Store.Create] is called with an id
// that already exists in the store.
var ErrDuplicateID = errors.New("approval/memory: duplicate request id")

const defaultLimit = 100

// Store is an in-memory [approval.Store]. Safe for concurrent use.
type Store struct {
	mu       sync.Mutex
	requests map[string]approval.Request
	clock    clock.Func

	// cursorSigner produces tamper-resistant List cursors. Required —
	// nil signers panic in [New] so misconfiguration is caught at
	// startup rather than at the first paginated read.
	cursorSigner *approval.CursorSigner
}

// Option configures the Store.
type Option func(*Store)

// WithClock overrides the wall-clock used to detect expiry inside
// Decide. Tests use this to make the "approve an expired request"
// branch hermetic. Panics on a nil fn.
func WithClock(fn clock.Func) Option {
	if fn == nil {
		panic("approval/memory: WithClock requires a non-nil function")
	}
	return func(s *Store) { s.clock = fn }
}

// New returns an empty Store. The signer is required; List results
// embed signed keyset cursors that verify against this signer on the
// next page request, so a nil signer would let clients forge cursors
// and skip ahead through pending-approval pages.
func New(signer *approval.CursorSigner, opts ...Option) *Store {
	if signer == nil {
		panic("approval/memory: New requires a non-nil *approval.CursorSigner")
	}
	s := &Store{
		requests:     make(map[string]approval.Request),
		clock:        time.Now,
		cursorSigner: signer,
	}
	for _, o := range opts {
		if o == nil {
			panic("approval/memory: option must not be nil")
		}
		o(s)
	}
	return s
}

// Create persists a new request in StatePending. Rejects requests
// without a future ExpiresAt — see [approval.ValidateForCreate].
func (s *Store) Create(ctx context.Context, r approval.Request) (approval.Request, error) {
	if err := ctx.Err(); err != nil {
		return approval.Request{}, err
	}
	if err := s.ready(); err != nil {
		return approval.Request{}, err
	}
	if err := approval.ValidateForCreate(r, s.clock()); err != nil {
		return approval.Request{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, dup := s.requests[r.ID]; dup {
		return approval.Request{}, ErrDuplicateID
	}

	r.State = approval.StatePending
	if r.CreatedAt.IsZero() {
		r.CreatedAt = s.clock()
	}
	r.CreatedAt = r.CreatedAt.UTC()
	r.ExpiresAt = r.ExpiresAt.UTC()
	r = cloneRequest(r)
	s.requests[r.ID] = r
	return cloneRequest(r), nil
}

// Get returns the request by id.
func (s *Store) Get(ctx context.Context, id string) (approval.Request, error) {
	if err := ctx.Err(); err != nil {
		return approval.Request{}, err
	}
	if err := s.ready(); err != nil {
		return approval.Request{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.requests[id]
	if !ok {
		return approval.Request{}, approval.ErrNotFound
	}
	return cloneRequest(r), nil
}

// List returns matching requests newest-first by CreatedAt. Returns
// [approval.ErrQueryTenantRequired] when the caller did not set
// [approval.Query.TenantID] or opt into AllTenants — see audit
// FR-053 for why cross-tenant listings must be explicit. Honours
// [approval.Query.Cursor] for keyset pagination so the full list is
// reachable by following the returned cursor; an empty next-cursor
// means the last page.
func (s *Store) List(ctx context.Context, q approval.Query) ([]approval.Request, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if err := s.ready(); err != nil {
		return nil, "", err
	}
	if err := q.Validate(); err != nil {
		return nil, "", err
	}
	cursorTime, cursorID, err := s.cursorSigner.Decode(q.Cursor)
	if err != nil {
		return nil, "", err
	}
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
		matched = append(matched, cloneRequest(r))
	}
	sort.Slice(matched, func(i, j int) bool {
		ti, tj := matched[i].CreatedAt, matched[j].CreatedAt
		if !ti.Equal(tj) {
			return ti.After(tj)
		}
		return matched[i].ID > matched[j].ID
	})
	if q.Cursor != "" {
		idx := 0
		for idx < len(matched) {
			r := matched[idx]
			if r.CreatedAt.Before(cursorTime) ||
				(r.CreatedAt.Equal(cursorTime) && r.ID < cursorID) {
				break
			}
			idx++
		}
		matched = matched[idx:]
	}
	var next string
	if len(matched) > limit {
		last := matched[limit-1]
		next = s.cursorSigner.Encode(last.CreatedAt, last.ID)
		matched = matched[:limit]
	}
	return matched, next, nil
}

// Decide records an approver's decision. See [approval.Store] for the
// full contract.
// Approve implements [approval.Store.Approve].
func (s *Store) Approve(ctx context.Context, id, decidedBy, reason string) (approval.Request, error) {
	return s.decide(ctx, id, decidedBy, reason, true)
}

// Reject implements [approval.Store.Reject].
func (s *Store) Reject(ctx context.Context, id, decidedBy, reason string) (approval.Request, error) {
	return s.decide(ctx, id, decidedBy, reason, false)
}

func (s *Store) decide(ctx context.Context, id, decidedBy, reason string, approve bool) (approval.Request, error) {
	if err := ctx.Err(); err != nil {
		return approval.Request{}, err
	}
	if err := s.ready(); err != nil {
		return approval.Request{}, err
	}
	if err := approval.ValidateDecision(decidedBy); err != nil {
		return approval.Request{}, err
	}
	if err := approval.ValidateReason(reason); err != nil {
		return approval.Request{}, err
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
	// approver thinks they're idempotently re-approving. The boundary
	// is exclusive: a request whose ExpiresAt equals now is already
	// expired (i.e. expire when now >= ExpiresAt).
	now := s.clock()
	if r.State == approval.StatePending && !r.ExpiresAt.IsZero() && !now.Before(r.ExpiresAt) {
		r.State = approval.StateExpired
		r.DecidedAt = now.UTC()
		s.requests[id] = r
		return approval.Request{}, fmt.Errorf("%w: request expired", approval.ErrInvalidTransition)
	}

	target := approval.StateApproved
	if !approve {
		target = approval.StateRejected
	}

	// Idempotency: same decision is a pure no-op. Preserve the original
	// decider metadata so a replay cannot rewrite the audit trail.
	if r.State == target {
		return cloneRequest(r), nil
	}

	// Terminal state: refuse to move out.
	if r.State.IsTerminal() {
		return approval.Request{}, fmt.Errorf("%w: cannot transition out of terminal state", approval.ErrInvalidTransition)
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
	return cloneRequest(r), nil
}

// MarkExecuted moves an approved request to executed.
func (s *Store) MarkExecuted(ctx context.Context, id string) (approval.Request, error) {
	if err := ctx.Err(); err != nil {
		return approval.Request{}, err
	}
	if err := s.ready(); err != nil {
		return approval.Request{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	r, ok := s.requests[id]
	if !ok {
		return approval.Request{}, approval.ErrNotFound
	}

	if r.State == approval.StateExecuted {
		return cloneRequest(r), nil
	}
	if r.State != approval.StateApproved {
		return approval.Request{}, fmt.Errorf("%w: request is not approved", approval.ErrInvalidTransition)
	}

	r.State = approval.StateExecuted
	s.requests[id] = r
	return cloneRequest(r), nil
}

func cloneRequest(r approval.Request) approval.Request {
	if r.Payload != nil {
		r.Payload = append(r.Payload[:0:0], r.Payload...)
	}
	return r
}

func (s *Store) ready() error {
	if s == nil || s.requests == nil || s.clock == nil || s.cursorSigner == nil {
		return approval.ErrInvalidStore
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
