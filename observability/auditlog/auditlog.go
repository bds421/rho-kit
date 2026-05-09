// asvs: V7.1.1, V7.4.1, V4.1.5
package auditlog

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
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
	store     Store
	logger    *slog.Logger
	dropped   prometheus.Counter
	dropTotal atomic.Uint64
	onDrop    func(ctx context.Context, event Event, err error)
}

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
		l.dropTotal.Add(1)
		if l.dropped != nil {
			l.dropped.Inc()
		}
		if l.onDrop != nil {
			l.onDrop(ctx, event, err)
		}
		return err
	}
	return nil
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
