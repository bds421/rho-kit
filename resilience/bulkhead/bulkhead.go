package bulkhead

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/observability/v2/promutil"
)

// ErrBulkheadFull is returned by [Bulkhead.ExecuteCtx] when the
// bulkhead is full and the configured wait elapsed (or
// MaxQueueWait is zero, meaning immediate rejection).
var ErrBulkheadFull = errors.New("bulkhead: full")

// Bulkhead is a counting semaphore bounding concurrent operations
// against a single downstream. Safe for concurrent use by any
// number of goroutines.
type Bulkhead struct {
	name      string
	max       int64
	current   atomic.Int64
	maxWait   time.Duration
	semaphore chan struct{}
	metrics   *Metrics
	logger    *slog.Logger
}

// Option configures [New].
type Option func(*Bulkhead)

// WithMaxQueueWait caps the wait when the bulkhead is full. When
// d <= 0, a full bulkhead rejects immediately with
// ErrBulkheadFull. The default is "wait for caller's ctx" — see
// the package preamble for rejection modes.
func WithMaxQueueWait(d time.Duration) Option {
	return func(b *Bulkhead) { b.maxWait = d }
}

// WithMetrics wires a constructed [*Metrics] into the bulkhead so
// every Acquire / Release records into the named histogram +
// counter. Pass nil to disable metrics (the default).
func WithMetrics(m *Metrics) Option {
	return func(b *Bulkhead) { b.metrics = m }
}

// WithLogger sets the *slog.Logger used by the bulkhead to record
// panics observed inside fn before re-raising. When unset the
// bulkhead falls back to [slog.Default]. Matches the kit's
// per-package [WithLogger] convention.
func WithLogger(l *slog.Logger) Option {
	return func(b *Bulkhead) {
		if l != nil {
			b.logger = l
		}
	}
}

// New constructs a Bulkhead with the supplied name (used in
// Prometheus labels — validated as a static label at
// construction) and a positive concurrency cap. Panics on max <=
// 0 or a name that would inflate label cardinality.
func New(name string, max int, opts ...Option) *Bulkhead {
	if max <= 0 {
		panic("bulkhead: New requires a positive max")
	}
	if err := promutil.ValidateStaticLabelValue("name", name); err != nil {
		panic("bulkhead: " + err.Error())
	}
	b := &Bulkhead{
		name:      name,
		max:       int64(max),
		semaphore: make(chan struct{}, max),
	}
	for _, opt := range opts {
		if opt == nil {
			panic("bulkhead: New option must not be nil")
		}
		opt(b)
	}
	if b.logger == nil {
		b.logger = slog.Default()
	}
	return b
}

// Name returns the configured bulkhead name. Useful for log lines
// and for callers that route work to one of N bulkheads keyed by
// downstream identity.
func (b *Bulkhead) Name() string { return b.name }

// InFlight returns the number of currently-held slots. Eventually-
// consistent under concurrent Acquire/Release.
func (b *Bulkhead) InFlight() int { return int(b.current.Load()) }

// Capacity returns the configured max concurrency.
func (b *Bulkhead) Capacity() int { return int(b.max) }

// ExecuteCtx invokes fn while holding one slot of the bulkhead.
// Acquires before fn runs; releases after fn returns (panicking
// fns are caught, the slot is released, and the panic is
// re-raised so the caller's recover middleware can observe it).
//
// Returns ErrBulkheadFull when the bulkhead is full and either
// the wait elapsed or MaxQueueWait is zero. Returns the caller's
// ctx error when ctx cancels before acquisition.
func (b *Bulkhead) ExecuteCtx(ctx context.Context, fn func(ctx context.Context) error) (retErr error) {
	if ctx == nil {
		return errors.New("bulkhead: ExecuteCtx requires a non-nil context")
	}
	if fn == nil {
		return errors.New("bulkhead: ExecuteCtx requires a non-nil fn")
	}

	start := time.Now()
	if err := b.acquire(ctx); err != nil {
		b.recordAcquire(time.Since(start), outcomeForErr(err))
		return err
	}
	b.recordAcquire(time.Since(start), outcomeAcquired)

	defer func() {
		b.release()
		if r := recover(); r != nil {
			// Record before re-raising so the panic is observable even
			// when the caller's recover-middleware suppresses output.
			b.logger.Error("bulkhead: fn panicked, releasing slot before re-raise",
				"name", b.name,
				redact.Panic(r),
				"stack", string(debug.Stack()),
			)
			// Re-raise so consumer panic-recovery middleware sees it.
			panic(r)
		}
	}()

	if err := fn(ctx); err != nil {
		return redact.WrapError("bulkhead/"+b.name, err)
	}
	return nil
}

// acquire grabs one slot or returns an error. Branches:
//   - immediate take (semaphore had room).
//   - immediate reject (MaxQueueWait == 0 and full).
//   - wait up to MaxQueueWait (or ctx, whichever fires first).
func (b *Bulkhead) acquire(ctx context.Context) error {
	// Fast path: slot available immediately.
	select {
	case b.semaphore <- struct{}{}:
		b.current.Add(1)
		return nil
	default:
	}

	if b.maxWait <= 0 {
		// Caller asked for immediate rejection on full.
		return fmt.Errorf("%w (cap=%d)", ErrBulkheadFull, b.max)
	}

	timer := time.NewTimer(b.maxWait)
	defer timer.Stop()

	select {
	case b.semaphore <- struct{}{}:
		b.current.Add(1)
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return fmt.Errorf("%w after %s wait (cap=%d)", ErrBulkheadFull, b.maxWait, b.max)
	}
}

func (b *Bulkhead) release() {
	// Decrement current BEFORE returning the semaphore slot so the
	// invariant `current <= capacity` holds at every observation.
	// If we returned the slot first, another goroutine could acquire
	// it and increment current to capacity+1 before this Add(-1)
	// landed. The momentary undershoot (current < actual in-flight)
	// is harmless because the semaphore is the authoritative cap.
	b.current.Add(-1)
	<-b.semaphore
}

const (
	outcomeAcquired = "acquired"
	outcomeFull     = "full"
	outcomeCtx      = "ctx_cancelled"
)

func outcomeForErr(err error) string {
	if errors.Is(err, ErrBulkheadFull) {
		return outcomeFull
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return outcomeCtx
	}
	return "error"
}

// Metrics holds the kit-side Prometheus collectors for bulkhead
// observability. Construct via [NewMetrics]; wire into a Bulkhead
// via [WithMetrics].
type Metrics struct {
	acquisitions      *prometheus.CounterVec
	acquireDuration   *prometheus.HistogramVec
}

// MetricsOption configures [NewMetrics].
type MetricsOption func(*metricsConfig)

type metricsConfig struct {
	registerer prometheus.Registerer
}

// WithRegisterer pins the Prometheus registerer used for bulkhead
// metrics. Nil panics so misconfiguration surfaces at startup.
func WithRegisterer(reg prometheus.Registerer) MetricsOption {
	if reg == nil {
		panic("bulkhead: WithRegisterer requires a non-nil registerer")
	}
	return func(c *metricsConfig) { c.registerer = reg }
}

// NewMetrics constructs and registers the bulkhead metric set.
func NewMetrics(opts ...MetricsOption) *Metrics {
	cfg := metricsConfig{registerer: prometheus.DefaultRegisterer}
	for _, opt := range opts {
		if opt == nil {
			panic("bulkhead: NewMetrics option must not be nil")
		}
		opt(&cfg)
	}
	// Namespace+Name (no Subsystem): the bulkhead is its own
	// domain; splitting "bulkhead_acquisitions_total" into
	// Namespace="bulkhead" + Name="acquisitions_total" preserves
	// the wire form while aligning the Go struct shape with the
	// kit-wide convention (cache/compute, redis/queue, http/ratelimit,
	// etc.) audited in the wave-184 consistency review.
	m := &Metrics{
		acquisitions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "bulkhead",
			Name:      "acquisitions_total",
			Help:      "Total bulkhead acquisition attempts by name and outcome (acquired / full / ctx_cancelled).",
		}, []string{"name", "outcome"}),
		acquireDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "bulkhead",
			Name:      "acquire_duration_seconds",
			Help:      "Time spent acquiring a bulkhead slot, by name and outcome.",
			Buckets:   []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 5},
		}, []string{"name", "outcome"}),
	}
	m.acquisitions = promutil.MustRegisterOrGet(cfg.registerer, m.acquisitions)
	m.acquireDuration = promutil.MustRegisterOrGet(cfg.registerer, m.acquireDuration)
	return m
}

func (b *Bulkhead) recordAcquire(d time.Duration, outcome string) {
	if b.metrics == nil {
		return
	}
	b.metrics.acquisitions.WithLabelValues(b.name, outcome).Inc()
	b.metrics.acquireDuration.WithLabelValues(b.name, outcome).Observe(d.Seconds())
}
