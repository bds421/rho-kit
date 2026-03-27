package outbox

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/bds421/rho-kit/infra/messaging"
)

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
// Each entry represents a single message to be published.
type Entry struct {
	ID            uuid.UUID       `gorm:"type:uuid;primaryKey"`
	Exchange      string          `gorm:"type:text;not null"`
	RoutingKey    string          `gorm:"type:text;not null;column:routing_key"`
	MessageID     string          `gorm:"type:text;not null;column:message_id"`
	MessageType   string          `gorm:"type:text;not null;column:message_type"`
	Payload       json.RawMessage `gorm:"type:jsonb;not null"`
	Headers       json.RawMessage `gorm:"type:jsonb"`
	SchemaVersion int             `gorm:"not null;default:0;column:schema_version"`
	Status        Status          `gorm:"type:text;not null;default:pending"`
	Attempts      int             `gorm:"not null;default:0"`
	CreatedAt     time.Time       `gorm:"not null;default:now()"`
	PublishedAt   *time.Time      `gorm:"column:published_at"`
	LastError     *string         `gorm:"type:text;column:last_error"`
}

// TableName returns the database table name for GORM.
func (Entry) TableName() string {
	return "outbox_entries"
}

// ToMessage converts the entry back into a messaging.Message.
func (e Entry) ToMessage() (messaging.Message, error) {
	var headers map[string]string
	if len(e.Headers) > 0 {
		if err := json.Unmarshal(e.Headers, &headers); err != nil {
			return messaging.Message{}, fmt.Errorf("outbox: unmarshal headers for entry %s: %w", e.ID, err)
		}
	}

	return messaging.Message{
		ID:            e.MessageID,
		Type:          e.MessageType,
		Payload:       e.Payload,
		Timestamp:     e.CreatedAt,
		SchemaVersion: uint(e.SchemaVersion),
		Headers:       headers,
	}, nil
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
func (w *Writer) Write(ctx context.Context, tx *gorm.DB, exchange, routingKey string, msg messaging.Message) error {
	if exchange == "" {
		return fmt.Errorf("outbox: exchange must not be empty")
	}
	if routingKey == "" {
		return fmt.Errorf("outbox: routing key must not be empty")
	}

	headersJSON, err := json.Marshal(msg.Headers)
	if err != nil {
		return fmt.Errorf("outbox: marshal headers: %w", err)
	}

	id, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("outbox: generate entry id: %w", err)
	}

	entry := Entry{
		ID:            id,
		Exchange:      exchange,
		RoutingKey:    routingKey,
		MessageID:     msg.ID,
		MessageType:   msg.Type,
		Payload:       msg.Payload,
		Headers:       headersJSON,
		SchemaVersion: int(msg.SchemaVersion),
		Status:        StatusPending,
		Attempts:      0,
		CreatedAt:     time.Now().UTC(),
	}

	return w.store.Insert(ctx, tx, entry)
}
