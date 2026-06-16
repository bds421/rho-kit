package outbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/bds421/rho-kit/core/v2/redact"
)

const (
	defaultPollInterval     = 1 * time.Second
	defaultBatchSize        = 100
	defaultMaxAttempts      = 10
	defaultRetention        = 7 * 24 * time.Hour  // 7 days
	defaultFailedRetention  = 30 * 24 * time.Hour // 30 days for entries in StatusFailed
	defaultStaleDuration    = 5 * time.Minute
	defaultPublishTimeout   = 2 * time.Minute
	staleRecoveryMultiplier = 10 // recover stale entries every N polls

	// Exponential backoff bounds for IncrementAttempts: delay = baseDelay * 2^(attempt-1),
	// clamped at maxBackoff. With maxAttempts=10 (default), backoff is applied only for
	// the 9 retries before the final attempt (the 10th attempt goes straight to
	// MarkFailed without a delay), so the schedule is roughly
	// 2s, 4s, 8s, 16s, 32s, 64s, 128s, 256s, 300s — totaling ~13.5 minutes
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
//
// Concurrency: multiple Relay instances are safe to run against the same
// table — atomic SELECT FOR UPDATE on the FetchPending path prevents
// duplicate claims. A given Relay may not have Start called more than
// once; the started flag enforces this.
type Relay struct {
	store     RelayStore
	publisher Publisher
	logger    *slog.Logger
	metrics   *Metrics

	pollInterval         time.Duration
	batchSize            int
	maxAttempts          int
	maxConcurrentPublish int
	retention            time.Duration
	failedRetention      time.Duration
	staleDuration        time.Duration
	publishTimeout       time.Duration

	cancel    context.CancelFunc
	mu        sync.Mutex
	done      chan struct{}
	started   bool
	stopped   bool
	pollCount int

	// claimed tracks ids claimed by FetchPending that have not yet reached
	// a terminal outcome (published / failed / attempts-incremented). On
	// shutdown the relay resets whatever remains here via PendingResetter
	// so a deploy does not strand claimed rows in "processing" until the
	// slow stale sweep. Guarded by claimedMu (separate from mu so the hot
	// publish path never contends with lifecycle state).
	claimedMu sync.Mutex
	claimed   map[string]struct{}
}

// resetTimeout bounds the best-effort ResetPending call issued on
// shutdown. The run context is already cancelled at that point, so the
// reset uses a fresh context derived from context.Background() with this
// deadline — long enough for a single UPDATE round-trip, short enough
// that a wedged database cannot block shutdown indefinitely.
const resetTimeout = 5 * time.Second

// RelayOption configures a Relay.
type RelayOption func(*Relay)

// WithPollInterval sets the polling interval for the relay. Default: 1s.
func WithPollInterval(d time.Duration) RelayOption {
	if d <= 0 {
		panic("outbox: WithPollInterval requires a positive duration")
	}
	return func(r *Relay) {
		r.pollInterval = d
	}
}

// WithBatchSize sets the maximum number of entries fetched per poll.
// Default: 100.
func WithBatchSize(n int) RelayOption {
	if n <= 0 {
		panic("outbox: WithBatchSize requires n > 0")
	}
	return func(r *Relay) {
		r.batchSize = n
	}
}

// WithMaxAttempts sets the maximum publish attempts before marking as failed.
// Default: 10.
func WithMaxAttempts(n int) RelayOption {
	if n <= 0 {
		panic("outbox: WithMaxAttempts requires n > 0")
	}
	return func(r *Relay) {
		r.maxAttempts = n
	}
}

// WithRetention sets how long published entries are kept before cleanup.
// Default: 7 days.
func WithRetention(d time.Duration) RelayOption {
	if d <= 0 {
		panic("outbox: WithRetention requires a positive duration")
	}
	return func(r *Relay) {
		r.retention = d
	}
}

// WithFailedRetention sets how long failed entries are kept before cleanup.
// Default: 30 days. Failed rows are useful for investigation, so this should
// usually be longer than the published-entry retention.
func WithFailedRetention(d time.Duration) RelayOption {
	if d <= 0 {
		panic("outbox: WithFailedRetention requires a positive duration")
	}
	return func(r *Relay) {
		r.failedRetention = d
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
// tune this knob.
//
// The duration must be positive.
func WithStaleDuration(d time.Duration) RelayOption {
	if d <= 0 {
		panic("outbox: WithStaleDuration requires a positive duration")
	}
	return func(r *Relay) {
		r.staleDuration = d
	}
}

// WithPublishTimeout bounds each Publisher.Publish call with a derived
// context deadline. Default: 2 minutes.
//
// Keep publishTimeout below staleDuration when the publisher honors
// cancellation promptly; the relay logs a startup warning when the timeout
// is longer than the stale window because operators should then rely on
// heartbeat health rather than stale recovery for hung publishes.
//
// The duration must be positive. Use [WithoutPublishTimeout] to opt out.
func WithPublishTimeout(d time.Duration) RelayOption {
	if d <= 0 {
		panic("outbox: WithPublishTimeout requires a positive duration")
	}
	return func(r *Relay) {
		r.publishTimeout = d
	}
}

// WithoutPublishTimeout disables the relay's Publisher.Publish deadline.
// Use only for publishers that enforce their own per-call deadline.
func WithoutPublishTimeout() RelayOption {
	return func(r *Relay) {
		r.publishTimeout = 0
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
	if n <= 0 {
		panic("outbox: WithMaxConcurrentPublishes requires n > 0")
	}
	return func(r *Relay) {
		r.maxConcurrentPublish = n
	}
}

// NewRelay creates a Relay that polls the outbox store and publishes entries
// via the given Publisher. Configure with RelayOption functions.
//
// store is the narrow [RelayStore] interface (claim + outcome + janitor +
// observer). The full [Store] satisfies it, but callers can hand in a
// composed value that excludes [Inserter] so the relay cannot accidentally
// produce new entries.
//
// Panics if store or publisher is nil — both are programming errors that
// would otherwise crash the first poll cycle. Logger nil is accepted and
// defaults to slog.Default() since dropping logs is recoverable.
func NewRelay(store RelayStore, publisher Publisher, logger *slog.Logger, opts ...RelayOption) *Relay {
	if store == nil {
		panic("outbox: NewRelay requires a non-nil RelayStore")
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
		failedRetention:      defaultFailedRetention,
		staleDuration:        defaultStaleDuration,
		publishTimeout:       defaultPublishTimeout,
		claimed:              make(map[string]struct{}),
	}
	for _, opt := range opts {
		if opt == nil {
			panic("outbox: Relay option must not be nil")
		}
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
	if ctx == nil {
		return errors.New("outbox: Relay.Start requires a non-nil context")
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	r.mu.Lock()
	// Check stopped before started so a relay that already ran and was
	// stopped reports the actual terminal state ("already stopped") on a
	// restart attempt, rather than the misleading "already started". A
	// relay that is currently running (started && !stopped) still reports
	// "already started" via the second branch.
	if r.stopped {
		r.mu.Unlock()
		cancel()
		return errors.New("outbox: Relay already stopped")
	}
	if r.started {
		r.mu.Unlock()
		cancel()
		return errors.New("outbox: Relay already started")
	}
	r.started = true
	r.cancel = cancel
	r.done = done
	r.mu.Unlock()

	defer func() {
		// The poll loop has returned, so no publish goroutine is still
		// mutating the claimed set (poll() waits for its worker pool before
		// returning). Reset any rows this relay claimed but never finished
		// publishing so a restart does not strand them in "processing" until
		// the stale sweep. The run ctx is already cancelled, so this uses a
		// fresh short-deadline context internally. Runs before close(done) so
		// Stop's wait covers the reset (bounded by resetTimeout).
		r.resetClaimedOnShutdown()
		close(done)
	}()

	r.logger.Info("outbox relay started",
		"poll_interval", r.pollInterval,
		"batch_size", r.batchSize,
		"max_attempts", r.maxAttempts,
		"retention", r.retention,
		"failed_retention", r.failedRetention,
		"stale_duration", r.staleDuration,
		"publish_timeout", r.publishTimeout,
	)

	// Initial poll immediately on start.
	r.poll(runCtx)
	r.cleanup(runCtx)

	ticker := time.NewTicker(r.pollInterval)
	defer ticker.Stop()

	// Cleanup ticker runs less frequently.
	cleanupBase := r.retention
	if r.failedRetention < cleanupBase {
		cleanupBase = r.failedRetention
	}
	cleanupInterval := cleanupBase / 10
	if cleanupInterval < time.Minute {
		cleanupInterval = time.Minute
	}
	cleanupTicker := time.NewTicker(cleanupInterval)
	defer cleanupTicker.Stop()

	for {
		select {
		case <-runCtx.Done():
			r.logger.Info("outbox relay stopping")
			return nil
		case <-ticker.C:
			r.poll(runCtx)
		case <-cleanupTicker.C:
			r.cleanup(runCtx)
		}
	}
}

// Stop cancels the relay context and waits for the poll loop to finish.
// Implements lifecycle.Component.
func (r *Relay) Stop(ctx context.Context) error {
	if ctx == nil {
		return errors.New("outbox: Relay.Stop requires a non-nil context")
	}
	r.mu.Lock()
	r.stopped = true
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

// trackClaimed records ids the relay has claimed but not yet driven to a
// terminal outcome, so shutdown can reset whatever is still in flight.
func (r *Relay) trackClaimed(entries []Entry) {
	r.claimedMu.Lock()
	defer r.claimedMu.Unlock()
	for i := range entries {
		r.claimed[entries[i].ID.String()] = struct{}{}
	}
}

// untrackClaimed drops an id once it has reached a terminal outcome
// (published / failed / attempts-incremented) or is otherwise no longer
// owned by this relay, so shutdown does not try to reset a row that is
// already done.
func (r *Relay) untrackClaimed(id string) {
	r.claimedMu.Lock()
	defer r.claimedMu.Unlock()
	delete(r.claimed, id)
}

// claimedIDs snapshots the ids still claimed by this relay.
func (r *Relay) claimedIDs() []string {
	r.claimedMu.Lock()
	defer r.claimedMu.Unlock()
	if len(r.claimed) == 0 {
		return nil
	}
	ids := make([]string, 0, len(r.claimed))
	for id := range r.claimed {
		ids = append(ids, id)
	}
	return ids
}

// resetClaimedOnShutdown returns rows this relay claimed but never drove
// to a terminal outcome back to "pending" so a freshly started replica
// can re-claim them immediately instead of waiting out the stale window.
//
// It is a no-op unless the store implements [PendingResetter]. The run
// context is already cancelled by the time this runs, so the reset uses a
// fresh, short-deadline context derived from context.Background(); the
// store's claim fence (where present) ensures only rows still owned by
// this relay are reset. Best-effort: failures are logged, never fatal.
func (r *Relay) resetClaimedOnShutdown() {
	resetter, ok := r.store.(PendingResetter)
	if !ok {
		return
	}
	ids := r.claimedIDs()
	if len(ids) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), resetTimeout)
	defer cancel()
	if err := resetter.ResetPending(ctx, ids); err != nil {
		r.logger.Warn("outbox relay: reset claimed-on-shutdown failed — rows wait for stale recovery",
			"count", len(ids),
			redact.Error(err))
		return
	}
	for _, id := range ids {
		r.untrackClaimed(id)
	}
	r.logger.Info("outbox relay: reset claimed entries on shutdown",
		"count", len(ids))
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
		r.logger.Error("outbox relay: fetch pending failed", redact.Error(err))
		return
	}

	if len(entries) == 0 {
		return
	}

	// Remember the claimed batch so a mid-batch shutdown can reset whatever
	// never reaches a terminal outcome (see resetClaimedOnShutdown). Each id
	// is dropped from this set as it is published / failed / re-queued.
	r.trackClaimed(entries)

	// FetchPending claims the whole batch to "processing" at T0. Every claimed
	// row — including those still queued behind a slow publish — must be
	// heartbeated for the lifetime of the batch, or another replica's
	// ResetStaleProcessing can reclaim a queued row mid-batch and both relays
	// publish it. A per-entry heartbeat only protects the row currently being
	// published, so heartbeat the whole batch here and mark each id done as it
	// reaches a terminal outcome.
	heartbeat := r.startBatchHeartbeat(ctx, entries)
	defer heartbeat.stop()

	concurrency := r.maxConcurrentPublish
	if concurrency <= 1 {
		// Serial fast path. Preserves FIFO ordering across the batch.
		for i := range entries {
			if ctx.Err() != nil {
				return
			}
			r.publishEntry(ctx, entries[i], heartbeat)
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
			r.publishEntry(ctx, entry, heartbeat)
		}()
	}
	wg.Wait()

	r.updatePendingGauge(ctx)
}

// publishEntry attempts to publish a single entry via the Publisher. The
// batch heartbeat keeps the claim alive; once the entry reaches a terminal
// outcome it is removed from the heartbeat set so a completed row is not
// resurrected by a later heartbeat tick.
func (r *Relay) publishEntry(ctx context.Context, entry Entry, heartbeat *batchHeartbeat) {
	defer heartbeat.done(entry.ID.String())

	publishCtx := ctx
	if r.publishTimeout > 0 {
		var cancel context.CancelFunc
		publishCtx, cancel = context.WithTimeout(ctx, r.publishTimeout)
		defer cancel()
	}

	start := time.Now()
	err := r.callPublisher(publishCtx, entry)
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
			// The row is no longer owned by this relay's claim, so it must
			// not be reset on shutdown — drop it from the claimed set.
			r.untrackClaimed(entry.ID.String())
			r.logger.Error("outbox relay: mark published lost row — likely concurrent stale recovery, possible duplicate publish",
				redact.Error(markErr))
			return
		}
		// Transient store error: the row likely stays in "processing" still
		// owned by us, so keep it tracked for shutdown reset.
		r.logger.Error("outbox relay: mark published failed",
			redact.Error(markErr))
		return
	}

	// Terminal success: the row is now "published" and no longer claimed.
	r.untrackClaimed(entry.ID.String())
	r.recordPublished()

	r.logger.Debug("outbox relay: published entry",
		"attempts", entry.Attempts,
	)
}

func (r *Relay) callPublisher(ctx context.Context, entry Entry) (err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("outbox relay: publisher panic: %s", redact.PanicValue(rec))
		}
	}()
	return r.publisher.Publish(ctx, entry)
}

// batchHeartbeat refreshes the updated_at timestamp of every
// claimed-but-unfinished row in a poll batch on a fixed cadence, so neither
// the in-flight publish nor the rows queued behind it get reset to pending by
// another relay's stale-recovery sweep. Callers mark each id done() as it
// reaches a terminal outcome and MUST defer stop().
type batchHeartbeat struct {
	mu      sync.Mutex
	pending map[string]struct{}

	stopCh chan struct{}
	doneCh chan struct{}
	once   sync.Once
}

// done removes id from the heartbeat set so a row that has already been marked
// published/failed (or had its attempts incremented) is not touched by a later
// heartbeat tick, which could otherwise resurrect a terminal row.
func (h *batchHeartbeat) done(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.pending, id)
}

// ids returns the currently-unfinished ids in the batch.
func (h *batchHeartbeat) ids() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.pending) == 0 {
		return nil
	}
	out := make([]string, 0, len(h.pending))
	for id := range h.pending {
		out = append(out, id)
	}
	return out
}

// stop halts the heartbeat goroutine and waits for it to exit. Idempotent.
func (h *batchHeartbeat) stop() {
	h.once.Do(func() {
		close(h.stopCh)
		<-h.doneCh
	})
}

// startBatchHeartbeat launches a goroutine that heartbeats every entry in the
// claimed batch on a fixed cadence until each id is marked done() or stop() is
// called.
//
// The heartbeat fires every staleDuration/3 (clamped at minHeartbeatInterval)
// — three heartbeats per stale window keeps the claim alive even if one
// heartbeat round-trip transiently fails.
func (r *Relay) startBatchHeartbeat(ctx context.Context, entries []Entry) *batchHeartbeat {
	pending := make(map[string]struct{}, len(entries))
	for i := range entries {
		pending[entries[i].ID.String()] = struct{}{}
	}
	h := &batchHeartbeat{
		pending: pending,
		stopCh:  make(chan struct{}),
		doneCh:  make(chan struct{}),
	}

	interval := r.staleDuration / 3
	if interval < minHeartbeatInterval {
		interval = minHeartbeatInterval
	}

	go func() {
		defer close(h.doneCh)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-h.stopCh:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				ids := h.ids()
				if len(ids) == 0 {
					continue
				}
				if _, err := r.store.Heartbeat(ctx, ids); err != nil {
					r.logger.Warn("outbox relay: heartbeat failed",
						redact.Error(err))
				}
			}
		}
	}()
	return h
}

// handlePublishError increments attempts or marks the entry as failed.
func (r *Relay) handlePublishError(ctx context.Context, entry Entry, publishErr error) {
	nextAttempt := entry.Attempts + 1
	errMsg := safePublishError(publishErr)

	if nextAttempt >= r.maxAttempts {
		if markErr := r.store.MarkFailed(ctx, entry.ID.String(), errMsg); markErr != nil {
			if errors.Is(markErr, ErrStaleState) || errors.Is(markErr, ErrNotFound) {
				// No longer owned by this claim — drop from shutdown reset set.
				r.untrackClaimed(entry.ID.String())
				r.logger.Error("outbox relay: mark failed lost row — likely concurrent stale recovery",
					redact.Error(markErr))
			} else {
				// Transient store error: row likely still "processing" and ours.
				r.logger.Error("outbox relay: mark failed error",
					redact.Error(markErr))
			}
		} else {
			// Terminal "failed": no longer claimed.
			r.untrackClaimed(entry.ID.String())
		}
		r.recordError()
		r.logger.Error("outbox relay: entry failed permanently",
			"attempts", nextAttempt,
			"error", errMsg,
			redact.ErrorChain(publishErr),
		)
		return
	}

	nextRetryAt := time.Now().UTC().Add(retryBackoff(nextAttempt))
	if incErr := r.store.IncrementAttempts(ctx, entry.ID.String(), errMsg, nextRetryAt); incErr != nil {
		if errors.Is(incErr, ErrStaleState) || errors.Is(incErr, ErrNotFound) {
			// No longer owned by this claim — drop from shutdown reset set.
			r.untrackClaimed(entry.ID.String())
			r.logger.Error("outbox relay: increment attempts lost row — likely concurrent stale recovery",
				redact.Error(incErr))
		} else {
			// Transient store error: row likely still "processing" and ours.
			r.logger.Error("outbox relay: increment attempts error",
				redact.Error(incErr))
		}
	} else {
		// Row returned to "pending" for a future retry; no longer claimed.
		r.untrackClaimed(entry.ID.String())
	}
	r.recordError()

	r.logger.Warn("outbox relay: publish failed, will retry",
		"attempt", nextAttempt,
		"max_attempts", r.maxAttempts,
		"error", errMsg,
		redact.ErrorChain(publishErr),
	)
}

// safePublishError renders a publish error into a form safe to persist as
// last_error and surface to operators. It keeps the concrete error type (so a
// timeout is distinguishable from an auth or routing failure for triage) while
// never including the raw Error() text, which from a broker/SDK/user callback
// may carry tenant-controlled keys, hostnames, or request fragments.
func safePublishError(err error) string {
	if err == nil {
		return ""
	}
	return redact.ErrorValue(err)
}

// recoverStale resets entries stuck in "processing" status from crashed relays.
func (r *Relay) recoverStale(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}

	reset, err := r.store.ResetStaleProcessing(ctx, r.staleDuration)
	if err != nil {
		r.logger.Error("outbox relay: recover stale failed", redact.Error(err))
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
		r.logger.Error("outbox relay: cleanup published failed", redact.Error(err))
	} else if deletedPublished > 0 {
		r.logger.Info("outbox relay: cleaned up published entries",
			"deleted", deletedPublished, "retention", r.retention)
	}

	failedCutoff := now.Add(-r.failedRetention)
	deletedFailed, err := r.store.DeleteFailedBefore(ctx, failedCutoff)
	if err != nil {
		r.logger.Error("outbox relay: cleanup failed entries failed", redact.Error(err))
	} else if deletedFailed > 0 {
		r.logger.Info("outbox relay: cleaned up failed entries",
			"deleted", deletedFailed, "retention", r.failedRetention)
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
			redact.Error(err))
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
