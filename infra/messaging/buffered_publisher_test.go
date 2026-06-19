package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
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

type bufferedContextKey struct{}

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

// withStateFileAbsoluteForTest sets the resolved absolute state-file
// path directly, bypassing the [WithStateDirectory] +
// [WithStateFile] containment pair the production constructor
// enforces. Test fixtures predate that pair and assert behaviour
// against paths in t.TempDir() (or deliberately unwritable paths) by
// joining components ahead of time; this option lets the test helper
// preserve that style without forcing every test to be rewritten.
// Not part of the public surface.
func withStateFileAbsoluteForTest(path string) BufferedPublisherOption {
	return func(o *BufferedPublisher) { o.stateFile = path }
}

// newTestBufferedPublisher constructs a BufferedPublisher using the option pattern
// instead of directly setting unexported fields. It uses nil for inner/conn
// because withPublishFn and withHealthFn override the defaults before any
// nil dereference can occur.
func newTestBufferedPublisher(publishFn func(ctx context.Context, exchange, routingKey string, msg Message) error, healthFn func() bool, opts ...BufferedPublisherOption) *BufferedPublisher {
	allOpts := append([]BufferedPublisherOption{withPublishFn(publishFn), withHealthFn(healthFn)}, opts...)
	return newBufferedPublisher(discardLogger(), allOpts...)
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

func assertNotPanics(t *testing.T, fn func()) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("unexpected panic: %v", r)
		}
	}()
	fn()
}

func waitForBufferedPublisherRunStarted(t *testing.T, pub *BufferedPublisher) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		pub.runMu.Lock()
		started := pub.started
		pub.runMu.Unlock()
		if started {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("BufferedPublisher.Run did not start")
}

func TestWithFinalDrainTimeout_PanicsOnNonPositive(t *testing.T) {
	for _, d := range []time.Duration{0, -time.Second} {
		t.Run(d.String(), func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Fatal("expected WithFinalDrainTimeout to panic")
				}
			}()
			WithFinalDrainTimeout(d)
		})
	}
}

func TestWithMaxSize_PanicDoesNotReflectValue(t *testing.T) {
	defer func() {
		rec := recover()
		if rec == nil {
			t.Fatal("expected WithMaxSize to panic")
		}
		msg, ok := rec.(string)
		if !ok {
			t.Fatalf("panic must be a stable string, got %T", rec)
		}
		if strings.Contains(msg, "-1") {
			t.Fatalf("panic leaked invalid size: %q", msg)
		}
	}()
	WithMaxSize(-1)
}

// fakeConnector / fakePublisher are the minimum implementations needed
// to exercise NewBufferedPublisher's nil-dependency guards.
type fakeConnector struct{ healthy bool }

func (f *fakeConnector) Healthy() bool              { return f.healthy }
func (f *fakeConnector) Stop(context.Context) error { return nil }

type noopPublisher struct{}

func (noopPublisher) Publish(_ context.Context, _, _ string, _ Message) error {
	return nil
}

func TestNewBufferedPublisher_PanicsOnNilInner(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic, got none")
		}
	}()
	NewBufferedPublisher(nil, &fakeConnector{}, slog.Default())
}

func TestNewBufferedPublisher_PanicsOnNilConnector(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic, got none")
		}
	}()
	NewBufferedPublisher(noopPublisher{}, nil, slog.Default())
}

func TestNewBufferedPublisher_PanicsOnNilOption(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic, got none")
		}
	}()
	NewBufferedPublisher(noopPublisher{}, &fakeConnector{healthy: true}, slog.Default(), nil)
}

func TestNewBufferedPublisher_PanicsWithoutStateFile(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic when no state file is configured, got none")
		}
	}()
	NewBufferedPublisher(noopPublisher{}, &fakeConnector{healthy: true}, slog.Default())
}

func TestNewBufferedPublisher_WithStateFileOK(t *testing.T) {
	dir := t.TempDir()
	pub := NewBufferedPublisher(
		noopPublisher{}, &fakeConnector{healthy: true},
		slog.Default(),
		WithStateDirectory(dir),
		WithStateFile("buf.json"),
	)
	if pub == nil {
		t.Fatal("expected non-nil publisher")
	}
}

func TestNewBufferedPublisher_EphemeralOptOut(t *testing.T) {
	pub := NewBufferedPublisher(
		noopPublisher{}, &fakeConnector{healthy: true},
		slog.Default(),
		WithEphemeralBuffer(),
	)
	if pub == nil {
		t.Fatal("expected non-nil publisher")
	}
}

// TestNewBufferedPublisher_PanicsOnCorruptStateFile pins the v2 round-2 fix:
// a corrupt state file silently dropping buffered messages is the exact
// data-loss scenario buffering exists to prevent. Default behaviour must
// fail startup so operators see the corruption rather than a silent empty
// queue.
func TestNewBufferedPublisher_PanicsOnCorruptStateFile(t *testing.T) {
	dir := t.TempDir()
	stateFile := filepath.Join(dir, "buffered.json")
	if err := os.WriteFile(stateFile, []byte(`not valid json`), 0600); err != nil {
		t.Fatalf("setup: %v", err)
	}

	defer func() {
		rec := recover()
		if rec == nil {
			t.Fatal("expected panic on corrupt state file, got none")
		}
		msg, ok := rec.(string)
		if !ok {
			t.Fatalf("panic must be a stable string, got %T", rec)
		}
		want := "messaging: BufferedPublisher state load failed — corrupt or unreadable state would silently drop buffered messages; pass WithLossyStateRecovery() to opt in"
		if msg != want {
			t.Fatalf("panic = %q, want %q", msg, want)
		}
		if strings.Contains(msg, stateFile) {
			t.Fatalf("panic reflected state file path: %q", msg)
		}
	}()
	NewBufferedPublisher(
		noopPublisher{}, &fakeConnector{healthy: true},
		discardLogger(),
		WithStateDirectory(dir),
		WithStateFile("buffered.json"),
	)
}

// TestNewBufferedPublisher_LossyStateRecoverySwallowsCorruption pins the
// opt-in escape hatch: when the caller explicitly accepts lossy startup,
// a corrupt state file is logged and the publisher starts empty.
func TestNewBufferedPublisher_LossyStateRecoverySwallowsCorruption(t *testing.T) {
	dir := t.TempDir()
	stateFile := filepath.Join(dir, "buffered.json")
	if err := os.WriteFile(stateFile, []byte(`not valid json`), 0600); err != nil {
		t.Fatalf("setup: %v", err)
	}

	pub := NewBufferedPublisher(
		noopPublisher{}, &fakeConnector{healthy: true},
		discardLogger(),
		WithStateDirectory(dir),
		WithStateFile("buffered.json"),
		WithLossyStateRecovery(),
	)
	if pub == nil {
		t.Fatal("expected non-nil publisher")
	}
	if got := pub.Pending(); got != 0 {
		t.Fatalf("expected lossy recovery to start empty, got %d pending", got)
	}
}

// TestWithStateFile_RejectsAbsolutePath pins THREAT_MODEL §4.3 M-05:
// an absolute state-file path bypasses the containment dir and must
// fail at construction time.
func TestWithStateFile_RejectsAbsolutePath(t *testing.T) {
	defer func() {
		rec := recover()
		if rec == nil {
			t.Fatal("expected panic for absolute WithStateFile path, got none")
		}
		msg, ok := rec.(string)
		if !ok {
			t.Fatalf("panic must be a stable string, got %T", rec)
		}
		if !strings.Contains(msg, "absolute") {
			t.Fatalf("panic missing absolute-path reason: %q", msg)
		}
	}()
	NewBufferedPublisher(
		noopPublisher{}, &fakeConnector{healthy: true},
		discardLogger(),
		WithStateDirectory(t.TempDir()),
		WithStateFile("/etc/passwd"),
	)
}

// TestWithStateFile_RejectsTraversal pins THREAT_MODEL §4.3 M-05:
// a `..` segment that would escape the configured directory is
// rejected before any disk write.
func TestWithStateFile_RejectsTraversal(t *testing.T) {
	defer func() {
		rec := recover()
		if rec == nil {
			t.Fatal("expected panic for traversal WithStateFile path, got none")
		}
		msg, ok := rec.(string)
		if !ok {
			t.Fatalf("panic must be a stable string, got %T", rec)
		}
		if !strings.Contains(msg, "escape") && !strings.Contains(msg, "parent") {
			t.Fatalf("panic missing escape reason: %q", msg)
		}
	}()
	NewBufferedPublisher(
		noopPublisher{}, &fakeConnector{healthy: true},
		discardLogger(),
		WithStateDirectory(t.TempDir()),
		WithStateFile("../escape.json"),
	)
}

// TestWithStateFile_RejectsAbsoluteEscape pins THREAT_MODEL §4.3
// M-05: an absolute path targeting a privileged location is the
// hostile-env scenario the containment guard exists to neutralise.
func TestWithStateFile_RejectsAbsoluteEscape(t *testing.T) {
	defer func() {
		rec := recover()
		if rec == nil {
			t.Fatal("expected panic for absolute escape WithStateFile path, got none")
		}
	}()
	NewBufferedPublisher(
		noopPublisher{}, &fakeConnector{healthy: true},
		discardLogger(),
		WithStateDirectory(t.TempDir()),
		WithStateFile("/etc/passwd"),
	)
}

// TestWithStateFile_RequiresStateDirectory pins the fail-fast
// invariant: callers that forget [WithStateDirectory] get a clear
// panic at construction time, not a silent fallback to "anywhere the
// process can write".
func TestWithStateFile_RequiresStateDirectory(t *testing.T) {
	defer func() {
		rec := recover()
		if rec == nil {
			t.Fatal("expected panic when WithStateFile lacks WithStateDirectory, got none")
		}
		msg, ok := rec.(string)
		if !ok {
			t.Fatalf("panic must be a stable string, got %T", rec)
		}
		if !strings.Contains(msg, "WithStateDirectory") {
			t.Fatalf("panic missing WithStateDirectory reason: %q", msg)
		}
	}()
	NewBufferedPublisher(
		noopPublisher{}, &fakeConnector{healthy: true},
		discardLogger(),
		WithStateFile("state.json"),
	)
}

// TestWithStateFile_AcceptsCleanRelative pins the happy path:
// a base-name resolves under the configured directory.
func TestWithStateFile_AcceptsCleanRelative(t *testing.T) {
	dir := t.TempDir()
	pub := NewBufferedPublisher(
		noopPublisher{}, &fakeConnector{healthy: true},
		discardLogger(),
		WithStateDirectory(dir),
		WithStateFile("state.json"),
	)
	if pub == nil {
		t.Fatal("expected non-nil publisher")
	}
	want := filepath.Join(dir, "state.json")
	if pub.stateFile != want {
		t.Fatalf("stateFile = %q, want %q", pub.stateFile, want)
	}
}

// TestWithStateFile_AcceptsNestedRelative pins the multi-segment
// happy path: nested relative components stay inside the directory.
func TestWithStateFile_AcceptsNestedRelative(t *testing.T) {
	dir := t.TempDir()
	pub := NewBufferedPublisher(
		noopPublisher{}, &fakeConnector{healthy: true},
		discardLogger(),
		WithStateDirectory(dir),
		WithStateFile("sub/state.json"),
	)
	if pub == nil {
		t.Fatal("expected non-nil publisher")
	}
	want := filepath.Join(dir, "sub", "state.json")
	if pub.stateFile != want {
		t.Fatalf("stateFile = %q, want %q", pub.stateFile, want)
	}
}

// TestWithStateDirectory_RejectsRelative pins the dual constraint:
// the directory itself must be absolute so containment compares
// like-for-like and doesn't pick up the process's working directory.
func TestWithStateDirectory_RejectsRelative(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for relative WithStateDirectory, got none")
		}
	}()
	WithStateDirectory("relative/dir")
}

// TestWithStateDirectory_RejectsEmpty pins the empty-string panic so
// a configuration mistake (env var unset, hard-coded "") fails loud
// instead of silently disabling persistence.
func TestWithStateDirectory_RejectsEmpty(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for empty WithStateDirectory, got none")
		}
	}()
	WithStateDirectory("")
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

func TestBufferedPublisher_BuffersMessageClone(t *testing.T) {
	fp := &fakePublisher{}
	pub := testBufferedPublisher(fp, false)
	msg := Message{
		ID:      "msg-1",
		Type:    "test.event",
		Payload: []byte(`{"key":"value"}`),
		Headers: map[string]string{"X-Trace-Id": "trace-1"},
	}

	if err := pub.Publish(context.Background(), "exchange", "routing.key", msg); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	msg.Payload[8] = 'X'
	msg.Headers["X-Trace-Id"] = "mutated"

	pub.mu.Lock()
	stored := pub.pending[0].Msg
	pub.mu.Unlock()
	if string(stored.Payload) != `{"key":"value"}` {
		t.Fatalf("buffered payload = %s, want original", stored.Payload)
	}
	if stored.Headers["X-Trace-Id"] != "trace-1" {
		t.Fatalf("buffered header = %q, want original", stored.Headers["X-Trace-Id"])
	}
}

func TestBufferedPublisher_InvalidHeadersRejectedBeforeDirectOrBuffer(t *testing.T) {
	fp := &fakePublisher{}
	pub := testBufferedPublisher(fp, false)
	msg := Message{
		ID:      "msg-1",
		Type:    "test.event",
		Payload: []byte(`{}`),
		Headers: map[string]string{"Bad Header": "value"},
	}

	err := pub.Publish(context.Background(), "exchange", "routing.key", msg)
	if !errors.Is(err, ErrInvalidMessageHeader) {
		t.Fatalf("expected ErrInvalidMessageHeader, got %v", err)
	}
	if fp.callCount() != 0 {
		t.Fatalf("expected no direct publish, got %d calls", fp.callCount())
	}
	if pub.Pending() != 0 {
		t.Fatalf("expected invalid message not to buffer, got %d pending", pub.Pending())
	}
}

func TestBufferedPublisher_InvalidMessageRejectedBeforeDirectOrBuffer(t *testing.T) {
	fp := &fakePublisher{}
	pub := testBufferedPublisher(fp, false)
	msg := Message{
		ID:      "msg-1",
		Type:    "bad\nevent",
		Payload: []byte(`{}`),
	}

	err := pub.Publish(context.Background(), "exchange", "routing.key", msg)
	if !errors.Is(err, ErrInvalidMessage) {
		t.Fatalf("expected ErrInvalidMessage, got %v", err)
	}
	if fp.callCount() != 0 {
		t.Fatalf("expected no direct publish, got %d calls", fp.callCount())
	}
	if pub.Pending() != 0 {
		t.Fatalf("expected invalid message not to buffer, got %d pending", pub.Pending())
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
	pub := testBufferedPublisher(fp, false, WithMaxSize(2))

	msg1, _ := NewMessage("test.event", "m1")
	msg2, _ := NewMessage("test.event", "m2")
	msg3, _ := NewMessage("test.event", "m3")

	_ = pub.Publish(context.Background(), "ex", "rk", msg1)
	_ = pub.Publish(context.Background(), "ex", "rk", msg2)

	err := pub.Publish(context.Background(), "ex", "rk", msg3)
	if err == nil {
		t.Fatal("expected error when buffer full")
	}
	if !errors.Is(err, ErrBufferFull) {
		t.Fatalf("buffer-full drop must be errors.Is(ErrBufferFull) so callers can shed load programmatically, got %v", err)
	}
	if strings.Contains(err.Error(), "2 messages") {
		t.Fatalf("buffer full error leaked configured size: %v", err)
	}

	if pub.Pending() != 2 {
		t.Fatalf("expected 2 pending, got %d", pub.Pending())
	}
}

// TestBufferedPublisher_BufferFullAfterDirectFailure exercises the second
// drop path: a direct publish fails, then the post-failure capacity re-check
// finds the buffer full and drops the message. It must also carry the
// ErrBufferFull sentinel.
func TestBufferedPublisher_BufferFullAfterDirectFailure(t *testing.T) {
	// Publisher always fails the direct publish so the failure-path append
	// is taken; maxSize=1 with one buffered message means the re-check drops.
	fp := &fakePublisher{failUntil: 1000}
	var healthy atomic.Bool
	healthy.Store(true)
	pub := testBufferedPublisherWithHealthPtr(fp, &healthy, WithMaxSize(1))

	// First publish: direct fails, buffer has room (0 -> 1).
	msg1, _ := NewMessage("test.event", "m1")
	if err := pub.Publish(context.Background(), "ex", "rk", msg1); err != nil {
		t.Fatalf("first publish should buffer after direct failure, got %v", err)
	}
	if pub.Pending() != 1 {
		t.Fatalf("expected 1 pending after direct failure, got %d", pub.Pending())
	}

	// Second publish: buffer already at capacity. Whether it takes the
	// direct-failure re-check drop or the reserved-slot drop, the result
	// must be the sentinel.
	msg2, _ := NewMessage("test.event", "m2")
	err := pub.Publish(context.Background(), "ex", "rk", msg2)
	if !errors.Is(err, ErrBufferFull) {
		t.Fatalf("over-capacity drop must be errors.Is(ErrBufferFull), got %v", err)
	}
}

func TestBufferedPublisher_MaxMessageBytesRejectsBeforeBuffering(t *testing.T) {
	fp := &fakePublisher{}
	pub := testBufferedPublisher(fp, false,
		WithMaxMessageBytes(32),
	)
	msg := Message{
		ID:      "msg-1",
		Type:    "large.event",
		Payload: json.RawMessage(`"this payload is intentionally too large"`),
	}

	err := pub.Publish(context.Background(), "events", "large.event", msg)

	if !errors.Is(err, ErrMessageTooLarge) {
		t.Fatalf("expected ErrMessageTooLarge, got %v", err)
	}
	if pending := pub.Pending(); pending != 0 {
		t.Fatalf("expected no buffered messages, got %d", pending)
	}
}

func TestBufferedPublisher_MetricCallbacksRecoverOutsideLock(t *testing.T) {
	fp := &fakePublisher{}
	var healthy atomic.Bool
	var pub *BufferedPublisher
	metrics := &BufferedPublisherMetrics{
		OnDirectPublish: func() { panic("direct metric exploded") },
		OnBuffer:        func() { panic("buffer metric exploded") },
		OnDrain:         func(int) { panic("drain metric exploded") },
		OnDrop:          func() { panic("drop metric exploded") },
		OnPendingGauge: func(int) {
			// This would deadlock if the gauge hook were still called
			// while BufferedPublisher.mu is held.
			_ = pub.Pending()
			panic("gauge metric exploded")
		},
	}
	pub = testBufferedPublisherWithHealthPtr(fp, &healthy,
		WithMaxSize(1),
		WithMetrics(metrics),
	)

	msg1, _ := NewMessage("test.event", "m1")
	done := make(chan error, 1)
	go func() {
		done <- pub.Publish(context.Background(), "ex", "rk", msg1)
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Publish returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Publish deadlocked in metric callback")
	}
	if got := pub.Pending(); got != 1 {
		t.Fatalf("expected 1 pending, got %d", got)
	}

	healthy.Store(true)
	assertNotPanics(t, func() { pub.drain(context.Background()) })
	if got := pub.Pending(); got != 0 {
		t.Fatalf("expected drain to clear pending messages, got %d", got)
	}

	msg2, _ := NewMessage("test.event", "m2")
	assertNotPanics(t, func() {
		if err := pub.Publish(context.Background(), "ex", "rk", msg2); err != nil {
			t.Fatalf("direct Publish returned error: %v", err)
		}
	})

	healthy.Store(false)
	msg3, _ := NewMessage("test.event", "m3")
	msg4, _ := NewMessage("test.event", "m4")
	if err := pub.Publish(context.Background(), "ex", "rk", msg3); err != nil {
		t.Fatalf("buffer Publish returned error: %v", err)
	}
	assertNotPanics(t, func() {
		if err := pub.Publish(context.Background(), "ex", "rk", msg4); err == nil {
			t.Fatal("expected buffer-full error")
		}
	})
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
	pub := testBufferedPublisher(fp, false, withStateFileAbsoluteForTest(stateFile))

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
	pub2 := newTestBufferedPublisher(fp2.publish, func() bool { return true }, withStateFileAbsoluteForTest(stateFile))
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

func TestBufferedPublisherPersistence_LoadPreservesEmptyRoutingKey(t *testing.T) {
	dir := t.TempDir()
	stateFile := filepath.Join(dir, "buffered.json")

	fp := &fakePublisher{}
	pub := testBufferedPublisher(fp, false, withStateFileAbsoluteForTest(stateFile))

	msg, _ := NewMessage("test.event", "payload")
	if err := pub.Publish(context.Background(), "fanout-exchange", "", msg); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if pub.Pending() != 1 {
		t.Fatalf("expected 1 pending, got %d", pub.Pending())
	}

	fp2 := &fakePublisher{}
	pub2 := newTestBufferedPublisher(fp2.publish, func() bool { return true }, withStateFileAbsoluteForTest(stateFile))
	if err := pub2.load(); err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if pub2.Pending() != 1 {
		t.Fatalf("expected empty-routing-key message to survive load, got %d pending", pub2.Pending())
	}

	pub2.drain(context.Background())
	if fp2.callCount() != 1 {
		t.Fatalf("expected restored message to publish once, got %d calls", fp2.callCount())
	}
	fp2.mu.Lock()
	gotRoutingKey := fp2.calls[0].RoutingKey
	fp2.mu.Unlock()
	if gotRoutingKey != "" {
		t.Fatalf("expected empty routing key after restart, got %q", gotRoutingKey)
	}
}

func TestBufferedPublisherPersistence_DrainClearsFile(t *testing.T) {
	dir := t.TempDir()
	stateFile := filepath.Join(dir, "buffered.json")

	fp := &fakePublisher{}
	var healthy atomic.Bool
	pub := testBufferedPublisherWithHealthPtr(fp, &healthy, withStateFileAbsoluteForTest(stateFile))

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
	pub3 := newTestBufferedPublisher(fp.publish, func() bool { return true }, withStateFileAbsoluteForTest(stateFile))
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

	done := make(chan error, 1)
	go func() {
		done <- pub.Run(ctx)
	}()

	time.Sleep(10 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned unexpected error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop after context cancel")
	}
}

func TestBufferedPublisherRun_RejectsNilContext(t *testing.T) {
	fp := &fakePublisher{}
	pub := testBufferedPublisher(fp, true)
	var ctx context.Context
	err := pub.Run(ctx)
	if err == nil || !strings.Contains(err.Error(), "non-nil context") {
		t.Fatalf("expected nil context error, got %v", err)
	}
}

func TestBufferedPublisherRun_RejectsInvalidPublisher(t *testing.T) {
	var nilPub *BufferedPublisher
	if err := nilPub.Run(context.Background()); !errors.Is(err, ErrInvalidPublisher) {
		t.Fatalf("expected ErrInvalidPublisher for nil receiver, got %v", err)
	}

	if err := (&BufferedPublisher{}).Run(context.Background()); !errors.Is(err, ErrInvalidPublisher) {
		t.Fatalf("expected ErrInvalidPublisher for uninitialized publisher, got %v", err)
	}
}

func TestBufferedPublisherRun_RejectsSecondStart(t *testing.T) {
	fp := &fakePublisher{}
	pub := testBufferedPublisher(fp, true)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- pub.Run(ctx) }()
	waitForBufferedPublisherRunStarted(t, pub)

	err := pub.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "already started") {
		t.Fatalf("expected duplicate start error, got %v", err)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("first Run returned unexpected error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first Run did not stop")
	}
}

func TestBufferedPublisherRun_RejectsRestartAfterCancel(t *testing.T) {
	fp := &fakePublisher{}
	pub := testBufferedPublisher(fp, true)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- pub.Run(ctx) }()
	waitForBufferedPublisherRunStarted(t, pub)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("first Run returned unexpected error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first Run did not stop")
	}

	err := pub.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "already started") {
		t.Fatalf("expected restart error, got %v", err)
	}
}

func TestBufferedPublisherFinalDrain_NoPending_Noop(t *testing.T) {
	fp := &fakePublisher{}
	pub := testBufferedPublisher(fp, true)

	// finalDrain with nothing pending should return immediately without publishing.
	pub.finalDrain(context.Background())
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
	pub.finalDrain(context.Background())

	if pub.Pending() != 0 {
		t.Fatalf("expected 0 pending after final drain, got %d", pub.Pending())
	}
	if fp.callCount() != 1 {
		t.Fatalf("expected 1 publish call, got %d", fp.callCount())
	}
}

func TestBufferedPublisherFinalDrainPreservesContextValuesAfterCancellation(t *testing.T) {
	var gotValue any
	var gotErr error
	var healthy atomic.Bool
	pub := newTestBufferedPublisher(
		func(ctx context.Context, _, _ string, _ Message) error {
			gotValue = ctx.Value(bufferedContextKey{})
			gotErr = ctx.Err()
			return nil
		},
		func() bool { return healthy.Load() },
	)

	msg, _ := NewMessage("test.event", "payload")
	if err := pub.Publish(context.Background(), "ex", "rk", msg); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	healthy.Store(true)

	parent := context.WithValue(context.Background(), bufferedContextKey{}, "trace-123")
	ctx, cancel := context.WithCancel(parent)
	cancel()
	pub.finalDrain(ctx)

	if pub.Pending() != 0 {
		t.Fatalf("expected 0 pending after final drain, got %d", pub.Pending())
	}
	if gotValue != "trace-123" {
		t.Fatalf("final-drain context value = %v, want trace-123", gotValue)
	}
	if gotErr != nil {
		t.Fatalf("final-drain context inherited cancellation: %v", gotErr)
	}
}

func TestBufferedPublisherFinalDrain_UnhealthyLeavesMessages(t *testing.T) {
	fp := &fakePublisher{}
	pub := testBufferedPublisher(fp, false)

	msg, _ := NewMessage("test.event", "payload")
	_ = pub.Publish(context.Background(), "ex", "rk", msg)

	// finalDrain while unhealthy should not publish.
	pub.finalDrain(context.Background())

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
		if err := pub.Run(ctx); err != nil {
			t.Errorf("Run returned unexpected error: %v", err)
		}
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
	pub := newBufferedPublisher(discardLogger())
	err := pub.load()
	if err != nil {
		t.Fatalf("expected no error for empty stateFile, got %v", err)
	}
}

func TestBufferedPublisherLoad_MissingFile_ReturnsNilPending(t *testing.T) {
	pub := newBufferedPublisher(
		discardLogger(),
		withStateFileAbsoluteForTest(filepath.Join(t.TempDir(), "nonexistent.json")),
	)
	err := pub.load()
	if err != nil {
		t.Fatalf("expected no error for missing file (ErrNotExist is handled), got %v", err)
	}
	if pub.Pending() != 0 {
		t.Fatalf("expected 0 pending, got %d", pub.Pending())
	}
}

func TestBufferedPublisherLoad_StrictByDefault_FailsOnInvalidEntry(t *testing.T) {
	// Wave 66: load() is strict by default — any invalid entry is
	// fatal. The previous test asserted "silently skips" which was
	// the bug codex flagged. Strict-by-default forces operators to
	// confront corrupt state.
	dir := t.TempDir()
	stateFile := filepath.Join(dir, "buffered.json")
	pending := []pendingMessage{
		{Exchange: "events", RoutingKey: "ok", Msg: Message{ID: "msg-1", Type: "ok.event", Payload: json.RawMessage(`{}`)}},
		{Exchange: "events", RoutingKey: "bad", Msg: Message{ID: "msg-2", Type: "bad event", Payload: json.RawMessage(`{}`)}},
	}
	data, err := json.Marshal(pending)
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	if err := os.WriteFile(stateFile, data, 0600); err != nil {
		t.Fatalf("write state: %v", err)
	}
	pub := newBufferedPublisher(
		discardLogger(),
		withStateFileAbsoluteForTest(stateFile),
	)
	if err := pub.load(); err == nil {
		t.Fatalf("expected strict load to fail on invalid entry, got nil")
	}
}

func TestBufferedPublisherLoad_LossyStateValidationSkipsInvalidEntries(t *testing.T) {
	dir := t.TempDir()
	stateFile := filepath.Join(dir, "buffered.json")
	pending := []pendingMessage{
		{Exchange: "events", RoutingKey: "ok", Msg: Message{ID: "msg-1", Type: "ok.event", Payload: json.RawMessage(`{}`)}},
		{Exchange: "events", RoutingKey: "bad", Msg: Message{ID: "msg-2", Type: "bad event", Payload: json.RawMessage(`{}`)}},
	}
	data, err := json.Marshal(pending)
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	if err := os.WriteFile(stateFile, data, 0600); err != nil {
		t.Fatalf("write state: %v", err)
	}
	pub := newBufferedPublisher(
		discardLogger(),
		withStateFileAbsoluteForTest(stateFile),
		WithLossyStateValidation(),
	)
	if err := pub.load(); err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if pub.Pending() != 1 {
		t.Fatalf("expected 1 valid pending message, got %d", pub.Pending())
	}
}

func TestBufferedPublisherLoad_CorruptFile_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	stateFile := filepath.Join(dir, "buffered.json")
	if err := os.WriteFile(stateFile, []byte(`not valid json`), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	pub := newBufferedPublisher(
		discardLogger(),
		withStateFileAbsoluteForTest(stateFile),
	)
	err := pub.load()
	if err == nil {
		t.Fatal("expected error for corrupt state file")
	}
}

func TestBufferedPublisherSaveLocked_NoStateFile_Noop(t *testing.T) {
	pub := newBufferedPublisher(discardLogger())
	if err := pub.saveLocked(); err != nil {
		t.Fatalf("expected nil with empty stateFile, got %v", err)
	}
}

func TestBufferedPublisherSaveLocked_InvalidPath_LogsError(t *testing.T) {
	pub := newBufferedPublisher(
		discardLogger(),
		withStateFileAbsoluteForTest("/nonexistent-dir/subdir/buffered.json"),
	)
	pub.pending = []pendingMessage{{Exchange: "ex", RoutingKey: "rk"}}
	if err := pub.saveLocked(); err == nil {
		t.Fatal("expected error from saveLocked with invalid path")
	}
}

func TestBufferedPublisherLogsRedactRuntimeIdentifiers(t *testing.T) {
	var buf strings.Builder
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	pub := newTestBufferedPublisher(
		func(context.Context, string, string, Message) error {
			return errors.New("broker failed for tenant-secret-route")
		},
		func() bool { return true },
		withStateFileAbsoluteForTest(filepath.Join(t.TempDir(), "buffered.json")),
	)
	pub.logger = logger

	msg, err := NewMessage("tenant.secret.type", map[string]string{"ok": "true"})
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	msg.ID = "tenant-secret-message-id"

	err = pub.Publish(context.Background(), "tenant-secret-exchange", "tenant-secret-routing", msg)
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}

	rendered := buf.String()
	for _, forbidden := range []string{
		"tenant-secret-route",
		"tenant.secret.type",
		"tenant-secret-message-id",
		"tenant-secret-exchange",
		"tenant-secret-routing",
	} {
		if strings.Contains(rendered, forbidden) {
			t.Fatalf("log leaked %q in %s", forbidden, rendered)
		}
	}
	for _, expected := range []string{"msg_id=\"<redacted", "exchange=\"<redacted", "routing_key=\"<redacted"} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("log missing %q in %s", expected, rendered)
		}
	}
}

func TestBufferedPublisherStateSaveLogRedactsPath(t *testing.T) {
	var buf strings.Builder
	stateFile := "/nonexistent-dir/tenant-secret-buffered.json"
	pub := newBufferedPublisher(
		slog.New(slog.NewTextHandler(&buf, nil)),
		withStateFileAbsoluteForTest(stateFile),
	)
	pub.pending = []pendingMessage{{Exchange: "ex", RoutingKey: "rk"}}

	if err := pub.saveLocked(); err == nil {
		t.Fatal("expected saveLocked error")
	}

	rendered := buf.String()
	for _, forbidden := range []string{stateFile, "tenant-secret-buffered"} {
		if strings.Contains(rendered, forbidden) {
			t.Fatalf("log leaked %q in %s", forbidden, rendered)
		}
	}
	if !strings.Contains(rendered, "file=\"<redacted") {
		t.Fatalf("log missing redacted file attr: %s", rendered)
	}
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
		if err := pub.Run(ctx); err != nil {
			t.Errorf("Run returned unexpected error: %v", err)
		}
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

func TestBufferedPublisher_PersistFailureSurfacesError(t *testing.T) {
	pub := newTestBufferedPublisher(
		(&fakePublisher{}).publish,
		func() bool { return false },
		withStateFileAbsoluteForTest("/nonexistent-dir/subdir/buffered.json"),
	)

	msg, _ := NewMessage("test.event", "payload")
	err := pub.Publish(context.Background(), "ex", "rk", msg)
	if err == nil {
		t.Fatal("expected Publish to surface persistence failure")
	}
	if pub.Pending() != 0 {
		t.Fatalf("expected rollback to leave 0 pending, got %d", pub.Pending())
	}
}

func TestBufferedPublisher_ContextAndRouteRejectedBeforeBuffer(t *testing.T) {
	pub := newTestBufferedPublisher(
		(&fakePublisher{}).publish,
		func() bool { return false },
		WithEphemeralBuffer(),
	)
	msg, _ := NewMessage("test.event", "payload")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := pub.Publish(ctx, "ex", "rk", msg)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if pub.Pending() != 0 {
		t.Fatalf("expected cancelled publish to leave 0 pending, got %d", pub.Pending())
	}

	err = pub.Publish(context.Background(), "ex", "bad key", msg)
	if !errors.Is(err, ErrInvalidRoute) {
		t.Fatalf("expected ErrInvalidRoute, got %v", err)
	}
	if pub.Pending() != 0 {
		t.Fatalf("expected invalid route to leave 0 pending, got %d", pub.Pending())
	}
}

func TestBufferedPublisher_LossyModeSwallowsPersistFailure(t *testing.T) {
	pub := newTestBufferedPublisher(
		(&fakePublisher{}).publish,
		func() bool { return false },
		withStateFileAbsoluteForTest("/nonexistent-dir/subdir/buffered.json"),
		WithLossyMode(),
	)

	msg, _ := NewMessage("test.event", "payload")
	if err := pub.Publish(context.Background(), "ex", "rk", msg); err != nil {
		t.Fatalf("expected lossy mode to swallow persistence failure, got %v", err)
	}
	if pub.Pending() != 1 {
		t.Fatalf("expected 1 pending in lossy mode, got %d", pub.Pending())
	}
}

func TestBufferedPublisher_PersistFailureAfterDirectPublishFail(t *testing.T) {
	fp := &fakePublisher{failUntil: 1}
	pub := newTestBufferedPublisher(
		fp.publish,
		func() bool { return true },
		withStateFileAbsoluteForTest("/nonexistent-dir/subdir/buffered.json"),
	)

	msg, _ := NewMessage("test.event", "payload")
	err := pub.Publish(context.Background(), "ex", "rk", msg)
	if err == nil {
		t.Fatal("expected Publish to surface persistence failure on buffering after direct publish failed")
	}
	if pub.Pending() != 0 {
		t.Fatalf("expected rollback to leave 0 pending, got %d", pub.Pending())
	}
	if pub.directInFlight {
		t.Fatal("directInFlight must be cleared after error rollback")
	}
}

// TestBufferedPublisherDrain_SaveErrorFiresHookAndLastSaveError pins
// the FR-068 [HIGH] fix: when drain successfully publishes a batch
// to the broker but the subsequent state-file save fails, the
// publisher must surface the error via OnSaveError and LastSaveError
// instead of silently swallowing it. Pre-fix `_ = o.saveLocked()`
// in drain hid disk-full / EROFS / quota conditions, leaving the
// on-disk pending list stale and creating a duplicate-replay risk
// on the next process crash.
func TestBufferedPublisherDrain_SaveErrorFiresHookAndLastSaveError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("chmod-based failure injection is bypassed when running as root")
	}
	dir := t.TempDir()
	stateFile := filepath.Join(dir, "buffered.json")

	var saveErrors atomic.Int32
	var lastErr atomic.Pointer[error]
	metrics := &BufferedPublisherMetrics{
		OnSaveError: func(err error) {
			saveErrors.Add(1)
			lastErr.Store(&err)
		},
	}

	fp := &fakePublisher{}
	healthy := &atomic.Bool{}
	pub := testBufferedPublisherWithHealthPtr(fp, healthy,
		withStateFileAbsoluteForTest(stateFile),
		WithMetrics(metrics),
	)

	// Buffer two messages while the broker is unhealthy. These calls
	// successfully save (dir is writable).
	for i := range 2 {
		msg, _ := NewMessage("test.event", fmt.Sprintf("m%d", i))
		if err := pub.Publish(context.Background(), "ex", "rk", msg); err != nil {
			t.Fatalf("publish %d: %v", i, err)
		}
	}
	if pub.Pending() != 2 {
		t.Fatalf("expected 2 pending, got %d", pub.Pending())
	}

	// Now break the directory so saveLocked() fails — atomicfile.Save
	// writes a temp file in the same dir and renames; chmod 0500
	// prevents temp-file creation.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	// Become healthy and drain. publishFn succeeds (in-memory
	// fakePublisher), then saveLocked fails because the dir is locked
	// down.
	healthy.Store(true)
	pub.drain(context.Background())

	if got := fp.callCount(); got != 2 {
		t.Errorf("expected 2 broker publishes, got %d", got)
	}
	if got := saveErrors.Load(); got != 1 {
		t.Errorf("OnSaveError fired %d times, want 1 (FR-068 regression)", got)
	}
	if pub.LastSaveError() == nil {
		t.Error("LastSaveError() must reflect the post-drain save failure")
	}
	if errPtr := lastErr.Load(); errPtr == nil || *errPtr == nil {
		t.Error("OnSaveError invoked with nil error")
	}
}

// TestBufferedPublisherPersistence_PreservesHeaders verifies that
// transport headers survive the save/load round-trip. Message.Headers
// is tagged json:"-", so a naive json.Marshal of the pending entry drops
// correlation/request/tenant headers; after crash+restart drain would
// then republish messages with nil Headers, silently losing metadata.
func TestBufferedPublisherPersistence_PreservesHeaders(t *testing.T) {
	dir := t.TempDir()
	stateFile := filepath.Join(dir, "buffered.json")

	fp := &fakePublisher{}
	pub := testBufferedPublisher(fp, false, withStateFileAbsoluteForTest(stateFile))

	msg := Message{
		ID:      "msg-h1",
		Type:    "test.event",
		Payload: []byte(`{"k":"v"}`),
		Headers: map[string]string{
			HeaderCorrelationID: "corr-123",
			HeaderRequestID:     "req-456",
			"X-Tenant-Id":       "tenant-789",
		},
	}
	if err := pub.Publish(context.Background(), "ex", "rk", msg); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if pub.Pending() != 1 {
		t.Fatalf("expected 1 pending, got %d", pub.Pending())
	}

	// Simulate process restart: load from the same state file.
	var headerSeen map[string]string
	pub2 := newTestBufferedPublisher(
		func(_ context.Context, _, _ string, m Message) error {
			headerSeen = m.Headers
			return nil
		},
		func() bool { return true },
		withStateFileAbsoluteForTest(stateFile),
	)
	if err := pub2.load(); err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if pub2.Pending() != 1 {
		t.Fatalf("expected 1 pending after load, got %d", pub2.Pending())
	}

	pub2.drain(context.Background())

	if headerSeen == nil {
		t.Fatal("restored message lost all headers across save/load")
	}
	for k, want := range msg.Headers {
		if got := headerSeen[k]; got != want {
			t.Errorf("header %q after restart = %q, want %q", k, got, want)
		}
	}
}

// TestBufferedPublisherDrain_DrainsBeyondBatchLimit verifies that a
// single drain() invocation publishes ALL pending messages while the
// broker stays healthy, not just one bufferedDrainBatchLimit batch.
// Without an internal batch loop, a sustained inflow above ~one batch
// per drain interval prevents the buffer from ever reaching zero, so
// direct mode never resumes and the buffer death-spirals to capacity.
func TestBufferedPublisherDrain_DrainsBeyondBatchLimit(t *testing.T) {
	fp := &fakePublisher{}
	var healthy atomic.Bool
	pub := testBufferedPublisherWithHealthPtr(fp, &healthy)

	const total = bufferedDrainBatchLimit*2 + 5
	for i := range total {
		msg, _ := NewMessage("test.event", fmt.Sprintf("m%d", i))
		_ = pub.Publish(context.Background(), "exchange", "routing.key", msg)
	}
	if pub.Pending() != total {
		t.Fatalf("expected %d pending, got %d", total, pub.Pending())
	}

	healthy.Store(true)
	pub.drain(context.Background())

	if pub.Pending() != 0 {
		t.Fatalf("expected 0 pending after single healthy drain, got %d", pub.Pending())
	}
	if fp.callCount() != total {
		t.Fatalf("expected %d publish calls, got %d", total, fp.callCount())
	}
}

// TestBufferedPublisherFinalDrain_DrainsBeyondBatchLimit verifies that
// the shutdown final drain empties the whole buffer when the broker is
// healthy and the timeout budget allows, rather than leaving pending-100
// messages unsent (silent loss with ephemeral buffers).
func TestBufferedPublisherFinalDrain_DrainsBeyondBatchLimit(t *testing.T) {
	fp := &fakePublisher{}
	pub := testBufferedPublisher(fp, false)

	const total = bufferedDrainBatchLimit*2 + 5
	for i := range total {
		msg, _ := NewMessage("test.event", fmt.Sprintf("m%d", i))
		_ = pub.Publish(context.Background(), "exchange", "routing.key", msg)
	}
	if pub.Pending() != total {
		t.Fatalf("expected %d pending, got %d", total, pub.Pending())
	}

	// Become healthy so finalDrain can publish.
	pub.healthyFn = func() bool { return true }
	pub.finalDrain(context.Background())

	if pub.Pending() != 0 {
		t.Fatalf("expected 0 pending after final drain, got %d", pub.Pending())
	}
	if fp.callCount() != total {
		t.Fatalf("expected %d publish calls, got %d", total, fp.callCount())
	}
}

// countJournalLines returns the number of non-empty newline-delimited lines
// in the state file. A freshly-compacted snapshot is exactly one line; each
// append adds one more. Tests use it to assert the on-disk shape (O(1) append
// vs full rewrite) without pinning the exact JSON bytes.
func countJournalLines(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read state file: %v", err)
	}
	count := 0
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count
}

// TestBufferedPublisherJournal_AppendsInsteadOfRewriting pins the core fix:
// each buffered Publish appends ONE journal line instead of rewriting the whole
// snapshot. After N buffered publishes (well under the compaction floor) the
// file is exactly the snapshot line plus one line per subsequent message.
func TestBufferedPublisherJournal_AppendsInsteadOfRewriting(t *testing.T) {
	dir := t.TempDir()
	stateFile := filepath.Join(dir, "buffered.json")

	fp := &fakePublisher{}
	pub := testBufferedPublisher(fp, false, withStateFileAbsoluteForTest(stateFile))

	const n = 5
	for i := range n {
		msg, _ := NewMessage("test.event", fmt.Sprintf("m%d", i))
		if err := pub.Publish(context.Background(), "ex", "rk", msg); err != nil {
			t.Fatalf("publish %d: %v", i, err)
		}
	}

	// First publish writes a one-entry snapshot line; the remaining n-1 each
	// append exactly one line. Total lines = 1 (snapshot) + (n-1) appends = n.
	if got := countJournalLines(t, stateFile); got != n {
		t.Fatalf("journal line count = %d, want %d (1 snapshot + %d appends)", got, n, n-1)
	}
}

// TestBufferedPublisherJournal_RestartReplaysAllInOrder simulates a broker
// outage burst followed by a process restart: publish N entries, then build a
// brand-new publisher over the SAME state path and assert all N are restored in
// FIFO order. This is the strict-durability contract the journal must preserve.
func TestBufferedPublisherJournal_RestartReplaysAllInOrder(t *testing.T) {
	dir := t.TempDir()
	stateFile := filepath.Join(dir, "buffered.json")

	fp := &fakePublisher{}
	pub := testBufferedPublisher(fp, false, withStateFileAbsoluteForTest(stateFile))

	const n = 10
	want := make([]string, n)
	for i := range n {
		msg, _ := NewMessage("test.event", fmt.Sprintf("payload-%d", i))
		want[i] = msg.ID
		if err := pub.Publish(context.Background(), "ex", "rk", msg); err != nil {
			t.Fatalf("publish %d: %v", i, err)
		}
	}
	if pub.Pending() != n {
		t.Fatalf("expected %d pending, got %d", n, pub.Pending())
	}

	// Restart: new publisher, same file.
	fp2 := &fakePublisher{}
	pub2 := newTestBufferedPublisher(fp2.publish, func() bool { return true }, withStateFileAbsoluteForTest(stateFile))
	if err := pub2.load(); err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if pub2.Pending() != n {
		t.Fatalf("expected %d pending after restart, got %d", n, pub2.Pending())
	}

	pub2.drain(context.Background())
	if pub2.Pending() != 0 {
		t.Fatalf("expected 0 pending after drain, got %d", pub2.Pending())
	}

	fp2.mu.Lock()
	defer fp2.mu.Unlock()
	if len(fp2.calls) != n {
		t.Fatalf("expected %d republished, got %d", n, len(fp2.calls))
	}
	for i, want := range want {
		if got := fp2.calls[i].MsgID; got != want {
			t.Fatalf("replay order mismatch at %d: got %q, want %q", i, got, want)
		}
	}
}

// TestBufferedPublisherJournal_BackwardCompatOldSnapshot pins the in-flight
// upgrade contract: a state file written in the OLD single-array format (what
// the pre-journal code persisted) must restore intact, and subsequent publishes
// plus a drain must produce a consistent state.
func TestBufferedPublisherJournal_BackwardCompatOldSnapshot(t *testing.T) {
	dir := t.TempDir()
	stateFile := filepath.Join(dir, "buffered.json")

	// Write an OLD-format file: a plain JSON array, exactly what the
	// pre-journal saveLocked produced via atomicfile.Save.
	old := []pendingMessage{
		{Exchange: "ex", RoutingKey: "rk", Msg: Message{ID: "old-1", Type: "ok.event", Payload: json.RawMessage(`{}`)}},
		{Exchange: "ex", RoutingKey: "rk", Msg: Message{ID: "old-2", Type: "ok.event", Payload: json.RawMessage(`{}`)}},
	}
	data, err := json.Marshal(old)
	if err != nil {
		t.Fatalf("marshal old state: %v", err)
	}
	if err := os.WriteFile(stateFile, data, 0o600); err != nil {
		t.Fatalf("write old state: %v", err)
	}

	fp := &fakePublisher{}
	var healthy atomic.Bool
	pub := testBufferedPublisherWithHealthPtr(fp, &healthy, withStateFileAbsoluteForTest(stateFile))
	if err := pub.load(); err != nil {
		t.Fatalf("load old-format file: %v", err)
	}
	if pub.Pending() != 2 {
		t.Fatalf("expected 2 restored from old format, got %d", pub.Pending())
	}

	// A new buffered publish (broker still down) must migrate the legacy
	// single-array file into journal format without losing the old entries.
	msg, _ := NewMessage("test.event", "new-after-upgrade")
	if err := pub.Publish(context.Background(), "ex", "rk", msg); err != nil {
		t.Fatalf("publish after upgrade: %v", err)
	}
	if pub.Pending() != 3 {
		t.Fatalf("expected 3 pending after upgrade publish, got %d", pub.Pending())
	}

	// Reload from disk into a third publisher: all 3 must survive.
	fp3 := &fakePublisher{}
	pub3 := newTestBufferedPublisher(fp3.publish, func() bool { return true }, withStateFileAbsoluteForTest(stateFile))
	if err := pub3.load(); err != nil {
		t.Fatalf("reload after upgrade: %v", err)
	}
	if pub3.Pending() != 3 {
		t.Fatalf("expected 3 pending after reload, got %d", pub3.Pending())
	}

	// Drain the upgraded publisher and confirm a clean, compacted state.
	healthy.Store(true)
	pub.drain(context.Background())
	if pub.Pending() != 0 {
		t.Fatalf("expected 0 pending after drain, got %d", pub.Pending())
	}
	if got := countJournalLines(t, stateFile); got != 1 {
		t.Fatalf("expected compacted single snapshot line after drain, got %d lines", got)
	}
}

// TestBufferedPublisherJournal_CompactsAfterDrain pins compaction on drain:
// after appending several journal lines and then draining the buffer, the file
// shrinks back to a single snapshot line ("[]").
func TestBufferedPublisherJournal_CompactsAfterDrain(t *testing.T) {
	dir := t.TempDir()
	stateFile := filepath.Join(dir, "buffered.json")

	fp := &fakePublisher{}
	var healthy atomic.Bool
	pub := testBufferedPublisherWithHealthPtr(fp, &healthy, withStateFileAbsoluteForTest(stateFile))

	for i := range 5 {
		msg, _ := NewMessage("test.event", fmt.Sprintf("m%d", i))
		if err := pub.Publish(context.Background(), "ex", "rk", msg); err != nil {
			t.Fatalf("publish %d: %v", i, err)
		}
	}
	if got := countJournalLines(t, stateFile); got != 5 {
		t.Fatalf("expected 5 journal lines before drain, got %d", got)
	}

	healthy.Store(true)
	pub.drain(context.Background())
	if pub.Pending() != 0 {
		t.Fatalf("expected 0 pending after drain, got %d", pub.Pending())
	}
	if got := countJournalLines(t, stateFile); got != 1 {
		t.Fatalf("expected single compacted snapshot line after drain, got %d lines", got)
	}
}

// TestBufferedPublisherJournal_CompactsOnThreshold pins that a long append run
// compacts back to a single snapshot line once the journal tail reaches the
// compaction floor, instead of growing one line per message forever. Without
// compaction the line count would equal the message count.
func TestBufferedPublisherJournal_CompactsOnThreshold(t *testing.T) {
	dir := t.TempDir()
	stateFile := filepath.Join(dir, "buffered.json")

	fp := &fakePublisher{}
	pub := testBufferedPublisher(fp, false, withStateFileAbsoluteForTest(stateFile))

	// Publish past the compaction floor. The first append-driven compaction
	// fires when journalEntries reaches bufferedJournalCompactMinEntries, at
	// which point the file collapses to one snapshot line again.
	total := bufferedJournalCompactMinEntries + 5
	for i := range total {
		msg, _ := NewMessage("test.event", fmt.Sprintf("m%d", i))
		if err := pub.Publish(context.Background(), "ex", "rk", msg); err != nil {
			t.Fatalf("publish %d: %v", i, err)
		}
	}
	if pub.Pending() != total {
		t.Fatalf("expected %d pending, got %d", total, pub.Pending())
	}

	// The file must be far smaller than one-line-per-message: compaction has
	// happened at least once, so the line count is well below `total`.
	if got := countJournalLines(t, stateFile); got >= total {
		t.Fatalf("journal never compacted: %d lines for %d messages", got, total)
	}

	// Despite compaction, a restart still restores every message in order.
	fp2 := &fakePublisher{}
	pub2 := newTestBufferedPublisher(fp2.publish, func() bool { return true }, withStateFileAbsoluteForTest(stateFile))
	if err := pub2.load(); err != nil {
		t.Fatalf("load after compaction: %v", err)
	}
	if pub2.Pending() != total {
		t.Fatalf("expected %d restored after compaction, got %d", total, pub2.Pending())
	}
}

// TestBufferedPublisherJournal_RecoversFromTornTrailingLine pins torn-write
// recovery: a final partial append line (e.g. process crashed mid-write) is the
// message the caller's Publish failed on, so replay drops only that trailing
// line and restores every earlier message. Interior corruption stays fatal
// (covered by TestBufferedPublisherLoad_CorruptFile_ReturnsError).
func TestBufferedPublisherJournal_RecoversFromTornTrailingLine(t *testing.T) {
	dir := t.TempDir()
	stateFile := filepath.Join(dir, "buffered.json")

	fp := &fakePublisher{}
	pub := testBufferedPublisher(fp, false, withStateFileAbsoluteForTest(stateFile))

	const n = 3
	for i := range n {
		msg, _ := NewMessage("test.event", fmt.Sprintf("m%d", i))
		if err := pub.Publish(context.Background(), "ex", "rk", msg); err != nil {
			t.Fatalf("publish %d: %v", i, err)
		}
	}

	// Simulate a torn final append: a truncated JSON line with no newline.
	f, err := os.OpenFile(stateFile, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open for torn append: %v", err)
	}
	if _, err := f.WriteString("\n{\"exchange\":\"ex\",\"routing_key\":\"rk\",\"msg\":{\"id\":\"torn"); err != nil {
		t.Fatalf("torn append: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close torn file: %v", err)
	}

	fp2 := &fakePublisher{}
	pub2 := newTestBufferedPublisher(fp2.publish, func() bool { return true }, withStateFileAbsoluteForTest(stateFile))
	if err := pub2.load(); err != nil {
		t.Fatalf("expected torn trailing line to be tolerated, got load error: %v", err)
	}
	if pub2.Pending() != n {
		t.Fatalf("expected %d restored (torn line dropped), got %d", n, pub2.Pending())
	}
}

// TestBufferedPublisherJournal_InteriorCorruptionFatal pins that corruption
// before the final line stays fatal by default — only a torn TRAILING line is
// recoverable.
func TestBufferedPublisherJournal_InteriorCorruptionFatal(t *testing.T) {
	dir := t.TempDir()
	stateFile := filepath.Join(dir, "buffered.json")

	// Snapshot line + a corrupt interior entry + a valid trailing entry.
	content := "[]\n" +
		"{ this is not valid json }\n" +
		`{"exchange":"ex","routing_key":"rk","msg":{"id":"ok","type":"t","payload":{}}}` + "\n"
	if err := os.WriteFile(stateFile, []byte(content), 0o600); err != nil {
		t.Fatalf("write corrupt journal: %v", err)
	}

	pub := newBufferedPublisher(discardLogger(), withStateFileAbsoluteForTest(stateFile))
	if err := pub.load(); err == nil {
		t.Fatal("expected interior journal corruption to be fatal, got nil")
	}
}

// TestBufferedPublisherJournal_PreservesHeadersAcrossAppendReplay confirms the
// header round-trip (the json:"-" pitfall) still holds when entries arrive via
// journal appends rather than a single snapshot rewrite.
func TestBufferedPublisherJournal_PreservesHeadersAcrossAppendReplay(t *testing.T) {
	dir := t.TempDir()
	stateFile := filepath.Join(dir, "buffered.json")

	fp := &fakePublisher{}
	pub := testBufferedPublisher(fp, false, withStateFileAbsoluteForTest(stateFile))

	// Two publishes: the second arrives as a journal append, not a snapshot.
	for i := range 2 {
		msg := Message{
			ID:      fmt.Sprintf("h-%d", i),
			Type:    "test.event",
			Payload: []byte(`{"k":"v"}`),
			Headers: map[string]string{HeaderCorrelationID: fmt.Sprintf("corr-%d", i)},
		}
		if err := pub.Publish(context.Background(), "ex", "rk", msg); err != nil {
			t.Fatalf("publish %d: %v", i, err)
		}
	}

	seen := make([]map[string]string, 0, 2)
	pub2 := newTestBufferedPublisher(
		func(_ context.Context, _, _ string, m Message) error {
			seen = append(seen, m.Headers)
			return nil
		},
		func() bool { return true },
		withStateFileAbsoluteForTest(stateFile),
	)
	if err := pub2.load(); err != nil {
		t.Fatalf("load failed: %v", err)
	}
	pub2.drain(context.Background())

	if len(seen) != 2 {
		t.Fatalf("expected 2 republished, got %d", len(seen))
	}
	for i, h := range seen {
		want := fmt.Sprintf("corr-%d", i)
		if h[HeaderCorrelationID] != want {
			t.Fatalf("entry %d header = %q, want %q (lost across journal replay)", i, h[HeaderCorrelationID], want)
		}
	}
}
