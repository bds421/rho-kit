//go:build integration

package natsbackend_test

import (
	"context"
	"testing"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/bds421/rho-kit/infra/messaging/natsbackend/v2"
	"github.com/bds421/rho-kit/infra/v2/messaging"
	"github.com/bds421/rho-kit/infra/v2/messaging/messagingtest"
)

// TestNATSPublisher_Conformance runs the kit's
// messaging.Publisher conformance battery against natsbackend.
//
// NATS JetStream requires the publisher's subject to map to an
// existing stream. The kit's EnsureStream only succeeds on the
// first call per (url, name) — subsequent calls with a non-
// matching shape error out. This test therefore shares ONE
// stream across all conformance subtests by setting it up ONCE
// at the top of this function and handing the same Publisher
// pointer back from the factory.
//
// Each subtest publishes to the same broker, but since the suite
// only asserts publish acceptance (not consumer-side state),
// concurrent subtests don't interfere.
func TestNATSPublisher_Conformance(t *testing.T) {
	url := startNATS(t)
	conn, err := natsbackend.Connect(context.Background(), natsbackend.Config{
		URL:           url,
		AllowInsecure: true,
	})
	if err != nil {
		t.Fatalf("connect NATS: %v", err)
	}
	t.Cleanup(func() { conn.Stop(context.Background()) })

	// Suite publishes to (exchange, routingKey) pairs which NATS
	// treats as subjects. Declare specific patterns rather than
	// the catch-all ">" which JetStream rejects.
	err = conn.EnsureStream(context.Background(), natsbackend.StreamConfig{
		Name:        "CONFORMANCE",
		Subjects:    []string{"test-exchange.>", "orders.>"},
		Retention:   jetstream.LimitsPolicy,
		StorageType: jetstream.MemoryStorage,
	})
	if err != nil {
		t.Fatalf("ensure stream: %v", err)
	}
	pub := natsbackend.NewPublisher(conn)

	messagingtest.RunPublisher(t, func(_ *testing.T) messaging.Publisher {
		return pub
	})
}
