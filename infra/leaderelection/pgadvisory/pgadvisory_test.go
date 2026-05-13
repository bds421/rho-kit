package pgadvisory

import (
	"bytes"
	"context"
	"database/sql"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/v2/leaderelection"
)

type releaseContextKey struct{}

type fakeLockHandle struct {
	released atomic.Bool
	extendOK atomic.Bool
}

func (f *fakeLockHandle) Release(context.Context) error { f.released.Store(true); return nil }
func (f *fakeLockHandle) Extend(context.Context) (bool, error) {
	return f.extendOK.Load(), nil
}

func TestOptions_PanicOnInvalidDurations(t *testing.T) {
	for name, fn := range map[string]func(){
		"WithRetryInterval zero":     func() { WithRetryInterval(0) },
		"WithRetryInterval negative": func() { WithRetryInterval(-time.Second) },
		"WithHealthCheck zero":       func() { WithHealthCheck(0) },
		"WithHealthCheck negative":   func() { WithHealthCheck(-time.Second) },
	} {
		t.Run(name, func(t *testing.T) {
			require.Panics(t, fn)
		})
	}
}

func TestNew_PanicsOnEmptyKey(t *testing.T) {
	require.Panics(t, func() {
		New(nil, "")
	})
}

func TestNew_PanicsOnNilOption(t *testing.T) {
	require.Panics(t, func() {
		New(&sql.DB{}, "leader", nil)
	})
}

func TestHoldLeadership_OnAcquiredPanicReturnsError(t *testing.T) {
	e := &Elector{
		healthCheck: time.Hour,
	}
	handle := &fakeLockHandle{}
	handle.extendOK.Store(true)

	err := e.holdLeadership(context.Background(), handle, leaderelection.Callbacks{
		OnAcquired: func(context.Context) {
			panic("leader work exploded")
		},
	})
	require.ErrorContains(t, err, "OnAcquired panic")
	require.ErrorContains(t, err, "<redacted panic value: string>")
	require.NotContains(t, err.Error(), "leader work exploded")
}

func TestRun_DoesNotCallOnLostWithoutLeadership(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e := &Elector{logger: slog.Default(), key: "leader"}

	var called atomic.Bool
	err := e.Run(ctx, leaderelection.Callbacks{
		OnLost: func() {
			called.Store(true)
		},
	})
	require.ErrorIs(t, err, context.Canceled)
	require.False(t, called.Load())
}

func TestRunOnLost_PanicReturned(t *testing.T) {
	e := &Elector{logger: slog.Default(), key: "leader"}

	err := e.runOnLost(leaderelection.Callbacks{
		OnLost: func() {
			panic("lost cleanup exploded")
		},
	})
	require.ErrorContains(t, err, "OnLost panic")
	require.ErrorContains(t, err, "<redacted panic value: string>")
	require.NotContains(t, err.Error(), "lost cleanup exploded")
}

func TestRun_RejectsNilContext(t *testing.T) {
	e := &Elector{logger: slog.Default(), key: "leader"}
	var ctx context.Context
	err := e.Run(ctx, leaderelection.Callbacks{})
	require.Error(t, err)
	require.ErrorContains(t, err, "non-nil context")
}

func TestHoldLeadership_LossCancelsAndWaitsForCallback(t *testing.T) {
	e := &Elector{
		healthCheck: 10 * time.Millisecond,
	}
	handle := &fakeLockHandle{}
	handle.extendOK.Store(false)

	var callbackExited atomic.Bool
	err := e.holdLeadership(context.Background(), handle, leaderelection.Callbacks{
		OnAcquired: func(ctx context.Context) {
			<-ctx.Done()
			time.Sleep(10 * time.Millisecond)
			callbackExited.Store(true)
		},
	})
	require.ErrorContains(t, err, "handle reports lost")
	require.True(t, callbackExited.Load(), "leader work must drain before retry")
}

func TestHoldLeadership_LossDoesNotReturnUntilCallbackDrains(t *testing.T) {
	e := &Elector{
		healthCheck: 10 * time.Millisecond,
	}
	handle := &fakeLockHandle{}
	handle.extendOK.Store(false)

	started := make(chan struct{})
	cancelled := make(chan struct{})
	released := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(released) })
	})

	result := make(chan error, 1)
	go func() {
		result <- e.holdLeadership(context.Background(), handle, leaderelection.Callbacks{
			OnAcquired: func(ctx context.Context) {
				close(started)
				<-ctx.Done()
				close(cancelled)
				<-released
			},
		})
	}()

	select {
	case <-started:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("OnAcquired did not start")
	}
	select {
	case <-cancelled:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("OnAcquired was not cancelled after lock loss")
	}
	select {
	case err := <-result:
		t.Fatalf("holdLeadership returned before callback drained: %v", err)
	case <-time.After(30 * time.Millisecond):
	}

	releaseOnce.Do(func() { close(released) })
	select {
	case err := <-result:
		require.ErrorContains(t, err, "handle reports lost")
	case <-time.After(100 * time.Millisecond):
		t.Fatal("holdLeadership did not return after callback drained")
	}
}

func TestLeaderReleaseContextPreservesValuesAfterCancellation(t *testing.T) {
	parent := context.WithValue(context.Background(), releaseContextKey{}, "trace-123")
	ctx, cancel := context.WithCancel(parent)
	cancel()

	releaseCtx, releaseCancel := leaderReleaseContext(ctx, time.Second)
	defer releaseCancel()

	require.Equal(t, "trace-123", releaseCtx.Value(releaseContextKey{}))
	require.NoError(t, releaseCtx.Err())
}

func TestHoldLeadership_LongCallbackEmitsWarnAndMetric(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := NewMetrics(WithMetricsRegisterer(reg))

	logBuf := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	e := &Elector{
		key:           "tenant-sweeper",
		healthCheck:   5 * time.Millisecond,
		drainWarnTick: 10 * time.Millisecond,
		logger:        logger,
		metrics:       metrics,
	}
	handle := &fakeLockHandle{}
	handle.extendOK.Store(false) // force loss path so cancel + awaitCallbackDrain runs

	released := make(chan struct{})
	cb := leaderelection.Callbacks{
		OnAcquired: func(ctx context.Context) {
			<-ctx.Done()
			<-released
		},
	}

	result := make(chan error, 1)
	go func() {
		result <- e.holdLeadership(context.Background(), handle, cb)
	}()

	// Wait long enough for multiple warn ticks to fire on a stuck callback.
	require.Eventually(t, func() bool {
		return testutil.ToFloat64(metrics.drainWarns.WithLabelValues("tenant-sweeper")) >= 2
	}, time.Second, 5*time.Millisecond, "expected drain warn metric to increment at least twice")

	require.Contains(t, logBuf.String(), "OnAcquired callback still draining")
	// `key` is logged via redact.String — verify it shows up as a
	// redacted attribute rather than asserting on the raw key string.
	require.Contains(t, logBuf.String(), "key=")

	close(released)

	select {
	case err := <-result:
		require.ErrorContains(t, err, "handle reports lost")
	case <-time.After(500 * time.Millisecond):
		t.Fatal("holdLeadership did not return after callback drained")
	}

	// Terminal drained observation must also be recorded so the
	// drained-state histogram is non-empty for SLO dashboards.
	drainedHistogramHasObservation := func() bool {
		ch := make(chan prometheus.Metric, 16)
		metrics.drainDuration.Collect(ch)
		close(ch)
		for m := range ch {
			out := &dto.Metric{}
			if err := m.Write(out); err != nil {
				continue
			}
			var key, state string
			for _, lp := range out.Label {
				switch lp.GetName() {
				case "key":
					key = lp.GetValue()
				case "state":
					state = lp.GetValue()
				}
			}
			if key == "tenant-sweeper" && state == drainStateDrained && out.Histogram != nil && out.Histogram.GetSampleCount() > 0 {
				return true
			}
		}
		return false
	}
	require.True(t, drainedHistogramHasObservation(), "drained histogram must record terminal observation")
	require.True(t, strings.Contains(logBuf.String(), "elapsed"))
}
