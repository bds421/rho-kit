package outbox

import (
	"context"
	"time"
)

// Store abstracts the persistence layer for outbox entries.
// Implementations must be safe for concurrent use.
type Store interface {
	// Insert creates a new outbox entry.
	// For transactional writes, the implementation should accept a
	// transaction handle via context or a separate method.
	Insert(ctx context.Context, entry Entry) error

	// FetchPending atomically claims up to limit pending entries by setting
	// their status to "processing", ordered by creation time. Implementations
	// must prevent concurrent relay instances from claiming the same entries.
	FetchPending(ctx context.Context, limit int) ([]Entry, error)

	// MarkPublished sets the entry status to published with the given timestamp.
	MarkPublished(ctx context.Context, id string, publishedAt time.Time) error

	// MarkFailed sets the entry status to failed with the last error message.
	MarkFailed(ctx context.Context, id string, lastError string) error

	// IncrementAttempts increments the attempt counter, records the last error,
	// resets the entry status to pending, and sets next_retry_at to a future
	// timestamp computed from the new attempt count. The relay's FetchPending
	// must skip entries whose next_retry_at is still in the future, so a
	// persistently failing downstream produces exponential backoff rather than
	// a tight retry loop.
	IncrementAttempts(ctx context.Context, id string, lastError string, nextRetryAt time.Time) error

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

	// CountPending returns the number of entries with pending status.
	CountPending(ctx context.Context) (int64, error)
}
