package redis

import (
	"context"
	"errors"
	"fmt"

	"github.com/bds421/rho-kit/observability/v2/health"
)

// ErrUnavailable is returned by FailFastPolicy when Redis is unavailable.
var ErrUnavailable = errors.New("redis: service unavailable")

// DegradationPolicy defines how a feature behaves when Redis is unavailable.
// Each Redis-dependent feature declares its own policy so degradation is
// granular rather than binary across all features.
type DegradationPolicy interface {
	// Name returns a human-readable identifier for this policy (e.g. "passthrough", "fail-fast").
	Name() string

	// OnUnavailable is called when Redis is unhealthy. Implementations decide
	// whether to return a sentinel error (fail-fast) or nil (pass-through).
	OnUnavailable(ctx context.Context) error
}

// ReadOnlyAware is implemented by [DegradationPolicy] values that distinguish
// "Redis is unreachable" from "Redis is reachable but read-only" (failover in
// progress). Callers can use this to keep serving reads while writes return
// [ErrPrimaryReadOnly].
type ReadOnlyAware interface {
	// OnReadOnly is invoked when the connection is reachable but the
	// server reported a READONLY reply for the most recent probe. Returning
	// nil tells the caller to continue (read-only paths are still safe).
	OnReadOnly(ctx context.Context) error
}

// PassthroughPolicy returns nil on unavailability, allowing the caller to
// fall back to its own default behavior (e.g. cache miss, in-memory fallback).
// This is appropriate for features where missing data is acceptable.
type PassthroughPolicy struct{}

// Name returns "passthrough".
func (PassthroughPolicy) Name() string { return "passthrough" }

// OnUnavailable returns nil, signaling the caller to proceed without Redis.
func (PassthroughPolicy) OnUnavailable(_ context.Context) error { return nil }

// FailFastPolicy returns ErrUnavailable immediately when Redis is down.
// Use this for features that cannot function without Redis (e.g. distributed locks).
type FailFastPolicy struct{}

// Name returns "fail-fast".
func (FailFastPolicy) Name() string { return "fail-fast" }

// OnUnavailable returns ErrUnavailable.
func (FailFastPolicy) OnUnavailable(_ context.Context) error { return ErrUnavailable }

// OnReadOnly returns [ErrPrimaryReadOnly]. Write-dependent features will see
// this sentinel and can surface it to clients (mapped to 503 / Retry-After
// by HTTP adapters). Read-only callers can compare against the sentinel and
// keep serving reads on the same client.
func (FailFastPolicy) OnReadOnly(_ context.Context) error { return ErrPrimaryReadOnly }

// CustomPolicy wraps a user-supplied function as a DegradationPolicy.
// The name must be a valid health check name (lowercase alphanumeric with hyphens/underscores).
type CustomPolicy struct {
	name string
	fn   func(ctx context.Context) error
}

// NewCustomPolicy creates a policy with a custom unavailability handler.
// Panics if name is empty or contains invalid characters.
func NewCustomPolicy(name string, fn func(ctx context.Context) error) CustomPolicy {
	if err := health.ValidateCheckName(name); err != nil {
		panic("redis: NewCustomPolicy: invalid custom policy name")
	}
	if fn == nil {
		panic("redis: NewCustomPolicy: custom policy function must not be nil")
	}
	return CustomPolicy{name: name, fn: fn}
}

// Name returns the policy name.
func (p CustomPolicy) Name() string { return p.name }

// OnUnavailable delegates to the user-supplied function.
func (p CustomPolicy) OnUnavailable(ctx context.Context) error { return p.fn(ctx) }

// FeatureCheck holds a named feature and its degradation policy for health reporting.
type FeatureCheck struct {
	// Feature is the health check name for this feature (e.g. "cache", "rate-limit").
	Feature string

	// Policy is the degradation policy applied when Redis is unavailable.
	// It is required; choose PassthroughPolicy or FailFastPolicy explicitly
	// so missing wiring cannot silently become non-critical.
	Policy DegradationPolicy
}

// PerFeatureHealthChecks returns health DependencyChecks for each registered feature.
// When Redis is healthy, all features report healthy. When Redis is unhealthy,
// each feature reports based on its policy: FailFastPolicy features report
// "unhealthy" and are marked critical; all other policies (including CustomPolicy)
// report "degraded" and are non-critical.
//
// Panics if conn is nil, any feature name is invalid, or any feature policy is nil.
func PerFeatureHealthChecks(conn *Connection, features []FeatureCheck) []health.DependencyCheck {
	if conn == nil {
		panic("redis: PerFeatureHealthChecks: connection must not be nil")
	}
	checks := make([]health.DependencyCheck, len(features))
	for i, fc := range features {
		checks[i] = newFeatureHealthCheck(conn, fc)
	}
	return checks
}

func newFeatureHealthCheck(conn *Connection, fc FeatureCheck) health.DependencyCheck {
	if err := health.ValidateCheckName(fc.Feature); err != nil {
		panic("redis: invalid feature name")
	}
	if fc.Policy == nil {
		panic("redis: degradation policy must not be nil")
	}
	checkName := fmt.Sprintf("redis-%s", fc.Feature)
	_, isFailFast := fc.Policy.(FailFastPolicy)
	return health.DependencyCheck{
		Name: checkName,
		Check: func(_ context.Context) string {
			if conn.Healthy() {
				return health.StatusHealthy
			}
			if !conn.WasConnected() {
				return health.StatusConnecting
			}
			if isFailFast {
				return health.StatusUnhealthy
			}
			return health.StatusDegraded
		},
		Critical: isFailFast,
	}
}
