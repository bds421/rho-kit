package queue

import "context"

// Message represents a queued job.
type Message struct {
	ID      string
	Type    string
	Payload []byte
}

// Handler processes a queue message. Return nil to acknowledge,
// return an error to retry or dead-letter.
type Handler func(ctx context.Context, msg Message) error

// Publisher enqueues messages.
type Publisher interface {
	Enqueue(ctx context.Context, queue string, msg Message) error
}

// Consumer processes messages from a queue.
type Consumer interface {
	// Consume blocks and processes messages until ctx is cancelled.
	Consume(ctx context.Context, queue string, handler Handler)
}
