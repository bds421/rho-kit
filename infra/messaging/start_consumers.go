package messaging

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
)

// StartConsumers launches a consumer goroutine for each declared binding
// using the handler registered in the provided map keyed by routing key.
// Returns an error if any binding has no handler — this catches configuration
// drift (e.g., a new event was added to bindings but the handler was forgotten).
//
// If shutdownFn is non-nil, it is called when a consumer goroutine panics
// to trigger a graceful shutdown of the service.
func StartConsumers(
	ctx context.Context,
	c MessageConsumer,
	declared []Binding,
	handlers map[string]Handler,
	wg *sync.WaitGroup,
	logger *slog.Logger,
	shutdownFn func(),
) error {
	var missing []string
	for _, b := range declared {
		if _, ok := handlers[b.RoutingKey]; !ok {
			missing = append(missing, b.RoutingKey)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("no handlers registered for bindings: %v", missing)
	}

	for _, b := range declared {
		binding := b
		h := handlers[b.RoutingKey]
		wg.Go(func() {
			defer func() {
				if r := recover(); r != nil {
					logger.Error("consumer panicked",
						"routing_key", binding.RoutingKey,
						"panic", r,
						"stack", string(debug.Stack()),
					)
					if shutdownFn != nil {
						shutdownFn()
					}
				}
			}()
			if err := c.Consume(ctx, binding, h); err != nil && ctx.Err() == nil {
				logger.Error("consumer permanently failed", "queue", binding.Queue, "error", err)
			}
		})
	}
	return nil
}
