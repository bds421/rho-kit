package temporal

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorker_StartRejectsNilContext(t *testing.T) {
	w := &Worker{}
	var ctx context.Context
	err := w.Start(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-nil context")
}

func TestWorker_StartRejectsUninitializedWorker(t *testing.T) {
	w := &Worker{}

	err := w.Start(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not initialized")
}

func TestWorker_StartRejectsSecondStart(t *testing.T) {
	fw := newFakeTemporalWorker()
	w := &Worker{w: fw}

	ctx, cancel := context.WithCancel(context.Background())
	startDone := make(chan error, 1)
	go func() { startDone <- w.Start(ctx) }()
	waitForFakeTemporalRun(t, fw)

	err := w.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already started")

	cancel()
	require.NoError(t, <-startDone)
}

func TestWorker_StartRejectsRestartAfterStop(t *testing.T) {
	fw := newFakeTemporalWorker()
	w := &Worker{w: fw}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startDone := make(chan error, 1)
	go func() { startDone <- w.Start(ctx) }()
	waitForFakeTemporalRun(t, fw)

	require.NoError(t, w.Stop(context.Background()))
	require.NoError(t, <-startDone)

	err := w.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already started")
}

func TestWorker_StartRejectsAfterStopBeforeStart(t *testing.T) {
	fw := newFakeTemporalWorker()
	w := &Worker{w: fw}

	require.NoError(t, w.Stop(context.Background()))

	err := w.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already stopped")
}

func TestWorker_StopRejectsNilContext(t *testing.T) {
	fw := newFakeTemporalWorker()
	w := &Worker{w: fw}

	var ctx context.Context
	err := w.Stop(ctx)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-nil context")
}

func TestWorker_StopIsIdempotent(t *testing.T) {
	fw := newFakeTemporalWorker()
	w := &Worker{w: fw}

	require.NoError(t, w.Stop(context.Background()))
	require.NoError(t, w.Stop(context.Background()))

	assert.Equal(t, int32(1), fw.stopCount.Load())
}

type fakeTemporalWorker struct {
	runStarted chan struct{}
	stopped    chan struct{}
	stopOnce   sync.Once
	startOnce  sync.Once
	stopCount  atomic.Int32
}

func newFakeTemporalWorker() *fakeTemporalWorker {
	return &fakeTemporalWorker{
		runStarted: make(chan struct{}),
		stopped:    make(chan struct{}),
	}
}

func waitForFakeTemporalRun(t *testing.T, fw *fakeTemporalWorker) {
	t.Helper()
	select {
	case <-fw.runStarted:
	case <-time.After(time.Second):
		t.Fatal("Temporal worker Run did not start")
	}
}

func (f *fakeTemporalWorker) Run(interrupt <-chan interface{}) error {
	f.startOnce.Do(func() { close(f.runStarted) })
	select {
	case <-interrupt:
		return nil
	case <-f.stopped:
		return nil
	}
}

func (f *fakeTemporalWorker) Stop() {
	f.stopOnce.Do(func() {
		f.stopCount.Add(1)
		close(f.stopped)
	})
}
