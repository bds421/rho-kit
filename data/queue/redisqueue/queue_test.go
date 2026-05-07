package redisqueue

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }

// testWriter routes slog output to t.Log so failure traces include logs
// and successful runs stay quiet under -v.
type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(string(p))
	return len(p), nil
}

func newTestClient(t *testing.T) goredis.UniversalClient {
	t.Helper()
	mr := miniredis.RunT(t)
	return goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
}

func newTestClientWithMR(t *testing.T) (goredis.UniversalClient, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	return goredis.NewClient(&goredis.Options{Addr: mr.Addr()}), mr
}

func TestNewMessage(t *testing.T) {
	msg, err := NewMessage("test.job", map[string]string{"key": "value"})
	require.NoError(t, err)

	assert.NotEmpty(t, msg.ID)
	assert.Equal(t, "test.job", msg.Type)
	assert.NotNil(t, msg.Payload)
	assert.False(t, msg.Timestamp.IsZero())
	assert.Equal(t, 1, msg.Attempt)
}

func TestQueue_Enqueue_EmptyName(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	q := NewQueue(client)
	msg, _ := NewMessage("test", "data")

	err := q.Enqueue(context.Background(), "", msg)
	assert.Error(t, err)
}

func TestQueue_Enqueue_PayloadTooLarge(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	q := NewQueue(client, WithMaxPayloadSize(10))
	msg, err := NewMessage("test", "this is a payload that will be large enough when serialized")
	require.NoError(t, err)

	err = q.Enqueue(context.Background(), "test:queue", msg)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds max payload size")
}

func TestQueue_EnqueueBatch_Empty(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	q := NewQueue(client)
	err := q.EnqueueBatch(context.Background(), "test:queue", nil)
	require.NoError(t, err)
}

func TestQueue_Len(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	q := NewQueue(client)
	ctx := context.Background()

	msg, err := NewMessage("test", "data")
	require.NoError(t, err)
	require.NoError(t, q.Enqueue(ctx, "test:len", msg))

	n, err := q.Len(ctx, "test:len")
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)
}

func TestOptions(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	q := NewQueue(client,
		WithLogger(slog.Default()),
		WithBlockTimeout(10*time.Second),
		WithMaxRetries(10),
		WithDeadLetterMaxLen(5000),
		WithMaxPayloadSize(2<<20),
	)

	assert.Equal(t, 10*time.Second, q.blockTimeout)
	assert.Equal(t, 10, q.maxRetries)
	assert.Equal(t, int64(5000), q.deadLetterMax)
	assert.Equal(t, 2<<20, q.maxPayloadSize)
}

func TestOptions_IgnoresInvalid(t *testing.T) {
	q := &Queue{
		blockTimeout:   5 * time.Second,
		maxRetries:     5,
		deadLetterMax:  defaultDeadLetterMaxLen,
		maxPayloadSize: defaultMaxPayloadSize,
	}

	WithBlockTimeout(-1)(q)
	WithMaxRetries(-1)(q)
	WithDeadLetterMaxLen(-1)(q)
	WithMaxPayloadSize(-1)(q)

	assert.Equal(t, 5*time.Second, q.blockTimeout)
	assert.Equal(t, 5, q.maxRetries)
	assert.Equal(t, defaultDeadLetterMaxLen, q.deadLetterMax)
	assert.Equal(t, defaultMaxPayloadSize, q.maxPayloadSize)
}

func TestWithProcessingQueue_PanicsOnInvalid(t *testing.T) {
	assert.Panics(t, func() {
		WithProcessingQueue("")(&Queue{})
	})
}

func TestWithDeadLetterQueue_PanicsOnInvalid(t *testing.T) {
	assert.Panics(t, func() {
		WithDeadLetterQueue("")(&Queue{})
	})
}

func TestProcess_PanicsOnEmptyQueue(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	q := NewQueue(client)
	assert.Panics(t, func() {
		q.Process(context.TODO(), "", nil) //nolint:staticcheck // intentionally testing panic with empty queue name
	})
}

func TestNewQueue_GeneratesUniqueConsumerID(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	q1 := NewQueue(client)
	q2 := NewQueue(client)
	assert.NotEmpty(t, q1.ConsumerID())
	assert.NotEmpty(t, q2.ConsumerID())
	assert.NotEqual(t, q1.ConsumerID(), q2.ConsumerID(),
		"each Queue must have a unique consumer ID so per-consumer processing lists don't collide")
}

func TestWithConsumerID_OverridesDefault(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	q := NewQueue(client, WithConsumerID("worker-pod-7"))
	assert.Equal(t, "worker-pod-7", q.ConsumerID())
}

func TestWithConsumerID_EmptyKeepsGenerated(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	q := NewQueue(client, WithConsumerID(""))
	assert.NotEmpty(t, q.ConsumerID(), "empty override must not clear the auto-generated ID")
}

func TestRemoveByID_ScopedToMessageID(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	q := NewQueue(client)
	ctx := context.Background()
	processingQ := "test:processing"

	// Push two messages with identical PAYLOAD bytes but different IDs.
	// Old LRem-by-data would remove whichever copy Redis's scan finds first;
	// removeByID must remove only the one with the requested ID.
	msgA := `{"id":"id-A","type":"x","payload":"same","timestamp":"2026-01-01T00:00:00Z","attempt":1}`
	msgB := `{"id":"id-B","type":"x","payload":"same","timestamp":"2026-01-01T00:00:00Z","attempt":1}`
	require.NoError(t, client.LPush(ctx, processingQ, msgA).Err())
	require.NoError(t, client.LPush(ctx, processingQ, msgB).Err())

	require.NoError(t, q.removeByID(ctx, processingQ, "id-B"))

	remaining, err := client.LRange(ctx, processingQ, 0, -1).Result()
	require.NoError(t, err)
	require.Len(t, remaining, 1)
	assert.Contains(t, remaining[0], `"id":"id-A"`,
		"removeByID must keep the message whose ID was NOT specified")
}

func TestProcess_PanicsOnNilHandler(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	q := NewQueue(client)
	assert.PanicsWithValue(t,
		"redisqueue: Queue.Process requires a non-nil handler",
		func() {
			q.Process(context.TODO(), "test:queue", nil)
		},
	)
}

// TestReapDeadConsumers_ReclaimsStrandedEntries simulates an "old consumer"
// by writing entries directly into a `queue:processing:<old-id>` list with
// NO heartbeat key. A fresh queue with default config starts and the
// reaper must move those entries to the main queue tail so the new
// consumer picks them up.
func TestReapDeadConsumers_ReclaimsStrandedEntries(t *testing.T) {
	client, _ := newTestClientWithMR(t)
	t.Cleanup(func() { _ = client.Close() })

	ctx := context.Background()
	queueName := "test:queue:reclaim"
	deadProcessing := queueName + ":processing:dead-consumer"

	// Stranded entries left by the "old consumer".
	msgA, err := NewMessage("job", map[string]string{"k": "a"})
	require.NoError(t, err)
	msgB, err := NewMessage("job", map[string]string{"k": "b"})
	require.NoError(t, err)
	dataA, err := jsonMarshal(msgA)
	require.NoError(t, err)
	dataB, err := jsonMarshal(msgB)
	require.NoError(t, err)
	require.NoError(t, client.LPush(ctx, deadProcessing, dataA).Err())
	require.NoError(t, client.LPush(ctx, deadProcessing, dataB).Err())

	// Fresh queue with stable consumer ID and tight heartbeat windows
	// so the test does not need to wait for a 60s TTL.
	q := NewQueue(client,
		WithConsumerID("live-consumer"),
		WithLogger(slog.New(slog.NewTextHandler(testWriter{t}, nil))),
	)

	// Run the reaper directly so we don't need a Process loop.
	processingPrefix := queueName + ":processing:"
	heartbeatPrefix := queueName + ":heartbeat:"
	ownProcessing := processingPrefix + q.consumerID

	q.reapDeadConsumers(ctx, queueName, heartbeatPrefix, processingPrefix, ownProcessing, queueName+":dead", nil)

	// Both entries must be back on the main queue.
	mainLen, err := client.LLen(ctx, queueName).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(2), mainLen, "stranded entries must be reclaimed to the main queue")

	// Dead processing list must be drained.
	deadLen, err := client.LLen(ctx, deadProcessing).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(0), deadLen)
}

// TestReapDeadConsumers_SkipsLiveConsumers ensures a peer with a live
// heartbeat is NOT reclaimed.
func TestReapDeadConsumers_SkipsLiveConsumers(t *testing.T) {
	client, _ := newTestClientWithMR(t)
	t.Cleanup(func() { _ = client.Close() })

	ctx := context.Background()
	queueName := "test:queue:peer"
	peerProcessing := queueName + ":processing:peer-consumer"
	peerHeartbeat := queueName + ":heartbeat:peer-consumer"

	msg, err := NewMessage("job", "data")
	require.NoError(t, err)
	data, err := jsonMarshal(msg)
	require.NoError(t, err)
	require.NoError(t, client.LPush(ctx, peerProcessing, data).Err())
	// Peer is alive — heartbeat key present.
	require.NoError(t, client.Set(ctx, peerHeartbeat, "1", time.Minute).Err())

	q := NewQueue(client, WithConsumerID("self"))
	processingPrefix := queueName + ":processing:"
	heartbeatPrefix := queueName + ":heartbeat:"
	ownProcessing := processingPrefix + q.consumerID

	q.reapDeadConsumers(ctx, queueName, heartbeatPrefix, processingPrefix, ownProcessing, queueName+":dead", nil)

	// Peer's processing list must remain untouched.
	peerLen, err := client.LLen(ctx, peerProcessing).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(1), peerLen, "live peer's processing list must NOT be reclaimed")

	mainLen, err := client.LLen(ctx, queueName).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(0), mainLen)
}

// TestProcess_RecoversFromDeadConsumerOnStartup is an end-to-end test:
// stranded entries in a dead consumer's processing list are dispatched to
// a fresh consumer's handler shortly after Process starts.
func TestProcess_RecoversFromDeadConsumerOnStartup(t *testing.T) {
	client, _ := newTestClientWithMR(t)
	t.Cleanup(func() { _ = client.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	queueName := "test:queue:e2e:recover"
	deadProcessing := queueName + ":processing:old-pod"

	msg, err := NewMessage("job", map[string]string{"data": "stranded"})
	require.NoError(t, err)
	data, err := jsonMarshal(msg)
	require.NoError(t, err)
	require.NoError(t, client.LPush(ctx, deadProcessing, data).Err())

	q := NewQueue(client,
		WithConsumerID("new-pod"),
		WithBlockTimeout(time.Second),
		WithHeartbeatTTL(2*time.Second),
		WithHeartbeatInterval(500*time.Millisecond),
		WithLogger(slog.New(slog.NewTextHandler(testWriter{t}, nil))),
	)
	q.reapInitialDelay = 50 * time.Millisecond

	var received atomic.Int32
	processCtx, processCancel := context.WithCancel(ctx)
	done := make(chan struct{})

	go func() {
		defer close(done)
		q.Process(processCtx, queueName, func(_ context.Context, m Message) error {
			if m.ID == msg.ID {
				received.Add(1)
				processCancel()
			}
			return nil
		})
	}()

	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("Process never received the stranded message")
	}

	assert.GreaterOrEqual(t, received.Load(), int32(1), "the stranded message must reach the new consumer's handler")
}

func TestStartProcessors_PanicsOnNilQueue(t *testing.T) {
	assert.Panics(t, func() {
		_ = StartProcessors(context.TODO(), nil, nil, &sync.WaitGroup{}, slog.Default(), nil)
	})
}

func TestStartProcessors_PanicsOnNilWaitGroup(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	q := NewQueue(client)
	assert.Panics(t, func() {
		_ = StartProcessors(context.TODO(), q, nil, nil, slog.Default(), nil)
	})
}

func TestStartProcessors_NilLoggerNormalized(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	q := NewQueue(client)
	err := StartProcessors(context.TODO(), q, nil, &sync.WaitGroup{}, nil, nil)
	require.NoError(t, err)
}

func TestRemoveByID_NoMatchIsBenign(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	q := NewQueue(client)
	ctx := context.Background()
	processingQ := "test:processing"

	// Empty list — must not error.
	require.NoError(t, q.removeByID(ctx, processingQ, "missing-id"))

	// List with no matching ID — must not error and must not remove anything.
	require.NoError(t, client.LPush(ctx, processingQ, `{"id":"keep","type":"x","payload":"x","timestamp":"2026-01-01T00:00:00Z","attempt":1}`).Err())
	require.NoError(t, q.removeByID(ctx, processingQ, "absent"))
	remaining, err := client.LRange(ctx, processingQ, 0, -1).Result()
	require.NoError(t, err)
	require.Len(t, remaining, 1)
}
