package app

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/v2/leaderelection"
	"github.com/bds421/rho-kit/runtime/v2/lifecycle"
)

// stubElector is a no-op Elector that reports a fixed leader state.
type stubElector struct{ leader bool }

func (s *stubElector) Run(ctx context.Context, _ leaderelection.Callbacks) error {
	<-ctx.Done()
	return ctx.Err()
}
func (s *stubElector) IsLeader() bool { return s.leader }

type captureCallbacksElector struct {
	callbacks chan leaderelection.Callbacks
}

func (s *captureCallbacksElector) Run(ctx context.Context, cb leaderelection.Callbacks) error {
	s.callbacks <- cb
	<-ctx.Done()
	return ctx.Err()
}

func (s *captureCallbacksElector) IsLeader() bool { return false }

func TestNewLeaderModule_PanicsOnNil(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil elector")
		}
	}()
	newLeaderModule(nil)
}

func TestWithLeaderElection_PanicsOnNil(t *testing.T) {
	b := New("test", "v1", BaseConfig{})
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil elector")
		}
	}()
	b.WithLeaderElection(nil)
}

func TestWithLeaderElection_PopulatesInfrastructure(t *testing.T) {
	e := &stubElector{leader: true}
	m := newLeaderModule(e)
	infra := &Infrastructure{}
	m.Populate(infra)
	assert.Same(t, e, infra.Leader)
}

func TestWithLeaderElection_RegistersOnBuilder(t *testing.T) {
	e := &stubElector{}
	b := New("test", "v1", BaseConfig{}).WithLeaderElection(e)
	assert.Same(t, e, b.leaderElector)
}

func TestLeaderModule_OnAcquiredHoldsLeadershipUntilContextDone(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	e := &captureCallbacksElector{callbacks: make(chan leaderelection.Callbacks, 1)}
	runner := lifecycle.NewRunner(logger)
	m := newLeaderModule(e)

	require.NoError(t, m.Init(context.Background(), ModuleContext{
		Logger: logger,
		Runner: runner,
	}))

	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()
	runDone := make(chan error, 1)
	go func() { runDone <- runner.Run(runCtx) }()

	var cb leaderelection.Callbacks
	select {
	case cb = <-e.callbacks:
	case <-time.After(time.Second):
		t.Fatal("leader callbacks were not registered")
	}

	leaderCtx, leaderCancel := context.WithCancel(context.Background())
	acquiredDone := make(chan struct{})
	go func() {
		cb.OnAcquired(leaderCtx)
		close(acquiredDone)
	}()

	select {
	case <-acquiredDone:
		t.Fatal("OnAcquired returned before leadership context was cancelled")
	case <-time.After(20 * time.Millisecond):
	}

	leaderCancel()
	select {
	case <-acquiredDone:
	case <-time.After(time.Second):
		t.Fatal("OnAcquired did not return after leadership context was cancelled")
	}

	runCancel()
	select {
	case <-runDone:
	case <-time.After(time.Second):
		t.Fatal("runner did not stop")
	}
}
