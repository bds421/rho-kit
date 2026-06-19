package sqldb

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testDriverName = "rho-kit-sqldb-test"

func init() {
	sql.Register(testDriverName, testDriver{})
}

type testDriver struct{}

func (testDriver) Open(string) (driver.Conn, error) {
	return testConn{}, nil
}

type testConn struct{}

func (testConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("test driver: prepare not implemented")
}

func (testConn) Close() error {
	return nil
}

func (testConn) Begin() (driver.Tx, error) {
	return nil, errors.New("test driver: begin not implemented")
}

// TestNewPoolMetrics verifies that NewPoolMetrics creates and registers all collectors.
func TestNewPoolMetrics(t *testing.T) {
	// Use an explicit registry to avoid mutating prometheus.DefaultRegisterer,
	// which would race if tests run in parallel with -race.
	reg := prometheus.NewRegistry()

	m := NewPoolMetrics("testservice", WithRegisterer(reg))

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

func TestNewPoolMetrics_PanicsOnInvalidNamespace(t *testing.T) {
	assert.Panics(t, func() {
		NewPoolMetrics("my-service", WithRegisterer(prometheus.NewRegistry()))
	})
	assert.Panics(t, func() {
		NewPoolMetrics("tenant metrics", WithRegisterer(prometheus.NewRegistry()))
	})
}

// TestExportPoolMetrics_ExportsStats verifies that ExportPoolMetrics copies
// the live *sql.DB stats onto the gauges on each tick. With MaxOpenConns(1)
// and exactly one connection held open, the pool has one open and zero idle
// connections; the export loop must reflect those precise values rather than
// merely leaving the gauges at their non-negative default.
func TestExportPoolMetrics_ExportsStats(t *testing.T) {
	sqlDB, err := sql.Open(testDriverName, "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	// Cap the pool at one connection so the held connection accounts for the
	// entire pool: open == 1, idle == 0 while it is checked out.
	sqlDB.SetMaxOpenConns(1)

	// Acquire and hold a connection so the pool has a deterministic state.
	conn, err := sqlDB.Conn(context.Background())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	// Sanity-check the driver-level stats before exporting, so a failed gauge
	// assertion below points at the export loop and not at the pool setup.
	require.Equal(t, 1, sqlDB.Stats().OpenConnections)
	require.Equal(t, 0, sqlDB.Stats().Idle)

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

	// Run the export loop in the background; it blocks until ctx is cancelled.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		ExportPoolMetrics(ctx, sqlDB, metrics, 5*time.Millisecond)
	}()

	// A tick must set the open gauge to the held-connection count (exactly 1).
	// This fails (rather than passing vacuously) if the tick/select loop never
	// records the pool stats.
	require.Eventually(t, func() bool {
		return testutil.ToFloat64(openGauge) == float64(1)
	}, 2*time.Second, 5*time.Millisecond, "open connections gauge should reflect the single held connection")

	// Idle must be exactly 0 because the only connection is checked out.
	assert.Equal(t, float64(0), testutil.ToFloat64(idleGauge), "idle connections gauge should be zero while the only connection is held")

	cancel()
	<-done
}
