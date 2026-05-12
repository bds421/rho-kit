package messaging

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"

	"github.com/bds421/rho-kit/core/v2/redact"
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
	c Consumer,
	declared []Binding,
	handlers map[string]Handler,
	wg *sync.WaitGroup,
	logger *slog.Logger,
	shutdownFn func(),
) error {
	if len(declared) == 0 {
		return nil
	}
	if c == nil {
		return ErrInvalidConsumer
	}
	if wg == nil {
		return fmt.Errorf("messaging: StartConsumers requires a non-nil WaitGroup when bindings are declared")
	}
	if logger == nil {
		logger = slog.Default()
	}

	var missing []string
	var nilHandlers []string
	for _, b := range declared {
		h, ok := handlers[b.RoutingKey]
		if !ok {
			missing = append(missing, b.RoutingKey)
			continue
		}
		if h == nil {
			nilHandlers = append(nilHandlers, b.RoutingKey)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("messaging: no handlers registered for declared bindings (count=%d)", len(missing))
	}
	if len(nilHandlers) > 0 {
		return fmt.Errorf("messaging: nil handlers registered for declared bindings (count=%d)", len(nilHandlers))
	}

	for _, b := range declared {
		binding := b
		h := handlers[b.RoutingKey]
		wg.Go(func() {
			defer func() {
				if r := recover(); r != nil {
					logger.Error("consumer panicked",
						redact.String("routing_key", binding.RoutingKey),
						redact.Panic(r),
						"stack", string(debug.Stack()),
					)
					if shutdownFn != nil {
						shutdownFn()
					}
				}
			}()
			if err := c.Consume(ctx, binding, h); err != nil && ctx.Err() == nil {
				logger.Error("consumer permanently failed", redact.String("queue", binding.Queue), redact.Error(err))
				if shutdownFn != nil {
					shutdownFn()
				}
			}
		})
	}
	return nil
}
