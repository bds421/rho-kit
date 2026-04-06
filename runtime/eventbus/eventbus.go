package eventbus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"runtime/debug"
	"sync"

	"github.com/ThreeDotsLabs/watermill"
	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/ThreeDotsLabs/watermill/pubsub/gochannel"
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
// concurrent goroutines. When the pool is full, new async events are dropped
// and logged. Without this option, async handlers launch unbounded goroutines.
func WithWorkerPool(size int) Option {
	return func(b *Bus) {
		if size <= 0 {
			panic("eventbus: worker pool size must be positive")
		}
		b.poolSize = size
	}
}

// WithWorkerPoolBuffer sets the GoChannel output buffer size.
// Default: 256. Has no effect without [WithWorkerPool].
func WithWorkerPoolBuffer(size int) Option {
	return func(b *Bus) {
		if size <= 0 {
			panic("eventbus: worker pool buffer size must be positive")
		}
		b.bufSize = size
	}
}

// WithRegisterer sets the Prometheus registerer for eventbus metrics.
// Default: [prometheus.DefaultRegisterer].
func WithRegisterer(reg prometheus.Registerer) Option {
	return func(b *Bus) {
		b.registerer = reg
	}
}

// HandlerOption configures a single handler registration.
type HandlerOption func(*handlerConfig)

type handlerConfig struct {
	async bool
	name  string
}

// WithAsync makes the handler execute asynchronously via the GoChannel
// backend. Errors from async handlers are reported via the [WithOnError]
// callback instead of being returned from [Publish].
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

// Bus dispatches domain events to registered handlers within a single process.
// It is safe for concurrent use. Create one with [New].
//
// Sync handlers (default) are called sequentially in [Publish]. Their errors
// are collected via [errors.Join] and returned to the caller.
//
// Async handlers ([WithAsync]) are dispatched via a Watermill GoChannel
// backend. Their errors are logged and sent to the [WithOnError] callback.
// With [WithWorkerPool], async dispatch is bounded; events are dropped when
// the pool is full.
type Bus struct {
	goChan *gochannel.GoChannel
	mu     sync.RWMutex

	// handlers stores all registered handlers keyed by event name.
	handlers map[string][]registeredHandler

	// dispatchers tracks event names that have an active GoChannel subscriber.
	// Only one GoChannel subscriber is created per event name; it fans out
	// to all async handlers internally.
	dispatchers map[string]bool

	logger  *slog.Logger
	onError func(ctx context.Context, eventName string, handlerName string, err error)

	// Bounded async pool.
	poolSize int
	bufSize  int
	sem      chan struct{} // nil = unbounded

	// Metrics
	registerer prometheus.Registerer
	metrics    *busMetrics

	closed bool
}

// New creates a [Bus]. The zero value is not usable; always use New.
func New(opts ...Option) *Bus {
	b := &Bus{
		handlers:    make(map[string][]registeredHandler),
		dispatchers: make(map[string]bool),
		logger:      slog.Default(),
		bufSize:     256,
	}
	for _, opt := range opts {
		opt(b)
	}

	if b.poolSize > 0 {
		b.sem = make(chan struct{}, b.poolSize)
	}

	b.goChan = gochannel.NewGoChannel(gochannel.Config{
		OutputChannelBuffer: int64(b.bufSize),
		PreserveContext:     true,
	}, watermill.NewSlogLogger(b.logger))

	b.metrics = newBusMetrics(b.registerer)

	return b
}

// Subscribe registers a typed handler for events of type E.
// The event name is derived from E's [Event.EventName] method at registration time.
// Panics if handler is nil.
//
// Sync handlers (default) are called sequentially within [Publish].
// Async handlers ([WithAsync]) are dispatched via GoChannel with panic recovery.
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
			e, ok := event.(*E)
			if !ok {
				return fmt.Errorf("eventbus: handler %q expects %v but got %T (duplicate EventName?)",
					cfg.name, expectedType, event)
			}
			return handler(ctx, *e)
		},
	}

	b.mu.Lock()
	b.handlers[eventName] = append(b.handlers[eventName], rh)

	// Start a single GoChannel subscriber per event name for async dispatch.
	if cfg.async && !b.dispatchers[eventName] {
		b.dispatchers[eventName] = true
		msgs, err := b.goChan.Subscribe(context.Background(), eventName)
		if err != nil {
			b.mu.Unlock()
			panic(fmt.Sprintf("eventbus: subscribe to %q failed: %v", eventName, err))
		}
		go b.asyncDispatcher(msgs, eventName, expectedType)
	}
	b.mu.Unlock()
}

// Publish dispatches event to all handlers registered for E's event name.
//
// Sync handlers are called sequentially; their errors are joined via [errors.Join].
// Async handlers are published to the GoChannel backend; their errors go to
// the [WithOnError] callback.
//
// Returns nil if no handlers are registered for the event.
func Publish[E Event](b *Bus, ctx context.Context, event E) error {
	eventName := event.EventName()

	b.mu.RLock()
	src := b.handlers[eventName]
	if len(src) == 0 {
		b.mu.RUnlock()
		return nil
	}
	snapshot := make([]registeredHandler, len(src))
	copy(snapshot, src)
	b.mu.RUnlock()

	// 1. Run sync handlers directly — preserves context, returns errors.
	var syncErrs []error
	for _, h := range snapshot {
		if !h.async {
			if err := h.fn(ctx, &event); err != nil {
				syncErrs = append(syncErrs, fmt.Errorf("handler %q: %w", h.name, err))
			}
		}
	}

	// 2. Publish to GoChannel for async handlers.
	hasAsync := false
	for _, h := range snapshot {
		if h.async {
			hasAsync = true
			break
		}
	}
	if hasAsync {
		payload, err := json.Marshal(event)
		if err != nil {
			return fmt.Errorf("eventbus: marshal event: %w", err)
		}
		wmMsg := message.NewMessage(watermill.NewUUID(), payload)
		wmMsg.SetContext(ctx)
		if pubErr := b.goChan.Publish(eventName, wmMsg); pubErr != nil {
			b.logger.Error("eventbus: GoChannel publish failed",
				slog.String("event", eventName),
				logattr.Error(pubErr),
			)
		}
	}

	return errors.Join(syncErrs...)
}

// asyncDispatcher reads from a single GoChannel subscriber and fans out
// to all registered async handlers for the event name.
func (b *Bus) asyncDispatcher(msgs <-chan *message.Message, eventName string, eventType reflect.Type) {
	for msg := range msgs {
		b.dispatchAsync(msg, eventName, eventType)
	}
}

func (b *Bus) dispatchAsync(msg *message.Message, eventName string, eventType reflect.Type) {
	// Deserialize event once for all async handlers.
	eventPtr := reflect.New(eventType).Interface()
	if err := json.Unmarshal(msg.Payload, eventPtr); err != nil {
		b.logger.Error("event deserialization failed",
			slog.String("event", eventName),
			logattr.Error(err),
		)
		msg.Ack()
		return
	}

	ctx := msg.Context()

	// Snapshot async handlers under lock.
	b.mu.RLock()
	var asyncHandlers []registeredHandler
	for _, h := range b.handlers[eventName] {
		if h.async {
			asyncHandlers = append(asyncHandlers, h)
		}
	}
	b.mu.RUnlock()

	for _, h := range asyncHandlers {
		h := h
		if b.sem != nil {
			// Bounded pool: try to acquire semaphore.
			select {
			case b.sem <- struct{}{}:
				if b.metrics != nil {
					b.metrics.activeWorkers.Inc()
				}
				go func() {
					defer func() {
						<-b.sem
						if b.metrics != nil {
							b.metrics.activeWorkers.Dec()
						}
					}()
					b.runAsyncHandler(ctx, eventName, h, eventPtr)
					if b.metrics != nil {
						b.metrics.processed.WithLabelValues(eventName).Inc()
					}
				}()
			default:
				// Pool full — drop the event for this handler.
				if b.metrics != nil {
					b.metrics.dropped.Inc()
				}
				b.logger.Warn("eventbus: worker pool full, event dropped",
					slog.String("event", eventName),
					slog.String("handler", h.name),
				)
			}
		} else {
			// Unbounded: launch goroutine directly.
			go func() {
				b.runAsyncHandler(ctx, eventName, h, eventPtr)
				if b.metrics != nil {
					b.metrics.processed.WithLabelValues(eventName).Inc()
				}
			}()
		}
	}

	msg.Ack()
}

// runAsyncHandler executes a single async handler with panic recovery.
func (b *Bus) runAsyncHandler(ctx context.Context, eventName string, h registeredHandler, event any) {
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

// HasHandlers reports whether any handlers are registered for the given event name.
func (b *Bus) HasHandlers(eventName string) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.handlers[eventName]) > 0
}

// Start blocks until ctx is cancelled. Implements lifecycle.Component.
func (b *Bus) Start(ctx context.Context) error {
	<-ctx.Done()
	return nil
}

// Stop closes the GoChannel, stopping all async subscriber goroutines.
// Implements lifecycle.Component.
func (b *Bus) Stop(_ context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	b.closed = true
	return b.goChan.Close()
}
