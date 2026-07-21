// Package stream defines portable interfaces for event streaming with
// consumer groups: [Message], [Handler], [Producer], and [Consumer].
//
// # Implementations
//
// [github.com/bds421/rho-kit/data/stream/redisstream/v2] is the kit's
// Redis Streams backend. It defines its own Message/Handler types (with
// size caps and headers) and does not satisfy [Producer] or [Consumer]
// at the type level; import redisstream directly (as
// infra/messaging/redisbackend does). The core package shares the
// [ErrInvalidStream] sentinel used by that backend.
//
// [Producer] / [Consumer] remain exported for external adapters. Prefer
// redisstream's concrete API until an adapter bridges the shapes.
//
// [Producer.Produce] returns the backend-assigned message ID (empty when
// the backend does not assign one).
package stream
