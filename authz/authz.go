// Package authz defines the kit's authorization decision interface.
// v2 added this as the vendor-neutral seam so handlers ask "can this
// subject perform this action on this resource?" without coupling to
// a particular policy engine. Engine adapters (OpenFGA, Cedar,
// Casbin, in-memory) implement [Decider] and plug in via app.Builder
// configuration.
//
// The kit deliberately does NOT ship its own RBAC/ABAC engine —
// authorization is hard, error-prone, and the OWASP recommendation
// is to use a battle-tested external system. The kit's job is the
// interface, the audit-log integration, the request-context wiring;
// the actual decision goes to the engine.
//
// # Audit logging
//
// SOC2 CC6.3 requires the access-control decision to be recorded at the
// point of decision. Wrap any [Decider] with [Logged] (or pass an existing
// Decider into [Logged]) to emit a structured record on every Allow call:
//
//   - deny: slog Info ("authz.deny") with subject, resource, action, reason
//   - allow: slog Debug ("authz.allow") — operators enable verbose authz
//     logging to surface the allow path
//
// The same record is also delivered to an optional [AuditSink] for
// tamper-evident retention. Default Builder pipes a logger automatically;
// raw httpx.Middleware users must wire it themselves.
//
// asvs: V4.1.1, V4.1.5
package authz

import (
	"context"
	"errors"
	"fmt"
	"unicode"
	"unicode/utf8"

	"github.com/bds421/rho-kit/core/v2/redact"
)

// MaxRequestPartLen bounds the byte length of each field in a [Request]
// (subject, action, resource) before it reaches a [Decider]. Engines vary
// widely on their own ceilings; 512 bytes is large enough for SPIFFE
// IDs, deeply-nested resource paths, and namespaced action verbs, while
// small enough to keep policy-engine input lookups predictable and to
// make pathological inputs (gigabyte URLs, attacker-supplied logs)
// fail closed at the kit layer.
const MaxRequestPartLen = 512

// Decider answers authorization questions. Implementations may be
// engine-backed (OpenFGA, Cedar, Casbin), in-memory (the [Memory]
// adapter for tests), or fully custom.
//
// Allow returns nil when the subject is permitted to perform action
// on resource within ctx, [ErrDenied] when the engine evaluates the
// request and refuses it, or a wrapped engine error otherwise. The
// distinction matters: ErrDenied is a security-relevant audit event,
// other errors are infra failures the caller should surface
// differently.
type Decider interface {
	Allow(ctx context.Context, subject, action, resource string) error
}

// ErrDenied is the sentinel returned by [Decider.Allow] when the
// engine evaluates the request and refuses it. Wrap with errors.Is
// to distinguish from infra errors.
var ErrDenied = errors.New("authz: denied")

// ErrNoDecider is returned by [Allow] when the supplied Decider is
// nil. Audit FR-036: this used to panic, which gave handlers a 500
// instead of failing closed. Returning a typed error lets handlers
// distinguish wiring errors from authorization denials.
var ErrNoDecider = errors.New("authz: no decider configured")

// ErrDeciderPanic is returned by [Allow] when the supplied Decider
// panics while evaluating a request.
var ErrDeciderPanic = errors.New("authz: decider panicked")

// ErrInvalidRequest is returned when an authorization triple is not
// well-formed enough to submit to a policy engine. It is also wrapped
// with [ErrDenied] because malformed authorization questions must fail
// closed.
var ErrInvalidRequest = errors.New("authz: invalid request")

// ErrInvalidContext is returned when a nil context is supplied to a
// kit authorization helper.
var ErrInvalidContext = errors.New("authz: context is nil")

// Request bundles the inputs to a [Decider.Allow] call into a
// struct-shaped form. Useful for callers that build requests
// dynamically (e.g., from a route descriptor) and pass them down
// without expanding three positional args.
type Request struct {
	Subject  string
	Action   string
	Resource string
}

// Allow is a convenience wrapper that calls d.Allow with the fields
// of req. Normal Decider returns are passed through unchanged;
// decider panics are converted to an [ErrDeciderPanic]-wrapped error.
// Provided for readability at call sites that already have a Request
// value.
//
// FR-036 [MED]: returns [ErrNoDecider] (not a panic) when d is nil.
// Handlers using optional infrastructure get a typed configuration
// error they can translate into a 503/500 instead of a panic-bound
// 500 with no recovery information.
func Allow(ctx context.Context, d Decider, req Request) (err error) {
	if d == nil {
		return ErrNoDecider
	}
	if ctx == nil {
		return ErrInvalidContext
	}
	if err := ValidateRequest(req); err != nil {
		return err
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("%w: %s", ErrDeciderPanic, redact.PanicValue(recovered))
		}
	}()
	return d.Allow(ctx, req.Subject, req.Action, req.Resource)
}

// ValidateRequest validates an authorization triple before it reaches a
// Decider implementation. The kit keeps this intentionally generic:
// it does not impose an engine-specific tuple grammar, but it rejects
// empty, overlong, invalid UTF-8, whitespace, and control characters.
func ValidateRequest(req Request) error {
	if err := validatePart("subject", req.Subject); err != nil {
		return err
	}
	if err := validatePart("action", req.Action); err != nil {
		return err
	}
	if err := validatePart("resource", req.Resource); err != nil {
		return err
	}
	return nil
}

func validatePart(name, value string) error {
	if value == "" {
		return fmt.Errorf("%w: %s must not be empty: %w", ErrInvalidRequest, name, ErrDenied)
	}
	if len(value) > MaxRequestPartLen {
		return fmt.Errorf("%w: %s exceeds maximum length: %w", ErrInvalidRequest, name, ErrDenied)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("%w: %s must be valid UTF-8: %w", ErrInvalidRequest, name, ErrDenied)
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return fmt.Errorf("%w: %s must not contain whitespace or control characters: %w", ErrInvalidRequest, name, ErrDenied)
		}
	}
	return nil
}
