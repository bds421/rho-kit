package auditlog

import (
	"context"
	"log/slog"
	"time"

	"github.com/bds421/rho-kit/core/v2/redact"
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
//
// Interaction with chain verification: deleting the oldest events leaves the
// new head with a non-empty PrevHMAC (it still links to a now-deleted
// record). The genesis-anchored [VerifyChain] / [Logger.VerifyChain] reject
// such a chain with [ErrChainBroken]. Operators who run retention AND rely on
// tamper-evidence must verify with the retention-aware [VerifyChainFrom] /
// [Logger.VerifyChainFrom], passing the HMAC of the last-deleted event as the
// watermark. Persist that watermark alongside the surviving records when the
// retention sweep runs.
func RetentionJob(store RetentionStore, retention time.Duration, logger *slog.Logger) func(ctx context.Context) error {
	if store == nil {
		panic("auditlog: RetentionJob requires a non-nil store")
	}
	if retention <= 0 {
		panic("auditlog: RetentionJob requires a positive retention duration")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return func(ctx context.Context) error {
		cutoff := time.Now().Add(-retention)
		deleted, err := store.DeleteBefore(ctx, cutoff)
		if err != nil {
			logger.Error("audit retention cleanup failed",
				redact.Error(err),
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
