package messaging

import (
	"context"
	"fmt"
)

// NewVersionedHandler creates a Handler that dispatches to version-specific
// handlers based on the SchemaVersion of the incoming Delivery.
//
// The handlers map keys are schema versions. A key of 0 matches unversioned
// messages (backward compatibility). If no handler matches the delivery's
// schema version, the returned handler returns an error.
//
// Panics if handlers is nil, empty, or contains a nil handler value.
func NewVersionedHandler(handlers map[SchemaVersion]Handler) Handler {
	if len(handlers) == 0 {
		panic("versioned handler requires at least one version handler")
	}

	// Copy the map to prevent external mutation and validate entries.
	dispatch := make(map[SchemaVersion]Handler, len(handlers))
	for v, h := range handlers {
		if h == nil {
			panic(fmt.Sprintf("versioned handler: nil handler for schema version %d", v))
		}
		dispatch[v] = h
	}

	return func(ctx context.Context, d Delivery) error {
		h, ok := dispatch[d.SchemaVersion]
		if !ok {
			return fmt.Errorf("no handler registered for schema version %d (message type %s, id %s)",
				d.SchemaVersion, d.Message.Type, d.Message.ID)
		}
		return h(ctx, d)
	}
}
