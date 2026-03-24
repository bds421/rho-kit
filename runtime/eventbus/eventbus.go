package eventbus

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"runtime/debug"
	"sync"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/bds421/rho-kit/observability/logattr"
)

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

// WithWorkerPool enables bounded async dispatch with the given number of
// workers. Without this option, async handlers launch unbounded goroutines
// (legacy behavior). The pool must be started via [Bus.Start] before
// publishing events.
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
// Default: workers * 10. Has no effect without [WithWorkerPool].
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
// Default: [prometheus.DefaultRegisterer]. Has no effect without [WithWorkerPool].
func WithRegisterer(reg prometheus.Registerer) Option {
	return func(b *Bus) {
		if b.poolCfg == nil {
			b.poolCfg = &poolConfig{}
		}
		b.poolCfg.registerer = reg
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
		c.name = name
	}
}

// registeredHandler is the type-erased internal representation.
type registeredHandler struct {
	name      string
	async     bool
	eventType reflect.Type
	fn        func(ctx context.Context, event any) error
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
	mu       sync.RWMutex
	handlers map[string][]registeredHandler
	logger   *slog.Logger
	onError  func(ctx context.Context, eventName string, handlerName string, err error)
	pool     *workerPool
	poolCfg  *poolConfig // nil = no pool (backward compat)
}

// New creates a [Bus]. The zero value is not usable; always use New.
//
// When [WithWorkerPool] is used, the pool is constructed eagerly but workers
// are not started until [Bus.Start] is called.
func New(opts ...Option) *Bus {
	b := &Bus{
		handlers: make(map[string][]registeredHandler),
		logger:   slog.Default(),
	}
	for _, opt := range opts {
		opt(b)
	}
	if b.poolCfg != nil && b.poolCfg.workers > 0 {
		bufSize := b.poolCfg.bufSize
		if bufSize <= 0 {
			bufSize = b.poolCfg.workers * 10
		}
		m := newPoolMetrics(b.poolCfg.registerer)
		b.pool = newWorkerPool(b.poolCfg.workers, bufSize, b.logger, b.onError, m)
	}
	return b
}

// Subscribe registers a typed handler for events of type E.
// The event name is derived from E's [Event.EventName] method at registration time.
// Panics if handler is nil.
func Subscribe[E Event](b *Bus, handler func(ctx context.Context, event E) error, opts ...HandlerOption) {
	if handler == nil {
		panic("eventbus: handler must not be nil")
	}

	cfg := handlerConfig{name: "anonymous"}
	for _, opt := range opts {
		opt(&cfg)
	}

	var zero E
	eventName := zero.EventName()
	expectedType := reflect.TypeOf(zero)

	rh := registeredHandler{
		name:      cfg.name,
		async:     cfg.async,
		eventType: expectedType,
		fn: func(ctx context.Context, event any) error {
			e, ok := event.(E)
			if !ok {
				return fmt.Errorf("eventbus: handler %q expects %v but got %T (duplicate EventName?)",
					cfg.name, expectedType, event)
			}
			return handler(ctx, e)
		},
	}

	b.mu.Lock()
	b.handlers[eventName] = append(b.handlers[eventName], rh)
	b.mu.Unlock()
}

// Publish dispatches event to all handlers registered for E's event name.
// Sync handlers are called sequentially; their errors are joined via [errors.Join].
// Async handlers run in separate goroutines; their errors go to the [WithOnError] callback.
// Returns nil if no handlers are registered for the event.
//
// Async events may be silently dropped if the worker pool queue is full.
// Dropped events are logged and counted via the eventbus_events_dropped_total
// metric. Security-critical events should use synchronous handlers (without
// [WithAsync]) to guarantee delivery.
func Publish[E Event](b *Bus, ctx context.Context, event E) error {
	eventName := event.EventName()

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

	var syncErrs []error
	for _, h := range snapshot {
		if h.async {
			b.dispatchAsync(ctx, eventName, h, event)
		} else {
			if err := h.fn(ctx, event); err != nil {
				syncErrs = append(syncErrs, fmt.Errorf("handler %q: %w", h.name, err))
			}
		}
	}

	return errors.Join(syncErrs...)
}

// HasHandlers reports whether any handlers are registered for the given event name.
func (b *Bus) HasHandlers(eventName string) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.handlers[eventName]) > 0
}

// dispatchAsync routes an async handler invocation to either the bounded
// worker pool (if configured) or an unbounded goroutine (legacy behavior).
func (b *Bus) dispatchAsync(ctx context.Context, eventName string, h registeredHandler, event any) {
	if b.pool != nil {
		// submit() logs with full detail on drop; no additional logging needed here.
		task := taskPool.Get().(*asyncTask)
		task.ctx = ctx
		task.eventName = eventName
		task.handler = h
		task.event = event
		b.pool.submit(task)
		return
	}
	go b.runAsync(ctx, eventName, h, event)
}

// Start starts the worker pool. If no pool is configured, Start blocks until
// ctx is cancelled (for lifecycle.Component compatibility).
// Implements lifecycle.Component.
func (b *Bus) Start(ctx context.Context) error {
	if b.pool == nil {
		<-ctx.Done()
		return nil
	}

	b.logger.Info("eventbus worker pool started",
		slog.Int("workers", b.pool.workers),
		slog.Int("buffer_size", cap(b.pool.queue)),
	)
	b.pool.start(ctx)
	return nil
}

// Stop drains pending events and stops workers. No-op if no pool is configured.
// If the context has a deadline, Stop returns ctx.Err() if the deadline is
// reached before all workers finish draining.
//
// If Stop returns ctx.Err(), the pool goroutine and its workers may still be
// running. This is an inherent limitation of Go's lack of goroutine preemption.
// Ensure handler functions respect context cancellation to minimize drain time.
//
// Implements lifecycle.Component.
func (b *Bus) Stop(ctx context.Context) error {
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
			err := fmt.Errorf("panic: %v", rec)
			b.logger.Error("async event handler panicked",
				slog.String("event", eventName),
				slog.String("handler", h.name),
				slog.Any("panic", rec),
				slog.String("stack", string(debug.Stack())),
			)
			if b.onError != nil {
				b.onError(ctx, eventName, h.name, err)
			}
		}
	}()

	if err := h.fn(ctx, event); err != nil {
		b.logger.Warn("async event handler error",
			slog.String("event", eventName),
			slog.String("handler", h.name),
			logattr.Error(err),
		)
		if b.onError != nil {
			b.onError(ctx, eventName, h.name, err)
		}
	}
}
