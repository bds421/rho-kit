package messaging

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bds421/rho-kit/io/v2/atomicfile"
)

const (
	defaultBufferedMaxSize           = 10_000
	bufferedDrainInterval            = 5 * time.Second
	bufferedDrainBatchLimit          = 100
	defaultBufferedFinalDrainTimeout = 15 * time.Second
)

// pendingMessage is a message waiting to be published.
type pendingMessage struct {
	Exchange   string  `json:"exchange"`
	RoutingKey string  `json:"routing_key"`
	Msg        Message `json:"msg"`
}

// BufferedPublisher is a buffered publisher that provides at-least-once delivery
// with FIFO ordering when the broker is temporarily unreachable. When the
// broker is reachable AND no buffered messages are pending, messages are
// published directly. Otherwise they are appended to an in-memory buffer
// and drained in order by the background loop. With [WithBufferedStateFile],
// the buffer is persisted to disk to survive process restarts.
//
// IMPORTANT: This is NOT a transactional outbox. It does not solve the
// dual-write problem — if your application writes to a database and then
// publishes a message, a crash between the two operations will cause
// inconsistency. For true transactional guarantees, write messages to a
// database outbox table within the same transaction and poll/CDC them to
// the broker.
//
// Duplicate-on-restart risk (audit FR-068): the drain loop publishes a
// batch to the broker, then writes the new (smaller) pending list to
// disk. If the disk write fails (full disk, EROFS, quota, fsync error)
// AND the process crashes before the next successful save, the
// already-published messages are still on disk and will be replayed
// when the publisher restarts. Consumers MUST be idempotent on
// [Message.ID]. Wire [BufferedPublisherMetrics.OnSaveError] to a
// Prometheus counter / alert so a stuck disk surfaces before the crash
// — [LastSaveError] is the in-process probe for the same condition.
type BufferedPublisher struct {
	logger            *slog.Logger
	mu                sync.Mutex
	pending           []pendingMessage
	maxSize           int
	finalDrainTimeout time.Duration

	// directInFlight is true while a direct publish is in progress.
	// While set, concurrent Publish calls buffer instead of bypassing,
	// and the drain loop skips to avoid racing with the in-flight publish.
	directInFlight bool

	// stateFile is the path to the persistent state file. If empty,
	// the publisher operates in memory-only mode (not crash-safe).
	stateFile string

	// publishFn and healthyFn are the actual implementations.
	// In production these delegate to Publisher and Connection;
	// in tests they can be replaced with fakes.
	publishFn func(ctx context.Context, exchange, routingKey string, msg Message) error
	healthyFn func() bool

	// metrics collects operational metrics when set.
	metrics *BufferedPublisherMetrics

	// allowEphemeralBuffer opts out of the production-environment panic
	// when no state file is configured. Pattern matches csrf.WithDevSecret.
	allowEphemeralBuffer bool

	// lossyMode opts in to "Publish returns nil even when persistence
	// failed". By default, when a state file is configured, persistence
	// failure is reported as a Publish error so callers can react before
	// a process crash drops the message.
	lossyMode bool

	// lossyStateRecovery opts in to "start with an empty buffer when the
	// configured state file fails to load". By default, a load failure
	// is fatal at construction so a corrupt or unreadable state file does
	// not silently drop the messages buffering exists to preserve.
	lossyStateRecovery bool

	// lastSaveErr holds the most recent error from saveLocked(). Stored as
	// atomic.Pointer so [LastSaveError] reads it without contending with
	// the publish path.
	lastSaveErr atomic.Pointer[error]
}

// BufferedPublisherMetrics collects operational metrics for the BufferedPublisher.
// All fields are optional — nil callbacks are skipped.
type BufferedPublisherMetrics struct {
	// OnDirectPublish is called after a successful direct publish.
	OnDirectPublish func()
	// OnBuffer is called when a message is added to the buffer.
	OnBuffer func()
	// OnDrain is called after messages are drained from the buffer.
	OnDrain func(count int)
	// OnDrop is called when a message is dropped due to a full buffer.
	OnDrop func()
	// OnPendingGauge is called with the current buffer depth after changes.
	OnPendingGauge func(count int)
	// OnSaveError fires when a state-file save fails. Audit FR-068:
	// drain previously discarded the save-error from saveLocked, so a
	// disk-full / EROFS / quota condition could go unnoticed and cause
	// duplicate publishes after a crash+restart (the on-disk pending
	// list still carries messages that have already been delivered to
	// the broker). Wire this hook to a Prometheus counter / alert so
	// such conditions surface before the next crash.
	OnSaveError func(err error)
}

// BufferedPublisherOption configures a BufferedPublisher.
type BufferedPublisherOption func(*BufferedPublisher)

// WithBufferedMaxSize sets the maximum number of buffered messages.
// When the buffer is full, Publish returns an error (back-pressure).
//
// FR-069 [LOW]: panics on n <= 0 — pre-fix any non-positive value
// silently disabled the cap and allowed unbounded memory growth
// during broker outages. Use [WithUnlimitedBufferedBuffer] when an
// unbounded buffer is genuinely intended.
func WithBufferedMaxSize(n int) BufferedPublisherOption {
	if n <= 0 {
		panic(fmt.Sprintf("messaging: WithBufferedMaxSize requires n > 0 (got %d); use WithUnlimitedBufferedBuffer to opt out", n))
	}
	return func(o *BufferedPublisher) { o.maxSize = n }
}

// WithUnlimitedBufferedBuffer opts out of the per-buffer cap. Use
// only when an external mechanism (disk persistence, downstream rate
// limit) bounds memory growth — otherwise a long broker outage will
// OOM the service.
func WithUnlimitedBufferedBuffer() BufferedPublisherOption {
	return func(o *BufferedPublisher) { o.maxSize = -1 }
}

// WithBufferedStateFile enables persistent storage. Messages are written to
// this file atomically (write-temp + rename) so they survive process crashes.
func WithBufferedStateFile(path string) BufferedPublisherOption {
	return func(o *BufferedPublisher) { o.stateFile = path }
}

// WithBufferedMetrics sets the metrics callbacks for the buffered publisher.
func WithBufferedMetrics(m *BufferedPublisherMetrics) BufferedPublisherOption {
	return func(o *BufferedPublisher) { o.metrics = m }
}

// WithLossyMode opts in to the legacy behavior where Publish returns nil
// even when persistence to the configured state file fails. The default
// behavior is to surface the persistence error so callers can react
// before a process crash drops the buffered message. This option only
// affects publishers configured with [WithBufferedStateFile]; ephemeral
// buffers do not persist regardless.
func WithLossyMode() BufferedPublisherOption {
	return func(o *BufferedPublisher) { o.lossyMode = true }
}

// WithLossyStateRecovery opts in to "start with an empty buffer when the
// configured state file fails to load". The default fails startup so a
// corrupt or unreadable state file does not silently drop the messages
// buffering exists to preserve. Use only when the surrounding system has
// its own at-least-once guarantee (e.g. an upstream outbox), or for tests.
func WithLossyStateRecovery() BufferedPublisherOption {
	return func(o *BufferedPublisher) { o.lossyStateRecovery = true }
}

// WithEphemeralBuffer opts in to memory-only buffering. By default,
// [NewBufferedPublisher] panics when no state file is configured — a
// process restart would silently drop every buffered message, which is
// exactly the scenario buffering exists to prevent. Set this option only
// when the surrounding system has its own at-least-once guarantee
// (e.g. an upstream outbox), or for tests. The check is unconditional
// — there is no KIT_ENV escape hatch.
func WithEphemeralBuffer() BufferedPublisherOption {
	return func(o *BufferedPublisher) { o.allowEphemeralBuffer = true }
}

// WithBufferedFinalDrainTimeout sets how long the buffered publisher waits to
// drain remaining messages during shutdown. Default: 15 seconds.
func WithBufferedFinalDrainTimeout(d time.Duration) BufferedPublisherOption {
	return func(o *BufferedPublisher) {
		if d > 0 {
			o.finalDrainTimeout = d
		}
	}
}

// NewBufferedPublisher creates a BufferedPublisher that buffers messages when the
// broker is unreachable. Call Run() in a goroutine to drain the buffer.
//
// If a state file is configured via WithBufferedStateFile, pending messages from
// a previous run are loaded on creation.
//
// Panics if inner or conn is nil — both are dereferenced immediately to wire
// up publishFn / healthyFn closures, so passing nil here is a programming
// error. Logger nil is accepted and defaults to slog.Default().
func NewBufferedPublisher(inner MessagePublisher, conn Connector, logger *slog.Logger, opts ...BufferedPublisherOption) *BufferedPublisher {
	if inner == nil {
		panic("messaging: NewBufferedPublisher requires a non-nil MessagePublisher")
	}
	if conn == nil {
		panic("messaging: NewBufferedPublisher requires a non-nil Connector")
	}
	if logger == nil {
		logger = slog.Default()
	}
	o := &BufferedPublisher{
		logger:            logger,
		maxSize:           defaultBufferedMaxSize,
		finalDrainTimeout: defaultBufferedFinalDrainTimeout,
		publishFn:         inner.Publish,
		healthyFn:         conn.Healthy,
	}
	for _, opt := range opts {
		opt(o)
	}

	if o.stateFile == "" && !o.allowEphemeralBuffer {
		panic("messaging: BufferedPublisher requires WithBufferedStateFile — without persistence, buffered messages are silently lost on restart (call WithEphemeralBuffer() to opt out explicitly when an upstream outbox provides durability)")
	}

	if o.stateFile != "" {
		if err := o.load(); err != nil {
			if !o.lossyStateRecovery {
				panic(fmt.Sprintf("messaging: BufferedPublisher state load failed for %q: %v — corrupt or unreadable state would silently drop buffered messages; pass WithLossyStateRecovery() to opt in", o.stateFile, err))
			}
			logger.Error("failed to load buffered publisher state, starting empty — potential data loss", "error", err, "file", o.stateFile)
		} else if len(o.pending) > 0 {
			logger.Info("restored pending buffered publisher messages", "count", len(o.pending))
		}
	}

	return o
}

// Publish sends a message to RabbitMQ with FIFO ordering guarantees.
// Direct publish is only attempted when the buffer is empty, no other
// direct publish is in flight, AND the broker is healthy. Otherwise the
// message is appended to the buffer for the drain loop to publish in order.
// Returns an error only when the buffer is full (back-pressure).
func (o *BufferedPublisher) Publish(ctx context.Context, exchange, routingKey string, msg Message) error {
	o.mu.Lock()

	// Check health inside the lock to prevent FIFO violations. If health
	// were checked outside, a concurrent goroutine could buffer a message
	// between the health check and lock acquisition, causing the direct
	// publish to skip ahead of the buffered message.
	healthy := o.healthyFn()

	// Only attempt direct publish when (1) buffer is empty, (2) no other
	// goroutine is doing a direct publish, and (3) broker is healthy.
	// This ensures strict FIFO: we never skip ahead of buffered messages
	// or race with another concurrent direct publish.
	if len(o.pending) == 0 && !o.directInFlight && healthy {
		o.directInFlight = true
		o.mu.Unlock()

		// Guarantee directInFlight is cleared even if publishFn panics.
		// Without this, a panic permanently freezes the drain loop.
		published := false
		defer func() {
			if !published {
				o.mu.Lock()
				o.directInFlight = false
				o.mu.Unlock()
			}
		}()

		if err := o.publishFn(ctx, exchange, routingKey, msg); err == nil {
			o.mu.Lock()
			o.directInFlight = false
			published = true
			o.mu.Unlock()
			if o.metrics != nil && o.metrics.OnDirectPublish != nil {
				o.metrics.OnDirectPublish()
			}
			return nil
		}

		// Direct publish failed — buffer the message.
		o.logger.Warn("direct publish failed, buffering message",
			"exchange", exchange, "routing_key", routingKey, "msg_id", msg.ID)

		o.mu.Lock()
		o.directInFlight = false
		published = true // defer cleanup no longer needed
		// Re-check capacity after re-acquiring lock — the buffer may have
		// been filled by concurrent Publish calls or the drain loop.
		if o.maxSize > 0 && len(o.pending) >= o.maxSize {
			o.mu.Unlock()
			if o.metrics != nil && o.metrics.OnDrop != nil {
				o.metrics.OnDrop()
			}
			return fmt.Errorf("buffered publisher: buffer full (%d messages), message dropped", o.maxSize)
		}
		o.pending = append(o.pending, pendingMessage{
			Exchange:   exchange,
			RoutingKey: routingKey,
			Msg:        msg,
		})
		saveErr := o.saveLocked()
		if saveErr != nil && !o.lossyMode {
			// Roll back the buffered append so memory state matches what
			// was successfully persisted. Without this, a later successful
			// save would persist a message the caller was told failed.
			o.pending = o.pending[:len(o.pending)-1]
			o.mu.Unlock()
			return fmt.Errorf("buffered publisher: persist message after direct publish failure: %w", saveErr)
		}
		o.reportPending()
		pending := len(o.pending)
		o.mu.Unlock()

		if o.metrics != nil && o.metrics.OnBuffer != nil {
			o.metrics.OnBuffer()
		}
		o.logger.Info("message buffered",
			"exchange", exchange, "routing_key", routingKey,
			"msg_id", msg.ID, "pending", pending)
		return nil
	}

	// Buffer path: broker unhealthy, pending messages exist, or direct publish in flight.
	// Reserve one slot when a direct publish is in flight — if it fails,
	// the message will be appended to pending, so we must not be at capacity.
	// Skip check when maxSize <= 0 (unlimited buffer).
	//
	// Edge case: maxSize=1 + directInFlight=true → effectiveMax=0 would
	// reject any second message during normal operation, even when the
	// in-flight publish succeeds and clears the slot moments later. Skip
	// the reservation in that pathological config — the worst case is
	// a single buffered message exceeding the cap by 1, which is
	// strictly better than rejecting valid traffic.
	if o.maxSize > 0 {
		effectiveMax := o.maxSize
		if o.directInFlight && o.maxSize > 1 {
			effectiveMax--
		}
		if len(o.pending) >= effectiveMax {
			o.mu.Unlock()
			o.logger.Error("buffer full, dropping message",
				"exchange", exchange, "routing_key", routingKey,
				"msg_id", msg.ID, "buffer_size", o.maxSize)
			if o.metrics != nil && o.metrics.OnDrop != nil {
				o.metrics.OnDrop()
			}
			return fmt.Errorf("buffered publisher: buffer full (%d messages), message dropped", o.maxSize)
		}
	}

	o.pending = append(o.pending, pendingMessage{
		Exchange:   exchange,
		RoutingKey: routingKey,
		Msg:        msg,
	})
	saveErr := o.saveLocked()
	if saveErr != nil && !o.lossyMode {
		o.pending = o.pending[:len(o.pending)-1]
		o.mu.Unlock()
		return fmt.Errorf("buffered publisher: persist buffered message: %w", saveErr)
	}
	o.reportPending()
	pending := len(o.pending)
	o.mu.Unlock()

	if o.metrics != nil && o.metrics.OnBuffer != nil {
		o.metrics.OnBuffer()
	}

	o.logger.Info("message buffered",
		"exchange", exchange, "routing_key", routingKey,
		"msg_id", msg.ID, "pending", pending)
	return nil
}

// Pending returns the number of messages currently buffered.
func (o *BufferedPublisher) Pending() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.pending)
}

// Run starts the background drain loop. It periodically checks for pending
// messages and publishes them when the broker is healthy. Blocks until ctx
// is cancelled.
//
// On shutdown (ctx cancelled), a final best-effort drain is attempted using a
// short-lived context so in-flight messages are not lost.
func (o *BufferedPublisher) Run(ctx context.Context) {
	o.drain(ctx) // Drain immediately on startup to clear any restored messages.

	ticker := time.NewTicker(bufferedDrainInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			o.finalDrain()
			return
		case <-ticker.C:
			o.drain(ctx)
		}
	}
}

// finalDrain attempts one last drain with a short timeout so pending messages
// are not silently discarded on shutdown.
func (o *BufferedPublisher) finalDrain() {
	o.mu.Lock()
	remaining := len(o.pending)
	o.mu.Unlock()

	if remaining == 0 {
		return
	}

	o.logger.Info("buffered publisher final drain starting", "pending", remaining)

	ctx, cancel := context.WithTimeout(context.Background(), o.finalDrainTimeout)
	defer cancel()

	o.drain(ctx)

	o.mu.Lock()
	after := len(o.pending)
	o.mu.Unlock()

	if after > 0 {
		o.logger.Warn("buffered publisher shutdown with unsent messages — persisted for next restart", "remaining", after)
	} else {
		o.logger.Info("buffered publisher fully drained before shutdown")
	}
}

func (o *BufferedPublisher) drain(ctx context.Context) {
	if !o.healthyFn() {
		return
	}

	o.mu.Lock()
	if len(o.pending) == 0 || o.directInFlight {
		o.mu.Unlock()
		return
	}

	// Set directInFlight so concurrent Publish calls buffer instead of
	// bypassing — prevents FIFO violations during drain.
	o.directInFlight = true

	// Take a batch to drain while holding the lock briefly.
	batchSize := min(len(o.pending), bufferedDrainBatchLimit)
	batch := make([]pendingMessage, batchSize)
	copy(batch, o.pending[:batchSize])
	o.mu.Unlock()

	published := 0
	for _, pm := range batch {
		if ctx.Err() != nil {
			break
		}
		// Re-check broker health before each publish to avoid sequential
		// timeout waits when the broker goes down mid-batch.
		if !o.healthyFn() {
			o.logger.Warn("buffered publisher drain: broker unhealthy, pausing batch")
			break
		}
		if err := o.publishFn(ctx, pm.Exchange, pm.RoutingKey, pm.Msg); err != nil {
			o.logger.Warn("buffered publisher drain publish failed, will retry",
				"error", err, "msg_id", pm.Msg.ID)
			break
		}
		published++
	}

	o.mu.Lock()
	o.directInFlight = false

	var saveErr error
	if published > 0 {
		// Compact the slice to allow the backing array to be GC'd.
		// Without this, o.pending[published:] retains the original array
		// forever since we only ever shrink from the front.
		remaining := len(o.pending) - published
		compacted := make([]pendingMessage, remaining)
		copy(compacted, o.pending[published:])
		o.pending = compacted
		// FR-068 [HIGH]: surface the save error instead of swallowing
		// it. On disk-full / EROFS / quota the on-disk pending list
		// still contains the messages we just delivered to the broker,
		// so a crash before the next successful save would replay them.
		// LastSaveError() is the kit-internal probe; OnSaveError is the
		// metrics-side hook that pages someone before the next crash.
		saveErr = o.saveLocked()
		o.reportPending()
	}
	o.mu.Unlock()

	if saveErr != nil {
		o.logger.Error("buffered publisher state save failed AFTER successful broker publishes; restart-replay risk until next save succeeds",
			"error", saveErr, "published", published)
		if o.metrics != nil && o.metrics.OnSaveError != nil {
			o.metrics.OnSaveError(saveErr)
		}
	}

	if published > 0 {
		if o.metrics != nil && o.metrics.OnDrain != nil {
			o.metrics.OnDrain(published)
		}
		o.logger.Info("buffered publisher drained",
			"published", published)
	}
}

// saveLocked persists the current pending slice to disk. Must be called
// with o.mu held. Returns the underlying atomicfile error (or nil) so
// callers in the Publish path can fail-closed when persistence fails,
// rather than silently acknowledging a message that exists only in
// memory.
//
// State file permissions are 0600 (atomicfile.Save default). The file
// contains full message payloads as JSON — if the buffered publisher
// handles PII or secrets, restrict the parent directory to the service
// user and consider wrapping the payloads with crypto/encrypt before
// they reach Publish.
//
// Persistence errors are logged AND surfaced via [LastSaveError] so a
// monitoring loop can detect stuck disk-full / EROFS / quota conditions
// rather than discovering them only on shutdown drain.
func (o *BufferedPublisher) saveLocked() error {
	if o.stateFile == "" {
		return nil
	}

	if err := atomicfile.Save(o.stateFile, o.pending); err != nil {
		o.logger.Error("failed to save buffered publisher state", "error", err)
		o.lastSaveErr.Store(&err)
		return err
	}
	o.lastSaveErr.Store(nil)
	return nil
}

// LastSaveError returns the most recent state-file save error, or nil if
// the last save succeeded (or no save has been attempted). Useful for
// health checks and alerting; the buffered publisher does not block the
// publish path on persistence failures, so an external watcher is the
// only way to notice a stuck disk.
func (o *BufferedPublisher) LastSaveError() error {
	if e := o.lastSaveErr.Load(); e != nil {
		return *e
	}
	return nil
}

func (o *BufferedPublisher) reportPending() {
	if o.metrics != nil && o.metrics.OnPendingGauge != nil {
		o.metrics.OnPendingGauge(len(o.pending))
	}
}

// load reads pending messages from the state file on startup.
// Invalid entries (missing exchange or routing key) are skipped and logged
// rather than rejecting the entire file — this preserves valid messages
// when a single entry is corrupted.
func (o *BufferedPublisher) load() error {
	if o.stateFile == "" {
		return nil
	}

	pending, err := atomicfile.LoadOrZero[[]pendingMessage](o.stateFile)
	if err != nil {
		return err
	}

	valid := make([]pendingMessage, 0, len(pending))
	for i, pm := range pending {
		if pm.Exchange == "" || pm.RoutingKey == "" {
			o.logger.Warn("buffered publisher state: skipping invalid entry",
				"index", i, "msg_id", pm.Msg.ID)
			continue
		}
		valid = append(valid, pm)
	}

	o.pending = valid
	return nil
}
