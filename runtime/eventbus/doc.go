// Package eventbus provides an in-process domain event bus with type-safe
// generic publish/subscribe.
//
// Events are plain structs implementing the [Event] interface. Handlers are
// synchronous by default; use [WithAsync] for fire-and-forget dispatch.
//
// [Subscribe] and [Publish] are package-level functions (not methods) because
// Go methods cannot have type parameters.
//
// # Quick Start
//
//	type OrderPlaced struct { OrderID string }
//	func (OrderPlaced) EventName() string { return "order.placed" }
//
//	bus := eventbus.New(eventbus.WithLogger(logger))
//	eventbus.Subscribe(bus, func(ctx context.Context, e OrderPlaced) error {
//	    return sendConfirmationEmail(ctx, e.OrderID)
//	}, eventbus.WithName("email"))
//
//	err := eventbus.Publish(bus, ctx, OrderPlaced{OrderID: "123"})
//
// # Sync vs Async
//
// Sync handlers (default) are called sequentially. Their errors are collected
// via [errors.Join] and returned from [Publish]. Use sync for side-effects
// that must succeed (audit logging, cache invalidation).
//
// Async handlers ([WithAsync]) run in separate goroutines with panic recovery.
// Their errors are logged and sent to the [WithOnError] callback. Use async
// for best-effort side-effects (analytics, notifications).
//
// # Bounded Worker Pool
//
// By default, async handlers launch one goroutine per event (unbounded). For
// production use with high-throughput events, enable the bounded worker pool:
//
//	bus := eventbus.New(
//	    eventbus.WithWorkerPool(4),          // 4 worker goroutines
//	    eventbus.WithWorkerPoolBuffer(100),  // buffered channel of 100
//	    eventbus.WithLogger(logger),
//	)
//
//	// Start the pool (blocks until ctx is cancelled).
//	go bus.Start(ctx)
//
//	// On shutdown, drain pending events with a deadline.
//	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
//	defer cancel()
//	bus.Stop(shutdownCtx)
//
// When the queue is full, new async events are dropped and logged. Use
// [WithOnError] to hook into drop/error notifications.
//
// # When to Use
//
// Use eventbus for in-process domain event dispatch: decoupling handlers from
// side-effects within a single service. For cross-service messaging, use the
// messaging package (RabbitMQ). For durable event streaming, use redis/stream.
package eventbus
