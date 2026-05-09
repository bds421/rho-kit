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
// asvs: V4.1.1, V4.1.5
package authz

import (
	"context"
	"errors"
)

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
// of req. Behaves identically to [Decider.Allow]; provided for
// readability at call sites that already have a Request value.
//
// FR-036 [MED]: returns [ErrNoDecider] (not a panic) when d is nil.
// Handlers using optional infrastructure get a typed configuration
// error they can translate into a 503/500 instead of a panic-bound
// 500 with no recovery information.
func Allow(ctx context.Context, d Decider, req Request) error {
	if d == nil {
		return ErrNoDecider
	}
	return d.Allow(ctx, req.Subject, req.Action, req.Resource)
}
