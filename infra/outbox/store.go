package outbox

import (
	"context"
	"time"
)

// Inserter persists new outbox entries from application code. The
// majority of producers only need this slice — typically the same
// transaction that writes the business row also calls Insert.
type Inserter interface {
	// Insert creates a new outbox entry.
	// For transactional writes, the implementation should accept a
	// transaction handle via context or a separate method.
	Insert(ctx context.Context, entry Entry) error
}

// Claimer is the relay's read-side: atomically pick pending entries
// and keep their claim alive across long publishes.
type Claimer interface {
	// FetchPending atomically claims up to limit pending entries by setting
	// their status to "processing", ordered by creation time. Implementations
	// must prevent concurrent relay instances from claiming the same entries.
	FetchPending(ctx context.Context, limit int) ([]Entry, error)

	// Heartbeat refreshes the updated_at timestamp on processing rows
	// matching ids so that a long-running publish does not get reset by
	// [Janitor.ResetStaleProcessing]. Implementations MUST only update
	// rows currently in "processing" state to avoid resurrecting rows
	// that have already been marked published or failed. Returns the
	// number of rows touched (useful for diagnostics; the relay logs
	// unexpectedly low counts).
	Heartbeat(ctx context.Context, ids []string) (int64, error)
}

// Outcomer records the result of a publish attempt for a claimed
// entry. The three methods are mutually exclusive — every claimed
// entry transitions to exactly one terminal status per attempt.
type Outcomer interface {
	// MarkPublished sets the entry status to published with the given timestamp.
	// Implementations MUST return [ErrNotFound] when no row matches id and
	// [ErrStaleState] when the row exists but is not in the expected
	// "processing" state — typically because a concurrent stale-recovery
	// reset the row to pending while the publish was in flight.
	MarkPublished(ctx context.Context, id string, publishedAt time.Time) error

	// MarkFailed sets the entry status to failed with the last error message.
	// Implementations MUST return [ErrNotFound] when no row matches id and
	// [ErrStaleState] when the row exists but is not in the expected
	// "processing" state.
	MarkFailed(ctx context.Context, id string, lastError string) error

	// IncrementAttempts increments the attempt counter, records the last error,
	// resets the entry status to pending, and sets next_retry_at to a future
	// timestamp computed from the new attempt count. The relay's FetchPending
	// must skip entries whose next_retry_at is still in the future, so a
	// persistently failing downstream produces exponential backoff rather than
	// a tight retry loop.
	//
	// Implementations MUST guard the update on status='processing' so a row
	// that has already been moved out of processing (e.g. by a concurrent
	// stale-recovery, or because another worker already published or failed
	// it) is not resurrected. Implementations MUST return [ErrNotFound] when
	// no row matches id and [ErrStaleState] when the row exists but is no
	// longer in the expected "processing" state, matching
	// [Outcomer.MarkPublished] and [Outcomer.MarkFailed].
	IncrementAttempts(ctx context.Context, id string, lastError string, nextRetryAt time.Time) error
}

// Janitor handles housekeeping: retention deletion and stale-claim
// recovery. Typically driven by the relay's cleanup loop on a
// long-period ticker rather than the hot path.
type Janitor interface {
	// DeletePublishedBefore removes published entries older than the given time.
	// Returns the number of deleted rows.
	DeletePublishedBefore(ctx context.Context, before time.Time) (int64, error)

	// DeleteFailedBefore removes failed entries older than the given time.
	// Failed entries (those that exhausted max attempts) accumulate forever
	// without this; a periodic call from the relay's cleanup loop keeps the
	// table bounded. Returns the number of deleted rows.
	DeleteFailedBefore(ctx context.Context, before time.Time) (int64, error)

	// ResetStaleProcessing resets entries stuck in "processing" status back to
	// "pending" if they have been processing for longer than the given duration.
	// This recovers from relay crashes. Returns the number of reset rows.
	ResetStaleProcessing(ctx context.Context, staleDuration time.Duration) (int64, error)
}

// Observer exposes read-only inspection useful for dashboards and
// health checks. Kept separate so monitoring code can depend on the
// narrowest possible interface.
type Observer interface {
	// CountPending returns the number of entries with pending status.
	CountPending(ctx context.Context) (int64, error)
}

// Store is the full persistence contract — every production backend
// implements all four roles. The split into [Inserter], [Claimer],
// [Outcomer], [Janitor], and [Observer] exists so callers can declare
// the narrowest possible dependency: a transactional producer asks
// for just a Inserter; a metrics exporter asks for just an Observer.
// Implementations must be safe for concurrent use.
type Store interface {
	Inserter
	Claimer
	Outcomer
	Janitor
	Observer
}
