package redisqueue

import (
	"context"
	"log/slog"
	"runtime/debug"
	"sync"

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/infra/redis/v2"
)

// Binding pairs a queue name with its handler.
type Binding struct {
	Queue   string
	Handler Handler
}

// StartProcessors launches a processor goroutine for each queue binding.
// Each processor runs in its own goroutine tracked by wg. If a processor panics,
// the panic is logged with a stack trace and shutdownFn (if non-nil) is called
// to trigger graceful shutdown.
//
// Returns an error if any binding has an empty queue name, a nil handler, or
// a queue name that duplicates an earlier binding — this catches configuration
// errors at startup rather than at runtime (a duplicate queue would otherwise
// trip the active-queue guard at runtime and trigger shutdownFn).
// Panics if queue or wg is nil (programming errors); a nil logger is
// normalized to [slog.Default].
func StartProcessors(
	ctx context.Context,
	queue *Queue,
	bindings []Binding,
	wg *sync.WaitGroup,
	logger *slog.Logger,
	shutdownFn func(),
) error {
	if queue == nil {
		panic("redisqueue: StartProcessors requires a non-nil queue")
	}
	if wg == nil {
		panic("redisqueue: StartProcessors requires a non-nil sync.WaitGroup")
	}
	if logger == nil {
		logger = slog.Default()
	}

	seen := make(map[string]struct{}, len(bindings))
	for i, b := range bindings {
		if b.Queue == "" {
			return &redis.BindingError{Index: i, Reason: "queue name must not be empty"}
		}
		if b.Handler == nil {
			return &redis.BindingError{Index: i, Reason: "handler must not be nil"}
		}
		// Two bindings for the same queue would have the second Process call
		// hit the active-queue guard and panic, which the per-goroutine
		// recover turns into a full shutdownFn — i.e. a config error becomes
		// a runtime whole-app teardown. Reject it here so the duplicate
		// surfaces as a startup error instead. The queue name is omitted from
		// the reason to keep it out of error strings (queue names are treated
		// as opaque elsewhere in the kit).
		if _, dup := seen[b.Queue]; dup {
			return &redis.BindingError{Index: i, Reason: "duplicate queue name in bindings"}
		}
		seen[b.Queue] = struct{}{}
	}

	for _, binding := range bindings {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					logger.Error("queue processor panicked",
						redact.String("queue", binding.Queue),
						redact.Panic(r),
						"stack", string(debug.Stack()),
					)
					if shutdownFn != nil {
						shutdownFn()
					}
				}
			}()
			queue.Process(ctx, binding.Queue, binding.Handler)
		}()
	}
	return nil
}
