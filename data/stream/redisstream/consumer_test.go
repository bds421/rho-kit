package redisstream

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	kitstream "github.com/bds421/rho-kit/data/v2/stream"
)

func TestNewConsumer_EmptyGroup(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	_, err := NewConsumer(client, "")
	assert.Error(t, err)
}

func TestNewConsumer_PanicsOnNilOption(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	assert.Panics(t, func() {
		_, _ = NewConsumer(client, "test-group", nil)
	})
}

func TestConsumer_InvalidReceiverReturnsError(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name     string
		consumer *Consumer
	}{
		{"nil", nil},
		{"zero", &Consumer{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Empty(t, tc.consumer.Group())

			clone, err := tc.consumer.cloneForStream()
			assert.Nil(t, clone)
			assert.ErrorIs(t, err, kitstream.ErrInvalidStream)

			assert.Panics(t, func() {
				tc.consumer.Consume(ctx, "test:stream", func(context.Context, Message) error { return nil })
			})
		})
	}
}

func TestCallHandler_ConvertsPanicToError(t *testing.T) {
	c := &Consumer{}
	err := c.callHandler(context.Background(), func(context.Context, Message) error {
		panic("handler exploded")
	}, Message{ID: "msg-1"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "handler panic")
	assert.Contains(t, err.Error(), "<redacted panic value: string>")
	assert.NotContains(t, err.Error(), "handler exploded")
}

func TestCallHandler_ClonesMessageForHandler(t *testing.T) {
	c := &Consumer{}
	msg := Message{
		ID:      "msg-1",
		Payload: []byte(`{"ok":true}`),
		Headers: map[string]string{"X-Trace": "abc123"},
	}

	err := c.callHandler(context.Background(), func(_ context.Context, got Message) error {
		got.Payload[1] = 'X'
		got.Headers["X-Trace"] = "changed"
		return nil
	}, msg)

	require.NoError(t, err)
	assert.Equal(t, `{"ok":true}`, string(msg.Payload))
	assert.Equal(t, "abc123", msg.Headers["X-Trace"])
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
	assert.Equal(t, defaultStreamMaxPayloadSize, c.maxPayloadSize)
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
		WithConsumerMaxPayloadSize(4096),
		WithDeadLetterMaxLen(5000),
	)
	require.NoError(t, err)

	assert.Equal(t, 10*time.Second, c.blockDuration)
	assert.Equal(t, 10*time.Minute, c.claimMinIdle)
	assert.Equal(t, time.Minute, c.claimInterval)
	assert.Equal(t, int64(50), c.batchSize)
	assert.Equal(t, int64(10), c.maxRetries)
	assert.Equal(t, 4096, c.maxPayloadSize)
	assert.Equal(t, int64(5000), c.deadLetterMaxLen)
}

func TestConsumerOptions_PanicOnInvalid(t *testing.T) {
	for name, fn := range map[string]func(){
		"WithBlockDuration zero": func() { WithBlockDuration(0) },
		"WithBlockDuration negative": func() {
			WithBlockDuration(-time.Second)
		},
		"WithClaimMinIdle zero":     func() { WithClaimMinIdle(0) },
		"WithClaimMinIdle negative": func() { WithClaimMinIdle(-time.Second) },
		"WithClaimInterval zero":    func() { WithClaimInterval(0) },
		"WithClaimInterval negative": func() {
			WithClaimInterval(-time.Second)
		},
		"WithBatchSize zero":      func() { WithBatchSize(0) },
		"WithBatchSize negative":  func() { WithBatchSize(-1) },
		"WithBatchSize too large": func() { WithBatchSize(MaxBatchMessages + 1) },
		"WithMaxRetries negative": func() { WithMaxRetries(-1) },
		"WithConsumerMaxPayloadSize negative": func() {
			WithConsumerMaxPayloadSize(-1)
		},
		"WithDeadLetterMaxLen negative": func() {
			WithDeadLetterMaxLen(-1)
		},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Panics(t, fn)
		})
	}
}

func TestWithConsumerName_PanicsOnInvalid(t *testing.T) {
	assert.Panics(t, func() {
		WithConsumerName("")(&Consumer{})
	})
}

func TestGroupMetricLabelDoesNotExposeGroupName(t *testing.T) {
	label := groupMetricLabel("tenant-secret:workers.high")
	assert.Regexp(t, `^group-[0-9a-f]{12}$`, label)
	assert.NotContains(t, label, "tenant")
	assert.NotContains(t, label, "secret")
	assert.NotContains(t, label, "workers")
	assert.NotContains(t, label, "high")
	assert.Equal(t, label, groupMetricLabel("tenant-secret:workers.high"))
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

func TestConsume_PanicsOnNilHandler(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	c, err := NewConsumer(client, "test-group")
	require.NoError(t, err)

	assert.PanicsWithValue(t,
		"redisstream: Consumer.Consume requires a non-nil handler",
		func() {
			c.Consume(context.TODO(), "stream-x", nil)
		},
	)
}

// TestRemoveConsumer_PreservesPendingEntries verifies that the shutdown
// cleanup does NOT call XGROUP DELCONSUMER when the consumer still has
// pending PEL entries. Deleting a consumer in that state would erase the
// pending entries and silently lose those messages, since the group's
// last-delivered-ID has already advanced past them.
func TestRemoveConsumer_PreservesPendingEntries(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	ctx := context.Background()
	stream := "test:remove:pending"
	group := "g"

	require.NoError(t, client.XGroupCreateMkStream(ctx, stream, group, "0").Err())

	_, err := client.XAdd(ctx, &goredis.XAddArgs{
		Stream: stream,
		Values: map[string]any{"id": "msg-1", "type": "t", "payload": "p"},
	}).Result()
	require.NoError(t, err)

	c, err := NewConsumer(client, group, WithConsumerName("worker-1"))
	require.NoError(t, err)

	// Read the message into the consumer's PEL but DO NOT ACK it.
	msgs, err := client.XReadGroup(ctx, &goredis.XReadGroupArgs{
		Group:    group,
		Consumer: c.consumer,
		Streams:  []string{stream, ">"},
		Count:    1,
		Block:    -1,
	}).Result()
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	require.Len(t, msgs[0].Messages, 1)

	// Sanity: the message is in the consumer's PEL.
	pendingBefore, err := client.XPendingExt(ctx, &goredis.XPendingExtArgs{
		Stream:   stream,
		Group:    group,
		Start:    "-",
		End:      "+",
		Count:    10,
		Consumer: c.consumer,
	}).Result()
	require.NoError(t, err)
	require.Len(t, pendingBefore, 1, "message must be pending before removeConsumer")

	c.removeConsumer(ctx, stream)

	// The pending entry must STILL be present so XAUTOCLAIM can recover it.
	pendingAfter, err := client.XPendingExt(ctx, &goredis.XPendingExtArgs{
		Stream:   stream,
		Group:    group,
		Start:    "-",
		End:      "+",
		Count:    10,
		Consumer: c.consumer,
	}).Result()
	require.NoError(t, err)
	require.Len(t, pendingAfter, 1, "removeConsumer must NOT erase pending entries — that loses messages")
	assert.Equal(t, pendingBefore[0].ID, pendingAfter[0].ID)
}

func TestHandleMessage_InvalidMessageDeadLettersBeforeHandler(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	ctx := context.Background()
	stream := "test:stream:invalid"
	group := "g"
	dlStream := stream + ":dead"
	require.NoError(t, client.XGroupCreateMkStream(ctx, stream, group, "0").Err())

	rawID, err := client.XAdd(ctx, &goredis.XAddArgs{
		Stream: stream,
		ID:     "1-0",
		Values: map[string]any{
			"id":      "msg-1",
			"type":    "bad type",
			"payload": `{}`,
		},
	}).Result()
	require.NoError(t, err)

	c, err := NewConsumer(client, group, WithConsumerName("worker-invalid"))
	require.NoError(t, err)

	called := false
	c.handleMessage(ctx, stream, dlStream, goredis.XMessage{
		ID: rawID,
		Values: map[string]any{
			"id":      "msg-1",
			"type":    "bad type",
			"payload": `{}`,
		},
	}, func(context.Context, Message) error {
		called = true
		return nil
	}, 1)

	assert.False(t, called, "invalid stream messages must not reach handlers")
	dlEntries, err := client.XRange(ctx, dlStream, "-", "+").Result()
	require.NoError(t, err)
	require.Len(t, dlEntries, 1)
	assert.Equal(t, "invalid_message", dlEntries[0].Values["dl_reason"])
	assert.Equal(t, stream, dlEntries[0].Values["dl_source_stream"])
	assert.Equal(t, rawID, dlEntries[0].Values["dl_source_id"])
}

type streamContextKey struct{}

func TestHandleMessage_DetachedContextsPreserveValuesAfterCancellation(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	ctx := context.Background()
	stream := "test:stream:detached-context"
	group := "g"
	consumer := "worker-context"
	dlStream := stream + ":dead"
	require.NoError(t, client.XGroupCreateMkStream(ctx, stream, group, "0").Err())
	_, err := client.XAdd(ctx, &goredis.XAddArgs{
		Stream: stream,
		Values: map[string]any{
			"id":      "msg-1",
			"type":    "test.event",
			"payload": `{"ok":true}`,
			"ts":      time.Now().UTC().Format(time.RFC3339Nano),
		},
	}).Result()
	require.NoError(t, err)
	delivered, err := client.XReadGroup(ctx, &goredis.XReadGroupArgs{
		Group:    group,
		Consumer: consumer,
		Streams:  []string{stream, ">"},
		Count:    1,
		Block:    -1,
	}).Result()
	require.NoError(t, err)
	require.Len(t, delivered, 1)
	require.Len(t, delivered[0].Messages, 1)

	c, err := NewConsumer(client, group, WithConsumerName(consumer))
	require.NoError(t, err)

	parent := context.WithValue(context.Background(), streamContextKey{}, "trace-123")
	cancelledCtx, cancel := context.WithCancel(parent)
	cancel()

	called := false
	c.handleMessage(cancelledCtx, stream, dlStream, delivered[0].Messages[0], func(handlerCtx context.Context, got Message) error {
		called = true
		assert.Equal(t, "trace-123", handlerCtx.Value(streamContextKey{}))
		assert.NoError(t, handlerCtx.Err())
		assert.Equal(t, "msg-1", got.ID)
		return nil
	}, 1)

	assert.True(t, called)
	pending, err := client.XPendingExt(ctx, &goredis.XPendingExtArgs{
		Stream:   stream,
		Group:    group,
		Start:    "-",
		End:      "+",
		Count:    1,
		Consumer: consumer,
	}).Result()
	require.NoError(t, err)
	assert.Empty(t, pending)
}

// TestRemoveConsumer_DeletesIdleConsumer verifies that removeConsumer
// still cleans up consumers that have no pending entries (the harmless
// happy path).
func TestRemoveConsumer_DeletesIdleConsumer(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	ctx := context.Background()
	stream := "test:remove:idle"
	group := "g"

	require.NoError(t, client.XGroupCreateMkStream(ctx, stream, group, "0").Err())

	c, err := NewConsumer(client, group, WithConsumerName("worker-idle"))
	require.NoError(t, err)

	// Force creation of the consumer record without any pending entries:
	// XReadGroup with ">" returns the consumer to Redis even when no
	// messages are available.
	_, _ = client.XReadGroup(ctx, &goredis.XReadGroupArgs{
		Group:    group,
		Consumer: c.consumer,
		Streams:  []string{stream, ">"},
		Count:    1,
		Block:    -1,
	}).Result()

	c.removeConsumer(ctx, stream)

	consumers, err := client.XInfoConsumers(ctx, stream, group).Result()
	require.NoError(t, err)
	for _, info := range consumers {
		assert.NotEqual(t, c.consumer, info.Name, "idle consumer must be removed")
	}
}

func TestStartConsumers_PanicsOnNilConsumer(t *testing.T) {
	assert.Panics(t, func() {
		_ = StartConsumers(context.TODO(), nil, nil, &sync.WaitGroup{}, slog.Default(), nil)
	})
}

func TestStartConsumers_PanicsOnNilWaitGroup(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	consumer, err := NewConsumer(client, "g")
	require.NoError(t, err)

	assert.Panics(t, func() {
		_ = StartConsumers(context.TODO(), consumer, nil, nil, slog.Default(), nil)
	})
}

func TestStartConsumers_NilLoggerNormalized(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	consumer, err := NewConsumer(client, "g")
	require.NoError(t, err)

	// Should not panic with nil logger; nil logger normalises to slog.Default().
	err = StartConsumers(context.TODO(), consumer, nil, &sync.WaitGroup{}, nil, nil)
	require.NoError(t, err)
}
