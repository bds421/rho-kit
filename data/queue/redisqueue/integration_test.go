//go:build integration

package redisqueue

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/redis"
	"github.com/bds421/rho-kit/infra/redis/redistest"
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

func TestQueue_EnqueueAndProcess(t *testing.T) {
	client := redisClient(t)

	q := NewQueue(client, WithLogger(slog.Default()))
	ctx := context.Background()
	queueName := fmt.Sprintf("test:queue:%d", time.Now().UnixNano())

	msg, err := NewMessage("job.process", map[string]string{"task": "test"})
	require.NoError(t, err)

	err = q.Enqueue(ctx, queueName, msg)
	require.NoError(t, err)

	length, err := q.Len(ctx, queueName)
	require.NoError(t, err)
	assert.Equal(t, int64(1), length)

	var received Message
	var wg sync.WaitGroup
	wg.Add(1)

	processCtx, cancel := context.WithCancel(ctx)

	go q.Process(processCtx, queueName, func(_ context.Context, m Message) error {
		received = m
		wg.Done()
		cancel()
		return nil
	})

	wg.Wait()

	assert.Equal(t, msg.ID, received.ID)
	assert.Equal(t, "job.process", received.Type)
}

func TestQueue_EnqueueBatch(t *testing.T) {
	client := redisClient(t)

	q := NewQueue(client, WithLogger(slog.Default()))
	ctx := context.Background()
	queueName := fmt.Sprintf("test:queue:batch:%d", time.Now().UnixNano())

	var msgs []Message
	for i := range 3 {
		m, err := NewMessage("batch.job", map[string]int{"i": i})
		require.NoError(t, err)
		msgs = append(msgs, m)
	}

	err := q.EnqueueBatch(ctx, queueName, msgs)
	require.NoError(t, err)

	length, err := q.Len(ctx, queueName)
	require.NoError(t, err)
	assert.Equal(t, int64(3), length)
}

func TestQueue_DeadLetter(t *testing.T) {
	client := redisClient(t)

	q := NewQueue(client,
		WithLogger(slog.Default()),
		WithMaxRetries(2),
		WithBlockTimeout(500*time.Millisecond),
	)
	ctx := context.Background()
	queueName := fmt.Sprintf("test:queue:dl:%d", time.Now().UnixNano())
	deadQ := queueName + ":dead"

	msg, err := NewMessage("failing.job", "data")
	require.NoError(t, err)

	err = q.Enqueue(ctx, queueName, msg)
	require.NoError(t, err)

	var attempts atomic.Int32
	done := make(chan struct{})
	var doneOnce sync.Once

	processCtx, cancel := context.WithCancel(ctx)

	go q.Process(processCtx, queueName, func(_ context.Context, m Message) error {
		attempts.Add(1)
		if m.Attempt >= 2 {
			doneOnce.Do(func() { close(done) })
			return errors.New("still failing")
		}
		return errors.New("transient")
	})

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out")
	}

	cancel()

	// Poll until the dead-letter queue contains the message.
	require.Eventually(t, func() bool {
		dlLen, err := client.LLen(ctx, deadQ).Result()
		return err == nil && dlLen == 1
	}, 5*time.Second, 50*time.Millisecond)
}
