package auditlog

import (
	"context"
	"log/slog"
	"time"
)

// RetentionStore extends Store with the ability to delete old events.
type RetentionStore interface {
	Store
	// DeleteBefore removes all events with a timestamp before the given time.
	// Returns the number of deleted events.
	DeleteBefore(ctx context.Context, before time.Time) (int64, error)
}

// RetentionJob returns a function suitable for cron scheduling that deletes
// audit events older than the retention period. The function logs the number
// of deleted events.
func RetentionJob(store RetentionStore, retention time.Duration, logger *slog.Logger) func(ctx context.Context) error {
	if logger == nil {
		logger = slog.Default()
	}
	return func(ctx context.Context) error {
		cutoff := time.Now().Add(-retention)
		deleted, err := store.DeleteBefore(ctx, cutoff)
		if err != nil {
			logger.Error("audit retention cleanup failed",
				"error", err,
				"cutoff", cutoff,
			)
			return err
		}
		if deleted > 0 {
			logger.Info("audit retention cleanup completed",
				"deleted", deleted,
				"cutoff", cutoff,
			)
		}
		return nil
	}
}
