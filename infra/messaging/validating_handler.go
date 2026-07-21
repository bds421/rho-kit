package messaging

import (
	"context"
	"fmt"

	"github.com/bds421/rho-kit/core/v2/apperror"
	"github.com/bds421/rho-kit/core/v2/redact"
)

// ErrUnknownSchemaVersion is returned by a validating handler (default
// strict mode) and by [InMemorySchemaRegistry.ValidatePayload] when a
// delivery's type has at least one registered schema but the delivery's
// (transport-header-controlled) schema version is not one of them —
// including version 0 / missing version when no v0 schema is registered.
// It is an [apperror.ValidationError] so HTTP and gRPC adapters map it to
// 400/InvalidArgument.
var ErrUnknownSchemaVersion = apperror.NewValidation("messaging: unknown schema version for a type with registered schemas")

// ValidatingHandlerOption configures [NewValidatingHandler].
type ValidatingHandlerOption func(*validatingHandlerConfig)

type validatingHandlerConfig struct {
	// strictUnknownVersion defaults to true. When true, unregistered
	// versions (including 0) of types that have schemas are rejected.
	strictUnknownVersion bool
}

// WithLooseUnknownVersion restores the legacy fail-open behaviour: an
// unregistered schema version (including version 0) of a type that has
// registered schemas is allowed through without payload validation.
//
// Why the default is strict: the schema version is populated from a
// transport header the producer (or any peer that can publish) controls,
// so pass-through means a peer can set X-Schema-Version to an
// unregistered value and skip validation entirely for a type that
// otherwise enforces a schema. Strict mode closes that bypass.
//
// Prefer the default. Use this only for staged migrations of unversioned
// or multi-version peers. Pair with [WithSchemaLegacyPassThrough] when
// the registry is also [InMemorySchemaRegistry] so both layers agree.
//
// [WithLegacyPassThrough] is an alias of this option.
func WithLooseUnknownVersion() ValidatingHandlerOption {
	return func(c *validatingHandlerConfig) { c.strictUnknownVersion = false }
}

// WithLegacyPassThrough is an alias of [WithLooseUnknownVersion].
func WithLegacyPassThrough() ValidatingHandlerOption {
	return WithLooseUnknownVersion()
}

// NewValidatingHandler returns a handler that validates each delivery's payload
// against the schema registered for its SchemaVersion before delegating to next.
// If validation fails, the error is returned (the consumer decides ACK/NACK policy).
//
// Default (strict): when a message type has at least one registered schema
// and the delivery's SchemaVersion is not among them — including version 0
// / unversioned when no v0 schema is registered — the handler rejects with
// [ErrUnknownSchemaVersion]. Message types with no registered schemas still
// pass through. Pass [WithLooseUnknownVersion] / [WithLegacyPassThrough] to
// restore legacy pass-through for unknown versions.
//
// Panics if registry or next is nil (fail-fast on misconfiguration).
//
// Composes with NewVersionedHandler:
//
//	handler := NewValidatingHandler(registry, NewVersionedHandler(handlers))
func NewValidatingHandler(registry SchemaRegistry, next Handler, opts ...ValidatingHandlerOption) Handler {
	if registry == nil {
		panic("messaging: NewValidatingHandler requires a non-nil registry")
	}
	if next == nil {
		panic("messaging: NewValidatingHandler requires a non-nil next handler")
	}

	cfg := validatingHandlerConfig{strictUnknownVersion: true}
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
		// Strict mode: reject an unregistered version (including 0) of a
		// type that has registered schemas, before a legacy/loose registry
		// pass-through would silently skip validation. Types with no
		// schemas remain pass-through.
		if cfg.strictUnknownVersion {
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
