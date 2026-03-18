package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testComponent is a simple component for testing.
type testComponent struct {
	started  atomic.Bool
	stopped  atomic.Bool
	startErr error
	stopErr  error
	blockCh  chan struct{} // closed to unblock Start
}

func newTestComponent() *testComponent {
	return &testComponent{blockCh: make(chan struct{})}
}

func (c *testComponent) Start(ctx context.Context) error {
	c.started.Store(true)
	if c.startErr != nil {
		return c.startErr
	}
	select {
	case <-ctx.Done():
		return nil
	case <-c.blockCh:
		return nil
	}
}

func (c *testComponent) Stop(_ context.Context) error {
	c.stopped.Store(true)
	return c.stopErr
}

func TestRunner_CleanShutdown(t *testing.T) {
	logger := slog.Default()
	r := NewRunner(logger)

	c1 := newTestComponent()
	c2 := newTestComponent()
	r.Add("c1", c1)
	r.Add("c2", c2)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- r.Run(ctx)
	}()

	// Wait for components to start
	time.Sleep(50 * time.Millisecond)
	assert.True(t, c1.started.Load())
	assert.True(t, c2.started.Load())

	// Cancel triggers shutdown
	cancel()

	err := <-done
	require.NoError(t, err)
	assert.True(t, c1.stopped.Load())
	assert.True(t, c2.stopped.Load())
}

func TestRunner_ComponentError(t *testing.T) {
	logger := slog.Default()
	r := NewRunner(logger)

	expectedErr := errors.New("component failed")
	c1 := newTestComponent()
	c1.startErr = expectedErr

	c2 := newTestComponent()
	r.Add("failing", c1)
	r.Add("healthy", c2)

	err := r.Run(context.Background())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "component failed")
}

func TestRunner_AddFunc(t *testing.T) {
	logger := slog.Default()
	r := NewRunner(logger)

	started := atomic.Bool{}
	r.AddFunc("worker", func(ctx context.Context) error {
		started.Store(true)
		<-ctx.Done()
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- r.Run(ctx)
	}()

	time.Sleep(50 * time.Millisecond)
	assert.True(t, started.Load())

	cancel()
	err := <-done
	require.NoError(t, err)
}

func TestRunner_StopTimeout(t *testing.T) {
	r := NewRunner(slog.Default(), WithStopTimeout(100*time.Millisecond))

	c1 := newTestComponent()
	r.Add("slow-stop", c1)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- r.Run(ctx)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	err := <-done
	require.NoError(t, err)
}

func TestHTTPServer_NilPanics(t *testing.T) {
	assert.Panics(t, func() {
		HTTPServer(nil)
	})
}

func TestFuncComponent_Stop(t *testing.T) {
	fc := &FuncComponent{StartFn: func(ctx context.Context) error {
		<-ctx.Done()
		return nil
	}}
	err := fc.Stop(context.Background())
	assert.NoError(t, err)
}

// orderedComponent records its name to a shared slice when Stop is called,
// allowing tests to verify shutdown order.
type orderedComponent struct {
	name     string
	mu       *sync.Mutex
	stopLog  *[]string
	startErr error
}

func (c *orderedComponent) Start(ctx context.Context) error {
	if c.startErr != nil {
		return c.startErr
	}
	<-ctx.Done()
	return nil
}

func (c *orderedComponent) Stop(_ context.Context) error {
	c.mu.Lock()
	*c.stopLog = append(*c.stopLog, c.name)
	c.mu.Unlock()
	return nil
}

func TestRunner_ReverseOrderShutdown(t *testing.T) {
	logger := slog.Default()
	r := NewRunner(logger)

	var mu sync.Mutex
	stopLog := make([]string, 0, 3)

	compA := &orderedComponent{name: "A", mu: &mu, stopLog: &stopLog}
	compB := &orderedComponent{name: "B", mu: &mu, stopLog: &stopLog}
	compC := &orderedComponent{name: "C", mu: &mu, stopLog: &stopLog}

	r.Add("A", compA)
	r.Add("B", compB)
	r.Add("C", compC)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- r.Run(ctx)
	}()

	// Allow components to reach their blocking <-ctx.Done() select.
	time.Sleep(50 * time.Millisecond)

	cancel()

	if err := <-done; err != nil {
		t.Fatalf("Run returned unexpected error: %v", err)
	}

	mu.Lock()
	got := make([]string, len(stopLog))
	copy(got, stopLog)
	mu.Unlock()

	want := []string{"C", "B", "A"}
	if len(got) != len(want) {
		t.Fatalf("expected %d stopped components, got %d: %v", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("stop order mismatch at index %d: want %q, got %q (full order: %v)", i, want[i], got[i], got)
		}
	}
}

// panicComponent panics inside Start to exercise the Runner's panic recovery.
type panicComponent struct{}

func (p *panicComponent) Start(_ context.Context) error {
	panic("something went very wrong")
}

func (p *panicComponent) Stop(_ context.Context) error { return nil }

func TestRunner_PanicRecovery(t *testing.T) {
	logger := slog.Default()
	r := NewRunner(logger)
	r.Add("panicker", &panicComponent{})

	err := r.Run(context.Background())
	if err == nil {
		t.Fatal("expected Run to return a non-nil error after panic, got nil")
	}

	panicValue := "something went very wrong"
	if !strings.Contains(err.Error(), panicValue) {
		t.Errorf("error %q does not contain panic value %q", err.Error(), panicValue)
	}

	// Verify the error mentions the component name as well.
	if !strings.Contains(err.Error(), "panicker") {
		t.Errorf("error %q does not mention component name %q", err.Error(), "panicker")
	}

	// Consume the variable to satisfy errcheck / staticcheck.
	_ = fmt.Sprintf("%v", err)
}
