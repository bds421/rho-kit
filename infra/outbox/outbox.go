package outbox

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Publisher publishes outbox entries to an external system.
// Implementations exist for messaging (AMQP), streaming (Redis Streams),
// or any other transport.
type Publisher interface {
	Publish(ctx context.Context, entry Entry) error
}

// Status represents the lifecycle state of an outbox entry.
type Status string

const (
	// StatusPending indicates the entry is waiting to be published.
	StatusPending Status = "pending"

	// StatusProcessing indicates the entry has been claimed by a relay instance
	// and is being published. This prevents concurrent relay instances from
	// processing the same entry.
	StatusProcessing Status = "processing"

	// StatusPublished indicates the entry was successfully published.
	StatusPublished Status = "published"

	// StatusFailed indicates the entry exceeded max attempts.
	StatusFailed Status = "failed"
)

// Entry represents a single outbox row. It is a plain value object with no
// ORM-specific tags or dependencies. Storage implementations map this struct
// to their own persistence model.
type Entry struct {
	ID          uuid.UUID
	Topic       string
	RoutingKey  string
	MessageID   string
	MessageType string
	Payload     json.RawMessage
	Headers     json.RawMessage
	Status      Status
	Attempts    int
	CreatedAt   time.Time
	PublishedAt *time.Time
	LastError   *string
}

// HeadersMap returns the headers as a map. Returns nil if no headers are set.
func (e Entry) HeadersMap() (map[string]string, error) {
	if len(e.Headers) == 0 {
		return nil, nil
	}

	var headers map[string]string
	if err := json.Unmarshal(e.Headers, &headers); err != nil {
		return nil, fmt.Errorf("outbox: unmarshal headers for entry %s: %w", e.ID, err)
	}

	return headers, nil
}

// WriteParams holds the parameters for writing an outbox entry.
// All fields except Headers are required.
type WriteParams struct {
	Topic       string
	RoutingKey  string
	MessageID   string
	MessageType string
	Payload     json.RawMessage
	Headers     map[string]string
}

// Writer writes outbox entries via a Store implementation.
// Safe for concurrent use.
type Writer struct {
	store Store
}

// NewWriter creates a Writer backed by the given store.
func NewWriter(store Store) *Writer {
	return &Writer{store: store}
}

// Write inserts a new outbox entry via the configured store.
// The entry will be picked up by the Relay for publishing.
func (w *Writer) Write(ctx context.Context, params WriteParams) error {
	if params.Topic == "" {
		return fmt.Errorf("outbox: topic must not be empty")
	}
	if params.RoutingKey == "" {
		return fmt.Errorf("outbox: routing key must not be empty")
	}

	headersJSON, err := json.Marshal(params.Headers)
	if err != nil {
		return fmt.Errorf("outbox: marshal headers: %w", err)
	}

	id, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("outbox: generate entry id: %w", err)
	}

	entry := Entry{
		ID:          id,
		Topic:       params.Topic,
		RoutingKey:  params.RoutingKey,
		MessageID:   params.MessageID,
		MessageType: params.MessageType,
		Payload:     params.Payload,
		Headers:     headersJSON,
		Status:      StatusPending,
		Attempts:    0,
		CreatedAt:   time.Now().UTC(),
	}

	return w.store.Insert(ctx, entry)
}
