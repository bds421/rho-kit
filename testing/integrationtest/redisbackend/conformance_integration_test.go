//go:build integration

package redisbackend

import (
	"testing"

	stream "github.com/bds421/rho-kit/data/stream/redisstream/v2"
	"github.com/bds421/rho-kit/infra/messaging/redisbackend/v2"
	"github.com/bds421/rho-kit/infra/v2/messaging"
	"github.com/bds421/rho-kit/infra/v2/messaging/messagingtest"
)

// TestRedisStreamPublisher_Conformance runs the kit's
// messaging.Publisher conformance battery against the Redis
// Streams publisher.
//
// Redis Streams accept publishes to any stream name on the
// fly — no pre-declaration needed. Each call's "test-exchange"
// becomes a stream entry; FlushDB on cleanup drops it.
func TestRedisStreamPublisher_Conformance(t *testing.T) {
	messagingtest.RunPublisher(t, func(t *testing.T) messaging.Publisher {
		client := redisClient(t)
		prod := stream.NewProducer(client)
		return redisbackend.NewPublisher(prod)
	})
}
