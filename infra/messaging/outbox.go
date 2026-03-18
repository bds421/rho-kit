package messaging

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/bds421/rho-kit/io/atomicfile"
)

const (
	defaultOutboxMaxSize         = 10_000
	outboxDrainInterval          = 5 * time.Second
	outboxDrainBatchLimit        = 100
	defaultOutboxFinalDrainTimeout = 15 * time.Second
)

// pendingMessage is a message waiting to be published.
type pendingMessage struct {
	Exchange   string  `json:"exchange"`
	RoutingKey string  `json:"routing_key"`
	Msg        Message `json:"msg"`
}

// OutboxPublisher is a buffered publisher that provides at-least-once delivery
// with FIFO ordering when the broker is temporarily unreachable. When the
// broker is reachable AND no buffered messages are pending, messages are
// published directly. Otherwise they are appended to an in-memory buffer
// and drained in order by the background loop. With [WithOutboxStateFile],
// the buffer is persisted to disk to survive process restarts.
//
// IMPORTANT: This is NOT a transactional outbox. It does not solve the
// dual-write problem — if your application writes to a database and then
// publishes a message, a crash between the two operations will cause
// inconsistency. For true transactional guarantees, write messages to a
// database outbox table within the same transaction and poll/CDC them to
// the broker.
type OutboxPublisher struct {
	logger  *slog.Logger
	mu      sync.Mutex
	pending []pendingMessage
	maxSize           int
	finalDrainTimeout time.Duration

	// directInFlight is true while a direct publish is in progress.
	// While set, concurrent Publish calls buffer instead of bypassing,
	// and the drain loop skips to avoid racing with the in-flight publish.
	directInFlight bool

	// stateFile is the path to the persistent outbox file. If empty,
	// the outbox operates in memory-only mode (not crash-safe).
	stateFile string

	// publishFn and healthyFn are the actual implementations.
	// In production these delegate to Publisher and Connection;
	// in tests they can be replaced with fakes.
	publishFn func(ctx context.Context, exchange, routingKey string, msg Message) error
	healthyFn func() bool

	// metrics collects operational metrics when set.
	metrics *OutboxMetrics
}

// OutboxMetrics collects operational metrics for the OutboxPublisher.
// All fields are optional — nil callbacks are skipped.
type OutboxMetrics struct {
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
}

// OutboxOption configures an OutboxPublisher.
type OutboxOption func(*OutboxPublisher)

// WithOutboxMaxSize sets the maximum number of buffered messages.
// When the buffer is full, Publish returns an error (back-pressure).
func WithOutboxMaxSize(n int) OutboxOption {
	return func(o *OutboxPublisher) { o.maxSize = n }
}

// WithOutboxStateFile enables persistent storage. Messages are written to
// this file atomically (write-temp + rename) so they survive process crashes.
func WithOutboxStateFile(path string) OutboxOption {
	return func(o *OutboxPublisher) { o.stateFile = path }
}

// WithOutboxMetrics sets the metrics callbacks for the outbox publisher.
func WithOutboxMetrics(m *OutboxMetrics) OutboxOption {
	return func(o *OutboxPublisher) { o.metrics = m }
}

// WithOutboxFinalDrainTimeout sets how long the outbox waits to drain
// remaining messages during shutdown. Default: 15 seconds.
func WithOutboxFinalDrainTimeout(d time.Duration) OutboxOption {
	return func(o *OutboxPublisher) {
		if d > 0 {
			o.finalDrainTimeout = d
		}
	}
}

// NewOutboxPublisher creates an OutboxPublisher that buffers messages when the
// broker is unreachable. Call Run() in a goroutine to drain the buffer.
//
// If a state file is configured via WithOutboxStateFile, pending messages from
// a previous run are loaded on creation.
func NewOutboxPublisher(inner MessagePublisher, conn Connector, logger *slog.Logger, opts ...OutboxOption) *OutboxPublisher {
	o := &OutboxPublisher{
		logger:            logger,
		maxSize:           defaultOutboxMaxSize,
		finalDrainTimeout: defaultOutboxFinalDrainTimeout,
		publishFn:         inner.Publish,
		healthyFn:         conn.Healthy,
	}
	for _, opt := range opts {
		opt(o)
	}

	if o.stateFile != "" {
		if err := o.load(); err != nil {
			logger.Error("failed to load outbox state, starting empty — potential data loss", "error", err, "file", o.stateFile)
		} else if len(o.pending) > 0 {
			logger.Info("restored pending outbox messages", "count", len(o.pending))
		}
	}

	return o
}

// Publish sends a message to RabbitMQ with FIFO ordering guarantees.
// Direct publish is only attempted when the buffer is empty, no other
// direct publish is in flight, AND the broker is healthy. Otherwise the
// message is appended to the buffer for the drain loop to publish in order.
// Returns an error only when the buffer is full (back-pressure).
func (o *OutboxPublisher) Publish(ctx context.Context, exchange, routingKey string, msg Message) error {
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
			return fmt.Errorf("outbox: buffer full (%d messages), message dropped", o.maxSize)
		}
		o.pending = append(o.pending, pendingMessage{
			Exchange:   exchange,
			RoutingKey: routingKey,
			Msg:        msg,
		})
		o.saveLocked()
		o.reportPending()
		pending := len(o.pending)
		o.mu.Unlock()

		if o.metrics != nil && o.metrics.OnBuffer != nil {
			o.metrics.OnBuffer()
		}
		o.logger.Info("message buffered in outbox",
			"exchange", exchange, "routing_key", routingKey,
			"msg_id", msg.ID, "pending", pending)
		return nil
	}

	// Buffer path: broker unhealthy, pending messages exist, or direct publish in flight.
	// Reserve one slot when a direct publish is in flight — if it fails,
	// the message will be appended to pending, so we must not be at capacity.
	// Skip check when maxSize <= 0 (unlimited buffer).
	if o.maxSize > 0 {
		effectiveMax := o.maxSize
		if o.directInFlight {
			effectiveMax--
		}
		if len(o.pending) >= effectiveMax {
			o.mu.Unlock()
			o.logger.Error("outbox buffer full, dropping message",
				"exchange", exchange, "routing_key", routingKey,
				"msg_id", msg.ID, "buffer_size", o.maxSize)
			if o.metrics != nil && o.metrics.OnDrop != nil {
				o.metrics.OnDrop()
			}
			return fmt.Errorf("outbox buffer full (%d messages)", o.maxSize)
		}
	}

	o.pending = append(o.pending, pendingMessage{
		Exchange:   exchange,
		RoutingKey: routingKey,
		Msg:        msg,
	})
	o.saveLocked()
	o.reportPending()
	pending := len(o.pending)
	o.mu.Unlock()

	if o.metrics != nil && o.metrics.OnBuffer != nil {
		o.metrics.OnBuffer()
	}

	o.logger.Info("message buffered in outbox",
		"exchange", exchange, "routing_key", routingKey,
		"msg_id", msg.ID, "pending", pending)
	return nil
}

// Pending returns the number of messages currently buffered.
func (o *OutboxPublisher) Pending() int {
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
func (o *OutboxPublisher) Run(ctx context.Context) {
	o.drain(ctx) // Drain immediately on startup to clear any restored messages.

	ticker := time.NewTicker(outboxDrainInterval)
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
func (o *OutboxPublisher) finalDrain() {
	o.mu.Lock()
	remaining := len(o.pending)
	o.mu.Unlock()

	if remaining == 0 {
		return
	}

	o.logger.Info("outbox final drain starting", "pending", remaining)

	ctx, cancel := context.WithTimeout(context.Background(), o.finalDrainTimeout)
	defer cancel()

	o.drain(ctx)

	o.mu.Lock()
	after := len(o.pending)
	o.mu.Unlock()

	if after > 0 {
		o.logger.Warn("outbox shutdown with unsent messages — persisted for next restart", "remaining", after)
	} else {
		o.logger.Info("outbox fully drained before shutdown")
	}
}

func (o *OutboxPublisher) drain(ctx context.Context) {
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
	batchSize := min(len(o.pending), outboxDrainBatchLimit)
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
			o.logger.Warn("outbox drain: broker unhealthy, pausing batch")
			break
		}
		if err := o.publishFn(ctx, pm.Exchange, pm.RoutingKey, pm.Msg); err != nil {
			o.logger.Warn("outbox drain publish failed, will retry",
				"error", err, "msg_id", pm.Msg.ID)
			break
		}
		published++
	}

	o.mu.Lock()
	o.directInFlight = false

	if published > 0 {
		// Compact the slice to allow the backing array to be GC'd.
		// Without this, o.pending[published:] retains the original array
		// forever since we only ever shrink from the front.
		remaining := len(o.pending) - published
		compacted := make([]pendingMessage, remaining)
		copy(compacted, o.pending[published:])
		o.pending = compacted
		o.saveLocked()
		o.reportPending()
	}
	o.mu.Unlock()

	if published > 0 {
		if o.metrics != nil && o.metrics.OnDrain != nil {
			o.metrics.OnDrain(published)
		}
		o.logger.Info("outbox drained",
			"published", published)
	}
}

// saveLocked persists the current pending slice to disk. Must be called
// with o.mu held. Errors are logged but not returned — persistence is
// best-effort to avoid blocking the publish path.
func (o *OutboxPublisher) saveLocked() {
	if o.stateFile == "" {
		return
	}

	if err := atomicfile.Save(o.stateFile, o.pending); err != nil {
		o.logger.Error("failed to save outbox state", "error", err)
	}
}

func (o *OutboxPublisher) reportPending() {
	if o.metrics != nil && o.metrics.OnPendingGauge != nil {
		o.metrics.OnPendingGauge(len(o.pending))
	}
}

// load reads pending messages from the state file on startup.
// Invalid entries (missing exchange or routing key) are skipped and logged
// rather than rejecting the entire file — this preserves valid messages
// when a single entry is corrupted.
func (o *OutboxPublisher) load() error {
	if o.stateFile == "" {
		return nil
	}

	pending, err := atomicfile.Load[[]pendingMessage](o.stateFile)
	if err != nil {
		return err
	}

	valid := make([]pendingMessage, 0, len(pending))
	for i, pm := range pending {
		if pm.Exchange == "" || pm.RoutingKey == "" {
			o.logger.Warn("outbox state: skipping invalid entry",
				"index", i, "msg_id", pm.Msg.ID)
			continue
		}
		valid = append(valid, pm)
	}

	o.pending = valid
	return nil
}
