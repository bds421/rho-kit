package kafkabackend

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/v2/messaging"
)

func TestNewPublisher_RejectsEmptyBrokers(t *testing.T) {
	_, err := NewPublisher(nil)
	require.Error(t, err)
}

func TestNewPublisher_RejectsPlaintextNoAuth(t *testing.T) {
	_, err := NewPublisher([]string{"localhost:9092"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "FR-073")
}

func TestNewPublisher_RejectsNilOption(t *testing.T) {
	_, err := NewPublisherWithConfig(Config{
		Brokers:       []string{"localhost:9092"},
		AllowInsecure: true,
	}, nil)
	require.Error(t, err)
}

func TestPublisher_PublishOnNilWriterReturnsErrInvalidPublisher(t *testing.T) {
	var p *Publisher
	err := p.Publish(context.Background(), "events", "k", messaging.Message{})
	assert.ErrorIs(t, err, messaging.ErrInvalidPublisher)
}

func TestPublisher_ValidatesPublishContext(t *testing.T) {
	pub := mustPublisher(t)
	defer func() { _ = pub.Close() }()
	//nolint:staticcheck // intentionally passing a nil context to exercise the guard
	err := pub.Publish(nil, "events", "k", validMsg(t))
	require.ErrorIs(t, err, messaging.ErrInvalidPublishContext)
}

func TestPublisher_ValidatesRoute(t *testing.T) {
	pub := mustPublisher(t)
	defer func() { _ = pub.Close() }()
	err := pub.Publish(context.Background(), "", "k", validMsg(t))
	require.ErrorIs(t, err, messaging.ErrInvalidRoute)
}

func TestPublisher_ValidatesMessage(t *testing.T) {
	pub := mustPublisher(t)
	defer func() { _ = pub.Close() }()
	err := pub.Publish(context.Background(), "events", "k", messaging.Message{})
	require.ErrorIs(t, err, messaging.ErrInvalidMessage)
}

func TestPublisher_EnforcesSizeLimit(t *testing.T) {
	pub, err := NewPublisherWithConfig(
		Config{Brokers: []string{"localhost:9092"}, AllowInsecure: true},
		WithMaxMessageBytes(64),
	)
	require.NoError(t, err)
	defer func() { _ = pub.Close() }()
	huge, err := messaging.NewMessage("big", map[string]string{"payload": string(make([]byte, 1024))})
	require.NoError(t, err)
	err = pub.Publish(context.Background(), "events", "k", huge)
	assert.ErrorIs(t, err, messaging.ErrMessageTooLarge)
}

func TestWithBatchTimeout_NegativePanics(t *testing.T) {
	assert.Panics(t, func() { WithBatchTimeout(-1) })
}

func TestWithBatchSize_ZeroPanics(t *testing.T) {
	assert.Panics(t, func() { WithBatchSize(0) })
}

func TestWithPublisherMetrics_NilPanics(t *testing.T) {
	assert.Panics(t, func() { WithPublisherMetrics(nil) })
}

func mustPublisher(t *testing.T) *Publisher {
	t.Helper()
	pub, err := NewPublisherWithConfig(Config{
		Brokers:       []string{"localhost:9092"},
		AllowInsecure: true,
	})
	require.NoError(t, err)
	return pub
}

func validMsg(t *testing.T) messaging.Message {
	t.Helper()
	msg, err := messaging.NewMessage("user.created", map[string]string{"k": "v"})
	require.NoError(t, err)
	return msg
}
