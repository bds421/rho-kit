package eventbus

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"runtime/debug"
	"sync"
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/observability/v2/logattr"
	"github.com/bds421/rho-kit/observability/v2/promutil"
)

// OnFullPolicy controls what async dispatch does when the worker pool queue
// is full. Default is [OnFullError].
type OnFullPolicy int

const (
	// OnFullDrop drops the event and increments events_dropped_total. The
	// publisher's call to [Publish] sees no error. Use for high-volume
	// telemetry where loss under saturation is acceptable.
	OnFullDrop OnFullPolicy = iota
	// OnFullBlock waits for queue space, observing the publisher's ctx for
	// cancellation. Use when the publisher can absorb backpressure (offline
	// batch jobs, ingestion pipelines).
	OnFullBlock
	// OnFullError returns [ErrQueueFull] from [Publish] without enqueuing.
	// Use when the publisher needs to react to saturation (retry, fallback,
	// circuit-break).
	OnFullError
)

// ErrQueueFull is returned by [Publish] when [WithOnFull](OnFullError) is
// configured and the worker pool queue is full.
var ErrQueueFull = errors.New("eventbus: worker pool queue full")

// ErrStopped is returned by [Publish] when the bus has been stopped (or is
// being stopped concurrently). Callers configured with [OnFullError] must
// observe the shutdown rather than silently dropping the event.
var ErrStopped = errors.New("eventbus: bus stopped")

// Event is the constraint for publishable domain events.
// Each concrete event type returns a stable name used as the dispatch key.
type Event interface {
	EventName() string
}

// Option configures a [Bus].
type Option func(*Bus)

// WithLogger sets the logger for the bus. Defaults to [slog.Default].
func WithLogger(l *slog.Logger) Option {
	return func(b *Bus) {
		if l != nil {
			b.logger = l
		}
	}
}

// WithOnError sets a callback for async handler errors and panics.
// If not set, errors are only logged.
func WithOnError(fn func(ctx context.Context, eventName string, handlerName string, err error)) Option {
	return func(b *Bus) {
		b.onError = fn
	}
}

// WithWorkerPool overrides the default bounded async-dispatch worker count.
// Explicitly configured pools must be started via [Bus.Start] before publishing
// events; submit-before-start returns [ErrQueueFull] so wiring bugs surface
// instead of silently buffering events that may never run.
func WithWorkerPool(size int) Option {
	return func(b *Bus) {
		if size <= 0 {
			panic("eventbus: worker pool size must be positive")
		}
		if b.poolCfg == nil {
			b.poolCfg = &poolConfig{}
		}
		b.poolCfg.workers = size
	}
}

// WithWorkerPoolBuffer sets the channel buffer size for the worker pool.
// Default: workers * 10. When called without [WithWorkerPool], it applies to
// the default bounded worker pool.
func WithWorkerPoolBuffer(size int) Option {
	return func(b *Bus) {
		if size <= 0 {
			panic("eventbus: worker pool buffer size must be positive")
		}
		if b.poolCfg == nil {
			b.poolCfg = &poolConfig{}
		}
		b.poolCfg.bufSize = size
	}
}

// WithRegisterer sets the Prometheus registerer for eventbus metrics.
// Default: [prometheus.DefaultRegisterer].
func WithRegisterer(reg prometheus.Registerer) Option {
	return func(b *Bus) {
		if b.poolCfg == nil {
			b.poolCfg = &poolConfig{}
		}
		b.poolCfg.registerer = reg
	}
}

// WithOnFull sets the policy applied when async dispatch finds the worker
// pool queue full. Default: [OnFullError].
//
// Panics if p is not one of [OnFullDrop], [OnFullBlock], or [OnFullError].
// Silently treating unknown values as drop would mask configuration bugs.
func WithOnFull(p OnFullPolicy) Option {
	switch p {
	case OnFullDrop, OnFullBlock, OnFullError:
	default:
		panic("eventbus: WithOnFull: unknown policy")
	}
	return func(b *Bus) {
		b.onFull = p
		b.policySet = true
	}
}

// HandlerOption configures a single handler registration.
type HandlerOption func(*handlerConfig)

type handlerConfig struct {
	async bool
	name  string
}

// WithAsync makes the handler execute in a new goroutine.
// Errors from async handlers are reported via the [WithOnError] callback
// instead of being returned from [Publish].
func WithAsync() HandlerOption {
	return func(c *handlerConfig) {
		c.async = true
	}
}

// WithName sets a human-readable name for the handler, used in logs
// and error callbacks. Defaults to "anonymous".
func WithName(name string) HandlerOption {
	return func(c *handlerConfig) {
		if err := promutil.ValidateStaticLabelValue("handler name", name); err != nil {
			panic("eventbus: invalid handler name")
		}
		c.name = name
	}
}

// registeredHandler is the type-erased internal representation.
type registeredHandler struct {
	id        uint64
	name      string
	async     bool
	alive     bool
	eventType reflect.Type
	fn        func(ctx context.Context, event any) error
}

// Subscription is the token returned by [Subscribe]. Pass it to
// [Bus.Unsubscribe] to remove the handler.
type Subscription struct {
	eventName string
	id        uint64
}

// poolConfig holds configuration for the optional bounded worker pool.
type poolConfig struct {
	workers    int
	bufSize    int
	registerer prometheus.Registerer
}

// Bus dispatches domain events to registered handlers within a single process.
// It is safe for concurrent use. Create one with [New].
type Bus struct {
	mu              sync.RWMutex
	handlers        map[string][]registeredHandler
	logger          *slog.Logger
	onError         func(ctx context.Context, eventName string, handlerName string, err error)
	pool            *workerPool
	poolCfg         *poolConfig
	onFull          OnFullPolicy
	policySet       bool // FR-091: distinguishes "default" from "explicitly set to OnFullDrop"
	unboundedAsync  bool // FR-089: opts out of the default bounded worker pool
	autoStartedPool bool // default pool workers were started by New.
	nextID          atomic.Uint64
	startMu         sync.Mutex
	started         bool
	stopped         bool
}

// New creates a [Bus]. The zero value is not usable; always use New.
//
// When [WithWorkerPool] is used, the pool is constructed eagerly but workers
// are not started until [Bus.Start] is called. Without [WithWorkerPool], New
// installs and starts a default bounded worker pool for async handlers.
//
// When no [WithWorkerPool] is supplied, New installs
// [defaultEventBusWorkers] workers and the [OnFullError] saturation policy.
// Opt out via [WithUnboundedAsync] for "every async dispatch spawns a
// goroutine" semantics, or pick [WithOnFull](OnFullDrop) explicitly to keep
// drop-on-full semantics.
func New(opts ...Option) *Bus {
	b := &Bus{
		handlers: make(map[string][]registeredHandler),
		logger:   slog.Default(),
	}
	for _, opt := range opts {
		if opt == nil {
			panic("eventbus: option must not be nil")
		}
		opt(b)
	}
	defaultPool := false
	if !b.unboundedAsync {
		if b.poolCfg == nil {
			b.poolCfg = &poolConfig{}
		}
		if b.poolCfg.workers == 0 {
			b.poolCfg.workers = defaultEventBusWorkers
			defaultPool = true
		}
	}
	if !b.policySet {
		b.onFull = OnFullError
	}
	if b.poolCfg != nil && b.poolCfg.workers > 0 {
		bufSize := b.poolCfg.bufSize
		if bufSize <= 0 {
			bufSize = b.poolCfg.workers * 10
		}
		m := newPoolMetrics(b.poolCfg.registerer)
		b.pool = newWorkerPool(b.poolCfg.workers, bufSize, b.logger, b.onError, m)
		if defaultPool {
			// Auto-start the FR-089 default pool so callers that don't
			// run a lifecycle (tests, scripts, simple programs) keep
			// working without manual Start. Explicitly-configured pools
			// still require Start so submit-before-Start surfaces as an
			// error per FR-090.
			b.pool.startWorkers()
			b.autoStartedPool = true
		}
	}
	return b
}

// defaultEventBusWorkers is the worker count used when no
// [WithWorkerPool] option is supplied. 8 is enough to absorb modest
// async-event spikes while keeping the goroutine ceiling visible
// (audit FR-089).
const defaultEventBusWorkers = 8

// WithUnboundedAsync opts a Bus out of the FR-089 default worker
// pool. Async dispatches spawn one goroutine each — the legacy
// behaviour. Use only when an external mechanism bounds event
// volume, otherwise a publish spike will OOM the service.
func WithUnboundedAsync() Option {
	return func(b *Bus) { b.unboundedAsync = true }
}

// Subscribe registers a typed handler for events of type E and returns a
// [Subscription] token that can be passed to [Bus.Unsubscribe]. Callers that
// never unsubscribe may discard the return value.
//
// The event name is derived from E's [Event.EventName] method at registration
// time. Panics if handler is nil.
func Subscribe[E Event](b *Bus, handler func(ctx context.Context, event E) error, opts ...HandlerOption) Subscription {
	if handler == nil {
		panic("eventbus: handler must not be nil")
	}

	cfg := handlerConfig{name: "anonymous"}
	for _, opt := range opts {
		if opt == nil {
			panic("eventbus: handler option must not be nil")
		}
		opt(&cfg)
	}

	var zero E
	expectedType := reflect.TypeOf((*E)(nil)).Elem()
	// For pointer event types (e.g. *FooEvent), the zero value of E is a
	// typed-nil pointer; calling EventName on it would panic if the impl
	// reads receiver fields. Instantiate a fresh value of the pointee so
	// EventName runs against a non-nil receiver.
	probe := Event(zero)
	if expectedType.Kind() == reflect.Pointer {
		probe = reflect.New(expectedType.Elem()).Interface().(Event)
	}
	eventName := probe.EventName()
	if err := promutil.ValidateStaticLabelValue("event name", eventName); err != nil {
		panic("eventbus: invalid event name")
	}

	id := b.nextID.Add(1)
	rh := registeredHandler{
		id:        id,
		name:      cfg.name,
		async:     cfg.async,
		alive:     true,
		eventType: expectedType,
		fn: func(ctx context.Context, event any) error {
			e, ok := event.(E)
			if !ok {
				return fmt.Errorf("eventbus: handler received unexpected event type")
			}
			return handler(ctx, e)
		},
	}

	b.mu.Lock()
	b.handlers[eventName] = append(b.handlers[eventName], rh)
	// Opportunistic compaction: if dead-count is non-trivial relative to
	// the live slice, drop tombstoned entries while we're already holding
	// the write lock.
	b.maybeCompactLocked(eventName)
	b.mu.Unlock()

	return Subscription{eventName: eventName, id: id}
}

// compactionThreshold is the number of tombstoned handlers that triggers
// a compaction during the next [Subscribe] or [Bus.Unsubscribe] call.
// Small enough that wasted Publish-time skips stay bounded; large enough
// that a single Subscribe → Unsubscribe pair doesn't churn allocations.
const compactionThreshold = 8

// Unsubscribe marks the handler associated with sub as dead. The handler
// slot is left in place (a "tombstone") and Publish snapshots simply skip
// dead entries; the slot is reclaimed lazily during the next [Subscribe]
// when the dead-count exceeds [compactionThreshold]. Tombstoning is O(n)
// in the slice walk but allocation-free on the hot path, in contrast to
// the previous "allocate a fresh slice on every Unsubscribe" approach
// that hurt high-churn workloads (test churn, dynamic plugins).
//
// Returns true if the handler was found and tombstoned, false if it was
// already removed or sub is the zero value.
//
// Safe to call concurrently with [Publish]: in-flight dispatches use a
// snapshot of the handler slice taken at the start of Publish, so a handler
// already snapshotted may still receive one final event after Unsubscribe
// returns. Subsequent Publish calls will not include it.
func (b *Bus) Unsubscribe(sub Subscription) bool {
	if sub.eventName == "" || sub.id == 0 {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	hs := b.handlers[sub.eventName]
	for i := range hs {
		if hs[i].id == sub.id && hs[i].alive {
			hs[i].alive = false
			// Drop fn so the closure (and any captured large state) can
			// be GC'd before compaction reclaims the slot.
			hs[i].fn = nil
			b.handlers[sub.eventName] = hs
			b.maybeCompactLocked(sub.eventName)
			return true
		}
	}
	return false
}

// maybeCompactLocked drops tombstoned entries when their count exceeds
// [compactionThreshold]. Caller holds b.mu.
//
// Compaction allocates a new slice so any in-flight Publish snapshot is
// untouched — that property is what lets Publish copy the handler slice
// without re-acquiring the lock for each invocation.
func (b *Bus) maybeCompactLocked(eventName string) {
	hs := b.handlers[eventName]
	dead := 0
	for i := range hs {
		if !hs[i].alive {
			dead++
		}
	}
	if dead < compactionThreshold {
		return
	}
	next := make([]registeredHandler, 0, len(hs)-dead)
	for i := range hs {
		if hs[i].alive {
			next = append(next, hs[i])
		}
	}
	b.handlers[eventName] = next
}

// Publish dispatches event to all handlers registered for E's event name.
// Sync handlers are called sequentially; their errors are joined via [errors.Join].
// Async handlers run in separate goroutines; their errors go to the [WithOnError] callback.
// Returns nil if no handlers are registered for the event.
//
// Async dispatch behavior under saturation depends on [WithOnFull]:
//   - [OnFullError] (default): Publish returns [ErrQueueFull] without enqueuing.
//   - [OnFullDrop]: events are dropped silently and counted via
//     eventbus_events_dropped_total.
//   - [OnFullBlock]: Publish blocks until queue space is available or ctx
//     is cancelled (returns ctx.Err()).
//
// Security-critical events should use synchronous handlers (without
// [WithAsync]) to guarantee delivery.
func Publish[E Event](b *Bus, ctx context.Context, event E) error {
	eventName := event.EventName()
	if err := promutil.ValidateStaticLabelValue("event name", eventName); err != nil {
		return fmt.Errorf("eventbus: invalid event name: %w", err)
	}

	// Performance note: the handler slice is copied on every Publish call.
	// For very high publish rates (100K+/sec), consider replacing with
	// atomic.Pointer to eliminate the copy.
	b.mu.RLock()
	src := b.handlers[eventName]
	if len(src) == 0 {
		b.mu.RUnlock()
		return nil
	}
	snapshot := make([]registeredHandler, len(src))
	copy(snapshot, src)
	b.mu.RUnlock()

	var dispatchErrs []error
	for _, h := range snapshot {
		// Skip tombstoned handlers — Unsubscribe marks alive=false in
		// place rather than reallocating; the slot is reclaimed lazily
		// during the next Subscribe/Unsubscribe call.
		if !h.alive {
			continue
		}
		if h.async {
			if err := b.dispatchAsync(ctx, eventName, h, event); err != nil {
				dispatchErrs = append(dispatchErrs, fmt.Errorf("handler failed: %w", err))
			}
		} else {
			if err := callSync(ctx, h, event); err != nil {
				dispatchErrs = append(dispatchErrs, fmt.Errorf("handler failed: %w", err))
			}
		}
	}

	return errors.Join(dispatchErrs...)
}

// callSync invokes a sync handler with panic recovery. A buggy subscriber
// previously took down the publisher's goroutine because Publish called
// h.fn directly with no recover; the async path already wraps its handler.
// Recovering here brings sync handlers in line with that behavior — the
// panic surfaces as a regular error so other subscribers still run and the
// publisher's goroutine survives.
func callSync(ctx context.Context, h registeredHandler, event any) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic in sync handler: %s", redact.PanicValue(r))
		}
	}()
	return h.fn(ctx, event)
}

// HasHandlers reports whether any live handlers are registered for the
// given event name. Tombstoned (unsubscribed) handlers are not counted.
func (b *Bus) HasHandlers(eventName string) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for i := range b.handlers[eventName] {
		if b.handlers[eventName][i].alive {
			return true
		}
	}
	return false
}

// dispatchAsync routes an async handler invocation to the bounded worker pool,
// or to an unbounded goroutine only when callers explicitly opt into
// [WithUnboundedAsync].
//
// The returned error is non-nil only when the worker pool queue is full and
// the configured [OnFullPolicy] surfaces it: [OnFullError] returns
// [ErrQueueFull], [OnFullBlock] returns ctx.Err() if blocked too long, and
// [OnFullDrop] returns nil (the event is dropped and metric-counted).
func (b *Bus) dispatchAsync(ctx context.Context, eventName string, h registeredHandler, event any) error {
	if b.pool == nil {
		go b.runAsync(ctx, eventName, h, event)
		return nil
	}

	task := taskPool.Get().(*asyncTask)
	task.ctx = ctx
	task.eventName = eventName
	task.handler = h
	task.event = event

	ok, err := b.pool.submit(task, b.onFull, ctx)
	if err != nil {
		// ErrStopped is only surfaced to OnFullError callers — Drop and
		// Block policies treat shutdown as another bounded-drop signal.
		if errors.Is(err, ErrStopped) && b.onFull != OnFullError {
			return nil
		}
		return err
	}
	if !ok && b.onFull == OnFullError {
		return ErrQueueFull
	}
	return nil
}

// Start starts the worker pool. If no pool is configured, Start blocks until
// ctx is cancelled (for lifecycle.Component compatibility).
//
// When ctx is cancelled, Start stops the pool before returning so callers
// that drive Start directly (without a separate Stop call) do not leak
// worker goroutines. Stop remains safe to call afterwards (it is idempotent).
//
// Returns an error if Start has already been called or Stop has already
// permanently stopped the bus. Bus lifecycle is intentionally one-shot: a
// second Start would race a different context against the shared worker pool.
//
// Implements lifecycle.Component.
func (b *Bus) Start(ctx context.Context) error {
	if ctx == nil {
		return errors.New("eventbus: Start requires a non-nil context")
	}
	b.startMu.Lock()
	if b.started {
		b.startMu.Unlock()
		return errors.New("eventbus: Bus already started")
	}
	if b.stopped {
		b.startMu.Unlock()
		return errors.New("eventbus: Bus already stopped")
	}
	b.started = true
	b.startMu.Unlock()

	if b.pool == nil {
		<-ctx.Done()
		return nil
	}

	b.logger.Info("eventbus worker pool started",
		slog.Int("workers", b.pool.workers),
		slog.Int("buffer_size", cap(b.pool.queue)),
	)
	if b.autoStartedPool {
		<-ctx.Done()
	} else {
		b.pool.start(ctx)
	}
	b.pool.stop()
	return nil
}

// Stop drains pending events and stops workers. No-op if no pool is configured.
// If the context has a deadline, Stop returns ctx.Err() if the deadline is
// reached before all workers finish draining.
//
// Idempotent and safe for concurrent callers — the worker-pool teardown
// runs exactly once; subsequent calls return nil immediately. Subsequent
// [Publish] calls return [ErrStopped].
//
// If Stop returns ctx.Err(), the pool goroutine and its workers may still be
// running. This is an inherent limitation of Go's lack of goroutine preemption.
// Ensure handler functions respect context cancellation to minimize drain time.
//
// Implements lifecycle.Component.
func (b *Bus) Stop(ctx context.Context) error {
	if ctx == nil {
		return errors.New("eventbus: Stop requires a non-nil context")
	}
	b.startMu.Lock()
	alreadyStopped := b.stopped
	b.stopped = true
	b.startMu.Unlock()
	if alreadyStopped {
		return nil
	}

	if b.pool == nil {
		return nil
	}
	done := make(chan struct{})
	go func() {
		b.pool.stop()
		close(done)
	}()
	select {
	case <-done:
		b.logger.Info("eventbus worker pool stopped")
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// runAsync executes a handler in a goroutine with panic recovery.
func (b *Bus) runAsync(ctx context.Context, eventName string, h registeredHandler, event any) {
	defer func() {
		if rec := recover(); rec != nil {
			err := fmt.Errorf("panic: %s", redact.PanicValue(rec))
			b.logger.Error("async event handler panicked",
				slog.String("event", eventName),
				slog.String("handler", h.name),
				redact.Panic(rec),
				slog.String("stack", string(debug.Stack())),
			)
			callOnError(b.logger, b.onError, ctx, eventName, h.name, err)
		}
	}()

	if err := h.fn(ctx, event); err != nil {
		b.logger.Warn("async event handler error",
			slog.String("event", eventName),
			slog.String("handler", h.name),
			logattr.Error(err),
		)
		callOnError(b.logger, b.onError, ctx, eventName, h.name, err)
	}
}

// callOnError invokes the optional async error hook while containing hook
// panics. Handler panics/errors already represent a subscriber fault; a buggy
// observability hook must not take down the worker goroutine or process.
func callOnError(
	logger *slog.Logger,
	onError func(context.Context, string, string, error),
	ctx context.Context,
	eventName string,
	handlerName string,
	err error,
) {
	if onError == nil {
		return
	}
	if logger == nil {
		logger = slog.Default()
	}
	defer func() {
		if rec := recover(); rec != nil {
			logger.Error("eventbus onError callback panicked",
				slog.String("event", eventName),
				slog.String("handler", handlerName),
				redact.Panic(rec),
				slog.String("stack", string(debug.Stack())),
			)
		}
	}()
	onError(ctx, eventName, handlerName, err)
}
