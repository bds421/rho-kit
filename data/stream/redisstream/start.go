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
// Each binding gets its own [*Consumer] instance — cloned from the
// supplied prototype with a freshly-generated consumer ID — so the
// consumer-name → stream mapping in Redis stays unambiguous. The
// prototype itself is not used for consumption; it only carries shared
// configuration (group, claim interval, dead-letter settings, ...).
//
// Each consumer runs in its own goroutine tracked by wg. If a consumer
// panics, the panic is logged with a stack trace and shutdownFn (if
// non-nil) is called to trigger graceful shutdown.
//
// Returns an error if any binding is malformed or if cloning the
// prototype fails (UUID generation error).
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

	for i, binding := range bindings {
		bound, err := consumer.cloneForStream()
		if err != nil {
			return &redis.BindingError{Index: i, Reason: "clone consumer for stream: " + err.Error()}
		}
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
			bound.Consume(ctx, binding.Stream, binding.Handler)
		}()
	}
	return nil
}
