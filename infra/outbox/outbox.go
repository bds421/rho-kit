package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/bds421/rho-kit/core/v2/id"
	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/infra/v2/messaging"
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
		return nil, redact.WrapError("outbox: unmarshal headers", err)
	}
	if err := messaging.ValidateMessageHeaders(headers); err != nil {
		return nil, redact.WrapError("outbox: invalid headers", err)
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

// Writer writes outbox entries via an [Inserter]. Safe for concurrent
// use. Writer only needs the insert side of the persistence contract,
// so callers can hand in a typed transactional inserter rather than the
// full [Store] — keeping the producer codepath independent of relay
// semantics (claim, outcome, janitor).
type Writer struct {
	store       Inserter
	txCheck     func(context.Context) error
	sizeLimiter messaging.MessageSizeLimiter
}

// WriterOption configures the Writer.
type WriterOption func(*Writer)

// WithMessageSizeLimiter replaces the writer's message-size policy. The
// default is messaging.DefaultMaxMessageBytes so oversized rows are rejected
// before they are persisted to the outbox store.
func WithMessageSizeLimiter(l messaging.MessageSizeLimiter) WriterOption {
	return func(w *Writer) {
		w.sizeLimiter = l
	}
}

// WithMaxMessageBytes sets the default serialized message-size limit for
// writes. Route-specific limits can still override it.
func WithMaxMessageBytes(maxBytes int) WriterOption {
	return func(w *Writer) {
		w.sizeLimiter = w.sizeLimiter.WithDefaultMaxBytes(maxBytes)
	}
}

// WithoutMaxMessageBytes disables the default writer size limit. Use only
// when an outer protocol, database constraint, or publisher contract already
// bounds outbox entry size.
func WithoutMaxMessageBytes() WriterOption {
	return func(w *Writer) {
		w.sizeLimiter = w.sizeLimiter.WithoutDefaultMaxBytes()
	}
}

// WithRouteMaxMessageBytes overrides the message-size limit for one exact
// topic+routing-key pair. routingKey may be empty for fanout-style routes.
func WithRouteMaxMessageBytes(topic, routingKey string, maxBytes int) WriterOption {
	return func(w *Writer) {
		w.sizeLimiter = w.sizeLimiter.WithRouteMaxBytes(topic, routingKey, maxBytes)
	}
}

// NewWriter creates a Writer that REQUIRES an active transaction on
// every Write call. txCheck is invoked first; if it returns a non-nil
// error the entry is rejected before it ever reaches the store. This
// enforces the atomicity invariant the outbox pattern exists to
// provide: a write to the outbox without the surrounding business
// transaction recreates the split-brain the pattern was supposed to
// prevent.
//
// txCheck stays generic so the outbox package does not depend on a
// specific tx backend (pgx, sqlc, raw database/sql). The predicate
// should return nil when ctx carries an active transaction handle, and
// a descriptive error otherwise. Typical wiring with a pgx-backed
// store:
//
//	import "github.com/jackc/pgx/v5"
//	w := outbox.NewWriter(store, func(ctx context.Context) error {
//	    if _, ok := ctx.Value(pgxTxKey).(pgx.Tx); !ok {
//	        return errors.New("outbox: write outside transaction not allowed")
//	    }
//	    return nil
//	})
//
// Panics if store is nil (would surface as nil-deref on first Write) or
// if txCheck is nil (callers that explicitly want no atomicity
// enforcement must use [NewWriterWithoutTransactionCheck]). Accepting
// an Inserter rather than the full [Store] means producer code only
// sees Insert and cannot accidentally call Claimer/Outcomer/Janitor
// methods.
func NewWriter(store Inserter, txCheck func(context.Context) error, opts ...WriterOption) *Writer {
	if store == nil {
		panic("outbox: NewWriter requires a non-nil Inserter")
	}
	if txCheck == nil {
		panic("outbox: NewWriter requires a non-nil txCheck — use NewWriterWithoutTransactionCheck to opt out explicitly")
	}
	return newWriter(store, txCheck, opts)
}

// NewWriterWithoutTransactionCheck creates a Writer that does NOT
// verify the request is wrapped in a transaction. It exists for
// tests, devmode wiring, and the (rare) services that have their own
// atomicity guarantee outside of the outbox abstraction (e.g. a
// single-writer scenario where the business state and the outbox row
// are the same row).
//
// The name is deliberately long and explicit: the safe entry point is
// [NewWriter], and accidentally typing this name out should give
// reviewers an obvious chance to ask "do we actually have atomicity
// elsewhere?". Production services should prefer [NewWriter] with a
// real transaction predicate.
func NewWriterWithoutTransactionCheck(store Inserter, opts ...WriterOption) *Writer {
	if store == nil {
		panic("outbox: NewWriterWithoutTransactionCheck requires a non-nil Inserter")
	}
	return newWriter(store, nil, opts)
}

func newWriter(store Inserter, txCheck func(context.Context) error, opts []WriterOption) *Writer {
	w := &Writer{
		store:       store,
		txCheck:     txCheck,
		sizeLimiter: messaging.DefaultMessageSizeLimiter(),
	}
	for _, opt := range opts {
		if opt == nil {
			panic("outbox: Writer option must not be nil")
		}
		opt(w)
	}
	return w
}

// Write inserts a new outbox entry via the configured store.
// The entry will be picked up by the Relay for publishing.
//
// When the Writer was constructed via [NewWriter] the txCheck
// predicate runs first; if it returns an error the entry is rejected
// before reaching the store. This guards against the "wrote to the
// outbox without a transaction" mistake that defeats the outbox
// pattern's atomicity guarantee. Writers constructed via
// [NewWriterWithoutTransactionCheck] skip this step.
func (w *Writer) Write(ctx context.Context, params WriteParams) error {
	if w.txCheck != nil {
		if err := w.txCheck(ctx); err != nil {
			return redact.WrapError("outbox", err)
		}
	}
	if err := validateWriteParams(params); err != nil {
		return err
	}
	if err := w.sizeLimiter.Check(params.Topic, params.RoutingKey, messaging.Message{
		ID:      params.MessageID,
		Type:    params.MessageType,
		Payload: params.Payload,
		Headers: params.Headers,
	}); err != nil {
		return err
	}

	headersJSON, err := json.Marshal(params.Headers)
	if err != nil {
		return redact.WrapError("outbox: marshal headers", err)
	}

	entry := Entry{
		ID:          uuid.UUID(id.NewBytes()),
		Topic:       params.Topic,
		RoutingKey:  params.RoutingKey,
		MessageID:   params.MessageID,
		MessageType: params.MessageType,
		Payload:     cloneRawMessage(params.Payload),
		Headers:     headersJSON,
		Status:      StatusPending,
		Attempts:    0,
		CreatedAt:   time.Now().UTC(),
	}

	return w.store.Insert(ctx, entry)
}

func validateWriteParams(params WriteParams) error {
	if params.Topic == "" {
		return fmt.Errorf("outbox: topic must not be empty")
	}
	if params.RoutingKey == "" {
		return fmt.Errorf("outbox: routing key must not be empty")
	}
	if err := messaging.ValidatePublishRoute(params.Topic, params.RoutingKey); err != nil {
		return redact.WrapError("outbox: invalid publish route", err)
	}
	if err := validatePortableField("message id", params.MessageID); err != nil {
		return err
	}
	if err := validatePortableField("message type", params.MessageType); err != nil {
		return err
	}
	if len(params.Payload) == 0 {
		return fmt.Errorf("outbox: payload must not be empty")
	}
	if !json.Valid(params.Payload) {
		return fmt.Errorf("outbox: payload must be valid JSON")
	}
	if err := messaging.ValidateMessageHeaders(params.Headers); err != nil {
		return redact.WrapError("outbox: invalid headers", err)
	}
	return nil
}

func validatePortableField(kind, value string) error {
	if value == "" {
		return fmt.Errorf("outbox: %s must not be empty", kind)
	}
	if len(value) > messaging.MaxRouteNameBytes {
		return fmt.Errorf("outbox: %s exceeds maximum length", kind)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("outbox: %s contains invalid UTF-8", kind)
	}
	if strings.ContainsFunc(value, func(r rune) bool {
		return unicode.IsControl(r) || unicode.IsSpace(r)
	}) {
		return fmt.Errorf("outbox: %s contains whitespace or control characters", kind)
	}
	return nil
}

func cloneRawMessage(raw json.RawMessage) json.RawMessage {
	if raw == nil {
		return nil
	}
	return append(raw[:0:0], raw...)
}
