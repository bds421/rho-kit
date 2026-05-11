package cron

import (
	"context"
	"errors"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	robcron "github.com/robfig/cron/v3"

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/observability/v2/logattr"
	"github.com/bds421/rho-kit/observability/v2/promutil"
)

// defaultJobTimeout caps any cron job that has no explicit
// SetJobTimeout (audit FR-093). 30 minutes is generous for typical
// scheduled work — index rebuilds, data exports — and bounds the
// "forever-stuck job" failure mode that causes SkipIfStillRunning
// to skip every future tick.
const defaultJobTimeout = 30 * time.Minute

// Scheduler runs periodic jobs on cron schedules. It wraps robfig/cron/v3 with
// structured logging, Prometheus metrics, and context-based lifecycle.
type Scheduler struct {
	cron    *robcron.Cron
	logger  *slog.Logger
	metrics *metrics

	mu      sync.RWMutex // protects ctx and cancel against the Start/Stop/wrapJob race
	ctx     context.Context
	cancel  context.CancelFunc
	started bool // set under mu inside Start; rejects re-entry
	stopped bool // set under mu inside Stop; rejects Start after Stop-before-Start

	jobTimeouts map[string]time.Duration // per-job timeout; nil/missing = inherit scheduler ctx

	leaderFn func() bool // optional gate; jobs skip when this returns false
}

// Option configures a Scheduler.
type Option func(*config)

type config struct {
	location *time.Location
	registry prometheus.Registerer
	leaderFn func() bool
}

// WithLocation sets the timezone for cron schedule evaluation.
// Default: time.UTC.
func WithLocation(loc *time.Location) Option {
	if loc == nil {
		panic("cron: WithLocation requires a non-nil location")
	}
	return func(c *config) { c.location = loc }
}

// WithRegistry sets the Prometheus registerer for cron metrics.
// Default: prometheus.DefaultRegisterer.
func WithRegistry(reg prometheus.Registerer) Option {
	return func(c *config) { c.registry = reg }
}

// WithLeaderGate gates every job's execution on the supplied
// predicate. When fn returns false, the scheduled job is skipped (a
// `cron_job_skipped_not_leader_total` counter is incremented and a
// debug log line emitted) but the schedule keeps ticking.
//
// Use this with [github.com/bds421/rho-kit/infra/v2/leaderelection]:
// `WithLeaderGate(elector.IsLeader)` ensures cron jobs run only on
// the elected leader replica without each job needing its own gate.
//
// The predicate is called once per scheduled tick; it must be
// non-blocking and concurrency-safe.
func WithLeaderGate(fn func() bool) Option {
	if fn == nil {
		panic("cron: WithLeaderGate requires a non-nil predicate")
	}
	return func(c *config) { c.leaderFn = fn }
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
		if o == nil {
			panic("cron: option must not be nil")
		}
		o(&cfg)
	}

	c := robcron.New(
		robcron.WithLocation(cfg.location),
		robcron.WithChain(robcron.SkipIfStillRunning(&slogCronLogger{logger: logger})),
	)

	return &Scheduler{
		cron:        c,
		logger:      logger,
		metrics:     newMetrics(cfg.registry),
		jobTimeouts: make(map[string]time.Duration),
		leaderFn:    cfg.leaderFn,
	}
}

// WithJobTimeout configures a per-run timeout for the named job. The job's
// context is derived from the scheduler ctx with WithTimeout(d), so a
// long-running job that ignores cancellation will see ctx.Err() == context
// .DeadlineExceeded after d.
//
// FR-094 [LOW]: panics on d <= 0 — pre-fix the call silently
// returned without configuring a timeout, leaving the job
// unbounded. Pass [defaultJobTimeout] explicitly when no per-job
// override is needed.
//
// Call AFTER Add — the timeout takes effect on the next tick. Repeated
// calls override the previous timeout.
func (s *Scheduler) SetJobTimeout(name string, d time.Duration) {
	if d <= 0 {
		panic("cron: SetJobTimeout requires d > 0")
	}
	if err := promutil.ValidateStaticLabelValue("job name", name); err != nil {
		panic("cron: invalid job name")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobTimeouts[name] = d
}

// Add registers a named job with a cron schedule expression. The job function
// receives a context that is cancelled when the scheduler shuts down.
//
// Panics if the schedule expression is invalid, the name is empty, or fn
// is nil — invalid wiring should fail at registration, not the first time
// the schedule fires.
func (s *Scheduler) Add(name, schedule string, fn func(ctx context.Context) error) {
	if name == "" {
		panic("cron: Scheduler.Add requires a non-empty name")
	}
	if err := promutil.ValidateStaticLabelValue("job name", name); err != nil {
		panic("cron: invalid job name")
	}
	if fn == nil {
		panic("cron: Scheduler.Add requires a non-nil job function")
	}
	wrapped := s.wrapJob(name, fn)
	if _, err := s.cron.AddFunc(schedule, wrapped); err != nil {
		panic("cron: invalid schedule for job")
	}
	s.logger.Info("cron job registered", "name", name, "schedule", schedule)
}

// Start begins running scheduled jobs and blocks until ctx is cancelled.
// Implements the lifecycle.Component interface.
//
// Returns an error if Start has already been called on this Scheduler.
// A Scheduler is intentionally one-shot: a second Start would race the
// previous Start's still-live context against in-progress wrapJob
// closures that captured the old ctx pointer.
func (s *Scheduler) Start(ctx context.Context) error {
	if ctx == nil {
		return errors.New("cron: Scheduler.Start requires a non-nil context")
	}
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return errors.New("cron: Scheduler already started")
	}
	if s.stopped {
		s.mu.Unlock()
		return errors.New("cron: Scheduler already stopped")
	}
	s.started = true
	s.ctx, s.cancel = context.WithCancel(ctx)
	startedCtx := s.ctx
	s.mu.Unlock()

	s.cron.Start()
	s.logger.Info("cron scheduler started")
	<-startedCtx.Done()
	return nil
}

// Stop halts the scheduler and waits for any running jobs to complete, or
// until the supplied context expires (whichever comes first). A long-running
// job that ignores its own context cancellation will not block Stop past the
// caller's deadline — the scheduler returns ctx.Err and the job continues in
// the background until it finishes naturally.
func (s *Scheduler) Stop(ctx context.Context) error {
	if ctx == nil {
		return errors.New("cron: Scheduler.Stop requires a non-nil context")
	}
	s.mu.Lock()
	cancel := s.cancel
	s.stopped = true
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	stopCtx := s.cron.Stop()
	select {
	case <-stopCtx.Done():
		s.logger.Info("cron scheduler stopped")
		return nil
	case <-ctx.Done():
		s.logger.Warn("cron scheduler shutdown deadline exceeded; running jobs left in background",
			redact.Error(ctx.Err()))
		return ctx.Err()
	}
}

// wrapJob wraps a job function with logging, metrics, and panic recovery.
func (s *Scheduler) wrapJob(name string, fn func(ctx context.Context) error) func() {
	return func() {
		s.mu.RLock()
		baseCtx := s.ctx
		timeout := s.jobTimeouts[name]
		s.mu.RUnlock()

		if baseCtx == nil {
			baseCtx = context.Background()
		}

		if s.leaderFn != nil && !s.isLeader(name) {
			s.metrics.skippedNotLeader.WithLabelValues(name).Inc()
			s.logger.Debug("cron job skipped (not leader)", "job", name)
			return
		}

		// FR-093 [MED]: install [defaultJobTimeout] when the job
		// has no explicit timeout. A job that blocks forever would
		// otherwise interact badly with SkipIfStillRunning (every
		// future tick is skipped) and survive past shutdown until
		// it returns of its own accord. Override per-job via
		// [Scheduler.SetJobTimeout]; opt out by passing a generous
		// duration (e.g. 24h) for legitimately long-running jobs.
		if timeout <= 0 {
			timeout = defaultJobTimeout
		}
		ctx, cancel := context.WithTimeout(baseCtx, timeout)
		defer cancel()

		start := time.Now()
		status := "success"

		defer func() {
			if r := recover(); r != nil {
				status = "panic"
				s.logger.Error("cron job panicked",
					"name", name,
					redact.Panic(r),
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

func (s *Scheduler) isLeader(job string) (leader bool) {
	defer func() {
		if rec := recover(); rec != nil {
			s.logger.Error("cron leader gate panicked",
				"job", job,
				redact.Panic(rec),
				"stack", string(debug.Stack()),
			)
			leader = false
		}
	}()
	return s.leaderFn()
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
