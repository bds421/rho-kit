package auditlog

import (
	"context"
	"errors"
	"time"
)

// RetentionStore is a [Store] that removes the oldest events by a [Cut] and
// keeps the sealed [RetentionRecord] of what it removed.
type RetentionStore interface {
	Store
	// RetentionRecord returns the record, zero when nothing was removed
	// yet, or [ErrRecordUnsealed] when its seal does not hold under key.
	// Only a stored record needs the key: a verifier without it fails once
	// the first sweep wrote one.
	RetentionRecord(ctx context.Context, key []byte) (RetentionRecord, error)
	// ApplyCut removes the events c names, in one transaction with the
	// record that then replaces the stored one, sealed with key; it
	// returns how many it removed. It refuses with [ErrCutRefused] unless
	// the oldest events are still exactly those c names and one stays, and
	// with [ErrRetentionTooShort] when c.Before is less than minimum
	// before now. The stored record must hold under key.
	ApplyCut(ctx context.Context, c Cut, key []byte, minimum time.Duration, now time.Time) (int64, error)
}

// ErrNotRetentionStore is returned by [Logger.Prune] when the Logger's
// store cannot remove events.
var ErrNotRetentionStore = errors.New("auditlog: the store is no RetentionStore")

// Prune is retention for an installation whose one role holds the chain
// keys and may remove events: it verifies the chain from the sealed record,
// computes the cut of what was written more than retention before now, and
// has the Logger's store apply it. Retention below minimum is refused with
// [ErrRetentionTooShort]. To keep the chain keys away from the role that
// removes events, compute and sign the cut with one role and apply it with
// the other ([Cut.Encode], [DecodeCut], [RetentionStore.ApplyCut]).
func (l *Logger) Prune(ctx context.Context, key []byte, retention, minimum time.Duration, now time.Time) (int64, error) {
	store, ok := l.store.(RetentionStore)
	if !ok {
		return 0, ErrNotRetentionStore
	}
	if retention <= 0 || retention < minimum {
		return 0, ErrRetentionTooShort
	}
	record, err := store.RetentionRecord(ctx, key)
	if err != nil {
		return 0, err
	}
	c, err := l.ComputeCut(ctx, record.Verification(retention, minimum, now), now.Add(-retention), now)
	if err != nil {
		return 0, err
	}
	return store.ApplyCut(ctx, c, key, minimum, now)
}
