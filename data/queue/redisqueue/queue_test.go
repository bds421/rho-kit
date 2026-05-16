package redisqueue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	kitqueue "github.com/bds421/rho-kit/data/v2/queue"
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

func TestNewMessage_RejectsInvalidType(t *testing.T) {
	for _, msgType := range []string{"", "bad type", "bad\ttype", "bad\ntype"} {
		t.Run(msgType, func(t *testing.T) {
			_, err := NewMessage(msgType, map[string]string{"key": "value"})
			assert.ErrorIs(t, err, kitqueue.ErrInvalidMessage)
		})
	}
}

func TestMessage_CloneDetachesPayload(t *testing.T) {
	msg := Message{
		ID:      "msg-1",
		Type:    "test.job",
		Payload: []byte(`{"ok":true}`),
		Attempt: 1,
	}

	clone := msg.Clone()
	clone.Payload[1] = 'X'

	assert.Equal(t, `{"ok":true}`, string(msg.Payload))
}

func TestQueue_Enqueue_EmptyName(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	q := NewQueue(client)
	msg, _ := NewMessage("test", "data")

	err := q.Enqueue(context.Background(), "", msg)
	assert.Error(t, err)
}

func TestQueue_RedisCommandErrorsDoNotReflectQueueName(t *testing.T) {
	client := newTestClient(t)
	q := NewQueue(client, WithBlockTimeout(10*time.Millisecond))
	ctx := context.Background()
	queueName := "test:queue:secret-token"
	msg, err := NewMessage("test.job", map[string]string{"ok": "true"})
	require.NoError(t, err)
	require.NoError(t, client.Close())

	err = q.Enqueue(ctx, queueName, msg)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "secret-token")

	_, err = q.Len(ctx, queueName)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "secret-token")
}

func TestQueue_DepthCheck_DoesNotExposeQueueName(t *testing.T) {
	var q *Queue
	check := q.DepthCheck("email:priority.high", 10)
	assert.Regexp(t, `^queue-depth-[0-9a-f]{12}$`, check.Name)
	assert.NotContains(t, check.Name, "email")
	assert.NotContains(t, check.Name, "priority")
	assert.NotContains(t, check.Name, "high")
}

func TestQueue_MetricLabelDoesNotExposeQueueName(t *testing.T) {
	label := queueMetricLabel("email:priority.high:tenant-secret")
	assert.Regexp(t, `^queue-[0-9a-f]{12}$`, label)
	assert.NotContains(t, label, "email")
	assert.NotContains(t, label, "priority")
	assert.NotContains(t, label, "high")
	assert.NotContains(t, label, "tenant")
	assert.NotContains(t, label, "secret")
	assert.Equal(t, label, queueMetricLabel("email:priority.high:tenant-secret"))
}

func TestNewQueue_PanicsOnNilOption(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	assert.Panics(t, func() {
		NewQueue(client, nil)
	})
}

func TestQueue_InvalidReceiverReturnsError(t *testing.T) {
	ctx := context.Background()
	msg := Message{ID: "msg-1", Type: "test", Payload: []byte(`{}`), Timestamp: time.Now()}
	cases := []struct {
		name string
		q    *Queue
	}{
		{"nil", nil},
		{"zero", &Queue{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Empty(t, tc.q.ConsumerID())

			err := tc.q.Enqueue(ctx, "test:queue", msg)
			assert.ErrorIs(t, err, kitqueue.ErrInvalidQueue)

			err = tc.q.EnqueueBatch(ctx, "test:queue", []Message{msg})
			assert.ErrorIs(t, err, kitqueue.ErrInvalidQueue)

			n, err := tc.q.Len(ctx, "test:queue")
			assert.Equal(t, int64(0), n)
			assert.ErrorIs(t, err, kitqueue.ErrInvalidQueue)

			assert.Panics(t, func() {
				tc.q.Process(ctx, "test:queue", func(context.Context, Message) error { return nil })
			})
		})
	}
}

func TestCallHandler_ConvertsPanicToError(t *testing.T) {
	err := callHandler(context.Background(), func(context.Context, Message) error {
		panic("handler exploded")
	}, Message{ID: "msg-1"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "handler panic")
	assert.Contains(t, err.Error(), "<redacted panic value: string>")
	assert.NotContains(t, err.Error(), "handler exploded")
}

func TestCallHandler_ClonesPayloadForHandler(t *testing.T) {
	msg := Message{ID: "msg-1", Payload: []byte(`{"ok":true}`)}

	err := callHandler(context.Background(), func(_ context.Context, got Message) error {
		got.Payload[1] = 'X'
		return nil
	}, msg)

	require.NoError(t, err)
	assert.Equal(t, `{"ok":true}`, string(msg.Payload))
}

func TestQueue_Enqueue_PayloadTooLarge(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	q := NewQueue(client, WithMaxMessageBytes(10))
	msg, err := NewMessage("test", "this is a payload that will be large enough when serialized")
	require.NoError(t, err)

	err = q.Enqueue(context.Background(), "test:queue", msg)
	assert.Error(t, err)
	assert.ErrorIs(t, err, kitqueue.ErrMessageTooLarge)
}

func TestQueue_Enqueue_RejectsInvalidMessage(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	q := NewQueue(client)
	ctx := context.Background()
	queueName := "test:queue:invalid-message"

	for _, msg := range []Message{
		{ID: "msg-1", Payload: []byte(`"x"`), Timestamp: time.Now(), Attempt: 1},
		{ID: "msg-2", Type: "bad\ntype", Payload: []byte(`"x"`), Timestamp: time.Now(), Attempt: 1},
	} {
		err := q.Enqueue(ctx, queueName, msg)
		assert.ErrorIs(t, err, kitqueue.ErrInvalidMessage)
	}

	n, err := q.Len(ctx, queueName)
	require.NoError(t, err)
	assert.Equal(t, int64(0), n)
}

func TestQueue_Enqueue_InvalidMessageIDDoesNotReflectValue(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	q := NewQueue(client)
	msg := Message{
		ID:        "secret-token/bad",
		Type:      "test.job",
		Payload:   []byte(`"x"`),
		Timestamp: time.Now(),
		Attempt:   1,
	}

	err := q.Enqueue(context.Background(), "test:queue:invalid-id", msg)
	require.Error(t, err)
	assert.ErrorIs(t, err, kitqueue.ErrInvalidMessage)
	assert.NotContains(t, strings.ToLower(err.Error()), "secret-token")
}

func TestQueue_EnqueueBatch_RejectsInvalidMessage(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	q := NewQueue(client)
	ctx := context.Background()
	queueName := "test:queue:batch:invalid-message"

	good, err := NewMessage("test", "data")
	require.NoError(t, err)
	bad := Message{ID: "msg-2", Type: "bad\rtype", Payload: []byte(`"x"`), Timestamp: time.Now(), Attempt: 1}

	err = q.EnqueueBatch(ctx, queueName, []Message{good, bad})
	assert.ErrorIs(t, err, kitqueue.ErrInvalidMessage)

	n, err := q.Len(ctx, queueName)
	require.NoError(t, err)
	assert.Equal(t, int64(0), n, "no message must be enqueued when validation fails")
}

func TestQueue_EnqueueBatch_Empty(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	q := NewQueue(client)
	err := q.EnqueueBatch(context.Background(), "test:queue", nil)
	require.NoError(t, err)
}

func TestQueue_EnqueueBatch_RejectsTooManyMessages(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	q := NewQueue(client)
	ctx := context.Background()
	queueName := "test:queue:batch:too-large"
	msgs := make([]Message, kitqueue.MaxBatchMessages+1)
	for i := range msgs {
		msgs[i] = Message{
			ID:        fmt.Sprintf("msg-%d", i),
			Type:      "test.job",
			Payload:   []byte(`"x"`),
			Timestamp: time.Now().UTC(),
			Attempt:   1,
		}
	}

	err := q.EnqueueBatch(ctx, queueName, msgs)
	assert.ErrorIs(t, err, kitqueue.ErrBatchTooLarge)

	n, err := q.Len(ctx, queueName)
	require.NoError(t, err)
	assert.Equal(t, int64(0), n)
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

func TestHandleMessage_RetryUsesOriginalPayloadWhenHandlerMutates(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	q := NewQueue(client)
	ctx := context.Background()
	queueName := "test:queue:retry-clone"
	processingQ := queueName + ":processing:self"
	deadQ := queueName + ":dead"
	msg := Message{
		ID:        "msg-1",
		Type:      "test.job",
		Payload:   []byte(`{"ok":true}`),
		Timestamp: time.Now().UTC(),
		Attempt:   1,
	}
	data, err := jsonMarshal(msg)
	require.NoError(t, err)
	require.NoError(t, client.LPush(ctx, processingQ, data).Err())

	q.handleMessage(ctx, string(data), processingQ, queueName, deadQ, func(_ context.Context, got Message) error {
		got.Payload[1] = 'X'
		return errors.New("retry")
	})

	retryData, err := client.RPop(ctx, queueName).Result()
	require.NoError(t, err)
	var retryMsg Message
	require.NoError(t, json.Unmarshal([]byte(retryData), &retryMsg))
	assert.Equal(t, `{"ok":true}`, string(retryMsg.Payload))
	assert.Equal(t, 2, retryMsg.Attempt)
}

type queueContextKey struct{}

func TestHandleMessage_DetachedContextsPreserveValuesAfterCancellation(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	q := NewQueue(client)
	queueName := "test:queue:detached-context"
	processingQ := queueName + ":processing:self"
	deadQ := queueName + ":dead"
	msg := Message{
		ID:        "msg-1",
		Type:      "test.job",
		Payload:   []byte(`{"ok":true}`),
		Timestamp: time.Now().UTC(),
		Attempt:   1,
	}
	data, err := jsonMarshal(msg)
	require.NoError(t, err)
	require.NoError(t, client.LPush(context.Background(), processingQ, data).Err())

	parent := context.WithValue(context.Background(), queueContextKey{}, "trace-123")
	ctx, cancel := context.WithCancel(parent)
	cancel()

	called := false
	q.handleMessage(ctx, string(data), processingQ, queueName, deadQ, func(handlerCtx context.Context, got Message) error {
		called = true
		assert.Equal(t, "trace-123", handlerCtx.Value(queueContextKey{}))
		assert.NoError(t, handlerCtx.Err())
		assert.Equal(t, msg.ID, got.ID)
		return nil
	})

	assert.True(t, called)
	processingLen, err := client.LLen(context.Background(), processingQ).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(0), processingLen)
}

func TestHandleMessage_InvalidDecodedMessageDiscardedBeforeHandler(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	q := NewQueue(client, WithMaxMessageBytes(16))
	ctx := context.Background()
	queueName := "test:queue:invalid-decoded"
	processingQ := queueName + ":processing:self"
	deadQ := queueName + ":dead"
	data := `{"id":"bad id","type":"test.job","payload":{"too":"large to fit"},"timestamp":"2026-01-01T00:00:00Z","attempt":1}`
	require.NoError(t, client.LPush(ctx, processingQ, data).Err())

	called := false
	q.handleMessage(ctx, data, processingQ, queueName, deadQ, func(context.Context, Message) error {
		called = true
		return nil
	})

	assert.False(t, called, "invalid decoded messages must not reach handlers")
	processingLen, err := client.LLen(ctx, processingQ).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(0), processingLen)
	queueLen, err := client.LLen(ctx, queueName).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(0), queueLen)
	deadLen, err := client.LLen(ctx, deadQ).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(0), deadLen)
}

func TestOptions(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	q := NewQueue(client,
		WithLogger(slog.Default()),
		WithBlockTimeout(10*time.Second),
		WithMaxRetries(10),
		WithDeadLetterMaxLen(5000),
		WithMaxMessageBytes(2<<20),
	)

	assert.Equal(t, 10*time.Second, q.blockTimeout)
	assert.Equal(t, 10, q.maxRetries)
	assert.Equal(t, int64(5000), q.deadLetterMax)
	assert.Equal(t, 2<<20, q.maxPayloadSize)
}

func TestOptions_PanicOnInvalid(t *testing.T) {
	for name, fn := range map[string]func(){
		"WithBlockTimeout zero":     func() { WithBlockTimeout(0) },
		"WithBlockTimeout negative": func() { WithBlockTimeout(-time.Second) },
		"WithMaxRetries negative":   func() { WithMaxRetries(-1) },
		"WithDeadLetterMaxLen negative": func() {
			WithDeadLetterMaxLen(-1)
		},
		"WithMaxMessageBytes negative": func() { WithMaxMessageBytes(-1) },
		"WithHeartbeatTTL zero":       func() { WithHeartbeatTTL(0) },
		"WithHeartbeatTTL negative":   func() { WithHeartbeatTTL(-time.Second) },
		"WithHeartbeatInterval zero":  func() { WithHeartbeatInterval(0) },
		"WithHeartbeatInterval negative": func() {
			WithHeartbeatInterval(-time.Second)
		},
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				r := recover()
				require.NotNil(t, r)
				msg, ok := r.(string)
				require.True(t, ok, "panic value must be a string, got %T", r)
				assert.NotContains(t, msg, "-1s")
				assert.NotContains(t, msg, "0s")
				assert.NotContains(t, msg, "got")
			}()
			fn()
		})
	}
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

func TestNewQueue_PanicsOnConsumerIDGenerationFailure(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	prev := newQueueConsumerID
	newQueueConsumerID = func() (uuid.UUID, error) {
		return uuid.Nil, errors.New("rng failed")
	}
	t.Cleanup(func() { newQueueConsumerID = prev })

	assert.PanicsWithValue(t,
		"redisqueue: NewQueue generate consumer ID: rng failed",
		func() { _ = NewQueue(client) })
}

func TestNewQueue_WithConsumerIDSkipsDefaultIDGeneration(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	prev := newQueueConsumerID
	newQueueConsumerID = func() (uuid.UUID, error) {
		return uuid.Nil, errors.New("should not be called")
	}
	t.Cleanup(func() { newQueueConsumerID = prev })

	q := NewQueue(client, WithConsumerID("worker-pod-7"))
	assert.Equal(t, "worker-pod-7", q.ConsumerID())
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

func TestWithConsumerID_PanicDoesNotReflectInvalidID(t *testing.T) {
	assert.PanicsWithValue(t,
		"redisqueue: WithConsumerID requires a safe bounded token",
		func() { WithConsumerID("pod/secret-token") },
	)
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

	removed, err := q.removeByID(ctx, processingQ, "id-B")
	require.NoError(t, err)
	assert.True(t, removed, "removeByID must report the entry as removed")

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
		"redisqueue: Process requires a non-nil handler",
		func() {
			q.Process(context.TODO(), "test:queue", nil)
		},
	)
}

func TestProcess_ActiveQueuePanicDoesNotReflectQueueName(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	q := NewQueue(client, WithBlockTimeout(10*time.Millisecond))
	queueName := "test:queue:secret-token"
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		q.Process(ctx, queueName, func(context.Context, Message) error { return nil })
	}()
	require.Eventually(t, func() bool {
		q.activeQueuesMu.Lock()
		defer q.activeQueuesMu.Unlock()
		return q.activeQueues[queueName]
	}, time.Second, 10*time.Millisecond)
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("first Process did not stop")
		}
	}()

	assert.PanicsWithValue(t,
		"redisqueue: Process queue already has an active Process goroutine",
		func() {
			q.Process(context.Background(), queueName, func(context.Context, Message) error { return nil })
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

	// Empty list — must not error and must report not-removed.
	removed, err := q.removeByID(ctx, processingQ, "missing-id")
	require.NoError(t, err)
	assert.False(t, removed, "empty list: removeByID must report not-removed")

	// List with no matching ID — must not error, must not remove anything,
	// and must report not-removed.
	require.NoError(t, client.LPush(ctx, processingQ, `{"id":"keep","type":"x","payload":"x","timestamp":"2026-01-01T00:00:00Z","attempt":1}`).Err())
	removed, err = q.removeByID(ctx, processingQ, "absent")
	require.NoError(t, err)
	assert.False(t, removed, "no matching ID: removeByID must report not-removed")
	remaining, err := client.LRange(ctx, processingQ, 0, -1).Result()
	require.NoError(t, err)
	require.Len(t, remaining, 1)
}

// TestRemoveByID_PayloadFieldDoesNotCollide ensures that an `id` field in
// the payload (or any nested structure) does NOT cause the wrong entry to
// be removed. Caller-supplied IDs are validated against messageIDPattern so
// embedded "id":"..." substrings cannot match a different message's
// top-level ID — the Lua script decodes JSON and compares the top-level
// `id` exactly. The previous substring-match design was vulnerable to this
// collision: it would remove any list entry whose serialized JSON contained
// the literal needle, including a payload field by the same name.
func TestRemoveByID_PayloadFieldDoesNotCollide(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	q := NewQueue(client)
	ctx := context.Background()
	processingQ := "test:processing:collision"

	// Entry whose payload contains a nested "id" field equal to "otherID".
	// Removing message "otherID" must NOT remove this entry — its top-level
	// id is "msgA", not "otherID".
	collidingEntry := `{"id":"msgA","type":"x","payload":{"id":"otherID"},"timestamp":"2026-01-01T00:00:00Z","attempt":1}`
	otherEntry := `{"id":"otherID","type":"x","payload":"plain","timestamp":"2026-01-01T00:00:00Z","attempt":1}`
	require.NoError(t, client.LPush(ctx, processingQ, collidingEntry).Err())
	require.NoError(t, client.LPush(ctx, processingQ, otherEntry).Err())

	removed, err := q.removeByID(ctx, processingQ, "otherID")
	require.NoError(t, err)
	assert.True(t, removed, "removeByID must remove the entry whose top-level id matches")

	remaining, err := client.LRange(ctx, processingQ, 0, -1).Result()
	require.NoError(t, err)
	require.Len(t, remaining, 1)
	assert.Equal(t, collidingEntry, remaining[0],
		"removeByID must NOT remove an entry whose nested payload field happens to share the requested id")
}

// TestEnqueue_RejectsUnsafeID ensures Enqueue rejects IDs that contain
// JSON-escaping characters or whitespace. With the substring-needle design
// these IDs would never match in ack and the entry would silently leak.
func TestEnqueue_RejectsUnsafeID(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	q := NewQueue(client)
	ctx := context.Background()

	cases := []string{
		"",
		`id"with"quotes`,
		`id\with\backs`,
		"id with spaces",
		"id\nwith\nnewline",
		"id:with:colon",
	}
	for _, badID := range cases {
		msg := Message{ID: badID, Type: "test", Payload: []byte(`"x"`), Timestamp: time.Now(), Attempt: 1}
		err := q.Enqueue(ctx, "test:queue:reject", msg)
		assert.Error(t, err, "Enqueue must reject unsafe ID %q", badID)
		assert.ErrorIs(t, err, kitqueue.ErrInvalidMessage)
	}
}

// TestEnqueueBatch_RejectsUnsafeID ensures the batch path validates every
// ID up front; a single bad ID rejects the whole batch.
func TestEnqueueBatch_RejectsUnsafeID(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	q := NewQueue(client)
	ctx := context.Background()

	good, err := NewMessage("test", "data")
	require.NoError(t, err)
	bad := Message{ID: `id"quote"`, Type: "test", Payload: []byte(`"x"`), Timestamp: time.Now(), Attempt: 1}

	err = q.EnqueueBatch(ctx, "test:queue:batch:reject", []Message{good, bad})
	assert.Error(t, err)
	assert.ErrorIs(t, err, kitqueue.ErrInvalidMessage)

	n, err := q.Len(ctx, "test:queue:batch:reject")
	require.NoError(t, err)
	assert.Equal(t, int64(0), n, "no message must be enqueued when validation fails")
}

// TestNewQueue_PanicsOnBadHeartbeatRatio enforces interval <= TTL/2.
// Without this guard, a configuration like TTL=5s + interval=30s lets the
// heartbeat key expire between refreshes and triggers false-dead reclaim.
func TestNewQueue_PanicsOnBadHeartbeatRatio(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	defer func() {
		r := recover()
		require.NotNil(t, r, "NewQueue must panic when heartbeat interval > TTL/2")
		msg, ok := r.(string)
		require.True(t, ok, "panic value must be a string, got %T", r)
		assert.Contains(t, msg, "heartbeat interval", "panic must reference heartbeat configuration")
		assert.Contains(t, msg, "TTL/2", "panic must explain the ratio policy")
		assert.NotContains(t, msg, "5s")
		assert.NotContains(t, msg, "30s")
	}()
	_ = NewQueue(client,
		WithHeartbeatTTL(5*time.Second),
		WithHeartbeatInterval(30*time.Second),
	)
}

func TestNewQueue_PanicsOnHeartbeatRatioOverflow(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	const maxDuration = time.Duration(1<<63 - 1)
	assert.Panics(t, func() {
		_ = NewQueue(client,
			WithHeartbeatTTL(maxDuration),
			WithHeartbeatInterval(maxDuration),
		)
	})
}

// TestNewQueue_PanicsWhenIntervalEqualsTTL — the 1:1 ratio is also unsafe
// because clock skew and Redis stalls can stretch refresh latency past TTL.
func TestNewQueue_PanicsWhenIntervalEqualsTTL(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	defer func() {
		require.NotNil(t, recover(), "NewQueue must panic when interval == TTL")
	}()
	_ = NewQueue(client,
		WithHeartbeatTTL(10*time.Second),
		WithHeartbeatInterval(10*time.Second),
	)
}

// TestNewQueue_AcceptsHalfTTLRatio — exactly TTL/2 is the boundary case
// and must be accepted (one missed refresh leaves a full interval of
// safety margin before the key expires).
func TestNewQueue_AcceptsHalfTTLRatio(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	q := NewQueue(client,
		WithHeartbeatTTL(10*time.Second),
		WithHeartbeatInterval(5*time.Second),
	)
	require.NotNil(t, q)
}

// TestProcess_AckRoundTrip_QuotedID verifies that a message with quote
// characters in its ID makes it through enqueue, processing, and ack
// (the previous substring-needle design would have failed because
// JSON-encoded "id":"id\"with\"quotes" never contained the raw needle).
//
// Today, validateMessageID rejects quote characters at Enqueue time, so we
// inject the entry directly into the processing list to exercise the Lua
// path with an awkward (legacy or hand-crafted) ID — the cjson.decode
// implementation must still match it exactly.
func TestProcess_AckRoundTrip_QuotedID(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	q := NewQueue(client)
	ctx := context.Background()
	processingQ := "test:processing:quoted"

	weirdID := `id"with"quotes`
	entry, err := jsonMarshal(Message{ID: weirdID, Type: "x", Payload: []byte(`"y"`), Timestamp: time.Now(), Attempt: 1})
	require.NoError(t, err)
	require.NoError(t, client.LPush(ctx, processingQ, entry).Err())

	removed, err := q.removeByID(ctx, processingQ, weirdID)
	require.NoError(t, err)
	assert.True(t, removed, "Lua decode path must match a quoted ID exactly")

	n, err := client.LLen(ctx, processingQ).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(0), n)
}

// TestProcess_AckRoundTrip_BackslashID — same as the quoted-ID case for
// IDs with backslash characters.
func TestProcess_AckRoundTrip_BackslashID(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	q := NewQueue(client)
	ctx := context.Background()
	processingQ := "test:processing:backslash"

	weirdID := `id\with\backs`
	entry, err := jsonMarshal(Message{ID: weirdID, Type: "x", Payload: []byte(`"y"`), Timestamp: time.Now(), Attempt: 1})
	require.NoError(t, err)
	require.NoError(t, client.LPush(ctx, processingQ, entry).Err())

	removed, err := q.removeByID(ctx, processingQ, weirdID)
	require.NoError(t, err)
	assert.True(t, removed, "Lua decode path must match a backslash ID exactly")
}
