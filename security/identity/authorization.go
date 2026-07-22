package identity

import (
	"context"
	"errors"

	"github.com/bds421/rho-kit/authz/v2"
)

// ErrPrincipalRequired is returned when authorization is requested before an
// authentication boundary has established a canonical principal.
var ErrPrincipalRequired = errors.New("identity: principal required")

// Allow evaluates an authorization request for the canonical principal in
// ctx. It keeps HTTP and gRPC callers on the same subject projection instead
// of re-reading provider-specific JWT claims at each policy decision.
func Allow(ctx context.Context, decider authz.Decider, action, resource string) error {
	p, ok := FromContext(ctx)
	if !ok || p.Subject == "" {
		return ErrPrincipalRequired
	}
	return authz.Allow(ctx, decider, authz.Request{
		Subject:  p.Subject,
		Action:   action,
		Resource: resource,
	})
}

// AuditActor returns the canonical actor for an audit event, or "anonymous"
// if authentication did not establish a principal. It is suitable for an
// audit middleware actor extractor: identity.AuditActor(r.Context()).
func AuditActor(ctx context.Context) string {
	p, ok := FromContext(ctx)
	if !ok || p.Actor == "" {
		return "anonymous"
	}
	return p.Actor
}
