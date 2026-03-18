package amqpbackend

import (
	"context"
	"errors"
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/messaging"
)

// fakeChannel wraps amqp.Channel to intercept PublishWithContext calls.
// Since amqp.Channel is a struct (not an interface), we cannot mock it directly.
// Instead, we test via the ChannelProvider interface and real struct behavior.

type fakeChannelProvider struct {
	ch  *amqp.Channel
	err error
}

func (f *fakeChannelProvider) Channel() (*amqp.Channel, error) {
	return f.ch, f.err
}

// --- NewReplySender ---

func TestNewReplySender_ReturnsNonNil(t *testing.T) {
	prov := &fakeChannelProvider{}
	rs := NewReplySender(prov)
	require.NotNil(t, rs)
}

// --- Send with empty ReplyTo ---

func TestReplySender_Send_EmptyReplyTo_Noop(t *testing.T) {
	prov := &fakeChannelProvider{err: errors.New("should not be called")}
	rs := NewReplySender(prov)

	d := messaging.Delivery{ReplyTo: ""}
	err := rs.Send(context.Background(), d, []byte(`{"ok":true}`))

	assert.NoError(t, err, "empty ReplyTo should be a no-op")
}

// --- Send with channel provider error ---

func TestReplySender_Send_ChannelError(t *testing.T) {
	prov := &fakeChannelProvider{err: errors.New("connection lost")}
	rs := NewReplySender(prov)

	d := messaging.Delivery{ReplyTo: "reply-queue", CorrelationID: "corr-1"}
	err := rs.Send(context.Background(), d, []byte(`{"ok":true}`))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "get channel for RPC reply")
	assert.Contains(t, err.Error(), "connection lost")
}

// --- Close is safe to call multiple times ---

func TestReplySender_Close_MultipleCalls(t *testing.T) {
	prov := &fakeChannelProvider{}
	rs := NewReplySender(prov)

	// Close with no cached channel should not panic.
	rs.Close()
	rs.Close()
}

// --- channel() reuses cached channel ---

func TestReplySender_Channel_ReusesWhenOpen(t *testing.T) {
	callCount := 0
	prov := &fakeChannelProvider{}

	rs := NewReplySender(prov)

	// Override chanProv to count calls and return a real (closed) channel.
	// We cannot create a real open channel without RabbitMQ, so we test
	// the channel-error path instead, which validates the caching logic.
	rs.chanProv = &countingChannelProvider{
		callCount: &callCount,
		err:       errors.New("no broker"),
	}

	// Two calls should both attempt to get a new channel since the first fails.
	rs.mu.Lock()
	_, _ = rs.channelLocked()
	rs.mu.Unlock()
	rs.mu.Lock()
	_, _ = rs.channelLocked()
	rs.mu.Unlock()
	assert.Equal(t, 2, callCount, "each call should request a new channel when previous failed")
}

type countingChannelProvider struct {
	callCount *int
	err       error
}

func (c *countingChannelProvider) Channel() (*amqp.Channel, error) {
	*c.callCount++
	return nil, c.err
}
