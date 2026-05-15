// Package batchworker provides a periodic batch processor that runs a
// user-supplied function at a configurable interval with jitter, metrics,
// panic recovery, and graceful shutdown.
//
// Use it for tasks like retrying failed webhooks, syncing external state,
// cleaning up expired records, or any periodic batch operation.
//
// The Worker implements lifecycle.Component (Start/Stop) and can be
// registered directly with the lifecycle runner.
package batchworker

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/observability/v2/logattr"
	"github.com/bds421/rho-kit/observability/v2/promutil"
)

// Worker runs a batch function periodically with jitter, metrics, and
// panic recovery. Safe for concurrent use.
type Worker struct {
	name     string
	interval time.Duration
	jitter   float64 // 0.0–1.0; fraction of interval to randomize
	timeout  time.Duration
	fn       func(ctx context.Context) error
	logger   *slog.Logger
	metrics  *metrics
	done     chan struct{}
	doneOnce sync.Once
	started  atomic.Bool
	stopped  bool
	mu       sync.Mutex
	cancel   context.CancelFunc
}

// Option configures a Worker.
type Option func(*config)

type config struct {
	jitter   float64
	timeout  time.Duration
	logger   *slog.Logger
	registry prometheus.Registerer
}

// WithJitter sets the jitter fraction (0.0–1.0) applied to each interval.
// For example, WithJitter(0.1) on a 60s interval adds 0–6s of random delay.
// Default: 0.1 (10%).
//
// FR-092 [LOW]: panics on fractions outside [0, 1] so a typo
// surfaces at startup rather than silently keeping the default.
func WithJitter(fraction float64) Option {
	if fraction < 0 || fraction > 1 {
		panic("batchworker: WithJitter requires 0 <= fraction <= 1")
	}
	return func(c *config) {
		c.jitter = fraction
	}
}

// WithTimeout sets the per-batch wall-clock budget. When the budget
// elapses the per-batch context is cancelled (`ctx.Done()` fires); the
// timeout is COOPERATIVE — the batch function must observe ctx.Done()
// and return promptly for the bound to take effect. A batch that ignores
// its context will keep running past the timeout, blocking subsequent
// ticks; Worker.Stop in turn waits for it. Long-running CPU loops or
// SQL calls without context propagation should be re-shaped to honour
// the supplied context. Default: 2 minutes.
//
// FR-092 [LOW]: panics on d <= 0 so an unbounded-batch wiring error
// surfaces at startup.
func WithTimeout(d time.Duration) Option {
	if d <= 0 {
		panic("batchworker: WithTimeout requires d > 0")
	}
	return func(c *config) {
		c.timeout = d
	}
}

// WithLogger sets the structured logger. A nil logger is normalized to
// slog.Default() so test hooks that pass nil cannot create latent panics on
// start or panic-recovery paths. Default: slog.Default().
func WithLogger(l *slog.Logger) Option {
	return func(c *config) {
		if l == nil {
			l = slog.Default()
		}
		c.logger = l
	}
}

// WithRegisterer sets the Prometheus registerer for worker metrics.
// Default: prometheus.DefaultRegisterer.
func WithRegisterer(reg prometheus.Registerer) Option {
	return func(c *config) { c.registry = reg }
}

// New creates a Worker that calls fn every interval.
//
// The name is used for logging and Prometheus labels. The fn receives a
// context that is cancelled on shutdown or when the per-batch timeout
// elapses.
func New(name string, interval time.Duration, fn func(ctx context.Context) error, opts ...Option) *Worker {
	if name == "" {
		panic("batchworker: New: name must not be empty")
	}
	if err := promutil.ValidateStaticLabelValue("worker name", name); err != nil {
		panic("batchworker: New: invalid name")
	}
	if interval <= 0 {
		panic("batchworker: New: interval must be positive")
	}
	if fn == nil {
		panic("batchworker: New: fn must not be nil")
	}

	cfg := config{
		jitter:  0.1,
		timeout: 2 * time.Minute,
		logger:  slog.Default(),
	}
	for _, o := range opts {
		if o == nil {
			panic("batchworker: New: option must not be nil")
		}
		o(&cfg)
	}

	return &Worker{
		name:     name,
		interval: interval,
		jitter:   cfg.jitter,
		timeout:  cfg.timeout,
		fn:       fn,
		logger:   cfg.logger,
		metrics:  newMetrics(cfg.registry),
		done:     make(chan struct{}),
	}
}

// Start begins the periodic batch loop and blocks until ctx is cancelled
// (either externally or by [Worker.Stop]).
// Implements the lifecycle.Component interface.
func (w *Worker) Start(ctx context.Context) error {
	if ctx == nil {
		return errors.New("batchworker: Start requires a non-nil context")
	}
	// Set cancel + started together under a single lock so a concurrent
	// Stop sees both as a unit. Without this, Stop could observe
	// started=false right after Start has set it, then later Start would
	// install a cancel that nothing ever invokes.
	w.mu.Lock()
	if w.started.Load() {
		w.mu.Unlock()
		return errors.New("batchworker: Worker already started")
	}
	if w.stopped {
		w.mu.Unlock()
		return errors.New("batchworker: Worker already stopped")
	}
	ctx, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	w.started.Store(true)
	w.mu.Unlock()
	defer cancel()

	w.logger.Info("batch worker started", "name", w.name, "interval", w.interval)

	// Run once immediately on start.
	w.runBatch(ctx)

	for {
		delay := w.nextDelay()
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			w.doneOnce.Do(func() { close(w.done) })
			w.logger.Info("batch worker stopped", "name", w.name)
			return nil
		case <-timer.C:
			w.runBatch(ctx)
		}
	}
}

// Stop cancels the worker's internal context (which makes the in-flight
// batch's ctx.Done() fire) and waits for the batch loop to finish or for
// the supplied ctx to expire.
//
// Stop is COOPERATIVE: it does not abort a running batch. Cancellation
// propagates by the batch function observing ctx.Done() and returning. A
// batch that ignores its context keeps running until the per-batch
// timeout elapses (or longer if it ignores that too); during that window
// Stop blocks waiting for the loop to drain, and the caller's ctx is the
// only mechanism for bounding the wait. When the caller's ctx fires
// first, Stop returns ctx.Err() — the worker goroutine and any
// uncooperative batch may still be running afterwards.
//
// Calling Stop without first cancelling the parent ctx is safe — Stop
// installs its own cancel signal. Stop is also safe to call before
// Start: if the worker never started, Stop closes `done` itself and
// returns immediately.
//
// Implements the lifecycle.Component interface.
func (w *Worker) Stop(ctx context.Context) error {
	if ctx == nil {
		return errors.New("batchworker: Stop requires a non-nil context")
	}
	w.mu.Lock()
	started := w.started.Load()
	cancel := w.cancel
	w.stopped = true
	w.mu.Unlock()
	if !started {
		w.doneOnce.Do(func() { close(w.done) })
		return nil
	}
	if cancel != nil {
		cancel()
	}
	select {
	case <-w.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *Worker) runBatch(parentCtx context.Context) {
	ctx, cancel := context.WithTimeout(parentCtx, w.timeout)
	defer cancel()

	start := time.Now()
	status := "success"

	defer func() {
		if r := recover(); r != nil {
			status = "panic"
			w.logger.Error("batch worker panicked",
				"name", w.name,
				redact.Panic(r),
				"stack", string(debug.Stack()),
			)
		}
		duration := time.Since(start)
		w.metrics.runs.WithLabelValues(w.name, status).Inc()
		w.metrics.duration.WithLabelValues(w.name).Observe(duration.Seconds())
		w.logger.Info("batch worker finished",
			"name", w.name,
			"status", status,
			logattr.Duration(duration),
		)
	}()

	if err := w.fn(ctx); err != nil {
		status = "error"
		w.logger.Error("batch worker failed",
			"name", w.name,
			logattr.Error(err),
		)
	}
}

func (w *Worker) nextDelay() time.Duration {
	if w.jitter <= 0 {
		return w.interval
	}
	maxJitter := time.Duration(float64(w.interval) * w.jitter)
	// rand.Int64N panics on n <= 0; for sub-nanosecond truncation or when
	// the user passes interval=1ns with the default jitter, return the
	// base interval rather than crashing the worker loop.
	if maxJitter <= 0 {
		return w.interval
	}
	// Symmetric jitter centered on interval: distribute uniformly across
	// [-maxJitter, +maxJitter]. Earlier versions only added jitter
	// (interval ≥ true), which prevents thundering-herd on the upper edge
	// but lets the cluster drift later than the configured interval.
	span := int64(maxJitter) * 2
	offset := time.Duration(rand.Int64N(span)) - maxJitter
	d := w.interval + offset
	const minDelay = 10 * time.Millisecond
	if d < minDelay {
		// Pathological case: jitter > 100% combined with a tiny
		// interval can produce a non-positive value. Floor to 10ms
		// instead of 1ns to avoid a CPU-pinning hot loop when the
		// struct is built directly bypassing the New constructor.
		return minDelay
	}
	return d
}
