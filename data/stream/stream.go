package stream

import "context"

// Message represents a stream event.
type Message struct {
	ID      string
	Stream  string
	Payload map[string]string
}

// Handler processes a stream message. Return nil to acknowledge.
type Handler func(ctx context.Context, msg Message) error

// Producer publishes messages to a stream.
type Producer interface {
	Produce(ctx context.Context, stream string, payload map[string]string) (string, error)
}

// Consumer reads messages from a stream with consumer group support.
type Consumer interface {
	// Consume blocks and processes messages until ctx is cancelled.
	Consume(ctx context.Context, stream string, handler Handler)
}
