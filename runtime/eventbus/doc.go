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
// # When to Use
//
// Use eventbus for in-process domain event dispatch: decoupling handlers from
// side-effects within a single service. For cross-service messaging, use the
// messaging package (RabbitMQ). For durable event streaming, use redis/stream.
package eventbus
