package redisstream

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewConsumer_EmptyGroup(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	_, err := NewConsumer(client, "")
	assert.Error(t, err)
}

func TestNewConsumer_Defaults(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	c, err := NewConsumer(client, "test-group")
	require.NoError(t, err)

	assert.Equal(t, defaultBlockDuration, c.blockDuration)
	assert.Equal(t, defaultClaimMinIdle, c.claimMinIdle)
	assert.Equal(t, defaultClaimInterval, c.claimInterval)
	assert.Equal(t, int64(defaultBatchSize), c.batchSize)
	assert.Equal(t, int64(defaultMaxRetries), c.maxRetries)
	assert.Equal(t, defaultDeadLetterMaxLen, c.deadLetterMaxLen)
	assert.NotEmpty(t, c.consumer) // UUID v7 generated
}

func TestConsumerOptions(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	c, err := NewConsumer(client, "test-group",
		WithBlockDuration(10*time.Second),
		WithClaimMinIdle(10*time.Minute),
		WithClaimInterval(time.Minute),
		WithBatchSize(50),
		WithMaxRetries(10),
		WithDeadLetterMaxLen(5000),
	)
	require.NoError(t, err)

	assert.Equal(t, 10*time.Second, c.blockDuration)
	assert.Equal(t, 10*time.Minute, c.claimMinIdle)
	assert.Equal(t, time.Minute, c.claimInterval)
	assert.Equal(t, int64(50), c.batchSize)
	assert.Equal(t, int64(10), c.maxRetries)
	assert.Equal(t, int64(5000), c.deadLetterMaxLen)
}

func TestConsumerOptions_IgnoresInvalid(t *testing.T) {
	c := &Consumer{
		blockDuration: defaultBlockDuration,
		claimMinIdle:  defaultClaimMinIdle,
		claimInterval: defaultClaimInterval,
		batchSize:     defaultBatchSize,
		maxRetries:    defaultMaxRetries,
	}

	WithBlockDuration(-1)(c)
	WithClaimMinIdle(0)(c)
	WithClaimInterval(-time.Second)(c)
	WithBatchSize(0)(c)
	WithMaxRetries(-1)(c)
	WithDeadLetterMaxLen(-1)(c)

	assert.Equal(t, defaultBlockDuration, c.blockDuration)
	assert.Equal(t, defaultClaimMinIdle, c.claimMinIdle)
	assert.Equal(t, defaultClaimInterval, c.claimInterval)
	assert.Equal(t, int64(defaultBatchSize), c.batchSize)
	assert.Equal(t, int64(defaultMaxRetries), c.maxRetries)
}

func TestWithConsumerName_PanicsOnInvalid(t *testing.T) {
	assert.Panics(t, func() {
		WithConsumerName("")(&Consumer{})
	})
}

func TestWithDeadLetterStream_PanicsOnInvalid(t *testing.T) {
	assert.Panics(t, func() {
		WithDeadLetterStream("")(&Consumer{})
	})
}

func TestConsume_PanicsOnEmptyStream(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	c, err := NewConsumer(client, "test-group")
	require.NoError(t, err)

	assert.Panics(t, func() {
		c.Consume(context.TODO(), "", nil) //nolint:staticcheck // intentionally testing panic with empty stream name
	})
}

func TestConsumer_PanicsOnSecondConsume(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	c, err := NewConsumer(client, "test-group")
	require.NoError(t, err)

	// First call: prime the consumed flag without actually blocking on
	// Redis. We use an immediately-cancelled ctx so consumeOnce returns
	// quickly. (RunWithBackoff observes ctx.Err() and exits.)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c.Consume(ctx, "stream-a", func(_ context.Context, _ Message) error { return nil })

	// Second call: must panic, even with a different stream.
	assert.PanicsWithValue(t,
		"redisstream: Consumer.Consume called for a second stream — create a separate Consumer per stream (see StartConsumers)",
		func() {
			c.Consume(ctx, "stream-b", func(_ context.Context, _ Message) error { return nil })
		},
	)
}

func TestConsumer_CloneForStreamHasFreshID(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	c, err := NewConsumer(client, "test-group")
	require.NoError(t, err)

	cp, err := c.cloneForStream()
	require.NoError(t, err)
	assert.NotEqual(t, c.consumer, cp.consumer, "clone must have a fresh consumer ID")
	assert.False(t, cp.consumed.Load(), "clone must be reusable")
}
