package authz

import (
	"context"
	"errors"
	"log/slog"
)

// AuditEvent is the cross-package field set emitted for one authorization
// decision. The shape matches the cross-cutting audit envelope used elsewhere
// in rho-kit (see security/jwtutil/revocation) so a single sink can decode
// events from any source.
type AuditEvent struct {
	Action   string // dot-namespaced verb: "authz.allow" or "authz.deny"
	Actor    string // subject (the principal being checked)
	Resource string // resource the decision applied to
	Verb     string // engine-level action verb (the third tuple element)
	Outcome  string // "success" | "deny" | "error"
	Reason   string // short error-class for deny / error; empty on allow
}

// AuditSink consumes structured authorization-decision events. The shape
// matches the minimum surface of observability/auditlog.Logger so production
// callers wire the concrete Logger here without authz importing the
// observability module. Implementations must be safe for concurrent use.
type AuditSink interface {
	LogAuthz(ctx context.Context, event AuditEvent)
}

// LoggedOption configures a [Logged] decorator.
type LoggedOption func(*loggedConfig)

type loggedConfig struct {
	logger *slog.Logger
	sink   AuditSink
}

// WithLogger wires a slog.Logger that receives a structured record on every
// Allow call wrapped by [Logged]. Deny is emitted at info level; allow at
// debug. Failure to satisfy the request validation pre-check is treated as a
// deny so the audit record is still produced.
//
// Panics on nil to fail fast at wiring time.
func WithLogger(l *slog.Logger) LoggedOption {
	if l == nil {
		panic("authz: WithLogger requires a non-nil logger")
	}
	return func(c *loggedConfig) { c.logger = l }
}

// WithAuditSink wires an [AuditSink] that receives the structured event for
// every Allow call wrapped by [Logged]. The concrete
// observability/auditlog.Logger satisfies this surface; authz does not depend
// on the observability module so consumers pay no extra dep cost when the
// sink is not wired.
//
// Panics on nil to fail fast at wiring time.
func WithAuditSink(sink AuditSink) LoggedOption {
	if sink == nil {
		panic("authz: WithAuditSink requires a non-nil sink")
	}
	return func(c *loggedConfig) { c.sink = sink }
}

// Logged decorates inner so every Allow call emits a structured audit record.
// The wrapper preserves the inner Decider's return contract: nil for allow,
// [ErrDenied] for deny, wrapped error otherwise.
//
// At least one of [WithLogger] / [WithAuditSink] is required; passing none
// panics at construction time so a misconfigured "audited" decider cannot
// silently degrade to plain pass-through.
//
// The wrapped Decider is invoked unchanged — Logged is purely additive. If
// inner is nil the returned Decider returns [ErrNoDecider]; callers wiring
// optional infrastructure get the same typed error they would have without
// the wrapper.
func Logged(inner Decider, opts ...LoggedOption) Decider {
	cfg := loggedConfig{}
	for _, opt := range opts {
		if opt == nil {
			panic("authz: Logged option must not be nil")
		}
		opt(&cfg)
	}
	if cfg.logger == nil && cfg.sink == nil {
		panic("authz: Logged requires WithLogger or WithAuditSink")
	}
	return &loggedDecider{inner: inner, cfg: cfg}
}

type loggedDecider struct {
	inner Decider
	cfg   loggedConfig
}

func (d *loggedDecider) Allow(ctx context.Context, subject, action, resource string) error {
	if d == nil {
		return ErrNoDecider
	}
	if d.inner == nil {
		return ErrNoDecider
	}
	err := d.inner.Allow(ctx, subject, action, resource)
	d.cfg.emit(ctx, subject, action, resource, err)
	return err
}

func (c *loggedConfig) emit(ctx context.Context, subject, action, resource string, err error) {
	verb, outcome, reason, level := classify(err)
	if c.logger != nil {
		c.logger.Log(ctx, level, "authz decision",
			slog.String("action", verb),
			slog.String("actor", subject),
			slog.String("resource", resource),
			slog.String("verb", action),
			slog.String("outcome", outcome),
			slog.String("reason", reason),
		)
	}
	if c.sink != nil {
		c.sink.LogAuthz(ctx, AuditEvent{
			Action:   verb,
			Actor:    subject,
			Resource: resource,
			Verb:     action,
			Outcome:  outcome,
			Reason:   reason,
		})
	}
}

// classify maps an Allow return value to the audit envelope. The reason string
// carries a short error class — never the wrapped Error() string, which can
// include engine topology / message text. Callers who need the full chain
// inspect the returned error.
func classify(err error) (action, outcome, reason string, level slog.Level) {
	switch {
	case err == nil:
		return "authz.allow", "success", "", slog.LevelDebug
	case errors.Is(err, ErrInvalidRequest):
		return "authz.deny", "deny", "invalid_request", slog.LevelInfo
	case errors.Is(err, ErrNoDecider):
		return "authz.deny", "error", "no_decider", slog.LevelInfo
	case errors.Is(err, ErrInvalidContext):
		return "authz.deny", "error", "invalid_context", slog.LevelInfo
	case errors.Is(err, ErrDeciderPanic):
		return "authz.deny", "error", "decider_panic", slog.LevelInfo
	case errors.Is(err, ErrDenied):
		return "authz.deny", "deny", "denied", slog.LevelInfo
	default:
		return "authz.deny", "error", "engine_error", slog.LevelInfo
	}
}
