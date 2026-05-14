package memory

import (
	"context"
	"errors"
	"sort"
	"sync"

	"github.com/bds421/rho-kit/data/v2/actionlog"
)

// ErrDuplicateID is returned by [Store.AppendChained] when an entry id
// is already present. The Logger generates UUIDv7 ids so duplicates
// only occur if a caller pre-populates [actionlog.Entry.ID]; we surface
// rather than overwrite because a silent overwrite hides the bug.
var ErrDuplicateID = errors.New("actionlog/memory: duplicate entry id")

const defaultLimit = 100

// Store is an in-memory [actionlog.Store]. Safe for concurrent use.
type Store struct {
	mu      sync.RWMutex
	entries []actionlog.Entry
	byID    map[string]int
	// tenantMu serialises chain extension per tenant. Uses sync.Map +
	// LoadOrStore so a global lock isn't needed for every Append; entries
	// are removed in [Store.PruneTenants] to avoid unbounded growth in
	// long-running test suites with many ephemeral tenants.
	tenantMu sync.Map // map[string]*sync.Mutex

	// cursorSigner produces tamper-resistant List cursors. Required —
	// nil signers panic in [New] so misconfiguration is caught at
	// process startup rather than at the first paginated read.
	cursorSigner *actionlog.CursorSigner
}

// New creates an empty Store. The signer is required; List results
// embed signed keyset cursors that verify against this signer on the
// next page request, so a nil signer would let clients forge cursors
// and skip ahead through entries they were not yet authorised to see.
func New(signer *actionlog.CursorSigner) *Store {
	if signer == nil {
		panic("actionlog/memory: New requires a non-nil *actionlog.CursorSigner")
	}
	return &Store{
		byID:         make(map[string]int),
		cursorSigner: signer,
	}
}

func (s *Store) ready() error {
	if s == nil || s.byID == nil || s.cursorSigner == nil {
		return actionlog.ErrInvalidStore
	}
	return nil
}

func (s *Store) lockFor(tenantID string) *sync.Mutex {
	if mu, ok := s.tenantMu.Load(tenantID); ok {
		return mu.(*sync.Mutex)
	}
	mu := &sync.Mutex{}
	actual, _ := s.tenantMu.LoadOrStore(tenantID, mu)
	return actual.(*sync.Mutex)
}

// PruneTenants drops the per-tenant locks for tenants that have no
// entries left in the store. Call this between test runs (or any time
// you know which tenant IDs are no longer in use) to bound the
// per-tenant lock map's memory footprint. Without this, the locks
// accumulate one entry per ever-seen tenant — a slow leak in long-
// running test suites.
//
// Concurrency: callers must guarantee no AppendChained is in flight for
// the pruned tenants. The store does not synchronise that for them.
func (s *Store) PruneTenants() {
	if s.ready() != nil {
		return
	}
	s.mu.RLock()
	live := make(map[string]struct{}, 16)
	for _, e := range s.entries {
		live[e.TenantID] = struct{}{}
	}
	s.mu.RUnlock()

	s.tenantMu.Range(func(k, _ any) bool {
		if _, ok := live[k.(string)]; !ok {
			s.tenantMu.Delete(k)
		}
		return true
	})
}

// AppendChained holds the per-tenant lock, reads the previous entry,
// runs build, and persists the resulting entry under the same lock.
func (s *Store) AppendChained(ctx context.Context, tenantID string, build func(prev actionlog.Entry, prevSeq int64) (actionlog.Entry, error)) (actionlog.Entry, error) {
	if err := s.ready(); err != nil {
		return actionlog.Entry{}, err
	}
	if err := ctx.Err(); err != nil {
		return actionlog.Entry{}, err
	}
	if tenantID == "" {
		return actionlog.Entry{}, actionlog.ErrInvalidEntry
	}
	if build == nil {
		return actionlog.Entry{}, actionlog.ErrInvalidEntry
	}
	tmu := s.lockFor(tenantID)
	tmu.Lock()
	defer tmu.Unlock()

	if err := ctx.Err(); err != nil {
		return actionlog.Entry{}, err
	}
	prev, prevSeq := s.latestForTenantLocked(tenantID)
	entry, err := build(prev, prevSeq)
	if err != nil {
		return actionlog.Entry{}, err
	}
	if err := actionlog.ValidateStoredEntry(tenantID, entry); err != nil {
		return actionlog.Entry{}, err
	}
	entry = entry.Clone()

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.byID[entry.ID]; dup {
		return actionlog.Entry{}, ErrDuplicateID
	}
	s.byID[entry.ID] = len(s.entries)
	s.entries = append(s.entries, entry)
	return entry.Clone(), nil
}

// latestForTenantLocked returns the highest-Seq entry for tenantID
// (and its Seq), or zero values if none exist. Caller must hold the
// per-tenant lock to make the read-then-append atomic.
func (s *Store) latestForTenantLocked(tenantID string) (actionlog.Entry, int64) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var (
		best    actionlog.Entry
		bestSeq int64
	)
	for _, e := range s.entries {
		if e.TenantID != tenantID {
			continue
		}
		if e.Seq > bestSeq {
			bestSeq = e.Seq
			best = e
		}
	}
	return best.Clone(), bestSeq
}

// Get returns the entry by id, or [actionlog.ErrNotFound] if absent.
func (s *Store) Get(ctx context.Context, id string) (actionlog.Entry, error) {
	if err := s.ready(); err != nil {
		return actionlog.Entry{}, err
	}
	if err := ctx.Err(); err != nil {
		return actionlog.Entry{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	idx, ok := s.byID[id]
	if !ok {
		return actionlog.Entry{}, actionlog.ErrNotFound
	}
	return s.entries[idx].Clone(), nil
}

// List returns entries matching q ordered by OccurredAt descending,
// then ID descending. Honours [actionlog.Query.Cursor] for keyset
// pagination so the full list is reachable by following the returned
// cursor; an empty next-cursor means the last page. ctx.Err() is
// checked before scanning, and again every [ctxCheckBatch] scanned
// entries, so a cancelled request does not pay for the full table
// scan.
func (s *Store) List(ctx context.Context, q actionlog.Query) ([]actionlog.Entry, string, error) {
	if err := s.ready(); err != nil {
		return nil, "", err
	}
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if err := q.Validate(); err != nil {
		return nil, "", err
	}
	cursorTime, cursorID, err := s.cursorSigner.Decode(q.Cursor)
	if err != nil {
		return nil, "", err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	limit := q.Limit
	if limit <= 0 {
		limit = defaultLimit
	}

	matched := make([]actionlog.Entry, 0, limit+1)
	for i, e := range s.entries {
		if i%ctxCheckBatch == 0 {
			if err := ctx.Err(); err != nil {
				return nil, "", err
			}
		}
		if !match(e, q) {
			continue
		}
		matched = append(matched, e.Clone())
	}

	sort.Slice(matched, func(i, j int) bool {
		ti, tj := matched[i].OccurredAt, matched[j].OccurredAt
		if !ti.Equal(tj) {
			return ti.After(tj)
		}
		return matched[i].ID > matched[j].ID
	})

	if q.Cursor != "" {
		idx := 0
		for idx < len(matched) {
			e := matched[idx]
			if e.OccurredAt.Before(cursorTime) ||
				(e.OccurredAt.Equal(cursorTime) && e.ID < cursorID) {
				break
			}
			idx++
		}
		matched = matched[idx:]
	}

	var next string
	if len(matched) > limit {
		last := matched[limit-1]
		next = s.cursorSigner.Encode(last.OccurredAt, last.ID)
		matched = matched[:limit]
	}
	return matched, next, nil
}

// RangeByTenantSeq calls fn for every entry for tenantID in Seq ASC
// order. ctx.Err() is checked before scanning, every
// [ctxCheckBatch] entries during the scan, and before every fn
// invocation so a cancelled verification does not pay for the full
// per-tenant chain copy or iteration.
func (s *Store) RangeByTenantSeq(ctx context.Context, tenantID string, fn func(actionlog.Entry) error) error {
	if err := s.ready(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if tenantID == "" {
		return actionlog.ErrQueryTenantRequired
	}
	if fn == nil {
		return actionlog.ErrInvalidEntry
	}
	s.mu.RLock()
	out := make([]actionlog.Entry, 0)
	for i, e := range s.entries {
		if i%ctxCheckBatch == 0 {
			if err := ctx.Err(); err != nil {
				s.mu.RUnlock()
				return err
			}
		}
		if e.TenantID == tenantID {
			out = append(out, e.Clone())
		}
	}
	s.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	for _, e := range out {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := fn(e); err != nil {
			return err
		}
	}
	return nil
}

// ctxCheckBatch is the number of entries we scan between ctx.Err()
// checks. Tuned so the overhead is negligible compared to the work
// done per entry but a cancelled scan returns within a handful of
// microseconds even on multi-million-entry stores.
const ctxCheckBatch = 1024

// match reports whether e satisfies every non-zero filter on q.
func match(e actionlog.Entry, q actionlog.Query) bool {
	if q.TenantID != "" && e.TenantID != q.TenantID {
		return false
	}
	if q.Actor != "" && e.Actor != q.Actor {
		return false
	}
	if q.Action != "" && e.Action != q.Action {
		return false
	}
	if !q.Since.IsZero() && e.OccurredAt.Before(q.Since) {
		return false
	}
	if !q.Until.IsZero() && e.OccurredAt.After(q.Until) {
		return false
	}
	return true
}
