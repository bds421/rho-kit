package natsbackend

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestComposeSubject_RoutingKeyOptional(t *testing.T) {
	assert.Equal(t, "events", composeSubject("events", ""))
	assert.Equal(t, "events.user.created", composeSubject("events", "user.created"))
}

func TestSplitSubject_RoundTripsCompose(t *testing.T) {
	tests := []struct {
		subject  string
		exchange string
		routing  string
	}{
		{"events", "events", ""},
		{"events.user.created", "events", "user.created"},
		{"plain", "plain", ""},
	}
	for _, tt := range tests {
		ex, rk := splitSubject(tt.subject)
		assert.Equal(t, tt.exchange, ex, "subject=%q", tt.subject)
		assert.Equal(t, tt.routing, rk, "subject=%q", tt.subject)
	}
}

func TestConnect_RejectsEmptyURL(t *testing.T) {
	_, err := Connect(t.Context(), Config{})
	assert.Error(t, err)
}

// TestNewConsumer_DefaultsMaxDeliverTo5 pins the v1 H-3 audit fix:
// without a cap, JetStream's default of -1 (unlimited) means a
// poison-pill message that reliably triggers a panic in the handler
// gets nacked forever and blocks the consumer's progress. The fix
// sets MaxDeliver=5 when the operator hasn't supplied a value, so
// JetStream gives up after 5 attempts and either drops or routes to
// the configured DLQ.
func TestNewConsumer_DefaultsMaxDeliverTo5(t *testing.T) {
	c := NewConsumer(&Connection{}, ConsumerConfig{
		Stream:  "events",
		Durable: "consumer-1",
	}, nil)
	assert.Equal(t, 5, c.cfg.MaxDeliver,
		"NewConsumer must default MaxDeliver to 5 to cap poison-pill redelivery")
}

// TestNewConsumer_RespectsExplicitMaxDeliver confirms the operator
// can override the default — including with a negative value, which
// opts into JetStream's unlimited-redelivery semantics for callers
// that genuinely want it.
func TestNewConsumer_RespectsExplicitMaxDeliver(t *testing.T) {
	for _, n := range []int{1, 5, 100, -1} {
		c := NewConsumer(&Connection{}, ConsumerConfig{
			Stream:     "events",
			Durable:    "consumer-1",
			MaxDeliver: n,
		}, nil)
		assert.Equal(t, n, c.cfg.MaxDeliver, "MaxDeliver=%d must be honoured verbatim", n)
	}
}
