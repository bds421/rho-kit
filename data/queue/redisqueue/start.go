package redisqueue

import (
	"context"
	"log/slog"
	"runtime/debug"
	"sync"

	"github.com/bds421/rho-kit/infra/redis"
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
// Returns an error if any binding has an empty queue name — this catches
// configuration errors at startup rather than at runtime.
func StartProcessors(
	ctx context.Context,
	queue *Queue,
	bindings []Binding,
	wg *sync.WaitGroup,
	logger *slog.Logger,
	shutdownFn func(),
) error {
	for i, b := range bindings {
		if b.Queue == "" {
			return &redis.BindingError{Index: i, Reason: "queue name must not be empty"}
		}
		if b.Handler == nil {
			return &redis.BindingError{Index: i, Reason: "handler must not be nil"}
		}
	}

	for _, binding := range bindings {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					logger.Error("queue processor panicked",
						"queue", binding.Queue,
						"panic", r,
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

