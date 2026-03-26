package redis

import (
	"context"
	"errors"
	"fmt"

	"github.com/bds421/rho-kit/observability/health"
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
		panic(fmt.Sprintf("redis: invalid custom policy name: %v", err))
	}
	if fn == nil {
		panic("redis: custom policy function must not be nil")
	}
	return CustomPolicy{name: name, fn: fn}
}

// Name returns the policy name.
func (p CustomPolicy) Name() string { return p.name }

// OnUnavailable delegates to the user-supplied function.
func (p CustomPolicy) OnUnavailable(ctx context.Context) error { return p.fn(ctx) }

// FeatureStatus represents the degradation state of a single feature.
type FeatureStatus struct {
	Feature  string `json:"feature"`
	Policy   string `json:"policy"`
	Degraded bool   `json:"degraded"`
}

// FeatureCheck holds a named feature and its degradation policy for health reporting.
type FeatureCheck struct {
	// Feature is the health check name for this feature (e.g. "cache", "rate-limit").
	Feature string

	// Policy is the degradation policy applied when Redis is unavailable.
	Policy DegradationPolicy
}

// PerFeatureHealthChecks returns health DependencyChecks for each registered feature.
// When Redis is healthy, all features report healthy. When Redis is unhealthy,
// each feature reports based on its policy: passthrough policies report "degraded",
// fail-fast policies report "unhealthy".
func PerFeatureHealthChecks(conn *Connection, features []FeatureCheck) []health.DependencyCheck {
	checks := make([]health.DependencyCheck, len(features))
	for i, fc := range features {
		checks[i] = newFeatureHealthCheck(conn, fc)
	}
	return checks
}

func newFeatureHealthCheck(conn *Connection, fc FeatureCheck) health.DependencyCheck {
	_, isFailFast := fc.Policy.(FailFastPolicy)
	return health.DependencyCheck{
		Name: fmt.Sprintf("redis-%s", fc.Feature),
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
