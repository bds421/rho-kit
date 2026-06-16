package redis

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// histogramSampleCount returns the number of observations recorded by a single
// histogram series (one label combination). testutil.ToFloat64 only works for
// counters/gauges, so histogram assertions read SampleCount directly.
func histogramSampleCount(t *testing.T, obs prometheus.Observer) uint64 {
	t.Helper()
	metric, ok := obs.(prometheus.Metric)
	require.True(t, ok, "observer must implement prometheus.Metric")
	var m dto.Metric
	require.NoError(t, metric.Write(&m))
	require.NotNil(t, m.Histogram, "metric must be a histogram")
	return m.Histogram.GetSampleCount()
}

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
	m := NewMetrics(WithRegisterer(reg))
	client.AddHook(&metricsHook{instance: "cache", metrics: m})

	ctx := context.Background()
	require.NoError(t, client.Set(ctx, "test-key", "value", time.Minute).Err())

	val, err := client.Get(ctx, "test-key").Result()
	require.NoError(t, err)
	assert.Equal(t, "value", val)

	// The hook must observe a duration sample per command under the
	// instance/command labels — not silently no-op.
	assert.Equal(t, uint64(1),
		histogramSampleCount(t, m.commandDuration.WithLabelValues("cache", "set")),
		"set command must record one duration sample")
	assert.Equal(t, uint64(1),
		histogramSampleCount(t, m.commandDuration.WithLabelValues("cache", "get")),
		"get command must record one duration sample")

	// A successful Get is not an error: command_errors must stay zero.
	assert.Equal(t, float64(0),
		testutil.ToFloat64(m.commandErrors.WithLabelValues("cache", "get")))
}

// TestMetricsHook_RedisNilNotCountedAsError pins the error-counting branch:
// a missing-key Get surfaces redis.Nil, which is an absence-of-value signal,
// not a command failure, and must be excluded from command_errors.
func TestMetricsHook_RedisNilNotCountedAsError(t *testing.T) {
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	reg := prometheus.NewRegistry()
	m := NewMetrics(WithRegisterer(reg))
	client.AddHook(&metricsHook{instance: "cache", metrics: m})

	ctx := context.Background()
	_, err := client.Get(ctx, "missing-key").Result()
	require.ErrorIs(t, err, goredis.Nil)

	// Duration is still observed for the command...
	assert.Equal(t, uint64(1),
		histogramSampleCount(t, m.commandDuration.WithLabelValues("cache", "get")))
	// ...but redis.Nil must NOT advance the error counter.
	assert.Equal(t, float64(0),
		testutil.ToFloat64(m.commandErrors.WithLabelValues("cache", "get")),
		"redis.Nil must not be counted as a command error")
}

func TestMetricsHook_Pipeline(t *testing.T) {
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	reg := prometheus.NewRegistry()
	m := NewMetrics(WithRegisterer(reg))
	client.AddHook(&metricsHook{instance: "cache", metrics: m})

	ctx := context.Background()
	pipe := client.Pipeline()
	pipe.Set(ctx, "key1", "val1", time.Minute)
	pipe.Set(ctx, "key2", "val2", time.Minute)
	_, err := pipe.Exec(ctx)
	require.NoError(t, err)

	val, err := client.Get(ctx, "key1").Result()
	require.NoError(t, err)
	assert.Equal(t, "val1", val)

	// The pipeline hook records each batch as a "pipeline" duration sample
	// (individual command durations are unavailable in pipeline mode). go-redis
	// may also run its connection-init handshake as a pipeline, so assert the
	// batch produced at least one sample under the pipeline label.
	assert.GreaterOrEqual(t,
		histogramSampleCount(t, m.commandDuration.WithLabelValues("cache", "pipeline")), uint64(1),
		"pipeline batch must record a duration sample under the pipeline label")
	// All commands succeeded: no error samples.
	assert.Equal(t, float64(0),
		testutil.ToFloat64(m.commandErrors.WithLabelValues("cache", "set")))
}

func TestCollectPoolMetrics_NoError(t *testing.T) {
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	// Should not panic.
	CollectPoolMetrics(client, "test")
}

// TestCollectPoolMetrics_SnapshotsPoolStats asserts the collector actually
// writes pool stats into the gauges under the instance label, rather than
// merely not panicking. A successful round-trip drives at least one pool
// hit/miss so total_conns reflects a live connection.
func TestCollectPoolMetrics_SnapshotsPoolStats(t *testing.T) {
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	// Force the pool to open a connection so PoolStats has non-zero data.
	require.NoError(t, client.Ping(context.Background()).Err())

	reg := prometheus.NewRegistry()
	m := NewMetrics(WithRegisterer(reg))
	collectPoolMetrics(m, client, "cache")

	assert.GreaterOrEqual(t,
		testutil.ToFloat64(m.connectionPoolSize.WithLabelValues("cache")), float64(1),
		"total_conns gauge must reflect the live pool connection")
	// Timeouts should be zero on a healthy local pool; the gauge must still be
	// explicitly set (a present series), proving the snapshot ran.
	assert.Equal(t, float64(0),
		testutil.ToFloat64(m.connectionPoolTimeouts.WithLabelValues("cache")))
	assert.Equal(t, 6, testutil.CollectAndCount(m.connectionPoolSize)+
		testutil.CollectAndCount(m.connectionPoolHits)+
		testutil.CollectAndCount(m.connectionPoolMisses)+
		testutil.CollectAndCount(m.connectionPoolTimeouts)+
		testutil.CollectAndCount(m.connectionPoolIdle)+
		testutil.CollectAndCount(m.connectionPoolStale),
		"all six pool gauges must have one series set for the instance")
}

func TestCollectPoolMetrics_NilClient(t *testing.T) {
	// Nil client should be a no-op and not panic.
	CollectPoolMetrics(nil, "test")
}

func TestCollectPoolMetrics_PanicsOnInvalidInstanceWithoutReflectingName(t *testing.T) {
	assert.PanicsWithValue(t, "redis: StartPoolMetricsCollector invalid instance name", func() {
		CollectPoolMetrics(nil, "secret-token\nbad")
	})
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

func TestStartPoolMetricsCollector_PanicsOnNilOption(t *testing.T) {
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	assert.Panics(t, func() {
		StartPoolMetricsCollector(context.Background(), client, "test", time.Second, nil)
	})
}

func TestStartPoolMetricsCollector_PanicsOnInvalidInstanceWithoutReflectingName(t *testing.T) {
	assert.PanicsWithValue(t, "redis: StartPoolMetricsCollector invalid instance name", func() {
		StartPoolMetricsCollector(context.Background(), nil, "secret-token\nbad", time.Second)
	})
}
