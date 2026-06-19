// Package messagingtest provides Run functions that exercise the
// contracts a [github.com/bds421/rho-kit/infra/v2/messaging.Publisher]
// implementation must honour. Use it from your backend's test
// package to assert that a new Publisher implementation behaves
// identically to amqpbackend, kafkabackend, natsbackend, and
// redisbackend.
//
// # Publisher conformance
//
// RunPublisher exercises the Publisher contract:
//
//   - Publish with a nil context is rejected with
//     ErrInvalidPublishContext.
//   - Publish with a cancelled context returns context.Canceled
//     (or a wrapped ErrInvalidPublishContext).
//   - Publish of N messages in sequence succeeds (the broker
//     accepts them).
//   - Publish is safe to call concurrently from multiple
//     goroutines.
//   - Message values published are not mutated in flight (the
//     publish call accepts the canonical payload unchanged).
//
// # Consumer conformance
//
// The Consumer side is intentionally NOT in v2.0.0's conformance
// surface — its contract spans backend-specific retry/DLQ
// semantics (Kafka returns ErrRetryUnsupported, AMQP routes
// through a DLX, NATS uses JetStream max-deliver). Each backend's
// own integration tests exercise these per-broker invariants.
// A future kit wave can land a parameterized Consumer suite
// once enough backends agree on a common retry-shape contract.
//
// # Usage
//
//	package mybackend_test
//
//	import (
//	    "testing"
//
//	    "github.com/bds421/rho-kit/infra/v2/messaging"
//	    "github.com/bds421/rho-kit/infra/v2/messaging/messagingtest"
//	    "github.com/example/mybackend"
//	)
//
//	func TestPublisherConformance(t *testing.T) {
//	    messagingtest.RunPublisher(t, func(t *testing.T) messaging.Publisher {
//	        return mybackend.NewPublisher(/* ... */)
//	    })
//	}
package messagingtest
