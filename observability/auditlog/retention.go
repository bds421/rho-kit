package auditlog

import (
	"context"
	"encoding/hex"
	"log/slog"
	"time"

	"github.com/bds421/rho-kit/core/v2/redact"
)

// RetentionStore extends Store with the ability to delete old events.
type RetentionStore interface {
	Store
	// DeleteBefore removes a contiguous head of the seq-ordered chain
	// consisting of events older than before. It never deletes interior
	// rows (which would permanently break chain verification with no
	// watermark recovery). Returns the number of deleted events and the
	// HMAC of the newest deleted event (the watermark for
	// [VerifyChainFrom]). Watermark is nil/empty when nothing was deleted.
	//
	// Note: a backfilled event with an old occurred_at but a high seq is
	// NOT deleted until it becomes part of the contiguous head — this is
	// intentional so retention cannot create interior chain holes.
	DeleteBefore(ctx context.Context, before time.Time) (deleted int64, watermark []byte, err error)
}

// RetentionOption configures [RetentionJob].
type RetentionOption func(*retentionJobConfig)

type retentionJobConfig struct {
	// onWatermark, when non-nil, is invoked with the HMAC of the newest
	// deleted event after a successful non-empty prune. Operators should
	// persist this value and pass it to [Logger.VerifyChainFrom].
	onWatermark func([]byte)
}

// WithRetentionWatermarkSink registers a callback that receives the
// retention watermark (HMAC of the newest deleted event) after each
// non-empty DeleteBefore. Use it to persist the watermark so
// [VerifyChainFrom] can re-anchor the chain after pruning.
func WithRetentionWatermarkSink(fn func(watermark []byte)) RetentionOption {
	if fn == nil {
		panic("auditlog: WithRetentionWatermarkSink requires non-nil callback")
	}
	return func(c *retentionJobConfig) { c.onWatermark = fn }
}

// RetentionJob returns a function suitable for cron scheduling that deletes
// audit events older than the retention period. The function logs the number
// of deleted events and the watermark HMAC (hex) needed by
// [VerifyChainFrom].
//
// Interaction with chain verification: deleting the oldest events leaves the
// new head with a non-empty PrevHMAC (it still links to a now-deleted
// record). The genesis-anchored [VerifyChain] / [Logger.VerifyChain] reject
// such a chain with [ErrChainBroken]. Operators who run retention AND rely on
// tamper-evidence must verify with the retention-aware [VerifyChainFrom] /
// [Logger.VerifyChainFrom], passing the HMAC of the last-deleted event as the
// watermark. Persist that watermark via [WithRetentionWatermarkSink] (or from
// the job's log line) alongside the surviving records when the retention
// sweep runs.
func RetentionJob(store RetentionStore, retention time.Duration, logger *slog.Logger, opts ...RetentionOption) func(ctx context.Context) error {
	if store == nil {
		panic("auditlog: RetentionJob requires a non-nil store")
	}
	if retention <= 0 {
		panic("auditlog: RetentionJob requires a positive retention duration")
	}
	if logger == nil {
		logger = slog.Default()
	}
	cfg := retentionJobConfig{}
	for _, opt := range opts {
		if opt == nil {
			panic("auditlog: RetentionJob option must not be nil")
		}
		opt(&cfg)
	}
	return func(ctx context.Context) error {
		cutoff := time.Now().Add(-retention)
		deleted, watermark, err := store.DeleteBefore(ctx, cutoff)
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
				"watermark_hmac", hex.EncodeToString(watermark),
			)
			if cfg.onWatermark != nil && len(watermark) > 0 {
				cfg.onWatermark(append([]byte(nil), watermark...))
			}
		}
		return nil
	}
}
