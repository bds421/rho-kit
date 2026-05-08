package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// ErrNotFound indicates that the targeted outbox row no longer exists when
// a status update was attempted. Callers (typically the relay) should treat
// this as an unexpected condition: the row was deleted out from under the
// publish loop, e.g. by retention cleanup or external surgery.
var ErrNotFound = errors.New("outbox: entry not found")

// ErrStaleState indicates that the targeted outbox row exists but is no
// longer in the expected state for the requested update. The most common
// cause is concurrent stale recovery resetting a long-running processing
// row back to pending while the original relay was still publishing.
// Returning a typed error lets the relay detect the race instead of
// silently swallowing a no-op UPDATE.
var ErrStaleState = errors.New("outbox: entry not in expected state")

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
//
// NextRetryAt is set by [Store.IncrementAttempts] to a future timestamp when
// a publish fails; [Store.FetchPending] must skip entries whose NextRetryAt
// is still in the future. This implements exponential backoff between retry
// attempts and prevents the relay from tight-looping on a persistently
// failing downstream. NextRetryAt being nil means "eligible immediately"
// (matches legacy rows from before the column existed).
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
	NextRetryAt *time.Time
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
	store           Store
	txCheck         func(context.Context) error
	requireTxPolicy bool
}

// WriterOption configures the Writer.
type WriterOption func(*Writer)

// WithRequireTransaction makes Write fail when ctx does not carry an
// active transaction handle. The check is performed by the supplied
// predicate so outbox stays decoupled from any specific tx backend
// (pgx, sqlc-generated wrappers, raw database/sql). The predicate
// should return nil when ctx carries a tx, and a descriptive error
// otherwise.
//
// Typical wiring with a pgx-backed store:
//
//	import "github.com/jackc/pgx/v5"
//	w := outbox.NewWriter(store, outbox.WithRequireTransaction(func(ctx context.Context) error {
//	    if _, ok := ctx.Value(pgxTxKey).(pgx.Tx); !ok {
//	        return errors.New("outbox: write outside transaction not allowed")
//	    }
//	    return nil
//	}))
//
// The whole point of the transactional-outbox pattern is atomicity between
// the business write and the outbox-entry insert; without this option, a
// caller can accidentally write to the outbox outside the tx that produced
// the side effect, recreating the very split-brain the pattern exists to
// prevent. Make this the default for any new service: the kit ships it
// disabled only because tightening it on existing callers is a behaviour
// change.
func WithRequireTransaction(check func(context.Context) error) WriterOption {
	if check == nil {
		panic("outbox: WithRequireTransaction requires a non-nil check function")
	}
	return func(w *Writer) {
		w.txCheck = check
		w.requireTxPolicy = true
	}
}

// NewWriter creates a Writer backed by the given store. Panics if store is
// nil — the misconfiguration would otherwise surface as a nil-deref on the
// first Write call.
func NewWriter(store Store, opts ...WriterOption) *Writer {
	if store == nil {
		panic("outbox: NewWriter requires a non-nil Store")
	}
	w := &Writer{store: store}
	for _, opt := range opts {
		opt(w)
	}
	return w
}

// Write inserts a new outbox entry via the configured store.
// The entry will be picked up by the Relay for publishing.
//
// When the Writer was constructed with [WithRequireTransaction], the
// configured predicate runs before any work happens; if it returns an error
// the entry is rejected before reaching the store. This guards against the
// "wrote to the outbox without a transaction" mistake that defeats the
// outbox pattern's atomicity guarantee.
func (w *Writer) Write(ctx context.Context, params WriteParams) error {
	if w.requireTxPolicy {
		if err := w.txCheck(ctx); err != nil {
			return fmt.Errorf("outbox: %w", err)
		}
	}
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
