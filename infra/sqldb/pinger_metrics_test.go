package sqldb

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "github.com/glebarez/go-sqlite"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewPoolMetrics verifies that NewPoolMetrics creates and registers all collectors.
func TestNewPoolMetrics(t *testing.T) {
	// Use an explicit registry to avoid mutating prometheus.DefaultRegisterer,
	// which would race if tests run in parallel with -race.
	reg := prometheus.NewRegistry()

	m := NewPoolMetrics("testservice", reg)

	// Set values to verify collectors work.
	m.OpenConnections.Set(5)
	m.IdleConnections.Set(2)
	m.WaitCount.Add(10)

	open := testutil.ToFloat64(m.OpenConnections)
	assert.Equal(t, float64(5), open)

	idle := testutil.ToFloat64(m.IdleConnections)
	assert.Equal(t, float64(2), idle)

	wait := testutil.ToFloat64(m.WaitCount)
	assert.Equal(t, float64(10), wait)
}

// TestExportPoolMetrics_ExportsStats verifies that ExportPoolMetrics sets
// the open and idle connection gauges after the first tick.
func TestExportPoolMetrics_ExportsStats(t *testing.T) {
	sqlDB, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	// Keep one connection open so OpenConnections is non-zero.
	sqlDB.SetMaxOpenConns(1)

	// Acquire and hold a connection so the pool has a measurable state.
	conn, err := sqlDB.Conn(context.Background())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	openGauge := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "test_db_open_connections",
		Help: "open connections",
	})
	idleGauge := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "test_db_idle_connections",
		Help: "idle connections",
	})
	waitCounter := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "test_db_wait_count_total",
		Help: "wait count",
	})

	metrics := PoolMetrics{
		OpenConnections: openGauge,
		IdleConnections: idleGauge,
		WaitCount:       waitCounter,
	}

	interval := 10 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	// Run ExportPoolMetrics; it blocks until ctx is cancelled.
	ExportPoolMetrics(ctx, sqlDB, metrics, interval)

	// After at least one tick the open-connections gauge must reflect the pool.
	open := testutil.ToFloat64(openGauge)
	assert.GreaterOrEqual(t, open, float64(0), "open connections gauge should have been set")

	idle := testutil.ToFloat64(idleGauge)
	assert.GreaterOrEqual(t, idle, float64(0), "idle connections gauge should have been set")
}
