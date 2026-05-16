package messaging

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/core/v2/validate"
)

// TypedHandler is the typed variant of [Handler]: the kit decodes
// the [Delivery]'s payload into T (and validates it via
// [validate.Struct]) before invoking the handler with the
// decoded value. The original Delivery is available for callers
// that need header/correlation-id access.
type TypedHandler[T any] func(ctx context.Context, msg T, raw Delivery) error

// TypedSubscription wraps a [TypedHandler] in the kit's
// payload-decode + validate boilerplate so handlers operate on
// typed values rather than raw byte slices. The decoding contract
// mirrors httpx's typed handlers:
//
//   - Payload MUST be JSON. Other media types fall through with a
//     `messaging/subscription: decode` error and the message is
//     nacked.
//   - The decoded value is validated against any jsonschema tags
//     via [validate.Struct] (kit convention). [WithoutTypedValidation]
//     opts out for callers whose T has no tags or who validate
//     elsewhere.
//   - Decode and validation failures are returned to the consumer
//     so the backend's nack/dead-letter policy applies — the
//     handler is not called. This matches the kit's "fail loud"
//     stance on malformed payloads.
//
// Construct via [NewTypedSubscription]. The resulting value is a
// [Subscription] that can be wired into [SubscriptionGroup] or a
// [lifecycle.Runner] alongside other components.
type TypedSubscription[T any] struct {
	*Subscription
}

// NewTypedSubscription constructs a typed subscription. Same panic-
// on-misconfiguration guarantees as [NewSubscription] plus a panic
// when handler is the typed-nil variant.
func NewTypedSubscription[T any](
	name string,
	consumer Consumer,
	binding Binding,
	handler TypedHandler[T],
	opts ...SubscriptionOption,
) *TypedSubscription[T] {
	if handler == nil {
		panic("messaging: NewTypedSubscription requires a non-nil handler")
	}
	cfg := subscriptionConfig{}
	for _, opt := range opts {
		if opt == nil {
			panic("messaging: NewTypedSubscription option must not be nil")
		}
		opt(&cfg)
	}
	skipValidate := cfg.skipTypedValidation
	if cfg.logger == nil {
		cfg.logger = slog.Default()
	}
	logger := cfg.logger
	wrapped := func(ctx context.Context, d Delivery) error {
		var msg T
		if len(d.Message.Payload) > 0 {
			if err := json.Unmarshal(d.Message.Payload, &msg); err != nil {
				logger.WarnContext(ctx, "messaging: typed subscription decode failure",
					redact.String("name", name),
					redact.Error(err),
				)
				return redact.WrapError("messaging/subscription: decode", err)
			}
		}
		if !skipValidate {
			if err := validate.Struct(msg); err != nil {
				logger.WarnContext(ctx, "messaging: typed subscription validation failure",
					redact.String("name", name),
					redact.Error(err),
				)
				return fmt.Errorf("messaging/subscription: validate: %w", err)
			}
		}
		return handler(ctx, msg, d)
	}
	// Re-apply options for the underlying Subscription so its logger
	// matches. Passing an additional WithSubscriptionLogger after a
	// caller-supplied one is harmless: later wins, both have the same
	// value here.
	sub := NewSubscription(name, consumer, binding, wrapped, opts...)
	return &TypedSubscription[T]{Subscription: sub}
}

// WithoutTypedValidation suppresses the kit's [validate.Struct]
// call inside [TypedSubscription] dispatch. Use when T has no
// jsonschema tags (validation is a no-op anyway) or when the
// service performs its own validation in the handler.
//
// Discouraged for shared business types where tag-driven validation
// catches schema drift between producers and consumers.
func WithoutTypedValidation() SubscriptionOption {
	return func(c *subscriptionConfig) { c.skipTypedValidation = true }
}
