package messaging

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestNewPrometheusMetrics_PanicsOnEmptyName(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on empty publisher name")
		}
	}()
	NewPrometheusMetrics(prometheus.NewRegistry(), "")
}

func TestNewPrometheusMetrics_PanicsOnInvalidName(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic on invalid publisher name")
		}
		msg, ok := r.(string)
		if !ok {
			t.Fatalf("panic must be a stable string, got %T", r)
		}
		// The label leak guard must not reflect the invalid value.
		if strings.Contains(msg, "bad value with spaces") {
			t.Fatalf("panic leaked invalid label value: %q", msg)
		}
	}()
	NewPrometheusMetrics(prometheus.NewRegistry(), "bad value with spaces")
}

func TestNewPrometheusMetrics_RegistersOnDefaultWhenNil(t *testing.T) {
	// Use a unique publisher name so we do not pollute the default registry
	// with collisions across test runs (MustRegisterOrGet would reuse the
	// existing collector either way; we just want to prove nil works).
	pm := NewPrometheusMetrics(nil, "default_registerer_probe")
	if pm == nil {
		t.Fatal("expected non-nil metrics on nil registerer")
	}
}

func TestPrometheusBufferedPublisherMetrics_DropAndPendingAndBytes(t *testing.T) {
	reg := prometheus.NewRegistry()
	pm := NewPrometheusMetrics(reg, "events")

	fp := &fakePublisher{}
	pub := testBufferedPublisher(fp, false,
		WithMaxSize(1),
		WithMetrics(pm.Hooks()),
	)

	msg1, _ := NewMessage("test.event", "m1")
	if err := pub.Publish(context.Background(), "ex", "rk", msg1); err != nil {
		t.Fatalf("buffer msg1: %v", err)
	}
	if got := testutil.ToFloat64(pm.pending.WithLabelValues("events")); got != 1 {
		t.Fatalf("pending gauge = %v, want 1", got)
	}
	bytes1 := testutil.ToFloat64(pm.bufferedByte.WithLabelValues("events"))
	if bytes1 <= 0 {
		t.Fatalf("buffered_bytes gauge = %v, want > 0", bytes1)
	}

	// Second publish exceeds maxSize=1, triggering OnDrop.
	msg2, _ := NewMessage("test.event", "m2")
	if err := pub.Publish(context.Background(), "ex", "rk", msg2); err == nil {
		t.Fatal("expected buffer-full error on 2nd publish")
	}
	dropped := testutil.ToFloat64(pm.dropped.WithLabelValues("events", "buffer_full"))
	if dropped != 1 {
		t.Fatalf("dropped_total{reason=buffer_full} = %v, want 1", dropped)
	}
}

func TestPrometheusBufferedPublisherMetrics_PendingDecreasesOnDrain(t *testing.T) {
	reg := prometheus.NewRegistry()
	pm := NewPrometheusMetrics(reg, "events")

	fp := &fakePublisher{}
	var healthy atomic.Bool
	pub := testBufferedPublisherWithHealthPtr(fp, &healthy,
		WithMetrics(pm.Hooks()),
	)

	for i := range 3 {
		msg, _ := NewMessage("test.event", fmt.Sprintf("m%d", i))
		if err := pub.Publish(context.Background(), "ex", "rk", msg); err != nil {
			t.Fatalf("buffer %d: %v", i, err)
		}
	}
	if got := testutil.ToFloat64(pm.pending.WithLabelValues("events")); got != 3 {
		t.Fatalf("pending = %v, want 3", got)
	}

	healthy.Store(true)
	pub.drain(context.Background())

	if got := testutil.ToFloat64(pm.pending.WithLabelValues("events")); got != 0 {
		t.Fatalf("pending after drain = %v, want 0", got)
	}
	if got := testutil.ToFloat64(pm.bufferedByte.WithLabelValues("events")); got != 0 {
		t.Fatalf("buffered_bytes after drain = %v, want 0", got)
	}
}

func TestPrometheusBufferedPublisherMetrics_StateWriteSuccess(t *testing.T) {
	reg := prometheus.NewRegistry()
	pm := NewPrometheusMetrics(reg, "events")

	stateFile := filepath.Join(t.TempDir(), "buffered.json")
	fp := &fakePublisher{}
	pub := testBufferedPublisher(fp, false,
		withStateFileAbsoluteForTest(stateFile),
		WithMetrics(pm.Hooks()),
	)

	msg, _ := NewMessage("test.event", "payload")
	if err := pub.Publish(context.Background(), "ex", "rk", msg); err != nil {
		t.Fatalf("publish: %v", err)
	}

	success := testutil.ToFloat64(pm.stateWrites.WithLabelValues("events", "success"))
	if success < 1 {
		t.Fatalf("state_writes_total{outcome=success} = %v, want >= 1", success)
	}
	errors := testutil.ToFloat64(pm.stateWrites.WithLabelValues("events", "error"))
	if errors != 0 {
		t.Fatalf("state_writes_total{outcome=error} = %v, want 0", errors)
	}
}

func TestPrometheusBufferedPublisherMetrics_StateWriteError(t *testing.T) {
	reg := prometheus.NewRegistry()
	pm := NewPrometheusMetrics(reg, "events")

	dir := t.TempDir()
	stateFile := filepath.Join(dir, "buffered.json")

	fp := &fakePublisher{}
	var healthy atomic.Bool
	pub := testBufferedPublisherWithHealthPtr(fp, &healthy,
		withStateFileAbsoluteForTest(stateFile),
		WithMetrics(pm.Hooks()),
	)

	// Buffer a message successfully first (dir is writable).
	msg, _ := NewMessage("test.event", "payload")
	if err := pub.Publish(context.Background(), "ex", "rk", msg); err != nil {
		t.Fatalf("publish: %v", err)
	}

	// Lock down the directory so saveLocked() fails during drain's post-publish save.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	healthy.Store(true)
	pub.drain(context.Background())

	errors := testutil.ToFloat64(pm.stateWrites.WithLabelValues("events", "error"))
	if errors < 1 {
		t.Fatalf("state_writes_total{outcome=error} = %v, want >= 1", errors)
	}
}

func TestPrometheusBufferedPublisherMetrics_WithPrometheusMetrics(t *testing.T) {
	// Confirms the convenience option wires both registration AND hooks.
	reg := prometheus.NewRegistry()
	stateFile := filepath.Join(t.TempDir(), "buffered.json")
	fp := &fakePublisher{}
	pub := testBufferedPublisher(fp, false,
		WithMaxSize(1),
		withStateFileAbsoluteForTest(stateFile),
		WithPrometheusMetrics(reg, "convenience"),
	)
	if pub.metrics == nil {
		t.Fatal("WithPrometheusMetrics did not attach metrics hooks")
	}

	// Drive one of each metric family so it shows up in Gather().
	msg1, _ := NewMessage("test.event", "m1")
	if err := pub.Publish(context.Background(), "ex", "rk", msg1); err != nil {
		t.Fatalf("publish: %v", err)
	}
	msg2, _ := NewMessage("test.event", "m2")
	if err := pub.Publish(context.Background(), "ex", "rk", msg2); err == nil {
		t.Fatal("expected buffer-full error")
	}

	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	want := map[string]bool{
		"buffered_publisher_dropped_total":      false,
		"buffered_publisher_state_writes_total": false,
		"buffered_publisher_pending":            false,
		"buffered_publisher_buffered_bytes":     false,
	}
	for _, mf := range families {
		if _, ok := want[mf.GetName()]; ok {
			want[mf.GetName()] = true
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("metric %q was not registered", name)
		}
	}
}

func TestPrometheusBufferedPublisherMetrics_RepeatedRegistrationReusesCollectors(t *testing.T) {
	reg := prometheus.NewRegistry()
	pm1 := NewPrometheusMetrics(reg, "events")
	pm2 := NewPrometheusMetrics(reg, "events")

	pm1.onDrop()
	pm2.onDrop()

	got := testutil.ToFloat64(pm1.dropped.WithLabelValues("events", "buffer_full"))
	if got != 2 {
		t.Fatalf("duplicate registration broke metric sharing; counter = %v, want 2", got)
	}
}
