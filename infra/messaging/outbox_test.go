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
func withPublishFn(fn func(ctx context.Context, exchange, routingKey string, msg Message) error) OutboxOption {
	return func(o *OutboxPublisher) { o.publishFn = fn }
}

// withHealthFn overrides the health-check function (test-only option).
func withHealthFn(fn func() bool) OutboxOption {
	return func(o *OutboxPublisher) { o.healthyFn = fn }
}

// newTestOutboxPublisher constructs an OutboxPublisher using the option pattern
// instead of directly setting unexported fields. It uses nil for inner/conn
// because withPublishFn and withHealthFn override the defaults before any
// nil dereference can occur.
func newTestOutboxPublisher(publishFn func(ctx context.Context, exchange, routingKey string, msg Message) error, healthFn func() bool, opts ...OutboxOption) *OutboxPublisher {
	allOpts := append([]OutboxOption{withPublishFn(publishFn), withHealthFn(healthFn)}, opts...)
	return newOutboxPublisher(slog.New(slog.NewTextHandler(io.Discard, nil)), allOpts...)
}

// newOutboxPublisher mirrors NewOutboxPublisher but without requiring AMQP
// dependencies. It applies options over safe defaults.
func newOutboxPublisher(logger *slog.Logger, opts ...OutboxOption) *OutboxPublisher {
	o := &OutboxPublisher{
		logger:            logger,
		maxSize:           defaultOutboxMaxSize,
		finalDrainTimeout: defaultOutboxFinalDrainTimeout,
	}
	for _, opt := range opts {
		opt(o)
	}
	return o
}

func testOutbox(fp *fakePublisher, healthy bool, opts ...OutboxOption) *OutboxPublisher {
	return newTestOutboxPublisher(fp.publish, func() bool { return healthy }, opts...)
}

func testOutboxWithHealthPtr(fp *fakePublisher, healthy *atomic.Bool, opts ...OutboxOption) *OutboxPublisher {
	return newTestOutboxPublisher(fp.publish, func() bool { return healthy.Load() }, opts...)
}

func TestOutboxPublish_HealthyDirectPublish(t *testing.T) {
	fp := &fakePublisher{}
	outbox := testOutbox(fp, true)

	msg, _ := NewMessage("test.event", map[string]string{"key": "value"})

	if err := outbox.Publish(context.Background(), "exchange", "routing.key", msg); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if fp.callCount() != 1 {
		t.Fatalf("expected 1 publish call, got %d", fp.callCount())
	}
	if outbox.Pending() != 0 {
		t.Fatalf("expected 0 pending, got %d", outbox.Pending())
	}
}

func TestOutboxPublish_UnhealthyBuffers(t *testing.T) {
	fp := &fakePublisher{}
	outbox := testOutbox(fp, false)

	msg, _ := NewMessage("test.event", map[string]string{"key": "value"})

	if err := outbox.Publish(context.Background(), "exchange", "routing.key", msg); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if fp.callCount() != 0 {
		t.Fatalf("expected 0 publish calls when unhealthy, got %d", fp.callCount())
	}
	if outbox.Pending() != 1 {
		t.Fatalf("expected 1 pending, got %d", outbox.Pending())
	}
}

func TestOutboxPublish_PublishFailureBuffers(t *testing.T) {
	fp := &fakePublisher{failUntil: 1}
	outbox := testOutbox(fp, true)

	msg, _ := NewMessage("test.event", map[string]string{"key": "value"})

	if err := outbox.Publish(context.Background(), "exchange", "routing.key", msg); err != nil {
		t.Fatalf("expected no error (buffered), got %v", err)
	}

	if fp.callCount() != 1 {
		t.Fatalf("expected 1 publish attempt, got %d", fp.callCount())
	}
	if outbox.Pending() != 1 {
		t.Fatalf("expected 1 pending (buffered after failure), got %d", outbox.Pending())
	}
}

func TestOutboxPublish_BufferFull(t *testing.T) {
	fp := &fakePublisher{}
	outbox := testOutbox(fp, false, WithOutboxMaxSize(2))

	msg1, _ := NewMessage("test.event", "m1")
	msg2, _ := NewMessage("test.event", "m2")
	msg3, _ := NewMessage("test.event", "m3")

	_ = outbox.Publish(context.Background(), "ex", "rk", msg1)
	_ = outbox.Publish(context.Background(), "ex", "rk", msg2)

	err := outbox.Publish(context.Background(), "ex", "rk", msg3)
	if err == nil {
		t.Fatal("expected error when buffer full")
	}

	if outbox.Pending() != 2 {
		t.Fatalf("expected 2 pending, got %d", outbox.Pending())
	}
}

func TestOutboxDrain_PublishesBuffered(t *testing.T) {
	fp := &fakePublisher{}
	var healthy atomic.Bool
	outbox := testOutboxWithHealthPtr(fp, &healthy)

	for i := range 3 {
		msg, _ := NewMessage("test.event", fmt.Sprintf("m%d", i))
		_ = outbox.Publish(context.Background(), "exchange", "routing.key", msg)
	}
	if outbox.Pending() != 3 {
		t.Fatalf("expected 3 pending, got %d", outbox.Pending())
	}

	// Drain while unhealthy — nothing should happen.
	outbox.drain(context.Background())
	if outbox.Pending() != 3 {
		t.Fatalf("expected 3 pending after unhealthy drain, got %d", outbox.Pending())
	}

	// Set healthy and drain.
	healthy.Store(true)
	outbox.drain(context.Background())

	if outbox.Pending() != 0 {
		t.Fatalf("expected 0 pending after drain, got %d", outbox.Pending())
	}
	if fp.callCount() != 3 {
		t.Fatalf("expected 3 publish calls, got %d", fp.callCount())
	}
}

func TestOutboxDrain_StopsOnFailure(t *testing.T) {
	fp := &fakePublisher{failUntil: 2}
	var healthy atomic.Bool
	outbox := testOutboxWithHealthPtr(fp, &healthy)

	for i := range 5 {
		msg, _ := NewMessage("test.event", fmt.Sprintf("m%d", i))
		_ = outbox.Publish(context.Background(), "exchange", "routing.key", msg)
	}

	healthy.Store(true)
	outbox.drain(context.Background())

	// First call fails, drain stops immediately.
	if outbox.Pending() != 5 {
		t.Fatalf("expected 5 pending (drain failed on first), got %d", outbox.Pending())
	}

	// Second drain: call #2 still fails.
	outbox.drain(context.Background())
	if outbox.Pending() != 5 {
		t.Fatalf("expected 5 pending (drain failed again), got %d", outbox.Pending())
	}

	// Third drain: call #3 succeeds, all 5 drain.
	outbox.drain(context.Background())
	if outbox.Pending() != 0 {
		t.Fatalf("expected 0 pending after successful drain, got %d", outbox.Pending())
	}
}

func TestOutboxPersistence_SaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	stateFile := filepath.Join(dir, "outbox.json")

	fp := &fakePublisher{}
	outbox := testOutbox(fp, false, WithOutboxStateFile(stateFile))

	// Buffer 2 messages — they should be persisted.
	msg1, _ := NewMessage("test.event", "payload1")
	msg2, _ := NewMessage("test.event", "payload2")
	_ = outbox.Publish(context.Background(), "ex1", "rk1", msg1)
	_ = outbox.Publish(context.Background(), "ex2", "rk2", msg2)

	if outbox.Pending() != 2 {
		t.Fatalf("expected 2 pending, got %d", outbox.Pending())
	}

	// Verify file exists.
	if _, err := os.Stat(stateFile); err != nil {
		t.Fatalf("state file should exist: %v", err)
	}

	// Create a new outbox from the same state file — simulates process restart.
	fp2 := &fakePublisher{}
	outbox2 := newTestOutboxPublisher(fp2.publish, func() bool { return true }, WithOutboxStateFile(stateFile))
	if err := outbox2.load(); err != nil {
		t.Fatalf("load failed: %v", err)
	}

	if outbox2.Pending() != 2 {
		t.Fatalf("expected 2 pending after load, got %d", outbox2.Pending())
	}

	// Drain the restored messages.
	outbox2.drain(context.Background())

	if outbox2.Pending() != 0 {
		t.Fatalf("expected 0 pending after drain, got %d", outbox2.Pending())
	}
	if fp2.callCount() != 2 {
		t.Fatalf("expected 2 publish calls, got %d", fp2.callCount())
	}
}

func TestOutboxPersistence_DrainClearsFile(t *testing.T) {
	dir := t.TempDir()
	stateFile := filepath.Join(dir, "outbox.json")

	fp := &fakePublisher{}
	var healthy atomic.Bool
	outbox := testOutboxWithHealthPtr(fp, &healthy, WithOutboxStateFile(stateFile))

	msg, _ := NewMessage("test.event", "payload")
	_ = outbox.Publish(context.Background(), "ex", "rk", msg)

	if outbox.Pending() != 1 {
		t.Fatalf("expected 1 pending, got %d", outbox.Pending())
	}

	// Drain successfully.
	healthy.Store(true)
	outbox.drain(context.Background())

	if outbox.Pending() != 0 {
		t.Fatalf("expected 0 pending after drain, got %d", outbox.Pending())
	}

	// Reload from file — should be empty.
	outbox3 := newTestOutboxPublisher(fp.publish, func() bool { return true }, WithOutboxStateFile(stateFile))
	if err := outbox3.load(); err != nil {
		t.Fatalf("load failed: %v", err)
	}

	if outbox3.Pending() != 0 {
		t.Fatalf("expected 0 pending after reload, got %d", outbox3.Pending())
	}
}

func TestOutboxRun_StopsOnContextCancel(t *testing.T) {
	fp := &fakePublisher{}
	outbox := testOutbox(fp, true)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		outbox.Run(ctx)
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

func TestOutboxFinalDrain_NoPending_Noop(t *testing.T) {
	fp := &fakePublisher{}
	outbox := testOutbox(fp, true)

	// finalDrain with nothing pending should return immediately without publishing.
	outbox.finalDrain()
	if fp.callCount() != 0 {
		t.Fatalf("expected 0 publish calls, got %d", fp.callCount())
	}
}

func TestOutboxFinalDrain_PublishesPending(t *testing.T) {
	fp := &fakePublisher{}
	outbox := testOutbox(fp, false)

	msg, _ := NewMessage("test.event", "payload")
	_ = outbox.Publish(context.Background(), "ex", "rk", msg)

	if outbox.Pending() != 1 {
		t.Fatalf("expected 1 pending, got %d", outbox.Pending())
	}

	// Switch to healthy so finalDrain can publish.
	outbox.healthyFn = func() bool { return true }
	outbox.finalDrain()

	if outbox.Pending() != 0 {
		t.Fatalf("expected 0 pending after final drain, got %d", outbox.Pending())
	}
	if fp.callCount() != 1 {
		t.Fatalf("expected 1 publish call, got %d", fp.callCount())
	}
}

func TestOutboxFinalDrain_UnhealthyLeavesMessages(t *testing.T) {
	fp := &fakePublisher{}
	outbox := testOutbox(fp, false)

	msg, _ := NewMessage("test.event", "payload")
	_ = outbox.Publish(context.Background(), "ex", "rk", msg)

	// finalDrain while unhealthy should not publish.
	outbox.finalDrain()

	if outbox.Pending() != 1 {
		t.Fatalf("expected 1 pending, got %d", outbox.Pending())
	}
}

func TestOutboxRun_FinalDrainOnCancel(t *testing.T) {
	fp := &fakePublisher{}
	var healthy atomic.Bool
	outbox := testOutboxWithHealthPtr(fp, &healthy)

	// Buffer a message while unhealthy.
	msg, _ := NewMessage("test.event", "payload")
	_ = outbox.Publish(context.Background(), "ex", "rk", msg)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		outbox.Run(ctx)
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
	if outbox.Pending() != 0 {
		t.Fatalf("expected 0 pending after final drain, got %d", outbox.Pending())
	}
}

func TestOutboxDrain_CancelledContext(t *testing.T) {
	fp := &fakePublisher{}
	outbox := testOutbox(fp, false)

	// Buffer 3 messages.
	for i := range 3 {
		msg, _ := NewMessage("test.event", fmt.Sprintf("m%d", i))
		_ = outbox.Publish(context.Background(), "ex", "rk", msg)
	}

	// Switch to healthy but cancel context before drain.
	outbox.healthyFn = func() bool { return true }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	outbox.drain(ctx)

	// Context is cancelled, so drain should stop without publishing.
	if outbox.Pending() != 3 {
		t.Fatalf("expected 3 pending after cancelled drain, got %d", outbox.Pending())
	}
}

func TestOutboxLoad_NoStateFile_Noop(t *testing.T) {
	outbox := newOutboxPublisher(slog.New(slog.NewTextHandler(io.Discard, nil)))
	err := outbox.load()
	if err != nil {
		t.Fatalf("expected no error for empty stateFile, got %v", err)
	}
}

func TestOutboxLoad_MissingFile_ReturnsNilPending(t *testing.T) {
	outbox := newOutboxPublisher(
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		WithOutboxStateFile(filepath.Join(t.TempDir(), "nonexistent.json")),
	)
	err := outbox.load()
	if err != nil {
		t.Fatalf("expected no error for missing file (ErrNotExist is handled), got %v", err)
	}
	if outbox.Pending() != 0 {
		t.Fatalf("expected 0 pending, got %d", outbox.Pending())
	}
}

func TestOutboxLoad_CorruptFile_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	stateFile := filepath.Join(dir, "outbox.json")
	if err := os.WriteFile(stateFile, []byte(`not valid json`), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	outbox := newOutboxPublisher(
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		WithOutboxStateFile(stateFile),
	)
	err := outbox.load()
	if err == nil {
		t.Fatal("expected error for corrupt state file")
	}
}

func TestOutboxSaveLocked_NoStateFile_Noop(t *testing.T) {
	outbox := newOutboxPublisher(slog.New(slog.NewTextHandler(io.Discard, nil)))
	// Should not panic or error with empty stateFile.
	outbox.saveLocked()
}

func TestOutboxSaveLocked_InvalidPath_LogsError(t *testing.T) {
	outbox := newOutboxPublisher(
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		WithOutboxStateFile("/nonexistent-dir/subdir/outbox.json"),
	)
	// Manually seed pending so saveLocked has something to write.
	outbox.pending = []pendingMessage{{Exchange: "ex", RoutingKey: "rk"}}
	// Should not panic even though save will fail.
	outbox.saveLocked()
}

func TestOutboxRun_DrainOnTick(t *testing.T) {
	fp := &fakePublisher{}
	var healthy atomic.Bool
	healthy.Store(true)
	outbox := testOutboxWithHealthPtr(fp, &healthy)

	// Buffer a message while healthy fails (force buffer).
	healthy.Store(false)
	msg, _ := NewMessage("test.event", "tick-drain")
	_ = outbox.Publish(context.Background(), "ex", "rk", msg)

	if outbox.Pending() != 1 {
		t.Fatalf("expected 1 pending, got %d", outbox.Pending())
	}

	healthy.Store(true)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		outbox.Run(ctx)
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
	if outbox.Pending() != 0 {
		t.Fatalf("expected 0 pending after Run drain, got %d", outbox.Pending())
	}
}
