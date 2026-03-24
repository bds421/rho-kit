package config

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Default debounce duration for file watchers.
const defaultDebounce = 100 * time.Millisecond

// watcherConfig holds shared options for watchers.
type watcherConfig struct {
	logger   *slog.Logger
	debounce time.Duration
}

// WatcherOption configures a FileWatcher or EnvReloader.
type WatcherOption func(*watcherConfig)

// WithWatchLogger sets the logger used by the watcher.
func WithWatchLogger(l *slog.Logger) WatcherOption {
	return func(c *watcherConfig) {
		c.logger = l
	}
}

// WithDebounce sets the debounce interval for rapid file changes.
// The default is 100ms.
func WithDebounce(d time.Duration) WatcherOption {
	return func(c *watcherConfig) {
		c.debounce = d
	}
}

func applyWatcherOpts(opts []WatcherOption) watcherConfig {
	cfg := watcherConfig{
		logger:   slog.Default(),
		debounce: defaultDebounce,
	}
	for _, o := range opts {
		o(&cfg)
	}
	return cfg
}

// ---------- FileWatcher ----------

// FileWatcher watches a config file and reloads on change.
// It is compatible with lifecycle.Runner.AddFunc.
type FileWatcher[T any] struct {
	path      string
	watchable *Watchable[T]
	loadFn    func(path string) (T, error)
	cfg       watcherConfig
}

// NewFileWatcher creates a FileWatcher that calls loadFn whenever path
// changes, updating the Watchable on success.
func NewFileWatcher[T any](
	path string,
	loadFn func(string) (T, error),
	w *Watchable[T],
	opts ...WatcherOption,
) *FileWatcher[T] {
	return &FileWatcher[T]{
		path:      path,
		watchable: w,
		loadFn:    loadFn,
		cfg:       applyWatcherOpts(opts),
	}
}

// Watchable returns the underlying Watchable for reading config and
// registering subscribers.
func (fw *FileWatcher[T]) Watchable() *Watchable[T] {
	return fw.watchable
}

// Start begins watching the file. It blocks until ctx is cancelled.
func (fw *FileWatcher[T]) Start(ctx context.Context) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer watcher.Close()

	// Watch the directory so we also catch atomic-rename saves where
	// the original file is removed and a new one is created.
	dir := filepath.Dir(fw.path)
	if err := watcher.Add(dir); err != nil {
		return err
	}

	var debounceTimer *time.Timer
	var debounceCh <-chan time.Time

	reload := func() {
		val, loadErr := fw.loadFn(fw.path)
		if loadErr != nil {
			fw.cfg.logger.Warn("config reload failed, keeping previous value",
				"path", fw.path, "error", loadErr)
			return
		}
		fw.watchable.Set(val)
		fw.cfg.logger.Info("config reloaded", "path", fw.path)
	}

	base := filepath.Base(fw.path)

	for {
		select {
		case <-ctx.Done():
			if debounceTimer != nil {
				debounceTimer.Stop()
			}
			return nil

		case event, ok := <-watcher.Events:
			if !ok {
				return nil
			}
			// Only react to our specific file.
			if filepath.Base(event.Name) != base {
				continue
			}
			if !isRelevantEvent(event) {
				continue
			}
			// Reset or start debounce timer.
			if debounceTimer != nil {
				debounceTimer.Stop()
			}
			debounceTimer = time.NewTimer(fw.cfg.debounce)
			debounceCh = debounceTimer.C

		case watchErr, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			fw.cfg.logger.Warn("file watcher error", "error", watchErr)

		case <-debounceCh:
			debounceCh = nil
			reload()
		}
	}
}

func isRelevantEvent(e fsnotify.Event) bool {
	return e.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove|fsnotify.Rename) != 0
}

// ---------- EnvReloader ----------

// EnvReloader re-reads environment variables on SIGHUP signal.
// It is compatible with lifecycle.Runner.AddFunc.
type EnvReloader[T any] struct {
	watchable *Watchable[T]
	cfg       watcherConfig
}

// NewEnvReloader creates an EnvReloader that reloads config from
// environment variables via config.Load[T]() on each SIGHUP.
func NewEnvReloader[T any](w *Watchable[T], opts ...WatcherOption) *EnvReloader[T] {
	return &EnvReloader[T]{
		watchable: w,
		cfg:       applyWatcherOpts(opts),
	}
}

// Start listens for SIGHUP and reloads config from env vars.
// It blocks until ctx is cancelled.
func (r *EnvReloader[T]) Start(ctx context.Context) error {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGHUP)
	defer signal.Stop(sigCh)

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-sigCh:
			val, err := Load[T]()
			if err != nil {
				r.cfg.logger.Warn("env config reload failed, keeping previous value",
					"error", err)
				continue
			}
			r.watchable.Set(val)
			r.cfg.logger.Info("config reloaded from environment (SIGHUP)")
		}
	}
}
