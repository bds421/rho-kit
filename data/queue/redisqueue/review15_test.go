package redisqueue

import (
	"context"
	"errors"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	kitqueue "github.com/bds421/rho-kit/data/v2/queue"
)

// TestNewQueue_CustomRegistererDoesNotTouchDefaultRegisterer pins
// review-15: NewQueue must not materialise defaultMetrics() before the
// option loop, so WithMetricsRegisterer(custom) never registers on
// prometheus.DefaultRegisterer as a side effect.
func TestNewQueue_CustomRegistererDoesNotTouchDefaultRegisterer(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	// Pre-register an incompatible same-name collector on the default
	// registry. If NewQueue still eagerly called defaultMetrics(),
	// MustRegisterOrGet / registration would panic or collide.
	poison := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "redis_queue_messages_enqueued_total",
		Help: "poison collector occupying the default-registry name",
	})
	// Best-effort: if something already registered the real collector in
	// this process, skip the poison step and only assert custom registry.
	_ = prometheus.DefaultRegisterer.Register(poison)

	reg := prometheus.NewRegistry()
	q := NewQueue(client, WithMetricsRegisterer(reg))
	require.NotNil(t, q.metrics)

	// Custom registry must hold the kit collectors after a probe increment.
	q.metrics.messagesEnqueued.WithLabelValues("probe").Inc()
	n, err := testutil.GatherAndCount(reg, "redis_queue_messages_enqueued_total")
	require.NoError(t, err)
	assert.Equal(t, 1, n)
}

// TestEnqueue_DuplicateIDReturnsKitSentinel pins review-15: duplicate
// Message.ID surfaces as kitqueue.ErrDuplicateMessage so callers need not
// import hibiken/asynq.
func TestEnqueue_DuplicateIDReturnsKitSentinel(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	q := NewQueue(client)
	ctx := context.Background()
	msg := Message{ID: "dup-1", Type: "job", Payload: []byte(`{}`)}

	require.NoError(t, q.Enqueue(ctx, "jobs", msg))
	err := q.Enqueue(ctx, "jobs", msg)
	require.Error(t, err)
	assert.True(t, errors.Is(err, kitqueue.ErrDuplicateMessage), "got %v", err)
}
