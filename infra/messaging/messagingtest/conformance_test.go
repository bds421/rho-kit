package messagingtest_test

import (
	"testing"

	"github.com/bds421/rho-kit/infra/v2/messaging"
	"github.com/bds421/rho-kit/infra/v2/messaging/membroker"
	"github.com/bds421/rho-kit/infra/v2/messaging/messagingtest"
)

// TestMembroker_PublisherConformance dogfoods the publisher
// conformance suite against the in-memory membroker. Every
// production backend (amqpbackend, kafkabackend, natsbackend,
// redisbackend) MUST pass the same suite from its own
// integration tests.
func TestMembroker_PublisherConformance(t *testing.T) {
	messagingtest.RunPublisher(t, func(t *testing.T) messaging.Publisher {
		return membroker.New()
	})
}
