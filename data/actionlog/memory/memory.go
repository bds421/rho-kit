package memory

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/bds421/rho-kit/data/actionlog"
)

// ErrDuplicateID is returned by [Store.Append] when an entry id is
// already present. The Logger generates UUIDv7 ids so duplicates only
// occur if a caller pre-populates [actionlog.Entry.ID]; we surface
// rather than overwrite because a silent overwrite hides the bug.
var ErrDuplicateID = errors.New("actionlog/memory: duplicate entry id")

const defaultLimit = 100

// Store is an in-memory [actionlog.Store]. Safe for concurrent use.
type Store struct {
	mu      sync.RWMutex
	entries []actionlog.Entry // insertion order
	byID    map[string]int    // id -> index into entries
}

// New creates an empty Store.
func New() *Store {
	return &Store{byID: make(map[string]int)}
}

// Append persists an entry. Rejects duplicate ids with [ErrDuplicateID].
func (s *Store) Append(_ context.Context, e actionlog.Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.byID[e.ID]; dup {
		return fmt.Errorf("%w: %s", ErrDuplicateID, e.ID)
	}
	s.byID[e.ID] = len(s.entries)
	s.entries = append(s.entries, e)
	return nil
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

	// Filter pass.
	matched := make([]actionlog.Entry, 0, len(s.entries))
	for _, e := range s.entries {
		if !match(e, q) {
			continue
		}
		matched = append(matched, e)
	}

	// Sort: OccurredAt DESC, ID DESC. Stable sort would also be fine
	// but the explicit comparator captures the contract Store callers
	// rely on.
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
