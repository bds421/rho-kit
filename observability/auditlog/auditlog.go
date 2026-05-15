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
	"github.com/bds421/rho-kit/core/v2/secret"
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

// ErrLoggerClosed is returned by Logger operations after [Logger.Close]
// has zeroed the chain / cursor keys. Subsequent LogE / Query / VerifyChain
// calls fail fast with this error rather than appending unauthenticated
// records.
var ErrLoggerClosed = errors.New("auditlog: logger is closed")

// MaxPageLimit caps the per-page limit accepted by [Logger.List]. A
// caller mapping `?limit=1000000000` from a URL parameter would
// otherwise force the store to allocate huge slices and emit a giant
// LIMIT. Callers needing more must page using the signed cursor.
const MaxPageLimit = 10_000

// ErrLimitTooLarge is returned by [Logger.List] when the limit
// argument exceeds [MaxPageLimit].
var ErrLimitTooLarge = errors.New("auditlog: list limit exceeds MaxPageLimit")

// ErrLimitNegative is returned by [Logger.List] when the limit argument
// is negative. A negative limit's behaviour is Store-specific — the
// bundled [MemoryStore] treats limit <= 0 as a default-50 page, but a
// custom (e.g. postgres) Store might interpret it as "no limit" and
// stream the whole table into memory. Rejecting at the Logger keeps
// every Store implementation safe from caller-controlled unbounded
// scans without depending on Store-side defensive code.
var ErrLimitNegative = errors.New("auditlog: list limit must not be negative")

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
	// AppendChained reads the tail HMAC, calls build with it, and persists
	// the resulting event atomically under a per-store lock. Logger.LogE
	// uses this to extend the tamper-evident chain without holding its own
	// mutex across user-supplied Store code — pre-2.0 the Logger held a
	// global appendMu across LastHMAC+Append, which made a slow or
	// re-entrant Store implementation a deadlock attractor and serialised
	// every LogE call on Append latency.
	//
	// Implementations MUST hold their internal lock across the read-tail
	// / call-build / persist sequence so two concurrent appenders cannot
	// observe the same prev HMAC and produce a forked chain. The supplied
	// build function may return an error to abort the append (e.g. for
	// per-event validation); the Store must NOT persist the resulting
	// event in that case.
	AppendChained(ctx context.Context, build func(prev []byte) (Event, error)) error

	// Query returns events matching the filter, ordered by timestamp descending.
	// Returns the next cursor for pagination (empty string if no more results).
	// The cursor returned here is a raw store-level cursor; [Logger.List]
	// wraps it with a signed envelope before exposing it to callers.
	//
	// Query order is for user-facing list views only. It MUST NOT be
	// relied on for chain integrity: see [Store.RangeChain] for
	// append-order traversal.
	Query(ctx context.Context, filter Filter, cursor string, limit int) ([]Event, string, error)

	// RangeChain calls fn for every event in append order (oldest first),
	// regardless of timestamp. Used exclusively by [Logger.VerifyChain]
	// so chain integrity does not depend on caller-supplied
	// [Event.Timestamp] values — a service that backfills a historical
	// event with an older timestamp would otherwise break chain
	// verification under Query's timestamp ordering, even though the
	// HMAC chain is intact.
	//
	// Implementations MUST iterate in the same order events were
	// appended via [Store.AppendChained]. Postgres-backed stores
	// typically scan by a monotonic SERIAL column; the bundled
	// [MemoryStore] iterates its append slice. Returning fn's error
	// stops iteration immediately.
	RangeChain(ctx context.Context, fn func(Event) error) error

	// LastHMAC returns the HMAC of the most recently appended event, or an
	// empty / all-zero slice if the store is empty. Useful for chain
	// inspection / operator tooling; not on the Logger.LogE hot path
	// after the AppendChained refactor. Implementations must be safe for
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
// Chain serialization is delegated to [Store.AppendChained]: the Store
// holds its own per-store lock across the read-tail / compute-HMAC /
// persist callback so two concurrent appenders cannot observe the same
// PrevHMAC and produce a chain fork.
//
// Safe for concurrent use — Log / LogE / List / VerifyChain can be
// called from many goroutines. Close coordinates with in-flight LogE
// callbacks via secret.String.Use's internal lock plus an empty-key
// check inside the chained build, so an append cannot complete with a
// zeroed chainKey (see [Logger.Close]).
type Logger struct {
	store     Store
	logger    *slog.Logger
	dropped   prometheus.Counter
	dropTotal atomic.Uint64
	onDrop    func(ctx context.Context, event Event, err error)

	// chainKey wraps the HMAC key for the tamper-evident chain in
	// [secret.String] so the bytes can be zeroed at shutdown via
	// [Logger.Close] and never linger on the heap as a long-lived
	// []byte. Reveal-into-local happens inside the closure passed
	// to [computeHMACWithKey].
	chainKey    *secret.String
	chainKeyLen int
	cursors     signedCursor
	closed      atomic.Bool
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
		a.chainKey = secret.New(key)
		a.chainKeyLen = len(key)
	}
}

// WithCursorKey configures the HMAC key used to sign pagination cursors
// returned by [Logger.List]. The key must be at least [MinCursorKeyLen]
// (32) bytes; [New] panics if no key (or a too-short key) is supplied.
//
// Cursors are signed so an attacker cannot guess / forge cursors to skip
// records or enumerate IDs. The cursor key is independent of the chain
// key so the two can be rotated separately.
func WithCursorKey(key []byte) Option {
	return func(a *Logger) {
		a.cursors = signedCursor{key: secret.New(key), keyLen: len(key)}
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
		panic("auditlog: New: store must not be nil")
	}
	l := &Logger{
		store:  store,
		logger: slog.Default(),
	}
	for _, o := range opts {
		if o == nil {
			panic("auditlog: New: option must not be nil")
		}
		o(l)
	}
	if l.chainKeyLen < MinChainKeyLen {
		panic(fmt.Sprintf("auditlog: chain key must be at least %d bytes — pass WithChainKey", MinChainKeyLen))
	}
	if l.cursors.keyLen < MinCursorKeyLen {
		panic(fmt.Sprintf("auditlog: cursor key must be at least %d bytes — pass WithCursorKey", MinCursorKeyLen))
	}
	return l
}

// Close zeroes the chain and cursor HMAC keys held by the Logger.
// Subsequent calls to [Logger.LogE], [Logger.List], or
// [Logger.VerifyChain] return [ErrLoggerClosed]. Idempotent — calling
// Close after the Logger is already closed is a no-op.
//
// Close does not touch the underlying [Store]; close that separately
// if it owns connections / file handles.
//
// Race-safety: LogE re-reads the closed flag inside the AppendChained
// build callback AND inspects the snapshot it receives from
// chainKey.Use. If Close.Zero happens between the closed-load and the
// snapshot, the snapshot is empty; LogE treats that as
// [ErrLoggerClosed] and aborts the append, so no event signed with
// zeroed key material can ever reach the store.
func (l *Logger) Close() error {
	if l == nil {
		return nil
	}
	if !l.closed.CompareAndSwap(false, true) {
		return nil
	}
	if l.chainKey != nil {
		l.chainKey.Zero()
	}
	if l.cursors.key != nil {
		l.cursors.key.Zero()
	}
	return nil
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
// Chain construction (read-tail / compute-HMAC / persist) is delegated
// to [Store.AppendChained] so the Logger no longer holds a global mutex
// across user-supplied Store code. Caller-supplied PrevHMAC / HMAC
// fields on the input event are ignored — they are always recomputed
// from the store's current tail inside the chained build callback.
func (l *Logger) LogE(ctx context.Context, event Event) error {
	if l.closed.Load() {
		return ErrLoggerClosed
	}
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

	var buildErr error
	err := l.store.AppendChained(ctx, func(prev []byte) (Event, error) {
		// Re-check the closed flag inside the chained callback so a
		// concurrent Close that wipes chainKey cannot race a HMAC
		// computation over zeroed key material.
		if l.closed.Load() {
			buildErr = ErrLoggerClosed
			return Event{}, ErrLoggerClosed
		}
		if len(prev) > 0 {
			event.PrevHMAC = append([]byte(nil), prev...)
		}
		var (
			mac      []byte
			keyEmpty bool
		)
		l.chainKey.Use(func(k []byte) {
			// chainKey.Use snapshots the key under secret.String's
			// internal lock. If Close.Zero ran between the closed-load
			// above and the snapshot, k is empty here. Treat that as
			// ErrLoggerClosed so we never persist an event signed with
			// nil/zero key material — that would silently break
			// VerifyChain at shutdown boundaries (H2-003).
			if len(k) == 0 {
				keyEmpty = true
				return
			}
			mac = computeHMAC(k, event.PrevHMAC, event)
		})
		if keyEmpty {
			buildErr = ErrLoggerClosed
			return Event{}, ErrLoggerClosed
		}
		event.HMAC = mac
		if vErr := ValidateEvent(event); vErr != nil {
			buildErr = vErr
			return Event{}, vErr
		}
		return event, nil
	})
	if err != nil {
		if buildErr != nil {
			l.logger.Error("auditlog: failed to build event for append",
				redact.Error(buildErr),
				redact.String("event_id", event.ID),
			)
			l.recordDrop(ctx, event, buildErr)
			return buildErr
		}
		err = fmt.Errorf("auditlog: append event: %w", err)
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
func (l *Logger) List(ctx context.Context, filter Filter, cursor string, limit int) ([]Event, string, error) {
	if l.closed.Load() {
		return nil, "", ErrLoggerClosed
	}
	if limit < 0 {
		return nil, "", ErrLimitNegative
	}
	if limit > MaxPageLimit {
		return nil, "", ErrLimitTooLarge
	}
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

// VerifyChain re-reads every event from the underlying store and
// validates the tamper-evident HMAC chain end-to-end. It returns nil
// for an empty store (degenerate-valid chain).
//
// VerifyChain streams the store via [Store.RangeChain], which yields
// events in append order (oldest first) regardless of timestamp.
// Memory is bounded to one event in flight. At each step it confirms:
//
//  1. The recomputed HMAC over the canonical encoding of the event
//     (using event.PrevHMAC + event-without-HMAC) equals event.HMAC.
//  2. The chain link is intact: event[i].PrevHMAC must equal
//     event[i-1].HMAC for i > 0.
//  3. The first (oldest) event has an empty or zero PrevHMAC — i.e.
//     the chain has a valid genesis.
//
// Using append order (not timestamp order) means a service that
// backfills a historical entry, or one whose clock skews backwards,
// still has a verifiable chain — the previous timestamp-ordered
// implementation could reject a valid chain in either case.
//
// VerifyChain returns a wrapped [ErrChainBroken] at the first tamper
// site it detects, or any underlying Store I/O error. Successful
// return means every record's HMAC matches its content AND every
// record's PrevHMAC links to the previous record's HMAC.
func (l *Logger) VerifyChain(ctx context.Context) error {
	if l.closed.Load() {
		return ErrLoggerClosed
	}
	var verifyErr error
	l.chainKey.Use(func(k []byte) {
		if len(k) == 0 {
			verifyErr = ErrLoggerClosed
			return
		}
		if len(k) < MinChainKeyLen {
			verifyErr = fmt.Errorf("%w: chain key must be at least %d bytes", ErrChainBroken, MinChainKeyLen)
			return
		}
		verifyErr = l.streamVerifyChain(ctx, k)
	})
	return verifyErr
}

func (l *Logger) streamVerifyChain(ctx context.Context, chainKey []byte) error {
	var (
		prevHMAC []byte // HMAC of the previously visited (older) event
		seenAny  bool
		index    int // append-order index, for error messages
	)
	err := l.store.RangeChain(ctx, func(event Event) error {
		// 1. Self-consistency: recomputed HMAC must equal stored HMAC.
		expected := computeHMAC(chainKey, event.PrevHMAC, eventWithoutHMAC(event))
		if !constantTimeEqualHMAC(event.HMAC, expected) {
			return fmt.Errorf("%w: event[%d] HMAC does not match canonical content", ErrChainBroken, index)
		}
		// 2. Genesis / link: first event's PrevHMAC must be empty or
		// zero; subsequent events must point at the previous event's HMAC.
		if !seenAny {
			if len(event.PrevHMAC) != 0 && !isZeroBytes(event.PrevHMAC) {
				return fmt.Errorf("%w: event[0] PrevHMAC must be empty or zero", ErrChainBroken)
			}
		} else if !constantTimeEqualHMAC(event.PrevHMAC, prevHMAC) {
			return fmt.Errorf("%w: event[%d] PrevHMAC does not match event[%d] HMAC", ErrChainBroken, index, index-1)
		}
		prevHMAC = event.HMAC
		seenAny = true
		index++
		return nil
	})
	if err != nil {
		return err
	}
	return nil
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
