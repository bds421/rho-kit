package config

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

func TestFileWatcher_DetectsChange(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.txt")
	writeFile(t, cfgPath, "v1")

	w := NewWatchable("v1")

	loadFn := func(path string) (string, error) {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		return string(data), nil
	}

	fw := NewFileWatcher(cfgPath, loadFn, w,
		WithDebounce(20*time.Millisecond),
		WithWatchLogger(slog.Default()),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(started)
		done <- fw.Start(ctx)
	}()
	<-started

	// Allow watcher to initialise.
	time.Sleep(50 * time.Millisecond)

	// Modify the file.
	writeFile(t, cfgPath, "v2")

	// Wait for reload.
	assert.Eventually(t, func() bool {
		return w.Get() == "v2"
	}, 2*time.Second, 20*time.Millisecond)

	cancel()
	assert.NoError(t, <-done)
}

func TestFileWatcher_DebouncesRapidWrites(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.txt")
	writeFile(t, cfgPath, "v0")

	var loadCount atomic.Int32
	loadFn := func(path string) (string, error) {
		loadCount.Add(1)
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		return string(data), nil
	}

	w := NewWatchable("v0")
	fw := NewFileWatcher(cfgPath, loadFn, w,
		WithDebounce(100*time.Millisecond),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	started := make(chan struct{})
	go func() {
		close(started)
		_ = fw.Start(ctx)
	}()
	<-started
	time.Sleep(50 * time.Millisecond)

	// Write rapidly — each should reset the debounce timer.
	for i := range 5 {
		writeFile(t, cfgPath, fmt.Sprintf("v%d", i+1))
		time.Sleep(10 * time.Millisecond)
	}

	// Wait for debounce to settle.
	time.Sleep(250 * time.Millisecond)

	// Should have loaded only once (or at most a couple of times),
	// not 5 times.
	loads := loadCount.Load()
	assert.LessOrEqual(t, loads, int32(2), "expected debounce to coalesce writes, got %d loads", loads)

	// Final value should be the last write.
	assert.Equal(t, "v5", w.Get())

	cancel()
}

func TestFileWatcher_LoadErrorKeepsOldValue(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.txt")
	writeFile(t, cfgPath, "good")

	var failNext atomic.Bool

	loadFn := func(path string) (string, error) {
		if failNext.Load() {
			return "", fmt.Errorf("simulated load error")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		return string(data), nil
	}

	w := NewWatchable("good")
	fw := NewFileWatcher(cfgPath, loadFn, w,
		WithDebounce(20*time.Millisecond),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	started := make(chan struct{})
	go func() {
		close(started)
		_ = fw.Start(ctx)
	}()
	<-started
	time.Sleep(50 * time.Millisecond)

	// Make loadFn fail, then trigger a write.
	failNext.Store(true)
	writeFile(t, cfgPath, "bad")

	// Wait for debounce + processing.
	time.Sleep(200 * time.Millisecond)

	// Value should remain unchanged.
	assert.Equal(t, "good", w.Get())

	cancel()
}

func TestFileWatcher_StartBlocksUntilCancelled(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.txt")
	writeFile(t, cfgPath, "data")

	w := NewWatchable("data")
	fw := NewFileWatcher(cfgPath, func(string) (string, error) {
		return "data", nil
	}, w)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		_ = fw.Start(ctx)
		close(done)
	}()

	// Start should be blocking.
	select {
	case <-done:
		t.Fatal("Start returned before context was cancelled")
	case <-time.After(100 * time.Millisecond):
		// Expected: still blocking.
	}

	cancel()

	select {
	case <-done:
		// Expected: returned after cancel.
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return after context cancellation")
	}
}

func TestFileWatcher_WatchableAccessor(t *testing.T) {
	w := NewWatchable("val")
	fw := NewFileWatcher("unused", func(string) (string, error) {
		return "", nil
	}, w)

	assert.Same(t, w, fw.Watchable())
}
