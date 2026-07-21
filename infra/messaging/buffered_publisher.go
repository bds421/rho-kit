package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bds421/rho-kit/core/v2/redact"
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

// pendingMessageJSON is the on-disk representation of a pendingMessage.
// It exists so transport headers survive the state-file round-trip:
// [Message.Headers] is tagged json:"-" (carried as transport metadata,
// not body), so a plain json.Marshal of the embedded Message drops
// correlation/request/tenant headers entirely. Without this, the
// documented crash-recovery replay would silently strip those headers
// from every restored message. Headers are persisted alongside the
// message body and re-attached on load.
type pendingMessageJSON struct {
	Exchange   string            `json:"exchange"`
	RoutingKey string            `json:"routing_key"`
	Msg        Message           `json:"msg"`
	Headers    map[string]string `json:"headers,omitempty"`
}

// MarshalJSON persists the embedded message together with its transport
// headers so they survive a save/load cycle (see [pendingMessageJSON]).
func (p pendingMessage) MarshalJSON() ([]byte, error) {
	return json.Marshal(pendingMessageJSON{
		Exchange:   p.Exchange,
		RoutingKey: p.RoutingKey,
		Msg:        p.Msg,
		Headers:    p.Msg.Headers,
	})
}

// UnmarshalJSON restores a pendingMessage, re-attaching the separately
// persisted transport headers to the embedded Message. Legacy state
// files written before headers were persisted simply restore with nil
// Headers, matching the previous behaviour.
func (p *pendingMessage) UnmarshalJSON(data []byte) error {
	var raw pendingMessageJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	raw.Msg.Headers = raw.Headers
	p.Exchange = raw.Exchange
	p.RoutingKey = raw.RoutingKey
	p.Msg = raw.Msg
	return nil
}

// BufferedPublisher is a buffered publisher that provides at-least-once delivery
// with FIFO ordering when the broker is temporarily unreachable. When the
// broker is reachable AND no buffered messages are pending, messages are
// published directly. Otherwise they are appended to an in-memory buffer
// and drained in order by the background loop. With [WithStateDirectory]
// plus [WithStateFile], the buffer is persisted to disk to survive process
// restarts; state files are constrained to the configured directory so a
// hostile or buggy STATE_FILE env cannot escape it (THREAT_MODEL §4.3 M-05).
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
//
// Concurrency: Publish is safe for concurrent use — the in-flight /
// pending state is mu-guarded and direct publishes serialise on
// directInFlight. Run must be invoked from a single goroutine; the
// runMu + started flag guard against accidental double-Run.
type BufferedPublisher struct {
	logger            *slog.Logger
	mu                sync.Mutex
	runMu             sync.Mutex
	pending           []pendingMessage
	pendingBytes      int // running sum of Payload lengths; O(1) gauge
	maxSize           int
	finalDrainTimeout time.Duration
	started           bool

	// directInFlight is true while a direct publish is in progress.
	// While set, concurrent Publish calls buffer instead of bypassing,
	// and the drain loop skips to avoid racing with the in-flight publish.
	directInFlight bool

	// stateFile is the absolute path to the persistent state file
	// after path containment is enforced at construction time. If
	// empty, the publisher operates in memory-only mode (not
	// crash-safe). Service code never sets this directly — it is
	// derived from stateDir + the relative path passed to
	// [WithStateFile] so a hostile or buggy STATE_FILE env cannot
	// escape the configured directory.
	stateFile string

	// stateDir is the directory inside which the state file must
	// live. Set by [WithStateDirectory]. Empty means no state-file
	// persistence (the constructor rejects [WithStateFile] without
	// it). Stored as the cleaned absolute path so containment
	// checks compare like-for-like.
	stateDir string

	// stateFileRel is the caller-supplied relative path passed to
	// [WithStateFile]. The constructor combines it with stateDir to
	// produce stateFile. Stored separately so the constructor can
	// validate after every option is applied without depending on
	// option order.
	stateFileRel string

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

	// lossyStateValidation opts in to "skip individual invalid entries
	// when validating loaded state". By default, any entry that fails
	// per-message validation is fatal at construction so corrupt
	// entries never silently disappear. The strict default matches
	// the kit's broader "loud, not silent" posture; operators
	// recovering from a known-bad state file can flip this to log+
	// skip via [WithLossyStateValidation].
	lossyStateValidation bool

	sizeLimiter MessageSizeLimiter

	// lastSaveErr holds the most recent error from saveLocked(). Stored as
	// atomic.Pointer so [LastSaveError] reads it without contending with
	// the publish path.
	lastSaveErr atomic.Pointer[error]

	// Journal bookkeeping (all mu-guarded). The Publish hot path appends a
	// single entry to the on-disk journal instead of rewriting the whole
	// snapshot, turning an O(n) write per buffered Publish into O(1). The
	// file is compacted back to a single snapshot line on drain, when the
	// buffer empties, or once the appended tail grows past a threshold.
	//
	// journalReady is true once we have written a snapshot we may append to.
	// It starts false so the first persist after construction/load rewrites
	// a clean snapshot (also migrating any legacy single-array file into the
	// journal format before we ever append a line to it).
	journalReady bool
	// snapshotBaseLen is the number of entries captured in the last snapshot
	// line. Appends are compacted once the tail reaches this size so each
	// snapshot at least doubles the work since the previous one, bounding
	// total bytes written across a burst to O(n).
	snapshotBaseLen int
	// journalEntries counts entries appended since the last snapshot.
	journalEntries int
	// journalBytes approximates the bytes appended since the last snapshot,
	// used to force compaction before the tail bloats the replay file.
	journalBytes int
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
	// OnBufferedBytesGauge is called with the approximate in-memory bytes
	// pending after each buffer mutation. The figure sums Message.Payload
	// lengths and is used by [PrometheusBufferedPublisherMetrics] to expose a
	// silent-overflow gauge — operators can alert before backpressure forces
	// a drop.
	OnBufferedBytesGauge func(bytes int)
	// OnSaveError fires when a state-file save fails. Audit FR-068:
	// drain previously discarded the save-error from saveLocked, so a
	// disk-full / EROFS / quota condition could go unnoticed and cause
	// duplicate publishes after a crash+restart (the on-disk pending
	// list still carries messages that have already been delivered to
	// the broker). Wire this hook to a Prometheus counter / alert so
	// such conditions surface before the next crash.
	OnSaveError func(err error)
	// OnStateWrite fires after every state-file save attempt. The boolean
	// reports whether the underlying atomic write succeeded so a single
	// counter can track {success, error} outcomes side-by-side.
	OnStateWrite func(success bool)
}

// BufferedPublisherOption configures a BufferedPublisher.
type BufferedPublisherOption func(*BufferedPublisher)

// WithMaxSize sets the maximum number of buffered messages. When the
// buffer is full, Publish returns an error (back-pressure).
//
// Panics on n <= 0 — pre-fix any non-positive value silently disabled
// the cap and allowed unbounded memory growth during broker outages.
// Use [WithUnlimitedBuffer] when an unbounded buffer is genuinely intended.
func WithMaxSize(n int) BufferedPublisherOption {
	if n <= 0 {
		panic("messaging: WithMaxSize requires n > 0; use WithUnlimitedBuffer to opt out")
	}
	return func(o *BufferedPublisher) { o.maxSize = n }
}

// WithUnlimitedBuffer opts out of the per-buffer cap. Use only when an
// external mechanism (disk persistence, downstream rate limit) bounds
// memory growth — otherwise a long broker outage will OOM the service.
func WithUnlimitedBuffer() BufferedPublisherOption {
	return func(o *BufferedPublisher) { o.maxSize = -1 }
}

// WithStateDirectory sets the directory inside which the buffered
// publisher's state file must live. The directory is the only
// containment boundary for paths passed to [WithStateFile] — a
// caller-supplied relative path that resolves outside this directory
// causes [NewBufferedPublisher] to panic.
//
// The path is cleaned at option time so callers see the same
// containment regardless of trailing slashes or `./` components, but
// must be an absolute path: relative state directories pick up the
// process's working directory at construction time, which is rarely
// the operator's intent and complicates the symlink-traversal review
// in THREAT_MODEL §4.3 M-05.
//
// Panics when dir is empty or relative. Use [WithEphemeralBuffer] for
// memory-only operation; do not pass an empty string here.
func WithStateDirectory(dir string) BufferedPublisherOption {
	if dir == "" {
		panic("messaging: WithStateDirectory requires a non-empty directory")
	}
	cleaned := filepath.Clean(dir)
	if !filepath.IsAbs(cleaned) {
		panic("messaging: WithStateDirectory requires an absolute path")
	}
	return func(o *BufferedPublisher) { o.stateDir = cleaned }
}

// WithStateFile names the state file inside the directory configured
// by [WithStateDirectory]. Messages are written to this file
// atomically (write-temp + rename) so they survive process crashes.
//
// Path containment (THREAT_MODEL §4.3 M-05): the argument MUST be a
// relative path that resolves inside the configured state directory
// after [filepath.Clean]. Absolute paths and paths whose cleaned form
// escapes the directory via `..` segments are rejected at
// construction time with a panic. Calling [WithStateFile] without a
// prior [WithStateDirectory] also panics — the option pair must be
// used together so a hostile or buggy STATE_FILE env value cannot
// write outside the operator-chosen directory.
//
// The relative path may include nested components (e.g.
// `"shard-1/state.json"`), in which case the parent directories must
// already exist or be creatable by the calling process; the kit does
// not auto-create the tree to keep filesystem-side effects out of
// constructor code.
func WithStateFile(path string) BufferedPublisherOption {
	if path == "" {
		panic("messaging: WithStateFile requires a non-empty path")
	}
	return func(o *BufferedPublisher) { o.stateFileRel = path }
}

// WithMetrics sets the metrics callbacks for the buffered publisher.
func WithMetrics(m *BufferedPublisherMetrics) BufferedPublisherOption {
	return func(o *BufferedPublisher) { o.metrics = m }
}

// WithLossyMode opts in to the legacy behavior where Publish returns nil
// even when persistence to the configured state file fails. The default
// behavior is to surface the persistence error so callers can react
// before a process crash drops the buffered message. This option only
// affects publishers configured with [WithStateFile]; ephemeral
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

// WithLossyStateValidation opts in to skipping individual entries that
// fail per-message validation when loading persisted state. By default,
// any invalid entry is fatal at construction so corrupt entries cannot
// silently disappear — wave 66 closed a hostile-review finding that
// load() silently dropped invalid entries with only a Warn log. Use
// this option only when recovering from a known-bad state file or in
// tests; production wiring should fail loudly and force a deliberate
// recovery decision.
func WithLossyStateValidation() BufferedPublisherOption {
	return func(o *BufferedPublisher) { o.lossyStateValidation = true }
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

// WithFinalDrainTimeout sets how long the buffered publisher waits to
// drain remaining messages during shutdown. Default: 15 seconds.
func WithFinalDrainTimeout(d time.Duration) BufferedPublisherOption {
	if d <= 0 {
		panic("messaging: WithFinalDrainTimeout requires a positive duration")
	}
	return func(o *BufferedPublisher) {
		o.finalDrainTimeout = d
	}
}

// WithMessageSizeLimiter replaces the buffered publisher's message-size
// policy. The check runs before direct publishing or buffering, so an
// over-large message is never persisted into the retry buffer.
func WithMessageSizeLimiter(l MessageSizeLimiter) BufferedPublisherOption {
	return func(o *BufferedPublisher) { o.sizeLimiter = l }
}

// WithMaxMessageBytes sets the default serialized message-size limit.
func WithMaxMessageBytes(maxBytes int) BufferedPublisherOption {
	return func(o *BufferedPublisher) {
		o.sizeLimiter = o.sizeLimiter.WithDefaultMaxBytes(maxBytes)
	}
}

// WithoutMaxMessageBytes disables the default size limit. Route-specific
// overrides — supplied via [WithMessageSizeLimiter] using a
// [MessageSizeLimiter] built with [NewMessageSizeLimiter](default,
// overrides...) — still apply.
func WithoutMaxMessageBytes() BufferedPublisherOption {
	return func(o *BufferedPublisher) {
		o.sizeLimiter = o.sizeLimiter.WithoutDefaultMaxBytes()
	}
}

// NewBufferedPublisher creates a BufferedPublisher that buffers
// messages when the broker is unreachable. Note that the constructor
// performs file I/O when [WithStateFile] is configured: it reads the
// previous run's pending messages from disk before returning. Call
// Run() in a goroutine to drain the buffer.
//
// Panics if inner or conn is nil — both are dereferenced immediately to wire
// up publishFn / healthyFn closures, so passing nil here is a programming
// error. Logger nil is accepted and defaults to slog.Default().
func NewBufferedPublisher(inner Publisher, conn Connector, logger *slog.Logger, opts ...BufferedPublisherOption) *BufferedPublisher {
	if inner == nil {
		panic("messaging: NewBufferedPublisher requires a non-nil Publisher")
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
		sizeLimiter:       DefaultMessageSizeLimiter(),
		publishFn:         inner.Publish,
		healthyFn:         conn.Healthy,
	}
	for _, opt := range opts {
		if opt == nil {
			panic("messaging: BufferedPublisher option must not be nil")
		}
		opt(o)
	}

	// Resolve the state file inside the configured directory. The
	// check runs after every option has been applied so the
	// containment guarantee does not depend on whether the caller
	// passed WithStateDirectory before or after WithStateFile.
	if o.stateFileRel != "" {
		if o.stateDir == "" {
			panic("messaging: WithStateFile requires WithStateDirectory — without a configured directory, a hostile or buggy file path can write outside the intended location")
		}
		resolved, err := resolveStateFilePath(o.stateDir, o.stateFileRel)
		if err != nil {
			panic("messaging: WithStateFile rejected: " + err.Error())
		}
		o.stateFile = resolved
	}

	if o.stateFile == "" && !o.allowEphemeralBuffer {
		panic("messaging: BufferedPublisher requires WithStateFile — without persistence, buffered messages are silently lost on restart (call WithEphemeralBuffer() to opt out explicitly when an upstream outbox provides durability)")
	}

	if o.stateFile != "" {
		if err := o.load(); err != nil {
			if !o.lossyStateRecovery {
				panic("messaging: BufferedPublisher state load failed — corrupt or unreadable state would silently drop buffered messages; pass WithLossyStateRecovery() to opt in")
			}
			logger.Error("failed to load buffered publisher state, starting empty — potential data loss",
				redact.Error(err), redact.String("file", o.stateFile))
		} else if len(o.pending) > 0 {
			logger.Info("restored pending buffered publisher messages", "count", len(o.pending))
		}
	}

	return o
}

// Publish sends a message with FIFO ordering guarantees.
// Direct publish is only attempted when the buffer is empty, no other
// direct publish is in flight, AND the broker is healthy. Otherwise the
// message is appended to the buffer for the drain loop to publish in order.
//
// Returns an error when: the context/route/message fails validation, the
// size limiter rejects the payload, the buffer is full (back-pressure /
// [ErrBufferFull]), or (in rare paths) the underlying publisher fails in a
// way that cannot be buffered. A successful return means the message was
// either published or accepted into the buffer — not necessarily that the
// broker has acknowledged it yet.
func (o *BufferedPublisher) Publish(ctx context.Context, exchange, routingKey string, msg Message) error {
	if err := ValidatePublishContext(ctx); err != nil {
		return err
	}
	if err := ValidatePublishRoute(exchange, routingKey); err != nil {
		return err
	}
	msg = msg.Clone()
	if err := ValidateMessage(msg); err != nil {
		return err
	}
	if err := o.sizeLimiter.Check(exchange, routingKey, msg); err != nil {
		return err
	}
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

		if err := o.publishFn(ctx, exchange, routingKey, msg.Clone()); err == nil {
			o.mu.Lock()
			o.directInFlight = false
			published = true
			o.mu.Unlock()
			o.onDirectPublish()
			return nil
		} else if ctx.Err() != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
			// Caller's own cancellation/deadline must not convert into
			// guaranteed later delivery — return the context error instead of
			// buffering (broker failures still buffer for at-least-once).
			o.mu.Lock()
			o.directInFlight = false
			published = true
			o.mu.Unlock()
			return ctx.Err()
		}

		// Direct publish failed — buffer the message.
		o.logger.Warn("direct publish failed, buffering message",
			redact.String("exchange", exchange),
			redact.String("routing_key", routingKey),
			redact.String("msg_id", msg.ID))

		o.mu.Lock()
		o.directInFlight = false
		published = true // defer cleanup no longer needed
		// Re-check capacity after re-acquiring lock — the buffer may have
		// been filled by concurrent Publish calls or the drain loop.
		if o.maxSize > 0 && len(o.pending) >= o.maxSize {
			o.mu.Unlock()
			o.onDrop()
			return ErrBufferFull
		}
		o.addPendingLocked(pendingMessage{
			Exchange:   exchange,
			RoutingKey: routingKey,
			Msg:        msg,
		})
		saveErr := o.persistAppendLocked()
		if saveErr != nil && !o.lossyMode {
			// Roll back the buffered append so memory state matches what
			// was successfully persisted. Without this, a later successful
			// save would persist a message the caller was told failed.
			o.dropLastPendingLocked()
			o.mu.Unlock()
			return redact.WrapError("buffered publisher: persist message after direct publish failure", saveErr)
		}
		pending := len(o.pending)
		bytes := o.pendingBytesLocked()
		o.mu.Unlock()

		o.reportPending(pending, bytes)
		o.onBuffer()
		o.logger.Info("message buffered",
			redact.String("exchange", exchange),
			redact.String("routing_key", routingKey),
			redact.String("msg_id", msg.ID),
			"pending", pending)
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
				redact.String("exchange", exchange),
				redact.String("routing_key", routingKey),
				redact.String("msg_id", msg.ID),
				"buffer_size", o.maxSize)
			o.onDrop()
			return ErrBufferFull
		}
	}

	o.addPendingLocked(pendingMessage{
		Exchange:   exchange,
		RoutingKey: routingKey,
		Msg:        msg,
	})
	saveErr := o.persistAppendLocked()
	if saveErr != nil && !o.lossyMode {
		o.dropLastPendingLocked()
		o.mu.Unlock()
		return redact.WrapError("buffered publisher: persist buffered message", saveErr)
	}
	pending := len(o.pending)
	bytes := o.pendingBytesLocked()
	o.mu.Unlock()

	o.reportPending(pending, bytes)
	o.onBuffer()

	o.logger.Info("message buffered",
		redact.String("exchange", exchange),
		redact.String("routing_key", routingKey),
		redact.String("msg_id", msg.ID),
		"pending", pending)
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
// is cancelled and returns nil after the final drain completes.
//
// On shutdown (ctx cancelled), a final best-effort drain is attempted using a
// short-lived context so in-flight messages are not lost.
func (o *BufferedPublisher) Run(ctx context.Context) error {
	if o == nil || o.publishFn == nil || o.healthyFn == nil {
		return ErrInvalidPublisher
	}
	if ctx == nil {
		return errors.New("messaging: BufferedPublisher.Run requires a non-nil context")
	}
	o.runMu.Lock()
	if o.started {
		o.runMu.Unlock()
		return errors.New("messaging: BufferedPublisher.Run already started")
	}
	o.started = true
	o.runMu.Unlock()

	o.drain(ctx) // Drain immediately on startup to clear any restored messages.

	ticker := time.NewTicker(bufferedDrainInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			o.finalDrain(ctx)
			return nil
		case <-ticker.C:
			o.drain(ctx)
		}
	}
}

// finalDrain attempts one last drain so pending messages are not silently
// discarded on shutdown.
//
// The drain budget configured by [WithFinalDrainTimeout] sets ctx.Done()
// for the underlying publisher; the bound is COOPERATIVE because the
// upstream publisher's Publish call must honour the supplied context.
// A publisher that ignores ctx.Done() (rare for the kit's AMQP/NATS/
// Redis adapters, possible for custom integrations) will hold the
// drain past the configured timeout. Operators relying on a hard
// shutdown bound MUST wire publishers that respect context
// cancellation; the state file (configured via [WithStateFile]) is the
// kit's recovery hook when the bound is exceeded (L128).
func (o *BufferedPublisher) finalDrain(ctx context.Context) {
	o.mu.Lock()
	remaining := len(o.pending)
	o.mu.Unlock()

	if remaining == 0 {
		return
	}

	o.logger.Info("buffered publisher final drain starting", "pending", remaining)

	ctx, cancel := bufferedDetachedContext(ctx, o.finalDrainTimeout)
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

func bufferedDetachedContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(ctx), timeout)
}

// drain publishes all currently-buffered messages while the broker
// stays healthy and ctx is alive. It works in bounded batches (each
// batch holds o.mu only briefly), looping until the buffer is empty or
// a batch makes no progress (broker turned unhealthy, ctx cancelled, or
// a publish failed). Looping is required so the buffer can fully recover
// after a broker blip: capping a drain at a single batch would let
// sustained inflow above one batch per drain interval keep pending > 0
// forever, so direct mode would never resume and the buffer would
// death-spiral to capacity despite a healthy broker.
func (o *BufferedPublisher) drain(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		published, more := o.drainBatch(ctx)
		// Stop when this batch made no progress (unhealthy / cancelled /
		// publish failure) or the buffer is now empty.
		if published == 0 || !more {
			return
		}
	}
}

// drainBatch publishes at most one bufferedDrainBatchLimit batch from the
// front of the pending buffer. It returns the number of messages
// successfully published and whether more messages remain to be drained.
// o.mu is held only while copying the batch and while committing the
// result, never across the publish calls.
func (o *BufferedPublisher) drainBatch(ctx context.Context) (published int, more bool) {
	if !o.healthyFn() {
		return 0, false
	}

	o.mu.Lock()
	if len(o.pending) == 0 || o.directInFlight {
		o.mu.Unlock()
		return 0, false
	}

	// Set directInFlight so concurrent Publish calls buffer instead of
	// bypassing — prevents FIFO violations during drain.
	o.directInFlight = true

	// Take a batch to drain while holding the lock briefly.
	batchSize := min(len(o.pending), bufferedDrainBatchLimit)
	batch := make([]pendingMessage, batchSize)
	copy(batch, o.pending[:batchSize])
	o.mu.Unlock()

	// Guarantee directInFlight is cleared even if publishFn panics.
	// Without this (mirroring the Publish direct path), a recovered panic
	// permanently freezes the drain loop and forces all subsequent Publishes
	// down the buffer path until capacity is exhausted.
	cleared := false
	defer func() {
		if !cleared {
			o.mu.Lock()
			o.directInFlight = false
			o.mu.Unlock()
		}
	}()

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
		if err := o.publishFn(ctx, pm.Exchange, pm.RoutingKey, pm.Msg.Clone()); err != nil {
			o.logger.Warn("buffered publisher drain publish failed, will retry",
				redact.Error(err), redact.String("msg_id", pm.Msg.ID))
			break
		}
		published++
	}

	o.mu.Lock()
	o.directInFlight = false
	cleared = true

	var saveErr error
	pending := -1
	bytes := 0
	if published > 0 {
		// Compact the slice to allow the backing array to be GC'd.
		// Without this, o.pending[published:] retains the original array
		// forever since we only ever shrink from the front.
		remaining := len(o.pending) - published
		compacted := make([]pendingMessage, remaining)
		copy(compacted, o.pending[published:])
		o.pending = compacted
		o.pendingBytes = 0
		for i := range o.pending {
			o.pendingBytes += len(o.pending[i].Msg.Payload)
		}
		// FR-068 [HIGH]: surface the save error instead of swallowing
		// it. On disk-full / EROFS / quota the on-disk pending list
		// still contains the messages we just delivered to the broker,
		// so a crash before the next successful save would replay them.
		// LastSaveError() is the kit-internal probe; OnSaveError is the
		// metrics-side hook that pages someone before the next crash.
		saveErr = o.saveLocked()
		pending = len(o.pending)
		bytes = o.pendingBytesLocked()
	}
	remaining := len(o.pending)
	o.mu.Unlock()

	if pending >= 0 {
		o.reportPending(pending, bytes)
	}
	if saveErr != nil {
		o.logger.Error("buffered publisher state save failed AFTER successful broker publishes; restart-replay risk until next save succeeds",
			redact.Error(saveErr), "published", published)
		o.onSaveError(saveErr)
	}

	if published > 0 {
		o.onDrain(published)
		o.logger.Info("buffered publisher drained",
			"published", published)
	}

	// A persistence failure must stop the drain loop: the in-memory
	// buffer no longer matches disk, and continuing would compound the
	// restart-replay window. Surface "no more progress" so the caller
	// retries on the next tick after the save error has been observed.
	return published, saveErr == nil && remaining > 0
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
		o.logger.Error("failed to save buffered publisher state", redact.Error(err), redact.String("file", o.stateFile))
		// Invalidate journal append mode so the next persist rewrites a full
		// snapshot from the in-memory pending slice. Leaving journalReady
		// true would let a later successful append clear LastSaveError while
		// the on-disk snapshot still contains already-delivered messages.
		o.journalReady = false
		o.lastSaveErr.Store(&err)
		o.onStateWrite(false)
		return err
	}
	// The on-disk file is now a single compacted snapshot line: reset the
	// journal tail so subsequent Publish calls append to (not rewrite) it.
	o.journalReady = true
	o.snapshotBaseLen = len(o.pending)
	o.journalEntries = 0
	o.journalBytes = 0
	o.lastSaveErr.Store(nil)
	o.onStateWrite(true)
	return nil
}

// persistAppendLocked durably records the most-recently appended pending
// entry. It is the Publish-path replacement for the full-snapshot
// [saveLocked]: instead of rewriting every buffered message on each call
// (O(n) bytes per Publish, O(n^2) across a broker-outage burst), it appends
// only the new entry to the on-disk journal (O(1) per Publish). The append is
// fsync'd before returning, so the strict durability contract is unchanged —
// a Publish still does not report success until its message is on stable
// storage (unless [WithLossyMode] is set, matching the prior behaviour).
//
// Must be called with o.mu held, immediately after appending exactly one
// entry to o.pending. It writes a full snapshot instead of an append when (1)
// no snapshot has been established since construction/load — this also
// migrates a legacy single-array state file into the journal format before any
// line is appended — or (2) the journal tail has grown enough that compaction
// keeps replay bounded.
func (o *BufferedPublisher) persistAppendLocked() error {
	if o.stateFile == "" {
		return nil
	}
	if !o.journalReady || o.compactionDueLocked() {
		return o.saveLocked()
	}

	line, err := marshalPendingEntry(o.pending[len(o.pending)-1])
	if err != nil {
		o.logger.Error("failed to marshal buffered publisher journal entry", redact.Error(err))
		o.lastSaveErr.Store(&err)
		o.onStateWrite(false)
		return err
	}
	if err := appendPendingEntry(o.stateFile, line); err != nil {
		o.logger.Error("failed to append buffered publisher journal entry", redact.Error(err), redact.String("file", o.stateFile))
		// A failed append may have left a torn trailing line in the file
		// (e.g. disk filled mid-write). Force the NEXT persist to rewrite a
		// full atomic snapshot so the live file is restored to a consistent
		// state; until then, replay tolerates the torn trailing line (it
		// corresponds to this message, which the caller is about to roll back
		// because we are returning an error). See [parseJournalFile].
		o.journalReady = false
		o.lastSaveErr.Store(&err)
		o.onStateWrite(false)
		return err
	}
	o.journalEntries++
	o.journalBytes += len(line) + 1 // +1 for the newline framing
	o.lastSaveErr.Store(nil)
	o.onStateWrite(true)
	return nil
}

// compactionDueLocked reports whether the journal tail has grown enough to
// warrant rewriting a fresh snapshot. The entry-count rule (tail >= base,
// floored at a minimum) makes each snapshot at least double the work since the
// previous one, so total bytes written across a burst of n appends stay O(n)
// rather than O(n^2). The byte rule force-compacts a payload-heavy tail before
// it bloats the replay file toward [atomicfile.MaxLoadBytes].
func (o *BufferedPublisher) compactionDueLocked() bool {
	if o.journalBytes >= bufferedJournalCompactBytes {
		return true
	}
	return o.journalEntries >= bufferedJournalCompactMinEntries && o.journalEntries >= o.snapshotBaseLen
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

func (o *BufferedPublisher) onDirectPublish() {
	if o.metrics == nil {
		return
	}
	o.callMetric("OnDirectPublish", o.metrics.OnDirectPublish)
}

func (o *BufferedPublisher) onBuffer() {
	if o.metrics == nil {
		return
	}
	o.callMetric("OnBuffer", o.metrics.OnBuffer)
}

func (o *BufferedPublisher) onDrop() {
	if o.metrics == nil {
		return
	}
	o.callMetric("OnDrop", o.metrics.OnDrop)
}

func (o *BufferedPublisher) onDrain(count int) {
	if o.metrics == nil || o.metrics.OnDrain == nil {
		return
	}
	o.callMetric("OnDrain", func() {
		o.metrics.OnDrain(count)
	})
}

func (o *BufferedPublisher) onPendingGauge(count int) {
	if o.metrics == nil || o.metrics.OnPendingGauge == nil {
		return
	}
	o.callMetric("OnPendingGauge", func() {
		o.metrics.OnPendingGauge(count)
	})
}

func (o *BufferedPublisher) onBufferedBytesGauge(bytes int) {
	if o.metrics == nil || o.metrics.OnBufferedBytesGauge == nil {
		return
	}
	o.callMetric("OnBufferedBytesGauge", func() {
		o.metrics.OnBufferedBytesGauge(bytes)
	})
}

func (o *BufferedPublisher) onStateWrite(success bool) {
	if o.metrics == nil || o.metrics.OnStateWrite == nil {
		return
	}
	o.callMetric("OnStateWrite", func() {
		o.metrics.OnStateWrite(success)
	})
}

func (o *BufferedPublisher) onSaveError(err error) {
	if o.metrics == nil || o.metrics.OnSaveError == nil {
		return
	}
	o.callMetric("OnSaveError", func() {
		o.metrics.OnSaveError(err)
	})
}

// resolveStateFilePath enforces directory containment for a state
// file path supplied via [WithStateFile]. The caller-supplied path
// must be relative and, after [filepath.Clean], resolve to a
// location inside stateDir. Absolute paths and paths whose cleaned
// form escapes stateDir via `..` segments are rejected.
//
// The error messages intentionally identify the offending input
// (absolute / traversal / escape) without echoing the resolved
// absolute path back to the caller; doing so would print the
// operator-private state directory into panic output and tests that
// pin those messages.
func resolveStateFilePath(stateDir, relPath string) (string, error) {
	if relPath == "" {
		return "", errors.New("path must not be empty")
	}
	if filepath.IsAbs(relPath) {
		return "", errors.New("path must be relative to the configured state directory; got an absolute path")
	}
	cleanedDir := filepath.Clean(stateDir)
	cleanedRel := filepath.Clean(relPath)
	// Clean preserves any leading "..": if it returns "." or starts
	// with ".." (after splitting on the OS separator), the path
	// escapes the configured directory.
	if cleanedRel == ".." || strings.HasPrefix(cleanedRel, ".."+string(filepath.Separator)) {
		return "", errors.New("path escapes the configured state directory via parent (\"..\") segments")
	}
	joined := filepath.Join(cleanedDir, cleanedRel)
	rel, err := filepath.Rel(cleanedDir, joined)
	if err != nil {
		return "", redact.WrapError("path is not reachable from the configured state directory", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("path escapes the configured state directory")
	}
	if rel == "." {
		return "", errors.New("path resolves to the state directory itself, not a file inside it")
	}
	return joined, nil
}

// pendingBytesLocked returns the approximate in-memory byte cost of the
// currently-buffered messages. Must be called with o.mu held. The figure
// sums [Message.Payload] lengths only — headers, IDs, types, and JSON
// scaffolding are excluded because payload bytes dominate by orders of
// magnitude in realistic workloads, and computing the exact serialized
// size would require re-marshalling every entry on every gauge update.
func (o *BufferedPublisher) pendingBytesLocked() int {
	return o.pendingBytes
}

func (o *BufferedPublisher) addPendingLocked(pm pendingMessage) {
	o.pending = append(o.pending, pm)
	o.pendingBytes += len(pm.Msg.Payload)
}

func (o *BufferedPublisher) dropLastPendingLocked() {
	if len(o.pending) == 0 {
		return
	}
	last := o.pending[len(o.pending)-1]
	o.pendingBytes -= len(last.Msg.Payload)
	if o.pendingBytes < 0 {
		o.pendingBytes = 0
	}
	o.pending = o.pending[:len(o.pending)-1]
}

func (o *BufferedPublisher) reportPending(count, bytes int) {
	o.onPendingGauge(count)
	o.onBufferedBytesGauge(bytes)
}

func (o *BufferedPublisher) callMetric(name string, fn func()) {
	if fn == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			logger := o.logger
			if logger == nil {
				logger = slog.Default()
			}
			logger.Error("buffered publisher metric callback panicked",
				"callback", name,
				redact.Panic(r),
				"stack", string(debug.Stack()),
			)
		}
	}()
	fn()
}

// load reads pending messages from the state file on startup.
// By default an invalid entry (e.g. missing exchange) is fatal: load
// rejects the entire file and returns an error so corruption surfaces
// loudly instead of silently dropping messages. Pass
// [WithLossyStateValidation] to instead skip-and-log individual invalid
// entries while replaying the valid ones. An empty routing key is valid
// for fanout/exchange-only publishes and must be replayed.
func (o *BufferedPublisher) load() error {
	if o.stateFile == "" {
		return nil
	}

	pending, err := loadJournal(o.stateFile)
	if err != nil {
		return err
	}

	valid := make([]pendingMessage, 0, len(pending))
	for i, pm := range pending {
		if err := ValidatePublishRoute(pm.Exchange, pm.RoutingKey); err != nil {
			if !o.lossyStateValidation {
				return redact.WrapError(fmt.Sprintf("buffered publisher state: invalid entry at index %d (set WithLossyStateValidation to skip)", i), err)
			}
			o.logger.Warn("buffered publisher state: skipping invalid entry",
				"index", i, redact.String("msg_id", pm.Msg.ID), redact.Error(err))
			continue
		}
		if err := ValidateMessage(pm.Msg); err != nil {
			if !o.lossyStateValidation {
				return redact.WrapError(fmt.Sprintf("buffered publisher state: invalid message at index %d (set WithLossyStateValidation to skip)", i), err)
			}
			o.logger.Warn("buffered publisher state: skipping invalid message",
				"index", i, redact.String("msg_id", pm.Msg.ID), redact.Error(err))
			continue
		}
		valid = append(valid, pm)
	}

	o.pending = valid
	o.pendingBytes = 0
	for i := range o.pending {
		o.pendingBytes += len(o.pending[i].Msg.Payload)
	}
	return nil
}
