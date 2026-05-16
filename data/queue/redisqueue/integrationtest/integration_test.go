//go:build integration

package integrationtest

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hibiken/asynq"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/core/v2/apperror"
	"github.com/bds421/rho-kit/data/queue/redisqueue/v2"
	"github.com/bds421/rho-kit/infra/redis/redistest/v2"
	"github.com/bds421/rho-kit/infra/redis/v2"
)

func redisClient(t *testing.T) goredis.UniversalClient {
	t.Helper()
	url := redistest.Start(t)
	opts, err := goredis.ParseURL(url)
	require.NoError(t, err)
	conn, err := redis.Connect(opts)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return conn.Client()
}

// TestQueue_EnqueueAndProcess walks one message end-to-end through the
// asynq-backed queue: NewMessage -> Enqueue -> Process -> handler. Pins
// the kit's envelope round-trip against a real Redis container.
func TestQueue_EnqueueAndProcess(t *testing.T) {
	client := redisClient(t)

	q := redisqueue.NewQueue(client, redisqueue.WithLogger(slog.Default()))
	t.Cleanup(func() { _ = q.Close() })
	ctx := context.Background()
	queueName := fmt.Sprintf("test:queue:%d", time.Now().UnixNano())

	msg, err := redisqueue.NewMessage("job.process", map[string]string{"task": "test"})
	require.NoError(t, err)

	err = q.Enqueue(ctx, queueName, msg)
	require.NoError(t, err)

	length, err := q.Len(ctx, queueName)
	require.NoError(t, err)
	assert.Equal(t, int64(1), length)

	receivedCh := make(chan redisqueue.Message, 1)
	processCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		q.Process(processCtx, queueName, func(_ context.Context, m redisqueue.Message) error {
			select {
			case receivedCh <- m:
			default:
			}
			cancel()
			return nil
		})
	}()

	select {
	case received := <-receivedCh:
		assert.Equal(t, msg.ID, received.ID)
		assert.Equal(t, "job.process", received.Type)
	case <-processCtx.Done():
		t.Fatal("Process never received the enqueued message before timeout")
	}
	<-done
}

// TestQueue_EnqueueBatch verifies the kit's batch enqueue path against
// asynq. Asynq has no batch primitive — the kit issues one Enqueue per
// message and the assertion below pins all three are pending in the
// inspector.
func TestQueue_EnqueueBatch(t *testing.T) {
	client := redisClient(t)

	q := redisqueue.NewQueue(client, redisqueue.WithLogger(slog.Default()))
	t.Cleanup(func() { _ = q.Close() })
	ctx := context.Background()
	queueName := fmt.Sprintf("test:queue:batch:%d", time.Now().UnixNano())

	var msgs []redisqueue.Message
	for i := range 3 {
		m, err := redisqueue.NewMessage("batch.job", map[string]int{"i": i})
		require.NoError(t, err)
		msgs = append(msgs, m)
	}

	err := q.EnqueueBatch(ctx, queueName, msgs)
	require.NoError(t, err)

	length, err := q.Len(ctx, queueName)
	require.NoError(t, err)
	assert.Equal(t, int64(3), length)
}

// TestQueue_DeadLetter exercises the kit's SkipRetry path for permanent
// errors via [apperror.NewPermanent]: a permanent handler error must move
// the task straight into asynq's archive (dead-letter set) without paying
// the exponential-backoff retry delays. The assertion is against asynq's
// Inspector — the operator-visible source of truth for archived tasks.
func TestQueue_DeadLetter(t *testing.T) {
	client := redisClient(t)

	q := redisqueue.NewQueue(client,
		redisqueue.WithLogger(slog.Default()),
		redisqueue.WithMaxRetries(2),
	)
	t.Cleanup(func() { _ = q.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	queueName := fmt.Sprintf("test:queue:dl:%d", time.Now().UnixNano())

	msg, err := redisqueue.NewMessage("failing.job", "data")
	require.NoError(t, err)

	err = q.Enqueue(ctx, queueName, msg)
	require.NoError(t, err)

	var attempts atomic.Int32
	processCtx, processCancel := context.WithCancel(ctx)

	done := make(chan struct{})
	go func() {
		defer close(done)
		q.Process(processCtx, queueName, func(_ context.Context, _ redisqueue.Message) error {
			attempts.Add(1)
			// Use apperror.NewPermanent so the kit translates this into
			// asynq.SkipRetry — the archive path that bypasses retry
			// backoff. Without this the test would wait ~10s for asynq's
			// first retry interval.
			return apperror.NewPermanent("poison-pill")
		})
	}()

	// Poll asynq's Inspector for the archive count. One-shot dispatch +
	// SkipRetry should land the task in Archived within a few seconds.
	inspector := asynq.NewInspectorFromRedisClient(client)
	t.Cleanup(func() { _ = inspector.Close() })
	require.Eventually(t, func() bool {
		info, infoErr := inspector.GetQueueInfo(queueName)
		if infoErr != nil {
			return false
		}
		return info.Archived >= 1
	}, 15*time.Second, 200*time.Millisecond, "task must reach the archive within 15s")

	processCancel()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("Process did not stop after cancel")
	}

	assert.GreaterOrEqual(t, attempts.Load(), int32(1), "handler must have been invoked at least once")
}

// TestQueue_DepthCheck exercises the kit's health.DependencyCheck against
// asynq's Inspector. A queue with zero pending entries reports Healthy;
// a queue above the threshold reports Degraded.
func TestQueue_DepthCheck(t *testing.T) {
	client := redisClient(t)

	q := redisqueue.NewQueue(client, redisqueue.WithLogger(slog.Default()))
	t.Cleanup(func() { _ = q.Close() })
	queueName := fmt.Sprintf("test:queue:depth:%d", time.Now().UnixNano())

	ctx := context.Background()
	// Threshold of 0 + zero pending entries: healthy because Len reports
	// 0 for an unknown queue and the check is `n > threshold`.
	check := q.DepthCheck(queueName, 5)
	assert.Equal(t, "healthy", check.Check(ctx))

	// Enqueue past the threshold so the check reports degraded.
	var msgs []redisqueue.Message
	for i := 0; i < 10; i++ {
		m, err := redisqueue.NewMessage("noop", map[string]int{"i": i})
		require.NoError(t, err)
		msgs = append(msgs, m)
	}
	require.NoError(t, q.EnqueueBatch(ctx, queueName, msgs))

	require.Eventually(t, func() bool {
		return check.Check(ctx) == "degraded"
	}, 5*time.Second, 100*time.Millisecond, "depth check must report degraded above threshold")
}

// TestQueue_DoubleProcessGuard pins the per-Queue Process panic so a
// service that wires two goroutines onto the same queue learns at startup
// rather than at the first message delivery.
func TestQueue_DoubleProcessGuard(t *testing.T) {
	client := redisClient(t)

	q := redisqueue.NewQueue(client, redisqueue.WithLogger(slog.Default()))
	t.Cleanup(func() { _ = q.Close() })
	queueName := fmt.Sprintf("test:queue:double:%d", time.Now().UnixNano())

	processCtx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		q.Process(processCtx, queueName, func(context.Context, redisqueue.Message) error { return nil })
	}()

	// Poll until Process has registered the active queue: a second
	// Process on the same name must panic. assert.Panics returns false
	// when the call does NOT panic, so we retry until the first Process
	// has registered itself.
	require.Eventually(t, func() bool {
		didPanic := assert.Panics(new(testing.T), func() {
			q.Process(context.Background(), queueName, func(context.Context, redisqueue.Message) error { return nil })
		})
		return didPanic
	}, 5*time.Second, 50*time.Millisecond, "second Process must panic once the first has registered")

	cancel()
	wg.Wait()
}
