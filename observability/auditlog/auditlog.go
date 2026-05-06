package auditlog

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/trace"
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

// Store is the append-only persistence interface for audit events.
type Store interface {
	// Append persists an event. Implementations must be safe for concurrent use.
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
	store  Store
	logger *slog.Logger
}

// Option configures a Logger.
type Option func(*Logger)

// WithLogger sets the slog logger for error reporting. Default: slog.Default().
func WithLogger(l *slog.Logger) Option {
	return func(a *Logger) { a.logger = l }
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
		o(l)
	}
	return l
}

// Log appends an audit event. Auto-populates ID, Timestamp, and TraceID if empty.
// Errors are logged but not returned — audit logging must not break the caller.
func (l *Logger) Log(ctx context.Context, event Event) {
	if event.ID == "" {
		event.ID = uuid.Must(uuid.NewV7()).String()
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}
	if event.TraceID == "" {
		event.TraceID = extractTraceID(ctx)
	}

	if err := l.store.Append(ctx, event); err != nil {
		l.logger.Error("auditlog: failed to append event",
			"error", err,
			"event_id", event.ID,
			"action", event.Action,
		)
	}
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
	return l.store.Query(ctx, filter, cursor, limit)
}

// extractTraceID returns the OpenTelemetry trace ID from the context, or "".
func extractTraceID(ctx context.Context) string {
	sc := trace.SpanFromContext(ctx).SpanContext()
	if sc.HasTraceID() {
		return sc.TraceID().String()
	}
	return ""
}
