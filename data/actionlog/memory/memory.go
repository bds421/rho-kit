package memory

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/bds421/rho-kit/data/actionlog"
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
	// tenantMu serialises chain extension per tenant. The build callback
	// in AppendChained reads the latest entry and the persist runs while
	// the per-tenant lock is held — without this two concurrent appends
	// could observe the same prev and produce duplicate Seq values.
	tenantMu     map[string]*sync.Mutex
	tenantMuLock sync.Mutex
}

// New creates an empty Store.
func New() *Store {
	return &Store{
		byID:     make(map[string]int),
		tenantMu: make(map[string]*sync.Mutex),
	}
}

func (s *Store) lockFor(tenantID string) *sync.Mutex {
	s.tenantMuLock.Lock()
	defer s.tenantMuLock.Unlock()
	mu, ok := s.tenantMu[tenantID]
	if !ok {
		mu = &sync.Mutex{}
		s.tenantMu[tenantID] = mu
	}
	return mu
}

// AppendChained holds the per-tenant lock, reads the previous entry,
// runs build, and persists the resulting entry under the same lock.
func (s *Store) AppendChained(_ context.Context, tenantID string, build func(prev actionlog.Entry, prevSeq int64) (actionlog.Entry, error)) (actionlog.Entry, error) {
	if tenantID == "" {
		return actionlog.Entry{}, actionlog.ErrInvalidEntry
	}
	tmu := s.lockFor(tenantID)
	tmu.Lock()
	defer tmu.Unlock()

	prev, prevSeq := s.latestForTenantLocked(tenantID)
	entry, err := build(prev, prevSeq)
	if err != nil {
		return actionlog.Entry{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.byID[entry.ID]; dup {
		return actionlog.Entry{}, fmt.Errorf("%w: %s", ErrDuplicateID, entry.ID)
	}
	s.byID[entry.ID] = len(s.entries)
	s.entries = append(s.entries, entry)
	return entry, nil
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
	return best, bestSeq
}

// Get returns the entry by id, or [actionlog.ErrNotFound] if absent.
func (s *Store) Get(_ context.Context, id string) (actionlog.Entry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	idx, ok := s.byID[id]
	if !ok {
		return actionlog.Entry{}, actionlog.ErrNotFound
	}
	return s.entries[idx], nil
}

// List returns entries matching q ordered by OccurredAt descending,
// then ID descending.
func (s *Store) List(_ context.Context, q actionlog.Query) ([]actionlog.Entry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	limit := q.Limit
	if limit <= 0 {
		limit = defaultLimit
	}

	matched := make([]actionlog.Entry, 0, len(s.entries))
	for _, e := range s.entries {
		if !match(e, q) {
			continue
		}
		matched = append(matched, e)
	}

	sort.Slice(matched, func(i, j int) bool {
		ti, tj := matched[i].OccurredAt, matched[j].OccurredAt
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

// ListByTenantSeq returns every entry for tenantID in Seq ASC order.
// No limit is applied — VerifyChain needs the full chain.
func (s *Store) ListByTenantSeq(_ context.Context, tenantID string) ([]actionlog.Entry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]actionlog.Entry, 0)
	for _, e := range s.entries {
		if e.TenantID == tenantID {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out, nil
}

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
