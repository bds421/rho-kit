package config

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
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
	done := make(chan error, 1)
	go func() {
		close(started)
		done <- fw.Start(ctx)
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
	require.NoError(t, <-done)
}

func TestFileWatcher_LoadErrorKeepsOldValue(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.txt")
	writeFile(t, cfgPath, "good")
	var logs bytes.Buffer

	var failNext atomic.Bool

	loadFn := func(path string) (string, error) {
		if failNext.Load() {
			return "", fmt.Errorf("failed to load %s token=tenant-secret", path)
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
		WithWatchLogger(slog.New(slog.NewTextHandler(&logs, nil))),
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
	time.Sleep(50 * time.Millisecond)

	// Make loadFn fail, then trigger a write.
	failNext.Store(true)
	writeFile(t, cfgPath, "bad")

	// Wait for debounce + processing.
	time.Sleep(200 * time.Millisecond)

	// Value should remain unchanged.
	assert.Equal(t, "good", w.Get())

	cancel()
	require.NoError(t, <-done)
	got := logs.String()
	assert.Contains(t, got, "config reload failed")
	assert.Contains(t, got, "<redacted")
	assert.NotContains(t, got, cfgPath)
	assert.NotContains(t, got, "tenant-secret")
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

func TestFileWatcher_StartRejectsNilContext(t *testing.T) {
	w := NewWatchable("data")
	fw := NewFileWatcher("unused", func(string) (string, error) {
		return "data", nil
	}, w)

	var ctx context.Context
	err := fw.Start(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-nil context")
}

func TestFileWatcher_StartRejectsSecondStart(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.txt")
	writeFile(t, cfgPath, "data")

	w := NewWatchable("data")
	fw := NewFileWatcher(cfgPath, func(string) (string, error) {
		return "data", nil
	}, w)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- fw.Start(ctx) }()
	waitForFileWatcherStarted(t, fw)

	err := fw.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already started")

	cancel()
	require.NoError(t, <-done)
}

func TestFileWatcher_StartRejectsRestartAfterCancel(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.txt")
	writeFile(t, cfgPath, "data")

	w := NewWatchable("data")
	fw := NewFileWatcher(cfgPath, func(string) (string, error) {
		return "data", nil
	}, w)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- fw.Start(ctx) }()
	waitForFileWatcherStarted(t, fw)

	cancel()
	require.NoError(t, <-done)

	err := fw.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already started")
}

func TestFileWatcher_WatchableAccessor(t *testing.T) {
	w := NewWatchable("val")
	fw := NewFileWatcher("unused", func(string) (string, error) {
		return "", nil
	}, w)

	assert.Same(t, w, fw.Watchable())
}

func waitForFileWatcherStarted[T any](t *testing.T, fw *FileWatcher[T]) {
	t.Helper()
	require.Eventually(t, func() bool {
		fw.startMu.Lock()
		defer fw.startMu.Unlock()
		return fw.started
	}, time.Second, 10*time.Millisecond)
}

// ---------- EnvReloader tests ----------

type envReloaderCfg struct {
	Value string `env:"TEST_ENV_RELOAD_VALUE" default:"initial"`
}

func TestEnvReloader_StartBlocksUntilCancelled(t *testing.T) {
	w := NewWatchable(envReloaderCfg{Value: "initial"})
	r := NewEnvReloader[envReloaderCfg](w)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		_ = r.Start(ctx)
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

func TestEnvReloader_StartRejectsNilContext(t *testing.T) {
	w := NewWatchable(envReloaderCfg{Value: "initial"})
	r := NewEnvReloader[envReloaderCfg](w)

	var ctx context.Context
	err := r.Start(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-nil context")
}

func TestEnvReloader_StartRejectsSecondStart(t *testing.T) {
	w := NewWatchable(envReloaderCfg{Value: "initial"})
	r := NewEnvReloader[envReloaderCfg](w, WithSignalChannel(make(chan os.Signal, 1)))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Start(ctx) }()
	waitForEnvReloaderStarted(t, r)

	err := r.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already started")

	cancel()
	require.NoError(t, <-done)
}

func TestEnvReloader_StartRejectsRestartAfterCancel(t *testing.T) {
	w := NewWatchable(envReloaderCfg{Value: "initial"})
	r := NewEnvReloader[envReloaderCfg](w, WithSignalChannel(make(chan os.Signal, 1)))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Start(ctx) }()
	waitForEnvReloaderStarted(t, r)

	cancel()
	require.NoError(t, <-done)

	err := r.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already started")
}

func TestEnvReloader_SIGHUPTriggersReload(t *testing.T) {
	t.Setenv("TEST_ENV_RELOAD_VALUE", "updated")

	sigCh := make(chan os.Signal, 1)
	w := NewWatchable(envReloaderCfg{Value: "initial"})
	r := NewEnvReloader[envReloaderCfg](w, WithSignalChannel(sigCh))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	started := make(chan struct{})
	go func() {
		close(started)
		_ = r.Start(ctx)
	}()
	<-started

	// Send SIGHUP via injected channel to trigger reload.
	sigCh <- syscall.SIGHUP

	assert.Eventually(t, func() bool {
		return w.Get().Value == "updated"
	}, 2*time.Second, 20*time.Millisecond)

	cancel()
}

func waitForEnvReloaderStarted[T any](t *testing.T, r *EnvReloader[T]) {
	t.Helper()
	require.Eventually(t, func() bool {
		r.startMu.Lock()
		defer r.startMu.Unlock()
		return r.started
	}, time.Second, 10*time.Millisecond)
}

func TestEnvReloader_WithImmediateLoadAppliesEnvBeforeFirstSIGHUP(t *testing.T) {
	t.Setenv("TEST_ENV_RELOAD_VALUE", "from-env")

	sigCh := make(chan os.Signal, 1)
	w := NewWatchable(envReloaderCfg{Value: "construction-default"})
	r := NewEnvReloader[envReloaderCfg](w,
		WithSignalChannel(sigCh),
		WithWatchLogger(slog.Default()),
		WithImmediateLoad(),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	started := make(chan struct{})
	go func() {
		close(started)
		_ = r.Start(ctx)
	}()
	<-started

	assert.Eventually(t, func() bool {
		return w.Get().Value == "from-env"
	}, 2*time.Second, 20*time.Millisecond, "immediate load should override the construction-time default before any SIGHUP fires")
}

func TestEnvReloader_LoadErrorPreservesOldValue(t *testing.T) {
	// Use a required env var that is not set so Load fails.
	type strictCfg struct {
		Port int `env:"TEST_ENVRELOADER_REQUIRED_PORT,required"`
	}

	// Ensure the var is not set (use a unique name to avoid collisions).
	require.NoError(t, os.Unsetenv("TEST_ENVRELOADER_REQUIRED_PORT"))

	sigCh := make(chan os.Signal, 1)
	w := NewWatchable(strictCfg{Port: 8080})
	r := NewEnvReloader[strictCfg](w, WithSignalChannel(sigCh), WithWatchLogger(slog.Default()))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	started := make(chan struct{})
	go func() {
		close(started)
		_ = r.Start(ctx)
	}()
	<-started

	// Send SIGHUP via injected channel — Load should fail because the required var is not set.
	sigCh <- syscall.SIGHUP

	// Wait a bit for the signal to be processed.
	time.Sleep(200 * time.Millisecond)

	// Value should remain unchanged.
	assert.Equal(t, 8080, w.Get().Port)

	cancel()
}

func TestNewFileWatcher_PanicsOnNilLoadFn(t *testing.T) {
	w := NewWatchable("v")
	assert.PanicsWithValue(t, "config: NewFileWatcher requires a non-nil loadFn", func() {
		NewFileWatcher[string]("p", nil, w)
	})
}

func TestNewFileWatcher_PanicsOnNilWatchable(t *testing.T) {
	loadFn := func(string) (string, error) { return "", nil }
	assert.PanicsWithValue(t, "config: NewFileWatcher requires a non-nil Watchable", func() {
		NewFileWatcher[string]("p", loadFn, nil)
	})
}

func TestNewFileWatcher_PanicsOnNilOption(t *testing.T) {
	w := NewWatchable("v")
	loadFn := func(string) (string, error) { return "", nil }
	assert.PanicsWithValue(t, "config: watcher option must not be nil", func() {
		NewFileWatcher[string]("p", loadFn, w, nil)
	})
}

func TestNewEnvReloader_PanicsOnNilWatchable(t *testing.T) {
	assert.PanicsWithValue(t, "config: NewEnvReloader requires a non-nil Watchable", func() {
		NewEnvReloader[envReloaderCfg](nil)
	})
}

func TestNewEnvReloader_PanicsOnNilOption(t *testing.T) {
	w := NewWatchable(envReloaderCfg{})
	assert.PanicsWithValue(t, "config: watcher option must not be nil", func() {
		NewEnvReloader[envReloaderCfg](w, nil)
	})
}

func TestWithDebounce_PanicsOnNonPositive(t *testing.T) {
	for _, d := range []time.Duration{0, -time.Second} {
		t.Run(d.String(), func(t *testing.T) {
			assert.Panics(t, func() {
				WithDebounce(d)
			})
		})
	}
}

func TestEnvReloader_WithSignalChannel(t *testing.T) {
	t.Setenv("TEST_ENV_RELOAD_VALUE", "via-channel")

	sigCh := make(chan os.Signal, 1)
	w := NewWatchable(envReloaderCfg{Value: "initial"})
	r := NewEnvReloader[envReloaderCfg](w, WithSignalChannel(sigCh))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	started := make(chan struct{})
	go func() {
		close(started)
		_ = r.Start(ctx)
	}()
	<-started

	// Trigger reload via injected signal channel.
	sigCh <- syscall.SIGHUP

	assert.Eventually(t, func() bool {
		return w.Get().Value == "via-channel"
	}, 2*time.Second, 20*time.Millisecond)

	cancel()
}
