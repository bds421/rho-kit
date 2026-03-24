package eventbus

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"

	"github.com/bds421/rho-kit/observability/logattr"
)

// asyncTask represents a single async handler invocation queued for execution.
type asyncTask struct {
	ctx       context.Context
	eventName string
	handler   registeredHandler
	event     any
}

// workerPool provides bounded async event dispatch.
// Tasks are submitted to a buffered channel and consumed by a fixed number of
// worker goroutines. When the queue is full, tasks are dropped.
type workerPool struct {
	workers int
	queue   chan asyncTask
	logger  *slog.Logger
	onError func(ctx context.Context, eventName string, handlerName string, err error)
	metrics *poolMetrics
	wg      sync.WaitGroup
}

// newWorkerPool creates a worker pool with the given number of workers and
// channel buffer size. The pool does not start until [workerPool.start] is called.
func newWorkerPool(
	workers, bufSize int,
	logger *slog.Logger,
	onError func(ctx context.Context, eventName string, handlerName string, err error),
	m *poolMetrics,
) *workerPool {
	return &workerPool{
		workers: workers,
		queue:   make(chan asyncTask, bufSize),
		logger:  logger,
		onError: onError,
		metrics: m,
	}
}

// submit enqueues a task for async execution. Returns false if the queue is
// full, meaning the event was dropped.
func (p *workerPool) submit(task asyncTask) bool {
	select {
	case p.queue <- task:
		return true
	default:
		p.logger.Warn("event dropped: worker pool queue full",
			slog.String("event", task.eventName),
			slog.String("handler", task.handler.name),
		)
		if p.metrics != nil {
			p.metrics.dropped.Inc()
		}
		return false
	}
}

// start launches worker goroutines and blocks until ctx is cancelled.
// After ctx cancellation, the channel is closed so workers can drain remaining
// tasks before exiting.
func (p *workerPool) start(ctx context.Context) {
	for i := range p.workers {
		p.wg.Add(1)
		go p.worker(ctx, i)
	}
	<-ctx.Done()
}

// stop closes the queue channel and waits for all workers to finish
// processing remaining tasks.
func (p *workerPool) stop() {
	close(p.queue)
	p.wg.Wait()
}

// worker is the main loop for a single pool worker. It reads tasks from the
// queue and executes them with panic recovery.
func (p *workerPool) worker(_ context.Context, id int) {
	defer p.wg.Done()
	p.logger.Debug("worker started", slog.Int("worker_id", id))

	for task := range p.queue {
		if p.metrics != nil {
			p.metrics.activeWorkers.Inc()
		}
		p.executeTask(task)
		if p.metrics != nil {
			p.metrics.activeWorkers.Dec()
			p.metrics.processed.WithLabelValues(task.eventName).Inc()
		}
	}

	p.logger.Debug("worker stopped", slog.Int("worker_id", id))
}

// executeTask runs a single handler with panic recovery.
func (p *workerPool) executeTask(task asyncTask) {
	defer func() {
		if rec := recover(); rec != nil {
			err := fmt.Errorf("panic: %v", rec)
			p.logger.Error("async event handler panicked",
				slog.String("event", task.eventName),
				slog.String("handler", task.handler.name),
				slog.Any("panic", rec),
				slog.String("stack", string(debug.Stack())),
			)
			if p.onError != nil {
				p.onError(task.ctx, task.eventName, task.handler.name, err)
			}
		}
	}()

	if err := task.handler.fn(task.ctx, task.event); err != nil {
		p.logger.Warn("async event handler error",
			slog.String("event", task.eventName),
			slog.String("handler", task.handler.name),
			logattr.Error(err),
		)
		if p.onError != nil {
			p.onError(task.ctx, task.eventName, task.handler.name, err)
		}
	}
}
