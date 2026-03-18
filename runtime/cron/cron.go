package cron

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	robcron "github.com/robfig/cron/v3"

	"github.com/bds421/rho-kit/observability/logattr"
)

// Scheduler runs periodic jobs on cron schedules. It wraps robfig/cron/v3 with
// structured logging, Prometheus metrics, and context-based lifecycle.
type Scheduler struct {
	cron    *robcron.Cron
	logger  *slog.Logger
	metrics *metrics
	ctx     context.Context
	cancel  context.CancelFunc
}

// Option configures a Scheduler.
type Option func(*config)

type config struct {
	location *time.Location
	registry prometheus.Registerer
}

// WithLocation sets the timezone for cron schedule evaluation.
// Default: time.UTC.
func WithLocation(loc *time.Location) Option {
	return func(c *config) { c.location = loc }
}

// WithRegistry sets the Prometheus registerer for cron metrics.
// Default: prometheus.DefaultRegisterer.
func WithRegistry(reg prometheus.Registerer) Option {
	return func(c *config) { c.registry = reg }
}

// New creates a Scheduler. Jobs are added with [Scheduler.Add] and the
// scheduler is started with [Scheduler.Start].
func New(logger *slog.Logger, opts ...Option) *Scheduler {
	if logger == nil {
		logger = slog.Default()
	}
	cfg := config{
		location: time.UTC,
	}
	for _, o := range opts {
		o(&cfg)
	}

	c := robcron.New(
		robcron.WithLocation(cfg.location),
		robcron.WithChain(robcron.SkipIfStillRunning(&slogCronLogger{logger: logger})),
	)

	return &Scheduler{
		cron:    c,
		logger:  logger,
		metrics: newMetrics(cfg.registry),
	}
}

// Add registers a named job with a cron schedule expression. The job function
// receives a context that is cancelled when the scheduler shuts down.
//
// Panics if the schedule expression is invalid.
func (s *Scheduler) Add(name, schedule string, fn func(ctx context.Context) error) {
	wrapped := s.wrapJob(name, fn)
	if _, err := s.cron.AddFunc(schedule, wrapped); err != nil {
		panic(fmt.Sprintf("cron: invalid schedule %q for job %q: %v", schedule, name, err))
	}
	s.logger.Info("cron job registered", "name", name, "schedule", schedule)
}

// Start begins running scheduled jobs and blocks until ctx is cancelled.
// Implements the lifecycle.Component interface.
func (s *Scheduler) Start(ctx context.Context) error {
	s.ctx, s.cancel = context.WithCancel(ctx)
	s.cron.Start()
	s.logger.Info("cron scheduler started")
	<-s.ctx.Done()
	return nil
}

// Stop halts the scheduler and waits for any running jobs to complete.
// Implements the lifecycle.Component interface.
func (s *Scheduler) Stop(_ context.Context) error {
	if s.cancel != nil {
		s.cancel()
	}
	stopCtx := s.cron.Stop()
	<-stopCtx.Done()
	s.logger.Info("cron scheduler stopped")
	return nil
}

// wrapJob wraps a job function with logging, metrics, and panic recovery.
func (s *Scheduler) wrapJob(name string, fn func(ctx context.Context) error) func() {
	return func() {
		ctx := s.ctx
		if ctx == nil {
			ctx = context.Background()
		}

		start := time.Now()
		status := "success"

		defer func() {
			if r := recover(); r != nil {
				status = "panic"
				s.logger.Error("cron job panicked",
					"name", name,
					"panic", r,
					"stack", string(debug.Stack()),
				)
			}
			duration := time.Since(start)
			s.metrics.runs.WithLabelValues(name, status).Inc()
			s.metrics.duration.WithLabelValues(name).Observe(duration.Seconds())
			s.logger.Info("cron job finished",
				"name", name,
				"status", status,
				logattr.Duration(duration),
			)
		}()

		if err := fn(ctx); err != nil {
			status = "error"
			s.logger.Error("cron job failed",
				"name", name,
				logattr.Error(err),
			)
		}
	}
}

// slogCronLogger adapts slog.Logger to robfig/cron's Logger interface.
type slogCronLogger struct {
	logger *slog.Logger
}

func (l *slogCronLogger) Info(msg string, keysAndValues ...any) {
	l.logger.Info(msg, keysAndValues...)
}

func (l *slogCronLogger) Error(err error, msg string, keysAndValues ...any) {
	args := append([]any{logattr.Error(err)}, keysAndValues...)
	l.logger.Error(msg, args...)
}
