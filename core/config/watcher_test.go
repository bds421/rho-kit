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

	"github.com/fsnotify/fsnotify"
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

func TestFileWatcher_DetectsMultipleSequentialChanges(t *testing.T) {
	// Regression: the debounce timer must be re-armed after each reload.
	// A prior bug left debounceTimer non-nil after firing while debounceCh
	// was nil, so every change after the first was silently ignored.
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

	// Drive several sequential change->reload cycles. Each must take effect.
	for _, want := range []string{"v2", "v3", "v4"} {
		writeFile(t, cfgPath, want)
		assert.Eventually(t, func() bool {
			return w.Get() == want
		}, 2*time.Second, 20*time.Millisecond, "watcher must reload on change %q after a prior reload", want)
	}

	cancel()
	assert.NoError(t, <-done)
}

func TestFileWatcher_DetectsKubernetesSymlinkSwap(t *testing.T) {
	// Kubernetes mounts ConfigMaps/Secrets and updates them via an atomic
	// `..data` symlink swap: events fire for `..data`, `..data_tmp`, and a
	// timestamped data dir — NEVER for the logical config file, which is a
	// symlink chain config.yaml -> ..data/config.yaml. A base-name filter on
	// the config file would miss the update entirely. The watcher must detect
	// that the resolved real path changed and reload.
	dir := t.TempDir()

	// Build the initial k8s layout:
	//   <dir>/..2026_data_v1/config.yaml  (real file, "v1")
	//   <dir>/..data        -> ..2026_data_v1
	//   <dir>/config.yaml   -> ..data/config.yaml
	dataV1 := filepath.Join(dir, "..2026_data_v1")
	require.NoError(t, os.Mkdir(dataV1, 0o755))
	writeFile(t, filepath.Join(dataV1, "config.yaml"), "v1")

	dataLink := filepath.Join(dir, "..data")
	require.NoError(t, os.Symlink("..2026_data_v1", dataLink))

	cfgPath := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.Symlink(filepath.Join("..data", "config.yaml"), cfgPath))

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
		// Short poll so the k8s `..data` swap is detected deterministically:
		// fsnotify does not reliably deliver the symlink-swap event (kqueue
		// drops it on macOS), so the resolved-path poll is the real backstop.
		WithPollInterval(20*time.Millisecond),
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

	// Perform the atomic k8s update: stage a new data dir, point a temp
	// symlink at it, then rename it over `..data` atomically. No event ever
	// fires for `config.yaml` itself.
	dataV2 := filepath.Join(dir, "..2026_data_v2")
	require.NoError(t, os.Mkdir(dataV2, 0o755))
	writeFile(t, filepath.Join(dataV2, "config.yaml"), "v2")

	dataTmp := filepath.Join(dir, "..data_tmp")
	require.NoError(t, os.Symlink("..2026_data_v2", dataTmp))
	require.NoError(t, os.Rename(dataTmp, dataLink))

	// Wait for reload triggered by the resolved-path change.
	assert.Eventually(t, func() bool {
		return w.Get() == "v2"
	}, 2*time.Second, 20*time.Millisecond,
		"watcher must reload after a Kubernetes-style ..data symlink swap")

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
	}

	// Wait for the trailing-edge reload, then keep observing beyond another
	// debounce window. The old per-write sleeps could be stretched past the
	// debounce interval by a busy CI scheduler, turning the intended burst into
	// legitimately separate changes and making this assertion flaky.
	require.Eventually(t, func() bool {
		return w.Get() == "v5"
	}, 2*time.Second, 10*time.Millisecond)
	assert.Never(t, func() bool {
		return loadCount.Load() > 2
	}, 150*time.Millisecond, 10*time.Millisecond,
		"rapid writes must remain coalesced after the final value is visible")

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

// TestFileWatcher_StartRetryableAfterFailedInit is the regression pin
// for review MEDIUM: if watcher.Add fails (missing parent dir),
// started must remain false so a later Start succeeds once the path
// exists — lifecycle runners re-invoke Start on error.
func TestFileWatcher_StartRetryableAfterFailedInit(t *testing.T) {
	root := t.TempDir()
	// Parent of the config file does not exist yet → Add fails.
	missingDir := filepath.Join(root, "missing-subdir")
	cfgPath := filepath.Join(missingDir, "config.txt")

	w := NewWatchable("initial")
	fw := NewFileWatcher(cfgPath, func(string) (string, error) {
		return "loaded", nil
	}, w)

	err := fw.Start(context.Background())
	require.Error(t, err, "Start must fail when config parent dir is missing")

	fw.startMu.Lock()
	started := fw.started
	fw.startMu.Unlock()
	require.False(t, started, "failed init must leave started=false for retry")

	// Create the path and retry — must succeed now.
	require.NoError(t, os.MkdirAll(missingDir, 0o750))
	writeFile(t, cfgPath, "data")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- fw.Start(ctx) }()
	waitForFileWatcherStarted(t, fw)

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

// TestFileWatcher_EventsChannelClosedUnexpectedly pins the contract that
// an fsnotify Events channel close (watcher death) returns a descriptive
// error rather than nil, so lifecycle supervisors restart/alert instead of
// treating it as a clean shutdown.
func TestFileWatcher_EventsChannelClosedUnexpectedly(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.txt")
	writeFile(t, cfgPath, "data")

	orig := fsnotifyNewWatcher
	t.Cleanup(func() { fsnotifyNewWatcher = orig })
	fsnotifyNewWatcher = func() (*fsnotify.Watcher, error) {
		w, err := orig()
		if err != nil {
			return nil, err
		}
		// Close after Start has Add'd the directory so the loop observes
		// channel close, not an Add failure.
		go func() {
			time.Sleep(50 * time.Millisecond)
			_ = w.Close()
		}()
		return w, nil
	}

	w := NewWatchable("data")
	fw := NewFileWatcher(cfgPath, func(string) (string, error) {
		return "data", nil
	}, w)

	err := fw.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fsnotify")
	assert.Contains(t, err.Error(), "closed unexpectedly")
}
