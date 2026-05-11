// asvs: V7.1.1, V7.4.1, V4.1.5
package auditlog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"runtime/debug"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/trace"

	"github.com/bds421/rho-kit/core/v2/redact"
)

// Event represents a single audit log entry.
type Event struct {
	ID        string          `json:"id"`
	Timestamp time.Time       `json:"timestamp"`
	Actor     string          `json:"actor"`                // user ID, service name, "system"
	Action    string          `json:"action"`               // "create", "update", "delete", custom
	Resource  string          `json:"resource"`             // "users/123", "orders/456"
	Status    string          `json:"status"`               // "success", "failure", "denied"
	IPAddress string          `json:"ip_address,omitempty"` // client IP for compliance/security
	Metadata  json.RawMessage `json:"metadata,omitempty"`   // arbitrary context
	TraceID   string          `json:"trace_id,omitempty"`   // OpenTelemetry trace correlation
}

const (
	// MaxEventIDBytes caps caller-supplied IDs. Logger-generated IDs are UUIDv7.
	MaxEventIDBytes = 36
	// MaxActorBytes caps the actor principal identifier.
	MaxActorBytes = 255
	// MaxActionBytes caps the action verb.
	MaxActionBytes = 255
	// MaxResourceBytes caps the resource identifier/path.
	MaxResourceBytes = 2048
	// MaxStatusBytes caps status values.
	MaxStatusBytes = 32
	// MaxIPAddressBytes caps textual IPv4/IPv6 addresses.
	MaxIPAddressBytes = 64
	// MaxTraceIDBytes caps OpenTelemetry trace IDs (32 lowercase hex chars).
	MaxTraceIDBytes = 32
	// MaxMetadataBytes caps structured metadata persisted with one event.
	MaxMetadataBytes = 64 << 10
)

const (
	StatusSuccess = "success"
	StatusFailure = "failure"
	StatusDenied  = "denied"
)

// ErrInvalidEvent marks an audit event that cannot safely be persisted.
var ErrInvalidEvent = errors.New("auditlog: invalid event")

func cloneEvent(event Event) Event {
	if event.Metadata != nil {
		event.Metadata = append(json.RawMessage(nil), event.Metadata...)
	}
	return event
}

func cloneEvents(events []Event) []Event {
	if events == nil {
		return nil
	}
	out := make([]Event, len(events))
	for i, event := range events {
		out[i] = cloneEvent(event)
	}
	return out
}

// ValidateEvent enforces the audit-event persistence contract shared by Logger
// and bundled stores. It rejects missing required fields, unbounded text,
// control/whitespace-bearing tokens, malformed IP/trace IDs, and oversized or
// invalid JSON metadata.
func ValidateEvent(event Event) error {
	if event.Timestamp.IsZero() {
		return fmt.Errorf("%w: timestamp must not be zero", ErrInvalidEvent)
	}
	if err := validateRequiredToken("id", event.ID, MaxEventIDBytes); err != nil {
		return err
	}
	if err := validateRequiredToken("actor", event.Actor, MaxActorBytes); err != nil {
		return err
	}
	if err := validateRequiredToken("action", event.Action, MaxActionBytes); err != nil {
		return err
	}
	if err := validateRequiredToken("resource", event.Resource, MaxResourceBytes); err != nil {
		return err
	}
	if err := validateStatus(event.Status); err != nil {
		return err
	}
	if event.IPAddress != "" {
		if len(event.IPAddress) > MaxIPAddressBytes || !utf8.ValidString(event.IPAddress) {
			return fmt.Errorf("%w: ip_address is invalid", ErrInvalidEvent)
		}
		if _, err := netip.ParseAddr(event.IPAddress); err != nil {
			return fmt.Errorf("%w: ip_address is invalid", ErrInvalidEvent)
		}
	}
	if event.TraceID != "" {
		if len(event.TraceID) != MaxTraceIDBytes || !isLowerHex(event.TraceID) {
			return fmt.Errorf("%w: trace_id must be 32 lowercase hex characters", ErrInvalidEvent)
		}
	}
	if len(event.Metadata) > MaxMetadataBytes {
		return fmt.Errorf("%w: metadata exceeds maximum length", ErrInvalidEvent)
	}
	if len(event.Metadata) > 0 && !json.Valid(event.Metadata) {
		return fmt.Errorf("%w: metadata must be valid JSON", ErrInvalidEvent)
	}
	return nil
}

// Store is the append-only persistence interface for audit events.
type Store interface {
	// Append persists an event. Implementations must be safe for concurrent use
	// and must reject events that fail ValidateEvent.
	Append(ctx context.Context, event Event) error

	// Query returns events matching the filter, ordered by timestamp descending.
	// Returns the next cursor for pagination (empty string if no more results).
	Query(ctx context.Context, filter Filter, cursor string, limit int) ([]Event, string, error)
}

// Filter controls which events are returned by Query.
type Filter struct {
	Actor     string
	Action    string
	Resource  string
	Since     time.Time
	Until     time.Time
	IPAddress string
}

// Logger wraps a Store with convenience methods and automatic field population.
type Logger struct {
	store     Store
	logger    *slog.Logger
	dropped   prometheus.Counter
	dropTotal atomic.Uint64
	onDrop    func(ctx context.Context, event Event, err error)
}

var newAuditID = uuid.NewV7

// Option configures a Logger.
type Option func(*Logger)

// WithDroppedCounter registers a Prometheus counter that is incremented
// each time the underlying Store fails to persist an event. Without
// this, drops are only visible in the slog stream — operators who alert
// on counter rates miss compliance-affecting drops entirely.
func WithDroppedCounter(c prometheus.Counter) Option {
	return func(l *Logger) { l.dropped = c }
}

// WithOnDrop registers a callback invoked each time the underlying
// Store returns an error from Append. The callback runs in the calling
// goroutine; keep it fast or schedule its own goroutine. Use this to
// hand-off to a fallback sink (file, in-memory ring buffer, alerter).
func WithOnDrop(fn func(ctx context.Context, event Event, err error)) Option {
	return func(l *Logger) { l.onDrop = fn }
}

// DroppedCount returns the process-local count of drop events. Useful
// in tests; in production use the Prometheus counter from
// [WithDroppedCounter].
func (l *Logger) DroppedCount() uint64 {
	return l.dropTotal.Load()
}

// WithLogger sets the slog logger for error reporting. A nil logger is
// normalized to [slog.Default] so test wiring stays ergonomic; the audit
// logger never holds a nil slog.Logger.
func WithLogger(l *slog.Logger) Option {
	return func(a *Logger) {
		if l == nil {
			a.logger = slog.Default()
			return
		}
		a.logger = l
	}
}

// New creates an audit Logger backed by the given Store.
func New(store Store, opts ...Option) *Logger {
	if store == nil {
		panic("auditlog: store must not be nil")
	}
	l := &Logger{
		store:  store,
		logger: slog.Default(),
	}
	for _, o := range opts {
		if o == nil {
			panic("auditlog: option must not be nil")
		}
		o(l)
	}
	return l
}

// Log appends an audit event. Auto-populates ID, Timestamp, and TraceID if empty.
// Errors are logged but not returned — audit logging must not break the caller.
//
// FR-083 [MED]: services that need fail-on-drop semantics (compliance
// audit trails, financial events) should use [Logger.LogE] instead.
// LogE returns the underlying store error so callers can refuse the
// originating action when persistence fails. The fire-and-forget Log
// remains the right primitive for high-volume best-effort logging.
func (l *Logger) Log(ctx context.Context, event Event) {
	_ = l.LogE(ctx, event)
}

// LogE appends an audit event and returns the underlying store
// error. Use this when persistence is part of the request's success
// criterion — e.g. financial / compliance events that must not
// silently drop. The drop hook + counter still fire on failure so
// monitoring stays in place.
func (l *Logger) LogE(ctx context.Context, event Event) error {
	if event.ID == "" {
		id, err := newAuditID()
		if err != nil {
			err = fmt.Errorf("auditlog: generate event ID: %w", err)
			l.logger.Error("auditlog: failed to generate event ID", redact.Error(err))
			l.recordDrop(ctx, event, err)
			return err
		}
		event.ID = id.String()
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}
	if event.TraceID == "" {
		event.TraceID = extractTraceID(ctx)
	}
	event = cloneEvent(event)

	if err := ValidateEvent(event); err != nil {
		l.logger.Error("auditlog: invalid event", redact.Error(err))
		l.recordDrop(ctx, event, err)
		return err
	}

	if err := l.store.Append(ctx, event); err != nil {
		l.logger.Error("auditlog: failed to append event",
			redact.Error(err),
			redact.String("event_id", event.ID),
			redact.String("action", event.Action),
		)
		l.recordDrop(ctx, event, err)
		return err
	}
	return nil
}

func (l *Logger) recordDrop(ctx context.Context, event Event, err error) {
	l.dropTotal.Add(1)
	if l.dropped != nil {
		l.dropped.Inc()
	}
	l.callOnDrop(ctx, event, err)
}

func (l *Logger) callOnDrop(ctx context.Context, event Event, err error) {
	if l.onDrop == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			logger := l.logger
			if logger == nil {
				logger = slog.Default()
			}
			attrs := []any{
				redact.Panic(r),
				"stack", string(debug.Stack()),
			}
			if isSafeLogToken(event.ID, MaxEventIDBytes) {
				attrs = append(attrs, redact.String("event_id", event.ID))
			}
			logger.Error("auditlog: OnDrop callback panicked",
				attrs...,
			)
		}
	}()
	l.onDrop(ctx, event, err)
}

// LogAction is a convenience method for logging simple events without metadata.
func (l *Logger) LogAction(ctx context.Context, actor, action, resource, status string) {
	l.Log(ctx, Event{
		Actor:    actor,
		Action:   action,
		Resource: resource,
		Status:   status,
	})
}

// Query delegates to the underlying Store.
func (l *Logger) Query(ctx context.Context, filter Filter, cursor string, limit int) ([]Event, string, error) {
	events, next, err := l.store.Query(ctx, filter, cursor, limit)
	if err != nil {
		return nil, next, err
	}
	return cloneEvents(events), next, nil
}

// extractTraceID returns the OpenTelemetry trace ID from the context, or "".
func extractTraceID(ctx context.Context) string {
	sc := trace.SpanFromContext(ctx).SpanContext()
	if sc.HasTraceID() {
		return sc.TraceID().String()
	}
	return ""
}

func validateRequiredToken(kind, value string, maxBytes int) error {
	if value == "" {
		return fmt.Errorf("%w: %s must not be empty", ErrInvalidEvent, kind)
	}
	if !isSafeLogToken(value, maxBytes) {
		return fmt.Errorf("%w: %s contains invalid characters or exceeds maximum length", ErrInvalidEvent, kind)
	}
	return nil
}

func validateStatus(status string) error {
	if status == "" {
		return fmt.Errorf("%w: status must not be empty", ErrInvalidEvent)
	}
	if len(status) > MaxStatusBytes || !isSafeLogToken(status, MaxStatusBytes) {
		return fmt.Errorf("%w: status is invalid", ErrInvalidEvent)
	}
	switch status {
	case StatusSuccess, StatusFailure, StatusDenied:
		return nil
	default:
		return fmt.Errorf("%w: status must be one of success, failure, denied", ErrInvalidEvent)
	}
}

func isSafeLogToken(value string, maxBytes int) bool {
	if value == "" || len(value) > maxBytes || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

func isLowerHex(value string) bool {
	for _, r := range value {
		switch {
		case '0' <= r && r <= '9':
		case 'a' <= r && r <= 'f':
		default:
			return false
		}
	}
	return true
}
