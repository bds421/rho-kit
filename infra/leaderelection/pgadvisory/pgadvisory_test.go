package pgadvisory

import (
	"context"
	"database/sql"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bds421/rho-kit/infra/v2/leaderelection"
	"github.com/stretchr/testify/require"
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

func TestRun_OnLostPanicReturned(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e := &Elector{logger: slog.Default(), key: "leader"}

	err := e.Run(ctx, leaderelection.Callbacks{
		OnLost: func() {
			panic("lost cleanup exploded")
		},
	})
	require.ErrorIs(t, err, context.Canceled)
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

func TestLeaderReleaseContextPreservesValuesAfterCancellation(t *testing.T) {
	parent := context.WithValue(context.Background(), releaseContextKey{}, "trace-123")
	ctx, cancel := context.WithCancel(parent)
	cancel()

	releaseCtx, releaseCancel := leaderReleaseContext(ctx, time.Second)
	defer releaseCancel()

	require.Equal(t, "trace-123", releaseCtx.Value(releaseContextKey{}))
	require.NoError(t, releaseCtx.Err())
}
