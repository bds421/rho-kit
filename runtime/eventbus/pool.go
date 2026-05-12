package eventbus

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"sync/atomic"

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/observability/v2/logattr"
)

// asyncTask represents a single async handler invocation queued for execution.
// The context from Publish is stored in the task. If the publisher's context is
// cancelled before the task is processed, the handler receives a cancelled context.
//
// asyncTask instances are pooled via taskPool to reduce GC pressure under high
// throughput. Fields must be cleared before returning to the pool.
type asyncTask struct {
	ctx       context.Context
	eventName string
	handler   registeredHandler
	event     any
}

// taskPool reduces allocation pressure for asyncTask under high throughput.
var taskPool = sync.Pool{
	New: func() any { return &asyncTask{} },
}

// workerPool provides bounded async event dispatch.
// Tasks are submitted to a buffered channel and consumed by a fixed number of
// worker goroutines. When the queue is full, tasks are dropped.
type workerPool struct {
	workers   int
	queue     chan *asyncTask
	logger    *slog.Logger
	onError   func(ctx context.Context, eventName string, handlerName string, err error)
	metrics   *poolMetrics
	wg        sync.WaitGroup
	closeOnce sync.Once
	stopped   atomic.Bool
	started   atomic.Bool
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
		queue:   make(chan *asyncTask, bufSize),
		logger:  logger,
		onError: onError,
		metrics: m,
	}
}

// submit enqueues a task for async execution. The behavior under queue
// saturation depends on policy:
//   - [OnFullDrop]: returns (false, nil); the event is dropped and counted.
//   - [OnFullBlock]: blocks until queue space is available or pubCtx is
//     cancelled. Returns (false, ctx.Err()) on cancellation.
//   - [OnFullError]: returns (false, nil) without enqueuing; the caller is
//     responsible for synthesizing an error. The dropped counter is
//     incremented so [OnFullError] saturation is still observable.
//
// On any non-enqueue return path, the task is cleared and returned to the
// taskPool so the sync.Pool stays effective under saturation (the precise
// scenario the OnFull policy was added for).
//
// Calling submit after stop is handled gracefully (recovered from channel-close
// panic). The event is dropped and counted.
func (p *workerPool) submit(task *asyncTask, policy OnFullPolicy, pubCtx context.Context) (ok bool, err error) {
	if !p.started.Load() {
		// FR-090 [MED]: pre-fix the pool buffered events before
		// Start() and dropped them on stop without ever running. The
		// new behaviour is to refuse submit-before-Start outright so
		// the wiring bug surfaces at the call site rather than
		// silently losing async events.
		releaseTask(task)
		return false, ErrQueueFull
	}

	if p.stopped.Load() {
		if p.metrics != nil {
			p.metrics.dropped.Inc()
		}
		p.logger.Warn("eventbus: submit after stop, event dropped",
			slog.String("event", task.eventName),
		)
		releaseTask(task)
		// Surface the shutdown to OnFullError publishers; OnFullDrop /
		// OnFullBlock policies suppress this in dispatchAsync.
		return false, ErrStopped
	}

	// Use recover to handle the tiny race window between stopped check and channel close.
	// Surface ErrStopped to OnFullError callers so a shutdown-window submit is
	// not silently swallowed.
	defer func() {
		if r := recover(); r != nil {
			ok = false
			err = ErrStopped
			if p.metrics != nil {
				p.metrics.dropped.Inc()
			}
			releaseTask(task)
		}
	}()

	if policy == OnFullBlock {
		select {
		case p.queue <- task:
			if p.metrics != nil {
				p.metrics.queueDepth.Set(float64(len(p.queue)))
			}
			return true, nil
		case <-pubCtx.Done():
			if p.metrics != nil {
				p.metrics.dropped.Inc()
			}
			p.logger.Warn("eventbus: publisher context cancelled while waiting for queue space",
				slog.String("event", task.eventName),
				slog.String("handler", task.handler.name),
			)
			releaseTask(task)
			return false, pubCtx.Err()
		}
	}

	select {
	case p.queue <- task:
		// queueDepth is approximate: len(p.queue) is non-atomic relative to the
		// channel operation. Acceptable for Prometheus gauges.
		if p.metrics != nil {
			p.metrics.queueDepth.Set(float64(len(p.queue)))
		}
		return true, nil
	default:
		if p.metrics != nil {
			p.metrics.dropped.Inc()
		}
		p.logger.Warn("eventbus: worker pool queue full, event dropped",
			slog.String("event", task.eventName),
			slog.String("handler", task.handler.name),
		)
		releaseTask(task)
		return false, nil
	}
}

// releaseTask clears the asyncTask's strong references and returns it to
// the pool. Call this on every non-enqueue path so the sync.Pool remains
// effective and the dropped event's payload is not pinned in memory.
func releaseTask(task *asyncTask) {
	task.ctx = nil
	task.eventName = ""
	task.handler = registeredHandler{}
	task.event = nil
	taskPool.Put(task)
}

func (p *workerPool) startWorkers() {
	if p.started.Swap(true) {
		return
	}
	for i := range p.workers {
		p.wg.Add(1)
		go p.worker(i)
	}
}

// start launches worker goroutines and blocks until ctx is cancelled.
// After ctx cancellation, callers close the channel through stop so
// workers can drain remaining tasks before exiting.
func (p *workerPool) start(ctx context.Context) {
	p.startWorkers()
	<-ctx.Done()
}

// stop closes the queue channel and waits for all workers to finish
// processing remaining tasks. It is safe to call multiple times.
func (p *workerPool) stop() {
	p.stopped.Store(true)
	p.closeOnce.Do(func() { close(p.queue) })
	p.wg.Wait()
	if p.metrics != nil {
		p.metrics.queueDepth.Set(0)
	}
}

// worker is the main loop for a single pool worker. It reads tasks from the
// queue and executes them with panic recovery.
func (p *workerPool) worker(id int) {
	defer p.wg.Done()
	p.logger.Debug("worker started", slog.Int("worker_id", id))

	for task := range p.queue {
		if p.metrics != nil {
			// queueDepth is approximate: len(p.queue) is non-atomic relative to the
			// channel operation. Acceptable for Prometheus gauges.
			p.metrics.queueDepth.Set(float64(len(p.queue)))
			p.metrics.activeWorkers.Inc()
		}
		func() {
			if p.metrics != nil {
				defer p.metrics.activeWorkers.Dec()
			}
			p.executeTask(task)
		}()
		// Note: panicked events are counted as processed. The onError callback
		// and log provide distinction.
		if p.metrics != nil {
			p.metrics.processed.WithLabelValues(task.eventName).Inc()
		}
		// Clear fields to avoid retaining references, then return to pool.
		releaseTask(task)
	}

	p.logger.Debug("worker stopped", slog.Int("worker_id", id))
}

// executeTask runs a single handler with panic recovery.
func (p *workerPool) executeTask(task *asyncTask) {
	defer func() {
		if rec := recover(); rec != nil {
			err := fmt.Errorf("panic: %s", redact.PanicValue(rec))
			p.logger.Error("async event handler panicked",
				slog.String("event", task.eventName),
				slog.String("handler", task.handler.name),
				redact.Panic(rec),
				slog.String("stack", string(debug.Stack())),
			)
			callOnError(p.logger, p.onError, task.ctx, task.eventName, task.handler.name, err)
		}
	}()

	if err := task.handler.fn(task.ctx, task.event); err != nil {
		p.logger.Warn("async event handler error",
			slog.String("event", task.eventName),
			slog.String("handler", task.handler.name),
			logattr.Error(err),
		)
		callOnError(p.logger, p.onError, task.ctx, task.eventName, task.handler.name, err)
	}
}
