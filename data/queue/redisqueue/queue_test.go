package redisqueue

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
