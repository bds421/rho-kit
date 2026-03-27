package outbox

import (
	"context"
	"time"

	"gorm.io/gorm"
)

// Store abstracts the persistence layer for outbox entries.
// Implementations must be safe for concurrent use.
type Store interface {
	// Insert creates a new outbox entry within the given transaction.
	Insert(ctx context.Context, tx *gorm.DB, entry Entry) error

	// FetchPending retrieves up to limit pending entries ordered by creation
	// time, locking them to prevent concurrent relay instances from processing
	// the same entries.
	FetchPending(ctx context.Context, limit int) ([]Entry, error)

	// MarkPublished sets the entry status to published with the given timestamp.
	MarkPublished(ctx context.Context, id string, publishedAt time.Time) error

	// MarkFailed sets the entry status to failed with the last error message.
	MarkFailed(ctx context.Context, id string, lastError string) error

	// IncrementAttempts increments the attempt counter and records the last error.
	IncrementAttempts(ctx context.Context, id string, lastError string) error

	// DeletePublishedBefore removes published entries older than the given time.
	// Returns the number of deleted rows.
	DeletePublishedBefore(ctx context.Context, before time.Time) (int64, error)

	// CountPending returns the number of entries with pending status.
	CountPending(ctx context.Context) (int64, error)
}
