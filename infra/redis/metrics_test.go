package redis

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/prometheus/client_golang/prometheus"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSafeCommandName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"GET", "get"},
		{"get", "get"},
		{"SET", "set"},
		{"XADD", "xadd"},
		{"BLMOVE", "blmove"},
		{"pipeline", "pipeline"}, // internal label for pipeline hook
		{"CUSTOM_CMD", "other"},
		{"unknown", "other"},
		{"", "other"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.expected, safeCommandName(tt.input))
		})
	}
}

func TestMetricsHook_ProcessHook(t *testing.T) {
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	reg := prometheus.NewRegistry()
	m := NewRedisMetrics(reg)
	client.AddHook(&metricsHook{metrics: m})

	ctx := context.Background()
	err := client.Set(ctx, "test-key", "value", time.Minute).Err()
	require.NoError(t, err)

	val, err := client.Get(ctx, "test-key").Result()
	require.NoError(t, err)
	assert.Equal(t, "value", val)
}

func TestMetricsHook_Pipeline(t *testing.T) {
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	reg := prometheus.NewRegistry()
	m := NewRedisMetrics(reg)
	client.AddHook(&metricsHook{metrics: m})

	ctx := context.Background()
	pipe := client.Pipeline()
	pipe.Set(ctx, "key1", "val1", time.Minute)
	pipe.Set(ctx, "key2", "val2", time.Minute)
	_, err := pipe.Exec(ctx)
	require.NoError(t, err)

	val, err := client.Get(ctx, "key1").Result()
	require.NoError(t, err)
	assert.Equal(t, "val1", val)
}

func TestCollectPoolMetrics_NoError(t *testing.T) {
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	// Should not panic.
	CollectPoolMetrics(client, "test")
}

func TestCollectPoolMetrics_NilClient(t *testing.T) {
	// Nil client should be a no-op and not panic.
	CollectPoolMetrics(nil, "test")
}

func TestCollectPoolMetrics_NonClientType(t *testing.T) {
	// ClusterClient implements UniversalClient but is not *redis.Client.
	// Should be a no-op, not panic.
	cluster := goredis.NewClusterClient(&goredis.ClusterOptions{})
	t.Cleanup(func() { _ = cluster.Close() })
	CollectPoolMetrics(cluster, "test")
}

func TestStartPoolMetricsCollector_StopsOnCancel(t *testing.T) {
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		StartPoolMetricsCollector(ctx, client, "test", 50*time.Millisecond)
		close(done)
	}()

	// Let it run a few ticks.
	time.Sleep(200 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("collector did not stop after context cancel")
	}
}
