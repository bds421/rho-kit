// Package eventbus provides an in-process domain event bus with type-safe
// generic publish/subscribe, backed by Watermill's GoChannel.
//
// Events are plain structs implementing the [Event] interface. All handlers
// run asynchronously — [Publish] enqueues the event and returns immediately.
// Handler errors are reported via the [WithOnError] callback.
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
// # Error Handling
//
// All handlers are asynchronous. Their errors are logged and sent to the
// [WithOnError] callback. Panics are recovered and reported via the same
// callback.
//
// # When to Use
//
// Use eventbus for in-process domain event dispatch: decoupling handlers from
// side-effects within a single service. For cross-service messaging, use the
// messaging package (RabbitMQ, Kafka, NATS via Watermill). For durable event
// streaming, use redis/stream.
package eventbus
