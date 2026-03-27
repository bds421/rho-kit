package outbox

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/bds421/rho-kit/infra/messaging"
)

const (
	defaultPollInterval     = 1 * time.Second
	defaultBatchSize        = 100
	defaultMaxAttempts      = 10
	defaultRetention        = 7 * 24 * time.Hour // 7 days
	defaultStaleDuration    = 5 * time.Minute
	staleRecoveryMultiplier = 10 // recover stale entries every N polls
)

// Relay polls the outbox table and publishes pending entries to the broker.
// It implements lifecycle.Component for integration with the service runner.
// Safe for concurrent use -- multiple instances can run against the same table.
type Relay struct {
	store     Store
	publisher messaging.MessagePublisher
	logger    *slog.Logger
	metrics   *Metrics

	pollInterval time.Duration
	batchSize    int
	maxAttempts  int
	retention    time.Duration

	cancel    context.CancelFunc
	mu        sync.Mutex
	done      chan struct{}
	pollCount int
}

// RelayOption configures a Relay.
type RelayOption func(*Relay)

// WithPollInterval sets the polling interval for the relay. Default: 1s.
func WithPollInterval(d time.Duration) RelayOption {
	return func(r *Relay) {
		if d > 0 {
			r.pollInterval = d
		}
	}
}

// WithBatchSize sets the maximum number of entries fetched per poll.
// Default: 100.
func WithBatchSize(n int) RelayOption {
	return func(r *Relay) {
		if n > 0 {
			r.batchSize = n
		}
	}
}

// WithMaxAttempts sets the maximum publish attempts before marking as failed.
// Default: 10.
func WithMaxAttempts(n int) RelayOption {
	return func(r *Relay) {
		if n > 0 {
			r.maxAttempts = n
		}
	}
}

// WithRetention sets how long published entries are kept before cleanup.
// Default: 7 days.
func WithRetention(d time.Duration) RelayOption {
	return func(r *Relay) {
		if d > 0 {
			r.retention = d
		}
	}
}

// WithMetrics attaches Prometheus metrics to the relay.
func WithMetrics(m *Metrics) RelayOption {
	return func(r *Relay) {
		r.metrics = m
	}
}

// NewRelay creates a Relay that polls the outbox store and publishes to the
// given publisher. Configure with RelayOption functions.
func NewRelay(store Store, publisher messaging.MessagePublisher, logger *slog.Logger, opts ...RelayOption) *Relay {
	r := &Relay{
		store:        store,
		publisher:    publisher,
		logger:       logger,
		pollInterval: defaultPollInterval,
		batchSize:    defaultBatchSize,
		maxAttempts:  defaultMaxAttempts,
		retention:    defaultRetention,
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// Start begins polling the outbox table. Blocks until ctx is cancelled.
// Implements lifecycle.Component.
func (r *Relay) Start(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	r.mu.Lock()
	r.cancel = cancel
	r.done = make(chan struct{})
	r.mu.Unlock()

	defer func() {
		r.mu.Lock()
		close(r.done)
		r.mu.Unlock()
	}()

	r.logger.Info("outbox relay started",
		"poll_interval", r.pollInterval,
		"batch_size", r.batchSize,
		"max_attempts", r.maxAttempts,
		"retention", r.retention,
	)

	// Initial poll immediately on start.
	r.poll(ctx)

	ticker := time.NewTicker(r.pollInterval)
	defer ticker.Stop()

	// Cleanup ticker runs less frequently.
	cleanupInterval := r.retention / 10
	if cleanupInterval < time.Minute {
		cleanupInterval = time.Minute
	}
	cleanupTicker := time.NewTicker(cleanupInterval)
	defer cleanupTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			r.logger.Info("outbox relay stopping")
			return nil
		case <-ticker.C:
			r.poll(ctx)
		case <-cleanupTicker.C:
			r.cleanup(ctx)
		}
	}
}

// Stop cancels the relay context and waits for the poll loop to finish.
// Implements lifecycle.Component.
func (r *Relay) Stop(ctx context.Context) error {
	r.mu.Lock()
	cancel := r.cancel
	done := r.done
	r.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// poll fetches pending entries and publishes them.
func (r *Relay) poll(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}

	// Periodically recover entries stuck in "processing" from crashed relays.
	r.pollCount++
	if r.pollCount%staleRecoveryMultiplier == 0 {
		r.recoverStale(ctx)
	}

	entries, err := r.store.FetchPending(ctx, r.batchSize)
	if err != nil {
		r.logger.Error("outbox relay: fetch pending failed", "error", err)
		return
	}

	if len(entries) == 0 {
		return
	}

	for i := range entries {
		if ctx.Err() != nil {
			return
		}
		r.publishEntry(ctx, entries[i])
	}

	r.updatePendingGauge(ctx)
}

// publishEntry attempts to publish a single entry to the broker.
func (r *Relay) publishEntry(ctx context.Context, entry Entry) {
	msg, err := entry.ToMessage()
	if err != nil {
		r.handlePublishError(ctx, entry, err)
		return
	}

	start := time.Now()
	err = r.publisher.Publish(ctx, entry.Exchange, entry.RoutingKey, msg)
	elapsed := time.Since(start)

	if err != nil {
		r.handlePublishError(ctx, entry, err)
		return
	}

	r.recordLatency(elapsed)

	now := time.Now().UTC()
	if markErr := r.store.MarkPublished(ctx, entry.ID.String(), now); markErr != nil {
		r.logger.Error("outbox relay: mark published failed",
			"entry_id", entry.ID, "error", markErr)
		return
	}

	r.recordPublished()

	r.logger.Debug("outbox relay: published entry",
		"entry_id", entry.ID,
		"message_id", entry.MessageID,
		"exchange", entry.Exchange,
		"routing_key", entry.RoutingKey,
	)
}

// handlePublishError increments attempts or marks the entry as failed.
func (r *Relay) handlePublishError(ctx context.Context, entry Entry, publishErr error) {
	nextAttempt := entry.Attempts + 1
	errMsg := publishErr.Error()

	if nextAttempt >= r.maxAttempts {
		if markErr := r.store.MarkFailed(ctx, entry.ID.String(), errMsg); markErr != nil {
			r.logger.Error("outbox relay: mark failed error",
				"entry_id", entry.ID, "error", markErr)
		}
		r.recordError()
		r.logger.Error("outbox relay: entry failed permanently",
			"entry_id", entry.ID,
			"message_id", entry.MessageID,
			"attempts", nextAttempt,
			"error", errMsg,
		)
		return
	}

	if incErr := r.store.IncrementAttempts(ctx, entry.ID.String(), errMsg); incErr != nil {
		r.logger.Error("outbox relay: increment attempts error",
			"entry_id", entry.ID, "error", incErr)
	}
	r.recordError()

	r.logger.Warn("outbox relay: publish failed, will retry",
		"entry_id", entry.ID,
		"message_id", entry.MessageID,
		"attempt", nextAttempt,
		"max_attempts", r.maxAttempts,
		"error", errMsg,
	)
}

// recoverStale resets entries stuck in "processing" status from crashed relays.
func (r *Relay) recoverStale(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}

	reset, err := r.store.ResetStaleProcessing(ctx, defaultStaleDuration)
	if err != nil {
		r.logger.Error("outbox relay: recover stale failed", "error", err)
		return
	}

	if reset > 0 {
		r.logger.Warn("outbox relay: recovered stale processing entries",
			"count", reset, "stale_duration", defaultStaleDuration)
	}
}

// cleanup removes published entries older than the retention period.
func (r *Relay) cleanup(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}

	cutoff := time.Now().UTC().Add(-r.retention)
	deleted, err := r.store.DeletePublishedBefore(ctx, cutoff)
	if err != nil {
		r.logger.Error("outbox relay: cleanup failed", "error", err)
		return
	}

	if deleted > 0 {
		r.logger.Info("outbox relay: cleaned up published entries",
			"deleted", deleted, "retention", r.retention)
	}
}

// updatePendingGauge refreshes the pending count metric.
func (r *Relay) updatePendingGauge(ctx context.Context) {
	if r.metrics == nil {
		return
	}

	count, err := r.store.CountPending(ctx)
	if err != nil {
		r.logger.Error("outbox relay: count pending for metrics failed",
			"error", err)
		return
	}
	r.metrics.pendingCount.Set(float64(count))
}

// recordLatency records relay publish latency if metrics are configured.
func (r *Relay) recordLatency(d time.Duration) {
	if r.metrics == nil {
		return
	}
	r.metrics.relayLatency.Observe(d.Seconds())
}

// recordPublished increments the published counter if metrics are configured.
func (r *Relay) recordPublished() {
	if r.metrics == nil {
		return
	}
	r.metrics.publishedTotal.Inc()
}

// recordError increments the error counter if metrics are configured.
func (r *Relay) recordError() {
	if r.metrics == nil {
		return
	}
	r.metrics.errorsTotal.Inc()
}
