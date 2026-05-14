package auditlog

import (
	"context"
	"strings"
	"sync"
)

// MemoryStore is an in-memory Store for testing. Not suitable for production.
// Safe for concurrent use — the events slice is RWMutex-guarded; the
// AppendChained / Append paths hold the write lock across the chain
// extension so concurrent appenders cannot observe the same prev HMAC.
type MemoryStore struct {
	mu     sync.RWMutex
	events []Event
}

// NewMemoryStore creates an empty in-memory audit store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{}
}

// memCtxCheckBatch is the number of stored events scanned between
// ctx.Err() checks. Tuned so a cancelled scan returns within a
// handful of microseconds even over multi-million-event stores
// without making the common case noticeably slower.
const memCtxCheckBatch = 1024

// AppendChained holds the store mutex, reads the tail HMAC, runs build,
// validates the resulting event, and persists it atomically. Two
// concurrent appenders cannot observe the same prev HMAC because the
// read-tail / build / persist sequence happens under m.mu.
func (m *MemoryStore) AppendChained(ctx context.Context, build func(prev []byte) (Event, error)) error {
	if build == nil {
		return ErrInvalidEvent
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var prev []byte
	if len(m.events) > 0 {
		tail := m.events[len(m.events)-1].HMAC
		if len(tail) > 0 {
			prev = append([]byte(nil), tail...)
		}
	}
	event, err := build(prev)
	if err != nil {
		return err
	}
	if err := ValidateEvent(event); err != nil {
		return err
	}
	m.events = append(m.events, cloneEvent(event))
	return nil
}

// Append is retained as a free-form append for retention / replay
// tooling that legitimately needs to insert pre-built events (e.g. a
// historical chain restore). It does NOT participate in chain
// construction — use [MemoryStore.AppendChained] from production
// writers (Logger.LogE delegates to that path).
func (m *MemoryStore) Append(ctx context.Context, event Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := ValidateEvent(event); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, cloneEvent(event))
	return nil
}

// LastHMAC returns the HMAC of the most recently appended event, or nil if
// the store is empty. Logger.LogE uses this value as the PrevHMAC for the
// next event so the tamper-evident chain is preserved across restarts and
// across multiple Logger instances sharing the same store.
func (m *MemoryStore) LastHMAC(ctx context.Context) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if len(m.events) == 0 {
		return nil, nil
	}
	tail := m.events[len(m.events)-1].HMAC
	if len(tail) == 0 {
		return nil, nil
	}
	return append([]byte(nil), tail...), nil
}

// Query returns events matching the filter with cursor-based
// pagination. Events are returned in reverse insertion order (newest
// first). ctx.Err() is checked before scanning and every
// [memCtxCheckBatch] entries during the scan so a cancelled
// VerifyChain or List does not pay for the full table.
func (m *MemoryStore) Query(ctx context.Context, filter Filter, cursor string, limit int) ([]Event, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()

	if limit <= 0 {
		limit = 50
	}

	// Collect matching events in reverse order (newest first).
	matched := make([]Event, 0, limit+1)
	pastCursor := cursor == ""

	scanned := 0
	for i := len(m.events) - 1; i >= 0; i-- {
		if scanned%memCtxCheckBatch == 0 {
			if err := ctx.Err(); err != nil {
				return nil, "", err
			}
		}
		scanned++
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

		matched = append(matched, cloneEvent(e))
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
	return cloneEvents(m.events)
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
	if f.IPAddress != "" && e.IPAddress != f.IPAddress {
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
