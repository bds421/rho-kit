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
	"errors"
	"fmt"
	"sync/atomic"
	"unicode"
	"unicode/utf8"

	"github.com/open-feature/go-sdk/openfeature"

	"github.com/bds421/rho-kit/core/v2/contextutil"
	"github.com/bds421/rho-kit/core/v2/tenant"
)

const (
	// MaxKeyLen bounds feature flag names before they reach the SDK/provider path.
	MaxKeyLen = 256
	// MaxUserKeyLen bounds user targeting keys attached through WithUserKey.
	MaxUserKeyLen = 256
)

var (
	// ErrInvalidClient is returned when a method is called on a Client
	// whose inner OpenFeature client is nil (typically a zero-value
	// Client that bypassed [New]).
	ErrInvalidClient = errors.New("flags: client is not initialized")
	// ErrInvalidContext is returned when a nil context is passed to an
	// evaluation method.
	ErrInvalidContext = errors.New("flags: context is nil")
	// ErrInvalidKey is returned when a flag key is empty, too long,
	// non-UTF-8, or contains whitespace/control characters.
	ErrInvalidKey = errors.New("flags: key is invalid")
	// ErrInvalidUserKey is returned when a [WithUserKey] value violates
	// the same shape constraints as a flag key.
	ErrInvalidUserKey = errors.New("flags: user key is invalid")
)

// Provider is the OpenFeature provider interface. Adapters
// (LaunchDarkly, flagd, in-memory) implement it; the kit's [Client]
// consumes it.
type Provider = openfeature.FeatureProvider

// Client is the kit's flag client. It wraps an OpenFeature client and
// auto-populates the evaluation context from request context (tenant,
// user, correlation ID).
type Client struct {
	inner   *openfeature.Client
	errHook atomic.Pointer[func(key, message string, err error)]
}

// New returns a Client backed by the given Provider. The provider is
// installed against an OpenFeature DOMAIN keyed by `name`, NOT the
// global default — so multiple kit Clients in the same process (e.g.
// a test harness, a service that talks to two flag systems, an
// orchestrator embedding sub-services) do not clobber each other.
//
// Audit FR-033 [HIGH]: pre-2.0 this called openfeature.SetProvider
// (the global) and ignored its error. Two `flags.New(...)` calls in
// the same process — common across tests — silently overwrote one
// another's provider, and a provider-init failure (auth issue, SDK
// load) was swallowed.
//
// Returns an error when SetNamedProviderAndWait reports a provider
// initialization failure. Panics only on programmer errors (nil
// provider, empty name).
func New(name string, p Provider) (*Client, error) {
	if p == nil {
		panic("flags: New: provider must not be nil")
	}
	if name == "" {
		panic("flags: New: domain name must not be empty")
	}
	if err := openfeature.SetNamedProviderAndWait(name, p); err != nil {
		return nil, fmt.Errorf("flags: install provider: %w", err)
	}
	return &Client{inner: openfeature.NewClient(name)}, nil
}

// MustNew is the panic-on-error variant of [New] for startup wiring
// where provider failure is fatal anyway. Prefer New from anywhere
// that can plumb the error to a structured boot failure.
func MustNew(name string, p Provider) *Client {
	c, err := New(name, p)
	if err != nil {
		panic("flags: MustNew: client configuration is invalid")
	}
	return c
}

// Bool evaluates a boolean flag, returning fallback on any error.
//
// Audit FR-034: provider/evaluation errors are silently swallowed in
// the convenience getters. Use [Client.BoolE] (and the matching
// String/Int/Float/Object error variants) when an upstream outage,
// malformed flag value, or kill-switch misconfiguration must be
// surfaced. Wire [Client.SetEvalErrorHook] to a Prometheus counter /
// alert to detect provider regressions in production.
func (c *Client) Bool(ctx context.Context, key string, fallback bool) bool {
	v, _ := c.BoolE(ctx, key, fallback)
	return v
}

// BoolE returns the evaluated bool plus any provider error.
func (c *Client) BoolE(ctx context.Context, key string, fallback bool) (bool, error) {
	if err := c.ready(); err != nil {
		return fallback, err
	}
	if err := ValidateKey(key); err != nil {
		c.observeError(key, err.Error(), err)
		return fallback, err
	}
	ec, err := evalCtx(ctx)
	if err != nil {
		c.observeError(key, err.Error(), err)
		return fallback, err
	}
	d, err := c.inner.BooleanValueDetails(ctx, key, fallback, ec)
	return d.Value, c.finishEval(key, d.EvaluationDetails, err)
}

// String evaluates a string flag.
func (c *Client) String(ctx context.Context, key, fallback string) string {
	v, _ := c.StringE(ctx, key, fallback)
	return v
}

// StringE returns the evaluated string plus any provider error.
func (c *Client) StringE(ctx context.Context, key, fallback string) (string, error) {
	if err := c.ready(); err != nil {
		return fallback, err
	}
	if err := ValidateKey(key); err != nil {
		c.observeError(key, err.Error(), err)
		return fallback, err
	}
	ec, err := evalCtx(ctx)
	if err != nil {
		c.observeError(key, err.Error(), err)
		return fallback, err
	}
	d, err := c.inner.StringValueDetails(ctx, key, fallback, ec)
	return d.Value, c.finishEval(key, d.EvaluationDetails, err)
}

// Int evaluates an integer flag.
func (c *Client) Int(ctx context.Context, key string, fallback int64) int64 {
	v, _ := c.IntE(ctx, key, fallback)
	return v
}

// IntE returns the evaluated int plus any provider error.
func (c *Client) IntE(ctx context.Context, key string, fallback int64) (int64, error) {
	if err := c.ready(); err != nil {
		return fallback, err
	}
	if err := ValidateKey(key); err != nil {
		c.observeError(key, err.Error(), err)
		return fallback, err
	}
	ec, err := evalCtx(ctx)
	if err != nil {
		c.observeError(key, err.Error(), err)
		return fallback, err
	}
	d, err := c.inner.IntValueDetails(ctx, key, fallback, ec)
	return d.Value, c.finishEval(key, d.EvaluationDetails, err)
}

// Float evaluates a float64 flag.
func (c *Client) Float(ctx context.Context, key string, fallback float64) float64 {
	v, _ := c.FloatE(ctx, key, fallback)
	return v
}

// FloatE returns the evaluated float plus any provider error.
func (c *Client) FloatE(ctx context.Context, key string, fallback float64) (float64, error) {
	if err := c.ready(); err != nil {
		return fallback, err
	}
	if err := ValidateKey(key); err != nil {
		c.observeError(key, err.Error(), err)
		return fallback, err
	}
	ec, err := evalCtx(ctx)
	if err != nil {
		c.observeError(key, err.Error(), err)
		return fallback, err
	}
	d, err := c.inner.FloatValueDetails(ctx, key, fallback, ec)
	return d.Value, c.finishEval(key, d.EvaluationDetails, err)
}

// Object evaluates an opaque-shape flag (typically JSON). Useful for
// configuration-style flags that ship a struct.
//
// Prefer the package-level [Object] / [ObjectE] generic free functions
// when the flag's payload has a known Go shape — those return T
// directly without the caller-side type assertion that this method's
// signature forces.
func (c *Client) Object(ctx context.Context, key string, fallback any) any {
	v, _ := c.ObjectE(ctx, key, fallback)
	return v
}

// ObjectE returns the evaluated object plus any provider error.
func (c *Client) ObjectE(ctx context.Context, key string, fallback any) (any, error) {
	if err := c.ready(); err != nil {
		return fallback, err
	}
	if err := ValidateKey(key); err != nil {
		c.observeError(key, err.Error(), err)
		return fallback, err
	}
	ec, err := evalCtx(ctx)
	if err != nil {
		c.observeError(key, err.Error(), err)
		return fallback, err
	}
	d, err := c.inner.ObjectValueDetails(ctx, key, fallback, ec)
	return d.Value, c.finishEval(key, d.EvaluationDetails, err)
}

// Object is a generic free function that returns the evaluated flag
// as T, eliminating the caller-side type assertion that
// [Client.Object] forces. On any error (eval failure, type mismatch
// between the provider's value and T) the fallback is returned with
// no error — matching the swallow-errors contract of [Client.Object].
//
// Type-mismatch case: if the provider returns a value that cannot be
// asserted to T, fallback is returned. If the caller needs to
// distinguish "flag missing" from "flag exists but wrong type", use
// [ObjectE].
func Object[T any](c *Client, ctx context.Context, key string, fallback T) T {
	v, err := ObjectE(c, ctx, key, fallback)
	if err != nil {
		return fallback
	}
	return v
}

// ObjectE is a generic free function that returns the evaluated flag
// as T plus any provider error. A successful eval whose returned
// value is not a T surfaces an explicit error so callers can branch
// on type mismatches.
func ObjectE[T any](c *Client, ctx context.Context, key string, fallback T) (T, error) {
	raw, err := c.ObjectE(ctx, key, fallback)
	if err != nil {
		return fallback, err
	}
	v, ok := raw.(T)
	if !ok {
		return fallback, fmt.Errorf("flags: %s: provider returned %T, want %T", key, raw, fallback)
	}
	return v, nil
}

// SetEvalErrorHook installs a callback fired whenever any flag
// evaluation reports an error code. Use it to bump a Prometheus
// counter or push a warning into the kit's audit log so silent
// provider regressions surface in monitoring (audit FR-034).
//
// The hook is called from the evaluation goroutine — keep it
// non-blocking. Setting nil clears the hook.
func (c *Client) SetEvalErrorHook(fn func(key, message string, err error)) {
	c.errHook.Store(&fn)
}

func (c *Client) ready() error {
	if c == nil || c.inner == nil {
		return ErrInvalidClient
	}
	return nil
}

func (c *Client) finishEval(key string, d openfeature.EvaluationDetails, err error) error {
	if d.ErrorCode != "" || err != nil {
		c.observeError(key, d.ErrorMessage, err)
		if err == nil && d.ErrorMessage != "" {
			err = errors.New(d.ErrorMessage)
		}
	}
	return err
}

func (c *Client) observeError(key, msg string, err error) {
	hookPtr := c.errHook.Load()
	if hookPtr == nil || *hookPtr == nil {
		return
	}
	(*hookPtr)(key, msg, err)
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
func evalCtx(ctx context.Context) (openfeature.EvaluationContext, error) {
	if ctx == nil {
		return openfeature.EvaluationContext{}, ErrInvalidContext
	}

	attrs := map[string]any{}
	targetingKey := ""

	if t, ok := tenant.FromContext(ctx); ok {
		targetingKey = t.String()
		attrs["tenant"] = t.String()
	}
	switch stored := ctx.Value(userKeyCtx{}).(type) {
	case userKeyValue:
		if stored.err != nil {
			return openfeature.EvaluationContext{}, stored.err
		}
		if stored.value == "" {
			break
		}
		attrs["user"] = stored.value
		if targetingKey == "" {
			targetingKey = stored.value
		}
	case string:
		if stored == "" {
			break
		}
		if err := validateUserKey(stored); err != nil {
			return openfeature.EvaluationContext{}, err
		}
		attrs["user"] = stored
		if targetingKey == "" {
			targetingKey = stored
		}
	}
	if cid := contextutil.CorrelationID(ctx); cid != "" {
		attrs["correlation_id"] = cid
	}

	return openfeature.NewEvaluationContext(targetingKey, attrs), nil
}

// ValidateKey validates a feature flag key before it reaches the SDK
// and provider path.
func ValidateKey(key string) error {
	return validateTokenish("key", key, MaxKeyLen, ErrInvalidKey)
}

func validateUserKey(userID string) error {
	return validateTokenish("user key", userID, MaxUserKeyLen, ErrInvalidUserKey)
}

func validateTokenish(name, value string, maxLen int, sentinel error) error {
	if value == "" {
		return fmt.Errorf("%w: %s must not be empty", sentinel, name)
	}
	if len(value) > maxLen {
		return fmt.Errorf("%w: %s exceeds maximum length", sentinel, name)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("%w: %s must be valid UTF-8", sentinel, name)
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return fmt.Errorf("%w: %s must not contain whitespace or control characters", sentinel, name)
		}
	}
	return nil
}

type userKeyValue struct {
	value string
	err   error
}

func validUserKeyValue(userID string) userKeyValue {
	if err := validateUserKey(userID); err != nil {
		return userKeyValue{err: err}
	}
	return userKeyValue{value: userID}
}

// WithUserKey attaches a user identifier to the evaluation context for
// the next call. Use this from request handlers where the user ID has
// been resolved (e.g. from a verified JWT subject) but lives in a
// package the flags module cannot import. The returned context is for
// short-lived use — wrap a single Bool/String/etc. call, don't store.
func WithUserKey(ctx context.Context, userID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if userID == "" {
		return ctx
	}
	return context.WithValue(ctx, userKeyCtx{}, validUserKeyValue(userID))
}

type userKeyCtx struct{}
