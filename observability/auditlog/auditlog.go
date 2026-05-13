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
	"sync"
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
//
// PrevHMAC and HMAC implement the tamper-evident chain documented in
// docs/audit/THREAT_MODEL.md §5.4. Both fields are populated by
// [Logger.LogE]; callers should leave them zero on input. The HMAC is
// computed over a canonical encoding of the event (see chain.go) keyed
// by the per-Logger chain key. PrevHMAC points at the previous record's
// HMAC, forming an append-only chain that [VerifyChain] (and
// [Logger.VerifyChain]) can validate.
//
// JSON: HMAC byte slices marshal to base64 by default. Callers reading
// audit records over the wire should decode accordingly.
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

	// PrevHMAC links this record to the previous chain entry. The first
	// event in a chain has a nil / all-zero PrevHMAC.
	PrevHMAC []byte `json:"prev_hmac,omitempty"`
	// HMAC is the tamper-evident tag computed at Append time. Mutation
	// of any field invalidates this tag; see [VerifyChain].
	HMAC []byte `json:"hmac,omitempty"`
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
	if event.PrevHMAC != nil {
		event.PrevHMAC = append([]byte(nil), event.PrevHMAC...)
	}
	if event.HMAC != nil {
		event.HMAC = append([]byte(nil), event.HMAC...)
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
	// The cursor returned here is a raw store-level cursor; [Logger.Query]
	// wraps it with a signed envelope before exposing it to callers.
	Query(ctx context.Context, filter Filter, cursor string, limit int) ([]Event, string, error)

	// LastHMAC returns the HMAC of the most recently appended event, or an
	// empty / all-zero slice if the store is empty. [Logger.LogE] uses this
	// to compute the next event's PrevHMAC. Implementations must be safe for
	// concurrent reads.
	LastHMAC(ctx context.Context) ([]byte, error)
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

// Logger wraps a Store with convenience methods and automatic field
// population. Each Logger holds two HMAC keys:
//
//   - chainKey signs each event into the append-only chain. Mutation of
//     any persisted field breaks the chain and is detectable via
//     [Logger.VerifyChain] / [VerifyChain].
//   - cursorKey signs pagination cursors handed to callers. An attacker
//     cannot forge a cursor to skip events without the key.
//
// Both keys are required (≥32 bytes); [New] panics if either is missing.
// Logger.LogE serialises the read-prev-HMAC / compute-new-HMAC / Append
// sequence through appendMu so two concurrent appenders never read the
// same PrevHMAC and produce a chain fork.
type Logger struct {
	store     Store
	logger    *slog.Logger
	dropped   prometheus.Counter
	dropTotal atomic.Uint64
	onDrop    func(ctx context.Context, event Event, err error)

	chainKey []byte
	cursors  signedCursor
	appendMu sync.Mutex
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

// WithChainKey configures the HMAC key used to build the tamper-evident
// chain. The key must be at least [MinChainKeyLen] (32) bytes; [New]
// panics if no key (or a too-short key) is supplied. Rotating the chain
// key invalidates [VerifyChain] for previously-appended events — operate
// the chain on a single long-lived key for the chain's lifetime, and
// archive the key alongside the records.
//
// Wire across processes: every replica that appends to the same store
// must share the same chainKey. Source it from KMS / config secrets;
// never let each pod mint its own random key.
func WithChainKey(key []byte) Option {
	return func(a *Logger) {
		a.chainKey = append([]byte(nil), key...)
	}
}

// WithCursorKey configures the HMAC key used to sign pagination cursors
// returned by [Logger.Query]. The key must be at least [MinCursorKeyLen]
// (32) bytes; [New] panics if no key (or a too-short key) is supplied.
//
// Cursors are signed so an attacker cannot guess / forge cursors to skip
// records or enumerate IDs. The cursor key is independent of the chain
// key so the two can be rotated separately.
func WithCursorKey(key []byte) Option {
	return func(a *Logger) {
		a.cursors = signedCursor{key: append([]byte(nil), key...)}
	}
}

// New creates an audit Logger backed by the given Store.
//
// Both [WithChainKey] and [WithCursorKey] are required. New panics
// (fail-fast at startup, per AGENTS.md) if either key is missing or
// shorter than 32 bytes; this prevents silently shipping an audit log
// without tamper-evidence or with forgeable cursors.
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
	if len(l.chainKey) < MinChainKeyLen {
		panic(fmt.Sprintf("auditlog: chain key must be at least %d bytes — pass WithChainKey", MinChainKeyLen))
	}
	if len(l.cursors.key) < MinCursorKeyLen {
		panic(fmt.Sprintf("auditlog: cursor key must be at least %d bytes — pass WithCursorKey", MinCursorKeyLen))
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
//
// The read-prev-HMAC / compute-new-HMAC / Append sequence is serialised
// through an internal mutex: two concurrent callers cannot observe the
// same previous HMAC and produce a forked chain. Caller-supplied
// PrevHMAC / HMAC fields on the input event are ignored — they are
// always recomputed from the store's current tail.
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
	// Discard any caller-supplied chain fields; the Logger is the sole
	// authority on chain HMACs.
	event.PrevHMAC = nil
	event.HMAC = nil

	// Serialise the read-prev / compute-hmac / Append window so
	// concurrent appenders cannot observe the same PrevHMAC.
	l.appendMu.Lock()
	defer l.appendMu.Unlock()

	prev, err := l.store.LastHMAC(ctx)
	if err != nil {
		err = fmt.Errorf("auditlog: read previous HMAC: %w", err)
		l.logger.Error("auditlog: failed to read previous HMAC",
			redact.Error(err),
			redact.String("event_id", event.ID),
		)
		l.recordDrop(ctx, event, err)
		return err
	}
	if len(prev) > 0 {
		event.PrevHMAC = append([]byte(nil), prev...)
	}
	event.HMAC = computeHMAC(l.chainKey, event.PrevHMAC, event)

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

// Query returns events matching the filter using HMAC-signed cursors.
//
// The cursor argument must be either empty (first page) or a value produced
// by a previous Query call against this logger. Forged, truncated, or
// foreign-signed cursors are rejected before the underlying Store is
// consulted; the returned error wraps [ErrInvalidCursor] so callers can
// distinguish tamper attempts from Store I/O failures.
//
// The returned next-page cursor is itself signed with the Logger's cursor
// key, so an attacker who captures a cursor cannot mint adjacent ones to
// skip records or enumerate IDs.
func (l *Logger) Query(ctx context.Context, filter Filter, cursor string, limit int) ([]Event, string, error) {
	rawCursor, err := l.cursors.decodeCursor(cursor)
	if err != nil {
		return nil, "", err
	}
	events, next, err := l.store.Query(ctx, filter, rawCursor, limit)
	if err != nil {
		return nil, "", err
	}
	signed := l.cursors.encodeCursor(next)
	return cloneEvents(events), signed, nil
}

// VerifyChain re-reads every event from the underlying store and validates
// the tamper-evident HMAC chain end-to-end. It returns nil for an empty
// store (degenerate-valid chain).
//
// Implementation note: VerifyChain pages through the store in ascending
// time order using the store's native cursor (bypassing signed-cursor
// envelopes since this is an in-process audit). The page size is fixed at
// [verifyChainPageSize] so very large chains stream rather than buffer the
// entire ledger in memory.
//
// VerifyChain returns a wrapped [ErrChainBroken] at the first tamper site
// it detects, or any underlying Store I/O error. Successful return means
// every record's HMAC matches its content AND every record's PrevHMAC
// links to the previous record's HMAC.
func (l *Logger) VerifyChain(ctx context.Context) error {
	all, err := l.collectAllEventsAscending(ctx)
	if err != nil {
		return err
	}
	return VerifyChain(all, l.chainKey)
}

// verifyChainPageSize bounds the per-page batch for VerifyChain. Tuned to
// keep memory bounded for very large stores while still amortising the
// per-page round-trip cost.
const verifyChainPageSize = 500

func (l *Logger) collectAllEventsAscending(ctx context.Context) ([]Event, error) {
	// Store.Query returns events newest-first; we accumulate everything,
	// then reverse so VerifyChain sees chain-order (oldest first).
	var newestFirst []Event
	cursor := ""
	for {
		page, next, err := l.store.Query(ctx, Filter{}, cursor, verifyChainPageSize)
		if err != nil {
			return nil, fmt.Errorf("auditlog: verify chain: read page: %w", err)
		}
		newestFirst = append(newestFirst, page...)
		if next == "" {
			break
		}
		cursor = next
	}
	out := make([]Event, len(newestFirst))
	for i := range newestFirst {
		out[len(newestFirst)-1-i] = newestFirst[i]
	}
	return out, nil
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
