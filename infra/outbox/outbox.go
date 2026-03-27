package outbox

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
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

// Entry is the database model for an outbox row.
// Each entry represents a single event to be published. Field names are
// transport-agnostic: Topic maps to an exchange (AMQP), stream name
// (Redis Streams), or Kafka topic depending on the Publisher implementation.
type Entry struct {
	ID          uuid.UUID       `gorm:"type:uuid;primaryKey"`
	Topic       string          `gorm:"type:text;not null"`
	RoutingKey  string          `gorm:"type:text;not null;column:routing_key"`
	MessageID   string          `gorm:"type:text;not null;column:message_id"`
	MessageType string          `gorm:"type:text;not null;column:message_type"`
	Payload     json.RawMessage `gorm:"type:jsonb;not null"`
	Headers     json.RawMessage `gorm:"type:jsonb"`
	Status      Status          `gorm:"type:text;not null;default:pending"`
	Attempts    int             `gorm:"not null;default:0"`
	CreatedAt   time.Time       `gorm:"not null;default:now()"`
	PublishedAt *time.Time      `gorm:"column:published_at"`
	LastError   *string         `gorm:"type:text;column:last_error"`
}

// TableName returns the database table name for GORM.
func (Entry) TableName() string {
	return "outbox_entries"
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

// Writer writes outbox entries within a caller-provided GORM transaction.
// Safe for concurrent use.
type Writer struct {
	store Store
}

// NewWriter creates a Writer backed by the given store.
func NewWriter(store Store) *Writer {
	return &Writer{store: store}
}

// Write inserts a new outbox entry within the provided transaction.
// The entry will be picked up by the Relay for publishing.
func (w *Writer) Write(ctx context.Context, tx *gorm.DB, params WriteParams) error {
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

	return w.store.Insert(ctx, tx, entry)
}
