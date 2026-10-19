package auditlog

import (
	"context"
	"errors"
)

// ErrTailUnsupported is returned by [Logger.Tail] when the configured
// Store does not implement [Tailer].
var ErrTailUnsupported = errors.New("auditlog: store does not support reading in append order")

// Position is a place in the append-ordered chain. The zero Position is
// before the first event. Positions are store-internal and unsigned:
// keep them server-side (for example as a log-shipping checkpoint), never
// hand them to clients — use [Logger.List] cursors for that.
type Position int64

// DefaultTailLimit is the batch size [Logger.Tail] uses when limit is 0.
const DefaultTailLimit = 500

// Tailer is optionally implemented by a [Store] that can read events in
// append order, which is what log shipping to a SIEM needs: every event
// exactly once, oldest first, resumable from a checkpoint.
type Tailer interface {
	// Tail returns up to limit events appended after position, optionally
	// restricted to one tenant ("" means all), in append order, and the
	// position of the last returned event (position itself when none).
	Tail(ctx context.Context, tenant string, after Position, limit int) ([]Event, Position, error)
}

// Tail reads events appended after the given position, oldest first. A
// shipper stores the returned position once the events are delivered and
// passes it on the next call, so a crash re-sends at most one batch.
func (l *Logger) Tail(ctx context.Context, tenant string, after Position, limit int) ([]Event, Position, error) {
	if limit < 0 {
		return nil, after, ErrLimitNegative
	}
	if limit > MaxPageLimit {
		return nil, after, ErrLimitTooLarge
	}
	if after < 0 {
		return nil, after, ErrInvalidCursor
	}
	tailer, ok := l.store.(Tailer)
	if !ok {
		return nil, after, ErrTailUnsupported
	}
	if limit == 0 {
		limit = DefaultTailLimit
	}
	return tailer.Tail(ctx, tenant, after, limit)
}

// Tail implements [Tailer]. Positions are 1-based indexes into the
// append-ordered slice; the memory store never deletes a head.
func (m *MemoryStore) Tail(ctx context.Context, tenant string, after Position, limit int) ([]Event, Position, error) {
	if err := ctx.Err(); err != nil {
		return nil, after, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Event, 0, limit)
	last := after
	for i := int(after); i < len(m.events) && len(out) < limit; i++ {
		last = Position(i + 1)
		if tenant == "" || m.events[i].Tenant == tenant {
			out = append(out, cloneEvent(m.events[i]))
		}
	}
	return out, last, nil
}
