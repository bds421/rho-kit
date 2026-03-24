package messaging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakePublisher records Publish calls and can be configured to fail.
type fakePublisher struct {
	mu        sync.Mutex
	calls     []publishCall
	failUntil int // first N calls return error
}

type publishCall struct {
	Exchange   string
	RoutingKey string
	MsgID      string
}

func (f *fakePublisher) publish(_ context.Context, exchange, routingKey string, msg Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	call := publishCall{Exchange: exchange, RoutingKey: routingKey, MsgID: msg.ID}
	f.calls = append(f.calls, call)

	if len(f.calls) <= f.failUntil {
		return fmt.Errorf("publish failed (call %d)", len(f.calls))
	}
	return nil
}

func (f *fakePublisher) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// withPublishFn overrides the publish function (test-only option).
func withPublishFn(fn func(ctx context.Context, exchange, routingKey string, msg Message) error) BufferedPublisherOption {
	return func(o *BufferedPublisher) { o.publishFn = fn }
}

// withHealthFn overrides the health-check function (test-only option).
func withHealthFn(fn func() bool) BufferedPublisherOption {
	return func(o *BufferedPublisher) { o.healthyFn = fn }
}

// newTestBufferedPublisher constructs a BufferedPublisher using the option pattern
// instead of directly setting unexported fields. It uses nil for inner/conn
// because withPublishFn and withHealthFn override the defaults before any
// nil dereference can occur.
func newTestBufferedPublisher(publishFn func(ctx context.Context, exchange, routingKey string, msg Message) error, healthFn func() bool, opts ...BufferedPublisherOption) *BufferedPublisher {
	allOpts := append([]BufferedPublisherOption{withPublishFn(publishFn), withHealthFn(healthFn)}, opts...)
	return newBufferedPublisher(slog.New(slog.NewTextHandler(io.Discard, nil)), allOpts...)
}

// newBufferedPublisher mirrors NewBufferedPublisher but without requiring AMQP
// dependencies. It applies options over safe defaults.
func newBufferedPublisher(logger *slog.Logger, opts ...BufferedPublisherOption) *BufferedPublisher {
	o := &BufferedPublisher{
		logger:            logger,
		maxSize:           defaultBufferedMaxSize,
		finalDrainTimeout: defaultBufferedFinalDrainTimeout,
	}
	for _, opt := range opts {
		opt(o)
	}
	return o
}

func testBufferedPublisher(fp *fakePublisher, healthy bool, opts ...BufferedPublisherOption) *BufferedPublisher {
	return newTestBufferedPublisher(fp.publish, func() bool { return healthy }, opts...)
}

func testBufferedPublisherWithHealthPtr(fp *fakePublisher, healthy *atomic.Bool, opts ...BufferedPublisherOption) *BufferedPublisher {
	return newTestBufferedPublisher(fp.publish, func() bool { return healthy.Load() }, opts...)
}

func TestBufferedPublisher_HealthyDirectPublish(t *testing.T) {
	fp := &fakePublisher{}
	pub := testBufferedPublisher(fp, true)

	msg, _ := NewMessage("test.event", map[string]string{"key": "value"})

	if err := pub.Publish(context.Background(), "exchange", "routing.key", msg); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if fp.callCount() != 1 {
		t.Fatalf("expected 1 publish call, got %d", fp.callCount())
	}
	if pub.Pending() != 0 {
		t.Fatalf("expected 0 pending, got %d", pub.Pending())
	}
}

func TestBufferedPublisher_UnhealthyBuffers(t *testing.T) {
	fp := &fakePublisher{}
	pub := testBufferedPublisher(fp, false)

	msg, _ := NewMessage("test.event", map[string]string{"key": "value"})

	if err := pub.Publish(context.Background(), "exchange", "routing.key", msg); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if fp.callCount() != 0 {
		t.Fatalf("expected 0 publish calls when unhealthy, got %d", fp.callCount())
	}
	if pub.Pending() != 1 {
		t.Fatalf("expected 1 pending, got %d", pub.Pending())
	}
}

func TestBufferedPublisher_PublishFailureBuffers(t *testing.T) {
	fp := &fakePublisher{failUntil: 1}
	pub := testBufferedPublisher(fp, true)

	msg, _ := NewMessage("test.event", map[string]string{"key": "value"})

	if err := pub.Publish(context.Background(), "exchange", "routing.key", msg); err != nil {
		t.Fatalf("expected no error (buffered), got %v", err)
	}

	if fp.callCount() != 1 {
		t.Fatalf("expected 1 publish attempt, got %d", fp.callCount())
	}
	if pub.Pending() != 1 {
		t.Fatalf("expected 1 pending (buffered after failure), got %d", pub.Pending())
	}
}

func TestBufferedPublisher_BufferFull(t *testing.T) {
	fp := &fakePublisher{}
	pub := testBufferedPublisher(fp, false, WithBufferedMaxSize(2))

	msg1, _ := NewMessage("test.event", "m1")
	msg2, _ := NewMessage("test.event", "m2")
	msg3, _ := NewMessage("test.event", "m3")

	_ = pub.Publish(context.Background(), "ex", "rk", msg1)
	_ = pub.Publish(context.Background(), "ex", "rk", msg2)

	err := pub.Publish(context.Background(), "ex", "rk", msg3)
	if err == nil {
		t.Fatal("expected error when buffer full")
	}

	if pub.Pending() != 2 {
		t.Fatalf("expected 2 pending, got %d", pub.Pending())
	}
}

func TestBufferedPublisherDrain_PublishesBuffered(t *testing.T) {
	fp := &fakePublisher{}
	var healthy atomic.Bool
	pub := testBufferedPublisherWithHealthPtr(fp, &healthy)

	for i := range 3 {
		msg, _ := NewMessage("test.event", fmt.Sprintf("m%d", i))
		_ = pub.Publish(context.Background(), "exchange", "routing.key", msg)
	}
	if pub.Pending() != 3 {
		t.Fatalf("expected 3 pending, got %d", pub.Pending())
	}

	// Drain while unhealthy — nothing should happen.
	pub.drain(context.Background())
	if pub.Pending() != 3 {
		t.Fatalf("expected 3 pending after unhealthy drain, got %d", pub.Pending())
	}

	// Set healthy and drain.
	healthy.Store(true)
	pub.drain(context.Background())

	if pub.Pending() != 0 {
		t.Fatalf("expected 0 pending after drain, got %d", pub.Pending())
	}
	if fp.callCount() != 3 {
		t.Fatalf("expected 3 publish calls, got %d", fp.callCount())
	}
}

func TestBufferedPublisherDrain_StopsOnFailure(t *testing.T) {
	fp := &fakePublisher{failUntil: 2}
	var healthy atomic.Bool
	pub := testBufferedPublisherWithHealthPtr(fp, &healthy)

	for i := range 5 {
		msg, _ := NewMessage("test.event", fmt.Sprintf("m%d", i))
		_ = pub.Publish(context.Background(), "exchange", "routing.key", msg)
	}

	healthy.Store(true)
	pub.drain(context.Background())

	// First call fails, drain stops immediately.
	if pub.Pending() != 5 {
		t.Fatalf("expected 5 pending (drain failed on first), got %d", pub.Pending())
	}

	// Second drain: call #2 still fails.
	pub.drain(context.Background())
	if pub.Pending() != 5 {
		t.Fatalf("expected 5 pending (drain failed again), got %d", pub.Pending())
	}

	// Third drain: call #3 succeeds, all 5 drain.
	pub.drain(context.Background())
	if pub.Pending() != 0 {
		t.Fatalf("expected 0 pending after successful drain, got %d", pub.Pending())
	}
}

func TestBufferedPublisherPersistence_SaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	stateFile := filepath.Join(dir, "buffered.json")

	fp := &fakePublisher{}
	pub := testBufferedPublisher(fp, false, WithBufferedStateFile(stateFile))

	// Buffer 2 messages — they should be persisted.
	msg1, _ := NewMessage("test.event", "payload1")
	msg2, _ := NewMessage("test.event", "payload2")
	_ = pub.Publish(context.Background(), "ex1", "rk1", msg1)
	_ = pub.Publish(context.Background(), "ex2", "rk2", msg2)

	if pub.Pending() != 2 {
		t.Fatalf("expected 2 pending, got %d", pub.Pending())
	}

	// Verify file exists.
	if _, err := os.Stat(stateFile); err != nil {
		t.Fatalf("state file should exist: %v", err)
	}

	// Create a new publisher from the same state file — simulates process restart.
	fp2 := &fakePublisher{}
	pub2 := newTestBufferedPublisher(fp2.publish, func() bool { return true }, WithBufferedStateFile(stateFile))
	if err := pub2.load(); err != nil {
		t.Fatalf("load failed: %v", err)
	}

	if pub2.Pending() != 2 {
		t.Fatalf("expected 2 pending after load, got %d", pub2.Pending())
	}

	// Drain the restored messages.
	pub2.drain(context.Background())

	if pub2.Pending() != 0 {
		t.Fatalf("expected 0 pending after drain, got %d", pub2.Pending())
	}
	if fp2.callCount() != 2 {
		t.Fatalf("expected 2 publish calls, got %d", fp2.callCount())
	}
}

func TestBufferedPublisherPersistence_DrainClearsFile(t *testing.T) {
	dir := t.TempDir()
	stateFile := filepath.Join(dir, "buffered.json")

	fp := &fakePublisher{}
	var healthy atomic.Bool
	pub := testBufferedPublisherWithHealthPtr(fp, &healthy, WithBufferedStateFile(stateFile))

	msg, _ := NewMessage("test.event", "payload")
	_ = pub.Publish(context.Background(), "ex", "rk", msg)

	if pub.Pending() != 1 {
		t.Fatalf("expected 1 pending, got %d", pub.Pending())
	}

	// Drain successfully.
	healthy.Store(true)
	pub.drain(context.Background())

	if pub.Pending() != 0 {
		t.Fatalf("expected 0 pending after drain, got %d", pub.Pending())
	}

	// Reload from file — should be empty.
	pub3 := newTestBufferedPublisher(fp.publish, func() bool { return true }, WithBufferedStateFile(stateFile))
	if err := pub3.load(); err != nil {
		t.Fatalf("load failed: %v", err)
	}

	if pub3.Pending() != 0 {
		t.Fatalf("expected 0 pending after reload, got %d", pub3.Pending())
	}
}

func TestBufferedPublisherRun_StopsOnContextCancel(t *testing.T) {
	fp := &fakePublisher{}
	pub := testBufferedPublisher(fp, true)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		pub.Run(ctx)
		close(done)
	}()

	time.Sleep(10 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// OK
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop after context cancel")
	}
}

func TestBufferedPublisherFinalDrain_NoPending_Noop(t *testing.T) {
	fp := &fakePublisher{}
	pub := testBufferedPublisher(fp, true)

	// finalDrain with nothing pending should return immediately without publishing.
	pub.finalDrain()
	if fp.callCount() != 0 {
		t.Fatalf("expected 0 publish calls, got %d", fp.callCount())
	}
}

func TestBufferedPublisherFinalDrain_PublishesPending(t *testing.T) {
	fp := &fakePublisher{}
	pub := testBufferedPublisher(fp, false)

	msg, _ := NewMessage("test.event", "payload")
	_ = pub.Publish(context.Background(), "ex", "rk", msg)

	if pub.Pending() != 1 {
		t.Fatalf("expected 1 pending, got %d", pub.Pending())
	}

	// Switch to healthy so finalDrain can publish.
	pub.healthyFn = func() bool { return true }
	pub.finalDrain()

	if pub.Pending() != 0 {
		t.Fatalf("expected 0 pending after final drain, got %d", pub.Pending())
	}
	if fp.callCount() != 1 {
		t.Fatalf("expected 1 publish call, got %d", fp.callCount())
	}
}

func TestBufferedPublisherFinalDrain_UnhealthyLeavesMessages(t *testing.T) {
	fp := &fakePublisher{}
	pub := testBufferedPublisher(fp, false)

	msg, _ := NewMessage("test.event", "payload")
	_ = pub.Publish(context.Background(), "ex", "rk", msg)

	// finalDrain while unhealthy should not publish.
	pub.finalDrain()

	if pub.Pending() != 1 {
		t.Fatalf("expected 1 pending, got %d", pub.Pending())
	}
}

func TestBufferedPublisherRun_FinalDrainOnCancel(t *testing.T) {
	fp := &fakePublisher{}
	var healthy atomic.Bool
	pub := testBufferedPublisherWithHealthPtr(fp, &healthy)

	// Buffer a message while unhealthy.
	msg, _ := NewMessage("test.event", "payload")
	_ = pub.Publish(context.Background(), "ex", "rk", msg)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		pub.Run(ctx)
		close(done)
	}()

	// Let Run start, then become healthy and cancel.
	time.Sleep(10 * time.Millisecond)
	healthy.Store(true)
	cancel()

	select {
	case <-done:
		// OK
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop after context cancel")
	}

	// finalDrain should have published the pending message.
	if pub.Pending() != 0 {
		t.Fatalf("expected 0 pending after final drain, got %d", pub.Pending())
	}
}

func TestBufferedPublisherDrain_CancelledContext(t *testing.T) {
	fp := &fakePublisher{}
	pub := testBufferedPublisher(fp, false)

	// Buffer 3 messages.
	for i := range 3 {
		msg, _ := NewMessage("test.event", fmt.Sprintf("m%d", i))
		_ = pub.Publish(context.Background(), "ex", "rk", msg)
	}

	// Switch to healthy but cancel context before drain.
	pub.healthyFn = func() bool { return true }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	pub.drain(ctx)

	// Context is cancelled, so drain should stop without publishing.
	if pub.Pending() != 3 {
		t.Fatalf("expected 3 pending after cancelled drain, got %d", pub.Pending())
	}
}

func TestBufferedPublisherLoad_NoStateFile_Noop(t *testing.T) {
	pub := newBufferedPublisher(slog.New(slog.NewTextHandler(io.Discard, nil)))
	err := pub.load()
	if err != nil {
		t.Fatalf("expected no error for empty stateFile, got %v", err)
	}
}

func TestBufferedPublisherLoad_MissingFile_ReturnsNilPending(t *testing.T) {
	pub := newBufferedPublisher(
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		WithBufferedStateFile(filepath.Join(t.TempDir(), "nonexistent.json")),
	)
	err := pub.load()
	if err != nil {
		t.Fatalf("expected no error for missing file (ErrNotExist is handled), got %v", err)
	}
	if pub.Pending() != 0 {
		t.Fatalf("expected 0 pending, got %d", pub.Pending())
	}
}

func TestBufferedPublisherLoad_CorruptFile_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	stateFile := filepath.Join(dir, "buffered.json")
	if err := os.WriteFile(stateFile, []byte(`not valid json`), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	pub := newBufferedPublisher(
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		WithBufferedStateFile(stateFile),
	)
	err := pub.load()
	if err == nil {
		t.Fatal("expected error for corrupt state file")
	}
}

func TestBufferedPublisherSaveLocked_NoStateFile_Noop(t *testing.T) {
	pub := newBufferedPublisher(slog.New(slog.NewTextHandler(io.Discard, nil)))
	// Should not panic or error with empty stateFile.
	pub.saveLocked()
}

func TestBufferedPublisherSaveLocked_InvalidPath_LogsError(t *testing.T) {
	pub := newBufferedPublisher(
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		WithBufferedStateFile("/nonexistent-dir/subdir/buffered.json"),
	)
	// Manually seed pending so saveLocked has something to write.
	pub.pending = []pendingMessage{{Exchange: "ex", RoutingKey: "rk"}}
	// Should not panic even though save will fail.
	pub.saveLocked()
}

func TestBufferedPublisherRun_DrainOnTick(t *testing.T) {
	fp := &fakePublisher{}
	var healthy atomic.Bool
	healthy.Store(true)
	pub := testBufferedPublisherWithHealthPtr(fp, &healthy)

	// Buffer a message while healthy fails (force buffer).
	healthy.Store(false)
	msg, _ := NewMessage("test.event", "tick-drain")
	_ = pub.Publish(context.Background(), "ex", "rk", msg)

	if pub.Pending() != 1 {
		t.Fatalf("expected 1 pending, got %d", pub.Pending())
	}

	healthy.Store(true)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		pub.Run(ctx)
		close(done)
	}()

	// Wait for the tick-based drain to happen (default interval is 5s,
	// but the initial drain on startup should handle it).
	// The initial drain at Run startup should publish the message.
	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop")
	}

	// Initial drain should have published.
	if pub.Pending() != 0 {
		t.Fatalf("expected 0 pending after Run drain, got %d", pub.Pending())
	}
}
