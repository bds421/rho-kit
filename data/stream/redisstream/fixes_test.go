package redisstream

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDeadLetter_DoesNotCountWhenAckFails verifies that the dead-lettered
// counter is NOT incremented when the pipelined XADD succeeds but the XACK
// leg fails. In that state the message stays in the source PEL and will be
// dead-lettered again on a later delivery (a second DLQ write); counting the
// first attempt would diverge the counter from unique dead-lettered messages.
func TestDeadLetter_DoesNotCountWhenAckFails(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	ctx := context.Background()
	stream := "test:dl:ackfail"
	dlStream := stream + ".dead"
	group := "g"

	// Make XACK against the source stream fail with WRONGTYPE by storing a
	// non-stream value at the source key. XADD targets a different key
	// (dlStream) and still succeeds, reproducing the XADD-ok/XACK-fail path.
	require.NoError(t, client.Set(ctx, stream, "not-a-stream", 0).Err())

	reg := prometheus.NewRegistry()
	c, err := NewConsumer(client, group, WithConsumerRegisterer(reg))
	require.NoError(t, err)

	c.deadLetter(ctx, stream, dlStream, goredis.XMessage{
		ID:     "1-0",
		Values: map[string]any{"id": "msg-1", "type": "t", "payload": "p"},
	}, "permanent_error")

	// The DLQ write succeeded...
	dlEntries, err := client.XRange(ctx, dlStream, "-", "+").Result()
	require.NoError(t, err)
	require.Len(t, dlEntries, 1, "XADD to dead-letter stream must have succeeded")

	// ...but because XACK failed, the counter must NOT have advanced.
	got := testutil.ToFloat64(c.metrics.messagesDeadLettered.WithLabelValues(
		streamMetricLabel(stream), groupMetricLabel(group)))
	assert.Equal(t, float64(0), got, "counter must not advance when XACK fails")
}

// TestDeadLetter_CountsOnSuccess is the positive counterpart: when both XADD
// and XACK succeed, the dead-lettered counter advances by exactly one.
func TestDeadLetter_CountsOnSuccess(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	ctx := context.Background()
	stream := "test:dl:ok"
	dlStream := stream + ".dead"
	group := "g"

	require.NoError(t, client.XGroupCreateMkStream(ctx, stream, group, "0").Err())
	rawID, err := client.XAdd(ctx, &goredis.XAddArgs{
		Stream: stream,
		Values: map[string]any{"id": "msg-1", "type": "t", "payload": "p"},
	}).Result()
	require.NoError(t, err)

	reg := prometheus.NewRegistry()
	c, err := NewConsumer(client, group, WithConsumerName("w"), WithConsumerRegisterer(reg))
	require.NoError(t, err)

	// Deliver into the consumer's PEL so XACK has something to acknowledge.
	_, err = client.XReadGroup(ctx, &goredis.XReadGroupArgs{
		Group:    group,
		Consumer: c.consumer,
		Streams:  []string{stream, ">"},
		Count:    1,
		Block:    -1,
	}).Result()
	require.NoError(t, err)

	c.deadLetter(ctx, stream, dlStream, goredis.XMessage{
		ID:     rawID,
		Values: map[string]any{"id": "msg-1", "type": "t", "payload": "p"},
	}, "permanent_error")

	got := testutil.ToFloat64(c.metrics.messagesDeadLettered.WithLabelValues(
		streamMetricLabel(stream), groupMetricLabel(group)))
	assert.Equal(t, float64(1), got, "counter must advance once on full success")

	// Source PEL must now be empty (XACK removed the entry).
	pending, err := client.XPendingExt(ctx, &goredis.XPendingExtArgs{
		Stream: stream, Group: group, Start: "-", End: "+", Count: 10,
	}).Result()
	require.NoError(t, err)
	assert.Empty(t, pending, "XACK must have cleared the source PEL entry")
}

// TestNewConsumer_CustomRegistererRegistersOnCustomRegistry verifies that a
// caller supplying WithConsumerRegisterer gets the collectors registered on
// THAT registry (the deferred-default behaviour), not silently dropped.
func TestNewConsumer_CustomRegistererRegistersOnCustomRegistry(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	reg := prometheus.NewRegistry()
	c, err := NewConsumer(client, "g", WithConsumerRegisterer(reg))
	require.NoError(t, err)
	require.NotNil(t, c.metrics)

	// Touch one collector and confirm it is gathered from the custom registry.
	c.metrics.messagesConsumed.WithLabelValues(
		streamMetricLabel("s"), groupMetricLabel("g")).Inc()

	families, err := reg.Gather()
	require.NoError(t, err)
	var found bool
	for _, mf := range families {
		if mf.GetName() == "redis_stream_messages_consumed_total" {
			found = true
		}
	}
	assert.True(t, found, "custom registerer must own the consumer collectors")
}

// TestNewProducer_CustomRegistererRegistersOnCustomRegistry is the producer
// counterpart of the deferred-default-metrics behaviour.
func TestNewProducer_CustomRegistererRegistersOnCustomRegistry(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	reg := prometheus.NewRegistry()
	p := NewProducer(client, WithProducerRegisterer(reg))
	require.NotNil(t, p.metrics)

	p.metrics.messagesProduced.WithLabelValues(streamMetricLabel("s")).Inc()

	families, err := reg.Gather()
	require.NoError(t, err)
	var found bool
	for _, mf := range families {
		if mf.GetName() == "redis_stream_messages_produced_total" {
			found = true
		}
	}
	assert.True(t, found, "custom registerer must own the producer collector")
}

// TestPublishBatch_CountsEverySuccess verifies the produced counter reflects
// every committed XADD on the happy path (the new all-results loop). The
// partial-pipeline-failure scenario the loop also guards against is not
// reproducible with miniredis (its pipeline either fully succeeds or fully
// fails), so it is exercised by the loop's success accounting here.
func TestPublishBatch_CountsEverySuccess(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	reg := prometheus.NewRegistry()
	p := NewProducer(client, WithProducerRegisterer(reg))

	ctx := context.Background()
	stream := "test:batch:count"
	msgs := make([]Message, 4)
	for i := range msgs {
		m, err := NewMessage("t", map[string]int{"i": i})
		require.NoError(t, err)
		msgs[i] = m
	}

	ids, err := p.PublishBatch(ctx, stream, msgs)
	require.NoError(t, err)
	require.Len(t, ids, 4)
	for _, id := range ids {
		assert.NotEmpty(t, id)
	}

	got := testutil.ToFloat64(p.metrics.messagesProduced.WithLabelValues(streamMetricLabel(stream)))
	assert.Equal(t, float64(4), got, "every committed XADD must be counted exactly once")
}
