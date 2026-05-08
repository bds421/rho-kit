// Package flags wraps the OpenFeature Go SDK with the kit's
// tenant/user context conventions so call sites never see SDK types
// directly. The wrapping serves three purposes:
//
//  1. Vendor neutrality: providers (LaunchDarkly, flagd, GrowthBook,
//     in-memory) plug in via a single Provider type alias. Swapping
//     vendors does not touch call sites.
//  2. Context propagation: tenant + user IDs lift out of
//     core/contextutil and into the OpenFeature evaluation context
//     automatically. Consumers don't reconstruct evaluation contexts
//     by hand for every flag check.
//  3. Type-safe getters: Bool, String, Int, Float, JSON helpers that
//     return zero-value defaults on provider error so a flag-system
//     outage never crashes a request path.
package flags

import (
	"context"

	"github.com/open-feature/go-sdk/openfeature"

	"github.com/bds421/rho-kit/core/v2/contextutil"
	"github.com/bds421/rho-kit/core/v2/tenant"
)

// Provider is the OpenFeature provider interface. Adapters
// (LaunchDarkly, flagd, in-memory) implement it; the kit's [Client]
// consumes it.
type Provider = openfeature.FeatureProvider

// Client is the kit's flag client. It wraps an OpenFeature client and
// auto-populates the evaluation context from request context (tenant,
// user, correlation ID).
type Client struct {
	inner *openfeature.Client
}

// New returns a Client backed by the given Provider. Panics on a nil
// provider — flag wiring is startup-time configuration, not runtime
// state.
func New(name string, p Provider) *Client {
	if p == nil {
		panic("flags: provider must not be nil")
	}
	openfeature.SetProvider(p) //nolint:errcheck // SDK swallows nil-provider errors which we just rejected.
	return &Client{inner: openfeature.NewClient(name)}
}

// Bool evaluates a boolean flag, returning fallback on any error.
func (c *Client) Bool(ctx context.Context, key string, fallback bool) bool {
	v, _ := c.inner.BooleanValue(ctx, key, fallback, evalCtx(ctx))
	return v
}

// String evaluates a string flag.
func (c *Client) String(ctx context.Context, key, fallback string) string {
	v, _ := c.inner.StringValue(ctx, key, fallback, evalCtx(ctx))
	return v
}

// Int evaluates an integer flag.
func (c *Client) Int(ctx context.Context, key string, fallback int64) int64 {
	v, _ := c.inner.IntValue(ctx, key, fallback, evalCtx(ctx))
	return v
}

// Float evaluates a float64 flag.
func (c *Client) Float(ctx context.Context, key string, fallback float64) float64 {
	v, _ := c.inner.FloatValue(ctx, key, fallback, evalCtx(ctx))
	return v
}

// Object evaluates an opaque-shape flag (typically JSON). Useful for
// configuration-style flags that ship a struct.
func (c *Client) Object(ctx context.Context, key string, fallback any) any {
	v, _ := c.inner.ObjectValue(ctx, key, fallback, evalCtx(ctx))
	return v
}

// evalCtx builds an OpenFeature EvaluationContext from request
// context. Tenant ID is the targeting key by default — flag rollouts
// scope per tenant. Correlation ID lifts to a "correlation_id"
// attribute so flag-evaluation logs join cleanly to request traces.
//
// User-ID extraction lives in the auth middleware (httpx) to avoid a
// circular dep here. Callers that need user-level targeting should
// merge the user attribute themselves via [WithUserKey] before
// calling Bool/String/etc.
func evalCtx(ctx context.Context) openfeature.EvaluationContext {
	attrs := map[string]any{}
	targetingKey := ""

	if t, ok := tenant.FromContext(ctx); ok {
		targetingKey = t.String()
		attrs["tenant"] = t.String()
	}
	if uid, ok := ctx.Value(userKeyCtx{}).(string); ok && uid != "" {
		attrs["user"] = uid
		if targetingKey == "" {
			targetingKey = uid
		}
	}
	if cid := contextutil.CorrelationID(ctx); cid != "" {
		attrs["correlation_id"] = cid
	}

	return openfeature.NewEvaluationContext(targetingKey, attrs)
}

// WithUserKey attaches a user identifier to the evaluation context for
// the next call. Use this from request handlers where the user ID has
// been resolved (e.g. from a verified JWT subject) but lives in a
// package the flags module cannot import. The returned context is for
// short-lived use — wrap a single Bool/String/etc. call, don't store.
func WithUserKey(ctx context.Context, userID string) context.Context {
	if userID == "" {
		return ctx
	}
	return context.WithValue(ctx, userKeyCtx{}, userID)
}

type userKeyCtx struct{}
