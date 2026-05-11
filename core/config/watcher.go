package config

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/bds421/rho-kit/core/v2/redact"
)

// Default debounce duration for file watchers.
const defaultDebounce = 100 * time.Millisecond

// watcherConfig holds shared options for watchers.
type watcherConfig struct {
	logger        *slog.Logger
	debounce      time.Duration
	signalCh      chan os.Signal // optional external signal channel for EnvReloader
	immediateLoad bool
}

// WatcherOption configures a FileWatcher or EnvReloader.
type WatcherOption func(*watcherConfig)

// WithWatchLogger sets the logger used by the watcher.
// A nil logger is ignored and the default logger is kept.
func WithWatchLogger(l *slog.Logger) WatcherOption {
	return func(c *watcherConfig) {
		if l != nil {
			c.logger = l
		}
	}
}

// WithSignalChannel provides an external signal channel for EnvReloader
// instead of creating an internal one. This allows fine-grained control
// when multiple EnvReloader instances or other SIGHUP listeners coexist
// in the same process.
func WithSignalChannel(ch chan os.Signal) WatcherOption {
	return func(c *watcherConfig) {
		if ch != nil {
			c.signalCh = ch
		}
	}
}

// WithDebounce sets the debounce interval for rapid file changes.
// The duration must be positive.
func WithDebounce(d time.Duration) WatcherOption {
	if d <= 0 {
		panic("config: WithDebounce requires a positive duration")
	}
	return func(c *watcherConfig) {
		c.debounce = d
	}
}

// WithImmediateLoad makes [EnvReloader.Start] perform an initial reload
// before entering the SIGHUP wait. Without it, the Watchable holds whatever
// `initial` value was passed to [NewWatchable] until the first SIGHUP — any
// change to the environment between construction and Start is invisible.
//
// Default-on behaviour is the right choice for new code; the option exists
// so callers that have already wired their own initial-load can opt out.
func WithImmediateLoad() WatcherOption {
	return func(c *watcherConfig) { c.immediateLoad = true }
}

func applyWatcherOpts(opts []WatcherOption) watcherConfig {
	cfg := watcherConfig{
		logger:   slog.Default(),
		debounce: defaultDebounce,
	}
	for _, o := range opts {
		if o == nil {
			panic("config: watcher option must not be nil")
		}
		o(&cfg)
	}
	return cfg
}

// ---------- FileWatcher ----------

// FileWatcher watches a config file and reloads on change.
// It is compatible with lifecycle.Runner.AddFunc.
//
// To integrate with the app.Builder, register the watcher as a background
// goroutine inside the RouterFunc:
//
//	watchable := config.NewWatchable(initialCfg)
//	fw := config.NewFileWatcher("config.yaml", loadFn, watchable)
//	watchable.OnChange(func(old, new MyConfig) { /* react to changes */ })
//	infra.Background("config-watcher", fw.Start)
type FileWatcher[T any] struct {
	path      string
	watchable *Watchable[T]
	loadFn    func(path string) (T, error)
	cfg       watcherConfig
	startMu   sync.Mutex
	started   bool
}

// NewFileWatcher creates a FileWatcher that calls loadFn whenever path
// changes, updating the Watchable on success.
// The path is resolved to an absolute path at construction time.
//
// Panics if loadFn or w is nil — config wiring errors must fail fast at
// startup rather than panicking inside a reload goroutine after a file
// change.
func NewFileWatcher[T any](
	path string,
	loadFn func(string) (T, error),
	w *Watchable[T],
	opts ...WatcherOption,
) *FileWatcher[T] {
	if loadFn == nil {
		panic("config: NewFileWatcher requires a non-nil loadFn")
	}
	if w == nil {
		panic("config: NewFileWatcher requires a non-nil Watchable")
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		absPath = path
	}
	return &FileWatcher[T]{
		path:      absPath,
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
	if ctx == nil {
		return errors.New("config: FileWatcher.Start requires a non-nil context")
	}
	fw.startMu.Lock()
	if fw.started {
		fw.startMu.Unlock()
		return errors.New("config: FileWatcher already started")
	}
	fw.started = true
	fw.startMu.Unlock()

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer func() {
		if err := watcher.Close(); err != nil {
			fw.cfg.logger.Warn("failed to close fsnotify watcher", redact.Error(err))
		}
	}()

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
				redact.String("path", fw.path), redact.Error(loadErr))
			return
		}
		fw.watchable.Set(val)
		fw.cfg.logger.Info("config reloaded", redact.String("path", fw.path))
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
			if debounceTimer == nil {
				debounceTimer = time.NewTimer(fw.cfg.debounce)
				debounceCh = debounceTimer.C
			} else {
				debounceTimer.Reset(fw.cfg.debounce)
			}

		case watchErr, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			fw.cfg.logger.Warn("file watcher error", redact.Error(watchErr))

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
//
// SIGHUP is process-global: all EnvReloader instances and any other SIGHUP
// listeners in the process will be notified simultaneously. Use separate
// signal channels via WithSignalChannel for fine-grained control.
type EnvReloader[T any] struct {
	watchable *Watchable[T]
	cfg       watcherConfig
	startMu   sync.Mutex
	started   bool
}

// NewEnvReloader creates an EnvReloader that reloads config from
// environment variables via config.Load[T]() on each SIGHUP.
//
// Panics if w is nil — config wiring errors must fail fast at startup
// rather than panicking inside the SIGHUP loop on the first reload.
func NewEnvReloader[T any](w *Watchable[T], opts ...WatcherOption) *EnvReloader[T] {
	if w == nil {
		panic("config: NewEnvReloader requires a non-nil Watchable")
	}
	return &EnvReloader[T]{
		watchable: w,
		cfg:       applyWatcherOpts(opts),
	}
}

// Start listens for SIGHUP and reloads config from env vars.
// It blocks until ctx is cancelled.
//
// If [WithImmediateLoad] was passed, an initial Load runs once before the
// signal loop so the Watchable reflects current environment state instead of
// the construction-time `initial` value.
func (r *EnvReloader[T]) Start(ctx context.Context) error {
	if ctx == nil {
		return errors.New("config: EnvReloader.Start requires a non-nil context")
	}
	r.startMu.Lock()
	if r.started {
		r.startMu.Unlock()
		return errors.New("config: EnvReloader already started")
	}
	r.started = true
	r.startMu.Unlock()

	sigCh := r.cfg.signalCh
	if sigCh == nil {
		sigCh = make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGHUP)
		defer signal.Stop(sigCh)
	}

	if r.cfg.immediateLoad {
		val, err := Load[T]()
		if err != nil {
			r.cfg.logger.Warn("env config initial load failed, keeping construction-time value",
				redact.Error(err))
		} else {
			r.watchable.Set(val)
			r.cfg.logger.Info("config initial-load from environment")
		}
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-sigCh:
			val, err := Load[T]()
			if err != nil {
				r.cfg.logger.Warn("env config reload failed, keeping previous value",
					redact.Error(err))
				continue
			}
			r.watchable.Set(val)
			r.cfg.logger.Info("config reloaded from environment (SIGHUP)")
		}
	}
}
