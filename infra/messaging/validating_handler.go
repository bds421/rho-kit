package messaging

import (
	"context"
	"fmt"
)

// NewValidatingHandler returns a handler that validates each delivery's payload
// against the schema registered for its SchemaVersion before delegating to next.
// If validation fails, the error is returned (the consumer decides ACK/NACK policy).
// If no schema is registered for the version, the message passes through
// (backward compat: unversioned messages are not rejected).
//
// Panics if registry or next is nil (fail-fast on misconfiguration).
//
// Composes with NewVersionedHandler:
//
//	handler := NewValidatingHandler(registry, NewVersionedHandler(handlers))
func NewValidatingHandler(registry *InMemorySchemaRegistry, next Handler) Handler {
	if registry == nil {
		panic("validating handler: registry must not be nil")
	}
	if next == nil {
		panic("validating handler: next handler must not be nil")
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
		if err := registry.ValidateMessage(validationMsg); err != nil {
			return fmt.Errorf("message %s (type %s, version %d): %w",
				msg.ID, msg.Type, d.SchemaVersion, err)
		}
		return next(ctx, d)
	}
}
