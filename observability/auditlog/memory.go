package auditlog

import (
	"context"
	"strings"
	"sync"
)

// MemoryStore is an in-memory Store for testing. Not suitable for production.
type MemoryStore struct {
	mu     sync.RWMutex
	events []Event
}

// NewMemoryStore creates an empty in-memory audit store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{}
}

// Append adds an event to the in-memory store.
func (m *MemoryStore) Append(_ context.Context, event Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, event)
	return nil
}

// Query returns events matching the filter with cursor-based pagination.
// Events are returned in reverse insertion order (newest first).
func (m *MemoryStore) Query(_ context.Context, filter Filter, cursor string, limit int) ([]Event, string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if limit <= 0 {
		limit = 50
	}

	// Collect matching events in reverse order (newest first).
	var matched []Event
	pastCursor := cursor == ""

	for i := len(m.events) - 1; i >= 0; i-- {
		e := m.events[i]

		if !pastCursor {
			if e.ID == cursor {
				pastCursor = true
			}
			continue
		}

		if !matchesFilter(e, filter) {
			continue
		}

		matched = append(matched, e)
		if len(matched) > limit {
			break
		}
	}

	// If we got limit+1 results, there are more pages.
	var nextCursor string
	if len(matched) > limit {
		nextCursor = matched[limit-1].ID
		matched = matched[:limit]
	}
	return matched, nextCursor, nil
}

// Events returns all stored events (oldest first). Test helper.
func (m *MemoryStore) Events() []Event {
	m.mu.RLock()
	defer m.mu.RUnlock()
	cp := make([]Event, len(m.events))
	copy(cp, m.events)
	return cp
}

// Reset clears all stored events. Test helper.
func (m *MemoryStore) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = nil
}

func matchesFilter(e Event, f Filter) bool {
	if f.Actor != "" && e.Actor != f.Actor {
		return false
	}
	if f.Action != "" && e.Action != f.Action {
		return false
	}
	if f.Resource != "" && !strings.HasPrefix(e.Resource, f.Resource) {
		return false
	}
	if !f.Since.IsZero() && e.Timestamp.Before(f.Since) {
		return false
	}
	if !f.Until.IsZero() && e.Timestamp.After(f.Until) {
		return false
	}
	return true
}
