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
	"log/slog"
	"math/rand/v2"
	"runtime/debug"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/bds421/rho-kit/observability/logattr"
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
func WithJitter(fraction float64) Option {
	return func(c *config) {
		if fraction >= 0 && fraction <= 1 {
			c.jitter = fraction
		}
	}
}

// WithTimeout sets the maximum duration for each batch execution.
// If the batch function exceeds this timeout, its context is cancelled.
// Default: 2 minutes.
func WithTimeout(d time.Duration) Option {
	return func(c *config) {
		if d > 0 {
			c.timeout = d
		}
	}
}

// WithLogger sets the structured logger. Default: slog.Default().
func WithLogger(l *slog.Logger) Option {
	return func(c *config) { c.logger = l }
}

// WithRegistry sets the Prometheus registerer for worker metrics.
// Default: prometheus.DefaultRegisterer.
func WithRegistry(reg prometheus.Registerer) Option {
	return func(c *config) { c.registry = reg }
}

// New creates a Worker that calls fn every interval.
//
// The name is used for logging and Prometheus labels. The fn receives a
// context that is cancelled on shutdown or when the per-batch timeout
// elapses.
func New(name string, interval time.Duration, fn func(ctx context.Context) error, opts ...Option) *Worker {
	if name == "" {
		panic("batchworker: name must not be empty")
	}
	if interval <= 0 {
		panic("batchworker: interval must be positive")
	}
	if fn == nil {
		panic("batchworker: fn must not be nil")
	}

	cfg := config{
		jitter:  0.1,
		timeout: 2 * time.Minute,
		logger:  slog.Default(),
	}
	for _, o := range opts {
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

// Start begins the periodic batch loop and blocks until ctx is cancelled.
// Implements the lifecycle.Component interface.
func (w *Worker) Start(ctx context.Context) error {
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

// Stop signals the worker to stop. The worker finishes its current batch
// (bounded by the per-batch timeout) before returning.
// Implements the lifecycle.Component interface.
func (w *Worker) Stop(ctx context.Context) error {
	// Wait for Start() to finish, or give up when the stop context expires.
	select {
	case <-w.done:
	case <-ctx.Done():
	}
	return nil
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
				"panic", r,
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
	jitterDuration := time.Duration(rand.Int64N(int64(maxJitter)))
	return w.interval + jitterDuration
}
