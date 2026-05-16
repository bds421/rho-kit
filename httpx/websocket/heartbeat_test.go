package websocket

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeHeartbeatConn satisfies heartbeatConn for unit-testing
// runHeartbeat without a real WebSocket peer.
type fakeHeartbeatConn struct {
	pings     atomic.Int64
	pingErr   error
	closes    atomic.Int64
	closeCode atomic.Int64
}

func (f *fakeHeartbeatConn) Ping(ctx context.Context) error {
	f.pings.Add(1)
	if f.pingErr != nil {
		return f.pingErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func (f *fakeHeartbeatConn) Close(code StatusCode, _ string) error {
	f.closes.Add(1)
	f.closeCode.Store(int64(code))
	return nil
}

func TestRunHeartbeat_NoopOnNonPositiveInterval(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := NewMetrics(WithRegisterer(reg))
	conn := &fakeHeartbeatConn{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Should return immediately when interval is zero rather than
	// spinning a ticker that the caller cannot turn off.
	done := make(chan struct{})
	go func() {
		runHeartbeat(ctx, conn, 0, 0, slog.Default(), metrics)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("heartbeat with interval=0 should return immediately")
	}

	assert.EqualValues(t, 0, conn.pings.Load(), "no ping should be sent")
	assert.EqualValues(t, 0, conn.closes.Load(), "no close should be issued")
}

func TestRunHeartbeat_PingsAtInterval(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := NewMetrics(WithRegisterer(reg))
	conn := &fakeHeartbeatConn{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		runHeartbeat(ctx, conn, 20*time.Millisecond, 100*time.Millisecond, slog.Default(), metrics)
		close(done)
	}()

	// Give the heartbeat enough ticks to fire at least three times.
	time.Sleep(120 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("heartbeat did not return after ctx cancel")
	}

	pings := conn.pings.Load()
	assert.GreaterOrEqual(t, pings, int64(3),
		"expected at least 3 ticks in 120ms with a 20ms interval, got %d", pings)
	assert.EqualValues(t, 0, conn.closes.Load(),
		"heartbeat must not close the conn on successful pings")
}

// TestRunHeartbeat_ClosesOnPingError verifies the production-critical
// path: when Ping returns an error the heartbeat must close the
// connection with StatusPolicyViolation, bump the timeout metric, and
// return.
func TestRunHeartbeat_ClosesOnPingError(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := NewMetrics(WithRegisterer(reg))
	conn := &fakeHeartbeatConn{pingErr: errors.New("simulated ping failure")}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		runHeartbeat(ctx, conn, 10*time.Millisecond, 50*time.Millisecond, slog.Default(), metrics)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("heartbeat did not exit after first ping failure")
	}

	assert.EqualValues(t, 1, conn.pings.Load(), "exactly one ping should have been attempted")
	assert.EqualValues(t, 1, conn.closes.Load(), "the failing ping must trigger a close")
	assert.EqualValues(t, int64(StatusPolicyViolation), conn.closeCode.Load(),
		"close code must be StatusPolicyViolation so operators can distinguish heartbeat-driven closes")

	// Verify the timeout metric was emitted.
	families, err := reg.Gather()
	require.NoError(t, err)

	var timeoutCount float64
	for _, f := range families {
		if f.GetName() != "httpx_websocket_pings_total" {
			continue
		}
		for _, m := range f.GetMetric() {
			for _, lp := range m.GetLabel() {
				if lp.GetName() == "result" && lp.GetValue() == "timeout" {
					timeoutCount = m.GetCounter().GetValue()
				}
			}
		}
	}
	assert.Equal(t, float64(1), timeoutCount,
		"the timeout-bucket counter must record the heartbeat-driven close")
}

// TestRunHeartbeat_TolerantOfNilLogger guards against the silent-NPE
// failure mode where a caller forgets to pass a logger. The heartbeat
// is a background goroutine so a nil-deref here would crash the
// process rather than just the connection.
func TestRunHeartbeat_TolerantOfNilLogger(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := NewMetrics(WithRegisterer(reg))
	conn := &fakeHeartbeatConn{pingErr: io.ErrClosedPipe}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		runHeartbeat(ctx, conn, 10*time.Millisecond, 50*time.Millisecond, nil, metrics)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("heartbeat did not exit on ping failure with nil logger")
	}
}

func TestWithPingInterval_PanicsOnNegative(t *testing.T) {
	assert.Panics(t, func() { WithPingInterval(-1 * time.Millisecond) })
	// Zero is allowed (disables heartbeat).
	assert.NotPanics(t, func() { WithPingInterval(0) })
}

func TestWithPongTimeout_PanicsOnNonPositive(t *testing.T) {
	assert.Panics(t, func() { WithPongTimeout(0) })
	assert.Panics(t, func() { WithPongTimeout(-1 * time.Millisecond) })
}
