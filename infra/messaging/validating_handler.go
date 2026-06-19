package messaging

import (
	"context"
	"fmt"

	"github.com/bds421/rho-kit/core/v2/apperror"
	"github.com/bds421/rho-kit/core/v2/redact"
)

// ErrUnknownSchemaVersion is returned by a validating handler in strict
// mode ([WithStrictUnknownVersion]) when a delivery's type has at least
// one registered schema but the delivery's (transport-header-controlled)
// schema version is not one of them. It is an [apperror.ValidationError]
// so HTTP and gRPC adapters map it to 400/InvalidArgument.
var ErrUnknownSchemaVersion = apperror.NewValidation("messaging: unknown schema version for a type with registered schemas")

// ValidatingHandlerOption configures [NewValidatingHandler].
type ValidatingHandlerOption func(*validatingHandlerConfig)

type validatingHandlerConfig struct {
	strictUnknownVersion bool
}

// WithStrictUnknownVersion makes the validating handler reject a delivery
// whose message type HAS registered schemas but whose delivery-level
// SchemaVersion is not among them, instead of passing the unvalidated
// payload through.
//
// Why this is opt-in: the schema version is populated from a transport
// header the producer (or any peer that can publish) controls, so the
// default pass-through means a peer can set X-Schema-Version to an
// unregistered value and skip validation entirely for a type that
// otherwise enforces a schema. Strict mode closes that bypass.
//
// Strict mode still passes through the two intentional legacy cases:
//   - version 0 (unversioned/legacy messages), and
//   - message types with NO registered schemas at all.
//
// Only an unregistered NON-zero version of a type that has at least one
// registered schema is rejected.
func WithStrictUnknownVersion() ValidatingHandlerOption {
	return func(c *validatingHandlerConfig) { c.strictUnknownVersion = true }
}

// NewValidatingHandler returns a handler that validates each delivery's payload
// against the schema registered for its SchemaVersion before delegating to next.
// If validation fails, the error is returned (the consumer decides ACK/NACK policy).
// If no schema is registered for the version, the message passes through
// (backward compat: unversioned messages are not rejected). Pass
// [WithStrictUnknownVersion] to reject unregistered non-zero versions of
// types that have registered schemas.
//
// Panics if registry or next is nil (fail-fast on misconfiguration).
//
// Composes with NewVersionedHandler:
//
//	handler := NewValidatingHandler(registry, NewVersionedHandler(handlers))
func NewValidatingHandler(registry *InMemorySchemaRegistry, next Handler, opts ...ValidatingHandlerOption) Handler {
	if registry == nil {
		panic("messaging: NewValidatingHandler requires a non-nil registry")
	}
	if next == nil {
		panic("messaging: NewValidatingHandler requires a non-nil next handler")
	}

	var cfg validatingHandlerConfig
	for _, opt := range opts {
		if opt == nil {
			panic("messaging: NewValidatingHandler option must not be nil")
		}
		opt(&cfg)
	}

	return func(ctx context.Context, d Delivery) error {
		msg := d.Message
		// Use delivery-level SchemaVersion (populated from transport header)
		// to build the validation lookup key.
		validationMsg := Message{
			Type:          msg.Type,
			Payload:       msg.Payload,
			SchemaVersion: d.SchemaVersion,
		}
		// Strict mode: reject an unregistered non-zero version of a type
		// that has registered schemas, before the registry's pass-through
		// would silently skip validation. Version 0 (legacy) and types
		// with no schemas remain pass-through.
		if cfg.strictUnknownVersion && d.SchemaVersion != 0 {
			if _, err := registry.Lookup(msg.Type, d.SchemaVersion); err != nil {
				if len(registry.Versions(msg.Type)) > 0 {
					// Do not echo the attacker-controlled version value to
					// avoid log/response-injection; the sentinel is enough.
					return fmt.Errorf("%w", ErrUnknownSchemaVersion)
				}
			}
		}
		if err := registry.ValidateMessage(validationMsg); err != nil {
			return redact.WrapError("message validation failed", err)
		}
		return next(ctx, d)
	}
}
