package authz

import (
	"context"
	"errors"
	"log/slog"

	"github.com/bds421/rho-kit/core/v2/redact"
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

// AuditSink consumes structured authorization-decision events.
// Implementations must be safe for concurrent use.
//
// Wiring with observability/auditlog: the concrete
// observability/auditlog.Logger does NOT directly satisfy this
// interface (Logger ships Log/LogE/LogAction/List/VerifyChain, not
// LogAuthz, and authz keeps no dependency on the observability
// module). Adapt with [AuditSinkFunc]:
//
//	authz.WithAuditSink(authz.AuditSinkFunc(
//	    func(ctx context.Context, e authz.AuditEvent) {
//	        auditLogger.Log(ctx, auditlog.Event{
//	            Actor:    e.Actor,
//	            Action:   e.Action,
//	            Resource: e.Resource,
//	            Status:   e.Outcome,
//	        })
//	    },
//	))
type AuditSink interface {
	LogAuthz(ctx context.Context, event AuditEvent)
}

// AuditSinkFunc adapts an ordinary function into an [AuditSink]. Use
// it to wire a Logger / metric / async pipeline as the sink without
// declaring a struct.
type AuditSinkFunc func(ctx context.Context, event AuditEvent)

// LogAuthz implements [AuditSink].
func (f AuditSinkFunc) LogAuthz(ctx context.Context, event AuditEvent) { f(ctx, event) }

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
// every Allow call wrapped by [Logged]. The authz package keeps no dependency
// on observability — wire a Logger by adapting it via [AuditSinkFunc] (see
// the [AuditSink] docstring for the canonical pattern). Consumers pay no
// extra dep cost when the sink is not wired.
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
		// Identifier-shaped fields (subject SPIFFE/JWT id, resource path,
		// engine verb) can be up to 512 bytes of tenant- or topology-
		// carrying content. The structured audit sink keeps full values
		// for compliance; the slog stream is operator-facing and uses
		// redact.String so a tenant id or resource path does not leak
		// into ordinary runtime logs (matches httpx/middleware/auth's
		// redaction policy).
		c.logger.Log(ctx, level, "authz decision",
			slog.String("action", verb),
			redact.String("actor", subject),
			redact.String("resource", resource),
			redact.String("verb", action),
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
