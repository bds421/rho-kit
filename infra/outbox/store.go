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
	// number of rows touched for diagnostics; the relay currently
	// discards this count and only checks the error.
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
// implements all five roles. The split into [Inserter], [Claimer],
// [Outcomer], [Janitor], and [Observer] exists so callers can declare
// the narrowest possible dependency: a transactional producer asks
// for just an Inserter; a metrics exporter asks for just an Observer.
// Implementations must be safe for concurrent use.
type Store interface {
	Inserter
	Claimer
	Outcomer
	Janitor
	Observer
}

// RelayStore is the subset of [Store] the [Relay] actually consumes:
// claim entries, record outcomes, run janitorial work, and observe
// pending depth. NewRelay accepts this narrower contract so producer
// code paths (which only need [Inserter]) cannot be misused as relay
// state and so test doubles can implement just the relay-relevant
// methods.
type RelayStore interface {
	Claimer
	Outcomer
	Janitor
	Observer
}

// PendingResetter is an OPTIONAL capability a [RelayStore] may implement
// to support prompt shutdown recovery. When the relay's run context is
// cancelled mid-batch (e.g. a deploy or rolling restart), rows it has
// already claimed sit in "processing" until the slow stale-recovery
// sweep (see [Janitor.ResetStaleProcessing], default ~5 minutes) returns
// them to pending. That strands up to a full batch of claimed-but-
// unpublished messages for minutes on every restart.
//
// A store that implements PendingResetter lets the relay return its own
// still-claimed rows to "pending" immediately on shutdown, so a freshly
// started replica can re-claim them on its next poll without waiting for
// the stale window.
//
// Implementations MUST only reset rows that are still owned by the
// caller's claim. A backend that fences claims (e.g. the postgres store's
// claim_token) must require ownership so a late ResetPending cannot
// resurrect a row another relay has since re-claimed or published.
// Implementations MUST only touch rows still in "processing" state, the
// same guard the outcome methods use. The relay treats this as
// best-effort: failures are logged and never block shutdown.
//
// The relay detects support via a runtime type assertion, so existing
// [RelayStore] implementations remain valid without change.
type PendingResetter interface {
	// ResetPending returns the listed claimed rows to "pending" so they
	// become eligible for re-claim. ids that no longer belong to the
	// caller's claim (re-claimed, published, failed, or deleted) MUST be
	// skipped silently rather than reported as an error.
	ResetPending(ctx context.Context, ids []string) error
}
