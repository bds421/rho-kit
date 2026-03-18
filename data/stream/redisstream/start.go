package redisstream

import (
	"context"
	"log/slog"
	"runtime/debug"
	"sync"

	"github.com/bds421/rho-kit/infra/redis"
)

// Binding pairs a stream name with its handler.
type Binding struct {
	Stream  string
	Handler Handler
}

// StartConsumers launches a consumer goroutine for each stream binding.
// Each consumer runs in its own goroutine tracked by wg. If a consumer panics,
// the panic is logged with a stack trace and shutdownFn (if non-nil) is called
// to trigger graceful shutdown.
//
// Returns an error if any binding has an empty stream name — this catches
// configuration errors at startup rather than at runtime.
func StartConsumers(
	ctx context.Context,
	consumer *Consumer,
	bindings []Binding,
	wg *sync.WaitGroup,
	logger *slog.Logger,
	shutdownFn func(),
) error {
	for i, b := range bindings {
		if b.Stream == "" {
			return &redis.BindingError{Index: i, Reason: "stream name must not be empty"}
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
					logger.Error("stream consumer panicked",
						"stream", binding.Stream,
						"panic", r,
						"stack", string(debug.Stack()),
					)
					if shutdownFn != nil {
						shutdownFn()
					}
				}
			}()
			consumer.Consume(ctx, binding.Stream, binding.Handler)
		}()
	}
	return nil
}

