package outbox

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

const (
	defaultPollInterval     = 1 * time.Second
	defaultBatchSize        = 100
	defaultMaxAttempts      = 10
	defaultRetention        = 7 * 24 * time.Hour  // 7 days
	defaultFailedRetention  = 30 * 24 * time.Hour // 30 days for entries in StatusFailed
	defaultStaleDuration    = 5 * time.Minute
	staleRecoveryMultiplier = 10 // recover stale entries every N polls

	// Exponential backoff bounds for IncrementAttempts: delay = baseDelay * 2^attempts,
	// clamped at maxBackoff. With 10 attempts (default) the schedule is roughly
	// 2s, 4s, 8s, 16s, 32s, 64s, 128s, 256s, 300s, 300s — totaling ~17 minutes
	// of retries before the row is marked permanently failed.
	defaultBackoffBase = 2 * time.Second
	defaultBackoffMax  = 5 * time.Minute

	// minHeartbeatInterval guards against pathological staleDuration values
	// that would heartbeat tighter than the database round-trip, hammering
	// the row updated_at column for no benefit.
	minHeartbeatInterval = 100 * time.Millisecond
)

// Relay polls the outbox table and publishes pending entries via a Publisher.
// It implements lifecycle.Component for integration with the service runner.
// Safe for concurrent use -- multiple instances can run against the same table.
type Relay struct {
	store     Store
	publisher Publisher
	logger    *slog.Logger
	metrics   *Metrics

	pollInterval         time.Duration
	batchSize            int
	maxAttempts          int
	maxConcurrentPublish int
	retention            time.Duration
	staleDuration        time.Duration
	publishTimeout       time.Duration

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

// WithStaleDuration sets how long a row may remain in "processing" state
// before [Store.ResetStaleProcessing] resets it back to pending. Default:
// 5 minutes. The relay heartbeats processing rows roughly every
// staleDuration/3 while a publish is in flight, so legitimate long
// publishes do not get reset by another relay instance. Operators that
// know their publisher backend has a tighter or looser tail latency can
// tune this knob — but [WithPublishTimeout] should always be set strictly
// below staleDuration so a hung publisher is detected before the row is
// eligible for stale recovery.
//
// Values <= 0 are ignored (default preserved).
func WithStaleDuration(d time.Duration) RelayOption {
	return func(r *Relay) {
		if d > 0 {
			r.staleDuration = d
		}
	}
}

// WithPublishTimeout bounds each Publisher.Publish call with a derived
// context deadline. When zero (the default) the relay does not impose a
// deadline and a hung publisher pins the row in processing until the
// stale-recovery timer fires.
//
// Setting publishTimeout < staleDuration is REQUIRED for at-most-once
// duplicate avoidance: if the publisher legitimately exceeds
// staleDuration, another relay can pick the row up and republish it.
// The relay logs a startup warning when publishTimeout >= staleDuration.
//
// Values <= 0 are ignored (no timeout applied).
func WithPublishTimeout(d time.Duration) RelayOption {
	return func(r *Relay) {
		if d > 0 {
			r.publishTimeout = d
		}
	}
}

// WithMaxConcurrentPublishes caps the number of in-flight Publisher calls
// per poll batch. Default: 1 (serial — preserves the historical
// FIFO-on-the-wire behaviour). Increase for high-throughput workloads
// where Publisher latency dominates poll cycle time.
//
// Setting this above 1 means MarkPublished/MarkFailed run out of FIFO
// order; downstream consumers that rely on strict ordering should keep
// the default. Most messaging backends (AMQP, Kafka with single
// partition, NATS) preserve order in flight only for serial publish from
// the same connection — concurrent publish doesn't, and the kit cannot
// fix that at the relay layer.
func WithMaxConcurrentPublishes(n int) RelayOption {
	return func(r *Relay) {
		if n > 0 {
			r.maxConcurrentPublish = n
		}
	}
}

// NewRelay creates a Relay that polls the outbox store and publishes entries
// via the given Publisher. Configure with RelayOption functions.
//
// Panics if store or publisher is nil — both are programming errors that
// would otherwise crash the first poll cycle. Logger nil is accepted and
// defaults to slog.Default() since dropping logs is recoverable.
func NewRelay(store Store, publisher Publisher, logger *slog.Logger, opts ...RelayOption) *Relay {
	if store == nil {
		panic("outbox: NewRelay requires a non-nil Store")
	}
	if publisher == nil {
		panic("outbox: NewRelay requires a non-nil Publisher")
	}
	if logger == nil {
		logger = slog.Default()
	}
	r := &Relay{
		store:                store,
		publisher:            publisher,
		maxConcurrentPublish: 1,
		logger:               logger,
		pollInterval:         defaultPollInterval,
		batchSize:            defaultBatchSize,
		maxAttempts:          defaultMaxAttempts,
		retention:            defaultRetention,
		staleDuration:        defaultStaleDuration,
	}
	for _, opt := range opts {
		opt(r)
	}
	if r.publishTimeout > 0 && r.publishTimeout >= r.staleDuration {
		r.logger.Warn("outbox relay: publish_timeout >= stale_duration — long publishes may be reset before completing, causing duplicate sends",
			"publish_timeout", r.publishTimeout,
			"stale_duration", r.staleDuration)
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
		"stale_duration", r.staleDuration,
		"publish_timeout", r.publishTimeout,
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

	concurrency := r.maxConcurrentPublish
	if concurrency <= 1 {
		// Serial fast path. Preserves FIFO ordering across the batch.
		for i := range entries {
			if ctx.Err() != nil {
				return
			}
			r.publishEntry(ctx, entries[i])
		}
		r.updatePendingGauge(ctx)
		return
	}

	// Bounded worker pool. Order across in-flight publishes is no longer
	// preserved; callers that need strict ordering must keep the default
	// concurrency=1.
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for i := range entries {
		if ctx.Err() != nil {
			break
		}
		entry := entries[i]
		// Wait for a free slot OR ctx cancellation. An unguarded
		// `sem <- struct{}{}` would stall poll() (and Stop) until an
		// in-flight Publish finished — small but observable
		// shutdown-latency hit on a slow Publisher.
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			r.publishEntry(ctx, entry)
		}()
	}
	wg.Wait()

	r.updatePendingGauge(ctx)
}

// publishEntry attempts to publish a single entry via the Publisher.
func (r *Relay) publishEntry(ctx context.Context, entry Entry) {
	heartbeatStop := r.startHeartbeat(ctx, entry.ID.String())
	defer heartbeatStop()

	publishCtx := ctx
	if r.publishTimeout > 0 {
		var cancel context.CancelFunc
		publishCtx, cancel = context.WithTimeout(ctx, r.publishTimeout)
		defer cancel()
	}

	start := time.Now()
	err := r.publisher.Publish(publishCtx, entry)
	elapsed := time.Since(start)

	if err != nil {
		r.handlePublishError(ctx, entry, err)
		return
	}

	r.recordLatency(elapsed)

	now := time.Now().UTC()
	if markErr := r.store.MarkPublished(ctx, entry.ID.String(), now); markErr != nil {
		// ErrStaleState means a concurrent stale-recovery reset the row to
		// pending while the publish was in flight. The message has been sent
		// downstream once already; the next poll will pick the same row up
		// and publish it again. Log loudly so operators can tune
		// stale_duration / publish_timeout.
		if errors.Is(markErr, ErrStaleState) || errors.Is(markErr, ErrNotFound) {
			r.logger.Error("outbox relay: mark published lost row — likely concurrent stale recovery, possible duplicate publish",
				"entry_id", entry.ID, "error", markErr)
			return
		}
		r.logger.Error("outbox relay: mark published failed",
			"entry_id", entry.ID, "error", markErr)
		return
	}

	r.recordPublished()

	r.logger.Debug("outbox relay: published entry",
		"entry_id", entry.ID,
		"message_id", entry.MessageID,
		"topic", entry.Topic,
		"routing_key", entry.RoutingKey,
	)
}

// startHeartbeat refreshes the row's updated_at timestamp on a fixed
// cadence so a long-running publish does not get reset to pending by
// another relay's stale-recovery sweep. Returns a stop function the
// caller MUST defer.
//
// The heartbeat fires every staleDuration/3 (clamped at
// minHeartbeatInterval) — three heartbeats per stale window keeps a
// publish alive even if one heartbeat round-trip transiently fails.
func (r *Relay) startHeartbeat(ctx context.Context, id string) func() {
	interval := r.staleDuration / 3
	if interval < minHeartbeatInterval {
		interval = minHeartbeatInterval
	}
	stopCh := make(chan struct{})
	doneCh := make(chan struct{})
	go func() {
		defer close(doneCh)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stopCh:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, err := r.store.Heartbeat(ctx, []string{id}); err != nil {
					r.logger.Warn("outbox relay: heartbeat failed",
						"entry_id", id, "error", err)
				}
			}
		}
	}()
	return func() {
		close(stopCh)
		<-doneCh
	}
}

// handlePublishError increments attempts or marks the entry as failed.
func (r *Relay) handlePublishError(ctx context.Context, entry Entry, publishErr error) {
	nextAttempt := entry.Attempts + 1
	errMsg := publishErr.Error()

	if nextAttempt >= r.maxAttempts {
		if markErr := r.store.MarkFailed(ctx, entry.ID.String(), errMsg); markErr != nil {
			if errors.Is(markErr, ErrStaleState) || errors.Is(markErr, ErrNotFound) {
				r.logger.Error("outbox relay: mark failed lost row — likely concurrent stale recovery",
					"entry_id", entry.ID, "error", markErr)
			} else {
				r.logger.Error("outbox relay: mark failed error",
					"entry_id", entry.ID, "error", markErr)
			}
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

	nextRetryAt := time.Now().UTC().Add(retryBackoff(nextAttempt))
	if incErr := r.store.IncrementAttempts(ctx, entry.ID.String(), errMsg, nextRetryAt); incErr != nil {
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

	reset, err := r.store.ResetStaleProcessing(ctx, r.staleDuration)
	if err != nil {
		r.logger.Error("outbox relay: recover stale failed", "error", err)
		return
	}

	if reset > 0 {
		r.logger.Warn("outbox relay: recovered stale processing entries",
			"count", reset, "stale_duration", r.staleDuration)
	}
}

// cleanup removes published entries older than the retention period and
// failed entries older than the failed-retention period. Without the second
// step, rows in StatusFailed (those that exhausted max attempts) accumulate
// forever and pollute the pending-status index.
func (r *Relay) cleanup(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}

	now := time.Now().UTC()
	publishedCutoff := now.Add(-r.retention)
	deletedPublished, err := r.store.DeletePublishedBefore(ctx, publishedCutoff)
	if err != nil {
		r.logger.Error("outbox relay: cleanup published failed", "error", err)
	} else if deletedPublished > 0 {
		r.logger.Info("outbox relay: cleaned up published entries",
			"deleted", deletedPublished, "retention", r.retention)
	}

	failedCutoff := now.Add(-defaultFailedRetention)
	deletedFailed, err := r.store.DeleteFailedBefore(ctx, failedCutoff)
	if err != nil {
		r.logger.Error("outbox relay: cleanup failed entries failed", "error", err)
	} else if deletedFailed > 0 {
		r.logger.Info("outbox relay: cleaned up failed entries",
			"deleted", deletedFailed, "retention", defaultFailedRetention)
	}
}

// retryBackoff returns the delay before the next attempt, applying exponential
// backoff capped at defaultBackoffMax. attempt is 1-indexed (first retry is
// attempt=1 → defaultBackoffBase; second retry is attempt=2 → 2× base; etc.).
func retryBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	// shift may overflow with very large attempt counts; clamp first.
	if attempt > 30 {
		return defaultBackoffMax
	}
	d := defaultBackoffBase << (attempt - 1)
	if d <= 0 || d > defaultBackoffMax {
		return defaultBackoffMax
	}
	return d
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
