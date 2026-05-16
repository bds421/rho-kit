package redisqueue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/hibiken/asynq"
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
	t.Cleanup(func() { _ = q.Close() })
	msg, _ := NewMessage("test", "data")

	err := q.Enqueue(context.Background(), "", msg)
	assert.Error(t, err)
}

func TestQueue_RedisCommandErrorsDoNotReflectQueueName(t *testing.T) {
	client := newTestClient(t)
	q := NewQueue(client)
	t.Cleanup(func() { _ = q.Close() })
	ctx := context.Background()
	queueName := "test-queue-secret-token"
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
	check := q.DepthCheck("email-priority-high", 10)
	assert.Regexp(t, `^queue-depth-[0-9a-f]{12}$`, check.Name)
	assert.NotContains(t, check.Name, "email")
	assert.NotContains(t, check.Name, "priority")
	assert.NotContains(t, check.Name, "high")
}

func TestQueue_MetricLabelDoesNotExposeQueueName(t *testing.T) {
	label := queueMetricLabel("email-priority-high-tenant-secret")
	assert.Regexp(t, `^queue-[0-9a-f]{12}$`, label)
	assert.NotContains(t, label, "email")
	assert.NotContains(t, label, "priority")
	assert.NotContains(t, label, "high")
	assert.NotContains(t, label, "tenant")
	assert.NotContains(t, label, "secret")
	assert.Equal(t, label, queueMetricLabel("email-priority-high-tenant-secret"))
}

func TestNewQueue_PanicsOnNilOption(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	assert.Panics(t, func() {
		NewQueue(client, nil)
	})
}

func TestNewQueue_PanicsOnNilClient(t *testing.T) {
	assert.Panics(t, func() {
		NewQueue(nil)
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

			err := tc.q.Enqueue(ctx, "test-queue", msg)
			assert.ErrorIs(t, err, kitqueue.ErrInvalidQueue)

			err = tc.q.EnqueueBatch(ctx, "test-queue", []Message{msg})
			assert.ErrorIs(t, err, kitqueue.ErrInvalidQueue)

			n, err := tc.q.Len(ctx, "test-queue")
			assert.Equal(t, int64(0), n)
			assert.ErrorIs(t, err, kitqueue.ErrInvalidQueue)

			assert.Panics(t, func() {
				tc.q.Process(ctx, "test-queue", func(context.Context, Message) error { return nil })
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
	t.Cleanup(func() { _ = q.Close() })
	msg, err := NewMessage("test", "this is a payload that will be large enough when serialized")
	require.NoError(t, err)

	err = q.Enqueue(context.Background(), "test-queue", msg)
	assert.Error(t, err)
	assert.ErrorIs(t, err, kitqueue.ErrMessageTooLarge)
}

// TestQueue_Enqueue_AtPayloadLimitAcceptedDespiteEnvelopeBytes pins the
// fix for the send/receive envelope-limit asymmetry: a payload exactly
// at the configured cap must enqueue cleanly, because the handler side
// applies the same maxPayloadSize+queueEnvelopeOverhead headroom.
func TestQueue_Enqueue_AtPayloadLimitAcceptedDespiteEnvelopeBytes(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	const payloadCap = 64
	q := NewQueue(client, WithMaxMessageBytes(payloadCap))
	t.Cleanup(func() { _ = q.Close() })

	// Build a JSON-valid Payload (a JSON string literal whose body
	// length equals the configured payload cap exactly).
	inner := strings.Repeat("a", payloadCap-2) // 2 bytes for the enclosing quotes
	payload := json.RawMessage(`"` + inner + `"`)
	require.Len(t, payload, payloadCap)
	msg := Message{
		ID:        "msg-at-limit",
		Type:      "test.job",
		Payload:   payload,
		Timestamp: time.Now().UTC(),
		Attempt:   1,
	}
	// validateMessage caps msg.Payload at q.maxPayloadSize (inclusive
	// boundary); the marshaled envelope is many bytes larger but must
	// stay within maxPayloadSize+queueEnvelopeOverhead, which is the
	// boundary the kit treats as authoritative.
	require.NoError(t, q.Enqueue(context.Background(), "test-queue", msg))

	n, err := q.Len(context.Background(), "test-queue")
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)
}

func TestQueue_Enqueue_RejectsInvalidMessage(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	q := NewQueue(client)
	t.Cleanup(func() { _ = q.Close() })
	ctx := context.Background()
	queueName := "test-queue-invalid-message"

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
	t.Cleanup(func() { _ = q.Close() })
	msg := Message{
		ID:        "secret-token/bad",
		Type:      "test.job",
		Payload:   []byte(`"x"`),
		Timestamp: time.Now(),
		Attempt:   1,
	}

	err := q.Enqueue(context.Background(), "test-queue-invalid-id", msg)
	require.Error(t, err)
	assert.ErrorIs(t, err, kitqueue.ErrInvalidMessage)
	assert.NotContains(t, strings.ToLower(err.Error()), "secret-token")
}

func TestQueue_EnqueueBatch_RejectsInvalidMessage(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	q := NewQueue(client)
	t.Cleanup(func() { _ = q.Close() })
	ctx := context.Background()
	queueName := "test-queue-batch-invalid-message"

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
	t.Cleanup(func() { _ = q.Close() })
	err := q.EnqueueBatch(context.Background(), "test-queue", nil)
	require.NoError(t, err)
}

func TestQueue_EnqueueBatch_RejectsTooManyMessages(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	q := NewQueue(client)
	t.Cleanup(func() { _ = q.Close() })
	ctx := context.Background()
	queueName := "test-queue-batch-too-large"
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

func TestQueue_Len_UnknownQueueIsZero(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	q := NewQueue(client)
	t.Cleanup(func() { _ = q.Close() })

	n, err := q.Len(context.Background(), "never-enqueued")
	require.NoError(t, err)
	assert.Equal(t, int64(0), n)
}

func TestOptions(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	q := NewQueue(client,
		WithLogger(slog.Default()),
		WithMaxRetries(10),
		WithMaxMessageBytes(2<<20),
		WithConcurrency(4),
		WithInvisibilityTimeout(45*time.Second),
		WithShutdownTimeout(15*time.Second),
		WithRetention(time.Hour),
	)
	t.Cleanup(func() { _ = q.Close() })

	assert.Equal(t, 10, q.maxRetries)
	assert.Equal(t, 2<<20, q.maxPayloadSize)
	assert.Equal(t, 4, q.concurrency)
	assert.Equal(t, 45*time.Second, q.invisibilityTO)
	assert.Equal(t, 15*time.Second, q.shutdownTimeout)
	assert.Equal(t, time.Hour, q.retentionTTL)
}

func TestOptions_PanicOnInvalid(t *testing.T) {
	for name, fn := range map[string]func(){
		"WithMaxRetries negative":      func() { WithMaxRetries(-1) },
		"WithMaxMessageBytes negative": func() { WithMaxMessageBytes(-1) },
		"WithConcurrency zero":         func() { WithConcurrency(0) },
		"WithConcurrency negative":     func() { WithConcurrency(-1) },
		"WithInvisibilityTimeout zero": func() { WithInvisibilityTimeout(0) },
		"WithInvisibilityTimeout neg":  func() { WithInvisibilityTimeout(-time.Second) },
		"WithShutdownTimeout zero":     func() { WithShutdownTimeout(0) },
		"WithShutdownTimeout negative": func() { WithShutdownTimeout(-time.Second) },
		"WithRetention negative":       func() { WithRetention(-time.Second) },
		"WithMetricsRegisterer nil":    func() { WithMetricsRegisterer(nil) },
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

func TestProcess_PanicsOnEmptyQueue(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	q := NewQueue(client)
	t.Cleanup(func() { _ = q.Close() })
	assert.Panics(t, func() {
		q.Process(context.TODO(), "", nil) //nolint:staticcheck // intentionally testing panic with empty queue name
	})
}

func TestNewQueue_GeneratesUniqueConsumerID(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	q1 := NewQueue(client)
	t.Cleanup(func() { _ = q1.Close() })
	q2 := NewQueue(client)
	t.Cleanup(func() { _ = q2.Close() })
	assert.NotEmpty(t, q1.ConsumerID())
	assert.NotEmpty(t, q2.ConsumerID())
	assert.NotEqual(t, q1.ConsumerID(), q2.ConsumerID(),
		"each Queue must have a unique consumer ID for log disambiguation")
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
	t.Cleanup(func() { _ = q.Close() })
	assert.Equal(t, "worker-pod-7", q.ConsumerID())
}

func TestWithConsumerID_OverridesDefault(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	q := NewQueue(client, WithConsumerID("worker-pod-7"))
	t.Cleanup(func() { _ = q.Close() })
	assert.Equal(t, "worker-pod-7", q.ConsumerID())
}

func TestWithConsumerID_EmptyKeepsGenerated(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	q := NewQueue(client, WithConsumerID(""))
	t.Cleanup(func() { _ = q.Close() })
	assert.NotEmpty(t, q.ConsumerID(), "empty override must not clear the auto-generated ID")
}

func TestWithConsumerID_PanicDoesNotReflectInvalidID(t *testing.T) {
	assert.PanicsWithValue(t,
		"redisqueue: WithConsumerID requires a safe bounded token",
		func() { WithConsumerID("pod/secret-token") },
	)
}

func TestProcess_PanicsOnNilHandler(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	q := NewQueue(client)
	t.Cleanup(func() { _ = q.Close() })
	assert.PanicsWithValue(t,
		"redisqueue: Process requires a non-nil handler",
		func() {
			q.Process(context.TODO(), "test-queue", nil)
		},
	)
}

// TestProcess_DoubleProcessPanicsOnSameQueue uses an injected fake asynq
// server so the test is hermetic and does not require Redis.
func TestProcess_DoubleProcessPanicsOnSameQueue(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	q := NewQueue(client)
	t.Cleanup(func() { _ = q.Close() })

	released := make(chan struct{})
	q.serverFactory = func(_ asynq.Config) asynqServer {
		return &fakeAsynqServer{
			start:    func(asynq.Handler) error { return nil },
			shutdown: func() { close(released) },
		}
	}

	queueName := "test-queue-secret-token"
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
		<-released
	}()

	assert.PanicsWithValue(t,
		"redisqueue: Process queue already has an active Process goroutine",
		func() {
			q.Process(context.Background(), queueName, func(context.Context, Message) error { return nil })
		},
	)
}

// fakeAsynqServer satisfies the asynqServer interface for tests that
// must exercise [Queue.Process] without standing up a real asynq server.
type fakeAsynqServer struct {
	start    func(asynq.Handler) error
	shutdown func()
}

func (f *fakeAsynqServer) Start(h asynq.Handler) error {
	if f.start == nil {
		return nil
	}
	return f.start(h)
}

func (f *fakeAsynqServer) Shutdown() {
	if f.shutdown != nil {
		f.shutdown()
	}
}

// TestProcess_HandlerDispatchesEnvelope drives a single message through
// the asynq.HandlerFunc returned by [Queue.handlerForQueue] without
// running the real asynq server (the kit's handler wrapping is the
// integration seam, not asynq's worker pool).
func TestProcess_HandlerDispatchesEnvelope(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	q := NewQueue(client, WithLogger(slog.New(slog.NewTextHandler(testWriter{t}, nil))))
	t.Cleanup(func() { _ = q.Close() })

	msg, err := NewMessage("job.process", map[string]string{"task": "test"})
	require.NoError(t, err)
	data, err := jsonMarshal(msg)
	require.NoError(t, err)

	var got Message
	handler := q.handlerForQueue("test-queue", func(_ context.Context, m Message) error {
		got = m
		return nil
	})

	task := asynq.NewTask(envelopeTaskType, data)
	require.NoError(t, handler.ProcessTask(context.Background(), task))

	assert.Equal(t, msg.ID, got.ID)
	assert.Equal(t, msg.Type, got.Type)
}

// TestHandlerForQueue_RejectsOversizePayload mirrors the pre-v2 oversize
// guard. Asynq's SkipRetry sentinel is wrapped so the kit's archive
// metric increments on the next ErrorHandler tick (asynq behaviour) and
// the task never reaches a handler.
func TestHandlerForQueue_RejectsOversizePayload(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	q := NewQueue(client, WithMaxMessageBytes(16))
	t.Cleanup(func() { _ = q.Close() })

	called := false
	handler := q.handlerForQueue("test-queue", func(context.Context, Message) error {
		called = true
		return nil
	})

	// Build a payload larger than maxPayloadSize+queueEnvelopeOverhead so
	// the size guard fires before json.Unmarshal.
	oversize := make([]byte, q.maxPayloadSize+queueEnvelopeOverhead+1)
	for i := range oversize {
		oversize[i] = 'A'
	}
	err := handler.ProcessTask(context.Background(), asynq.NewTask(envelopeTaskType, oversize))
	require.Error(t, err)
	assert.True(t, errors.Is(err, asynq.SkipRetry))
	assert.False(t, called)
}

// TestHandlerForQueue_RejectsUndecodableEnvelope ensures malformed JSON
// is dropped without the handler being invoked.
func TestHandlerForQueue_RejectsUndecodableEnvelope(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	q := NewQueue(client)
	t.Cleanup(func() { _ = q.Close() })

	called := false
	handler := q.handlerForQueue("test-queue", func(context.Context, Message) error {
		called = true
		return nil
	})

	err := handler.ProcessTask(context.Background(), asynq.NewTask(envelopeTaskType, []byte("not-json")))
	require.Error(t, err)
	assert.True(t, errors.Is(err, asynq.SkipRetry))
	assert.False(t, called)
}

// TestHandlerForQueue_RejectsInvalidDecodedEnvelope ensures envelopes
// that decode but fail validateMessage are dropped.
func TestHandlerForQueue_RejectsInvalidDecodedEnvelope(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	q := NewQueue(client, WithMaxMessageBytes(16))
	t.Cleanup(func() { _ = q.Close() })

	called := false
	handler := q.handlerForQueue("test-queue", func(context.Context, Message) error {
		called = true
		return nil
	})

	data := `{"id":"bad id","type":"test.job","payload":{"too":"large to fit"},"timestamp":"2026-01-01T00:00:00Z","attempt":1}`
	err := handler.ProcessTask(context.Background(), asynq.NewTask(envelopeTaskType, []byte(data)))
	require.Error(t, err)
	assert.True(t, errors.Is(err, asynq.SkipRetry))
	assert.False(t, called, "invalid decoded messages must not reach handlers")
}

// TestStartProcessors_PanicsOnNilQueue is preserved from pre-v2.
func TestStartProcessors_PanicsOnNilQueue(t *testing.T) {
	assert.Panics(t, func() {
		_ = StartProcessors(context.TODO(), nil, nil, &sync.WaitGroup{}, slog.Default(), nil)
	})
}

func TestStartProcessors_PanicsOnNilWaitGroup(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	q := NewQueue(client)
	t.Cleanup(func() { _ = q.Close() })
	assert.Panics(t, func() {
		_ = StartProcessors(context.TODO(), q, nil, nil, slog.Default(), nil)
	})
}

func TestStartProcessors_NilLoggerNormalized(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	q := NewQueue(client)
	t.Cleanup(func() { _ = q.Close() })
	err := StartProcessors(context.TODO(), q, nil, &sync.WaitGroup{}, nil, nil)
	require.NoError(t, err)
}

// TestEnqueue_RejectsUnsafeID ensures Enqueue rejects IDs that contain
// JSON-escaping characters or whitespace. The asynq TaskID path accepts
// arbitrary bytes, but the kit applies its stricter [messageIDPattern]
// so log lines and metric labels are guaranteed safe.
func TestEnqueue_RejectsUnsafeID(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	q := NewQueue(client)
	t.Cleanup(func() { _ = q.Close() })
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
		err := q.Enqueue(ctx, "test-queue-reject", msg)
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
	t.Cleanup(func() { _ = q.Close() })
	ctx := context.Background()

	good, err := NewMessage("test", "data")
	require.NoError(t, err)
	bad := Message{ID: `id"quote"`, Type: "test", Payload: []byte(`"x"`), Timestamp: time.Now(), Attempt: 1}

	err = q.EnqueueBatch(ctx, "test-queue-batch-reject", []Message{good, bad})
	assert.Error(t, err)
	assert.ErrorIs(t, err, kitqueue.ErrInvalidMessage)

	n, err := q.Len(ctx, "test-queue-batch-reject")
	require.NoError(t, err)
	assert.Equal(t, int64(0), n, "no message must be enqueued when validation fails")
}
