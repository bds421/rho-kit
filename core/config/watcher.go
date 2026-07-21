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

// Default poll interval for the FileWatcher's resolved-path fallback. fsnotify
// does not reliably deliver events for the atomic `..data` symlink swap that
// Kubernetes uses to update mounted ConfigMaps/Secrets (kqueue on macOS drops
// them; some Linux filesystems coalesce them), so a low-frequency poll of the
// resolved real path guarantees such updates are eventually detected. Events
// still provide fast response for ordinary edits; the poll is only a backstop.
const defaultPollInterval = 10 * time.Second

// fsnotifyNewWatcher constructs an fsnotify Watcher. Overridden in tests to
// inject a watcher whose Events/Errors channels close unexpectedly so the
// descriptive error paths are pinned.
var fsnotifyNewWatcher = fsnotify.NewWatcher

// watcherConfig holds shared options for watchers.
type watcherConfig struct {
	logger        *slog.Logger
	debounce      time.Duration
	pollInterval  time.Duration  // FileWatcher resolved-path poll fallback; 0 disables
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
//
// The caller owns the channel's signal wiring: [EnvReloader.Start] does NOT
// call signal.Notify on an injected channel (it only does so for the channel
// it creates itself when this option is absent). To have real OS SIGHUPs
// delivered, the caller must register and later release the channel, e.g.:
//
//	ch := make(chan os.Signal, 1)
//	signal.Notify(ch, syscall.SIGHUP)
//	defer signal.Stop(ch)
//	r := config.NewEnvReloader(w, config.WithSignalChannel(ch))
//
// An injected channel that is never passed to signal.Notify will only reload
// when the caller sends to it manually; real SIGHUPs are ignored.
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

// WithPollInterval sets how often the FileWatcher re-resolves the watched
// path (via filepath.EvalSymlinks) as a fallback for detecting Kubernetes
// ConfigMap/Secret `..data` symlink swaps, whose fsnotify events are not
// reliably delivered. The default is 10s. Pass 0 to disable polling and rely
// solely on fsnotify events (only safe when the config file is never a
// symlink into a k8s projected volume). Has no effect on EnvReloader.
func WithPollInterval(d time.Duration) WatcherOption {
	if d < 0 {
		panic("config: WithPollInterval requires a non-negative duration")
	}
	return func(c *watcherConfig) {
		c.pollInterval = d
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
		logger:       slog.Default(),
		debounce:     defaultDebounce,
		pollInterval: defaultPollInterval,
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
//
// Start is one-shot after a successful init: once started=true, a
// second Start (including after a clean ctx-cancel exit) returns
// "already started". Fallible init failures (fsnotify.NewWatcher,
// watcher.Add) leave started=false so a lifecycle runner can retry.
func (fw *FileWatcher[T]) Start(ctx context.Context) error {
	if ctx == nil {
		return errors.New("config: FileWatcher.Start requires a non-nil context")
	}
	fw.startMu.Lock()
	if fw.started {
		fw.startMu.Unlock()
		return errors.New("config: FileWatcher already started")
	}
	// Mark started only after fallible init succeeds so a transient
	// NewWatcher/Add failure leaves the watcher retryable (lifecycle
	// runners re-invoke Start on error).
	fw.startMu.Unlock()

	watcher, err := fsnotifyNewWatcher()
	if err != nil {
		return err
	}
	defer func() {
		if err := watcher.Close(); err != nil {
			fw.cfg.logger.Warn("failed to close fsnotify watcher", redact.Error(err))
		}
	}()

	// Watch the directory so we also catch atomic-rename saves where the
	// original file is removed and a new one is created, as well as Kubernetes
	// ConfigMap/Secret updates that swap the mount's `..data` symlink (those
	// events name `..data` / `..data_tmp` / a timestamped dir, never the
	// config file itself).
	dir := filepath.Dir(fw.path)
	if err := watcher.Add(dir); err != nil {
		return err
	}

	fw.startMu.Lock()
	if fw.started {
		// Lost a race with another Start after init — close and reject.
		fw.startMu.Unlock()
		return errors.New("config: FileWatcher already started")
	}
	fw.started = true
	fw.startMu.Unlock()

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

	// Track the resolved real path so we can detect Kubernetes ConfigMap /
	// Secret updates. k8s swaps the mount's `..data` symlink atomically, so
	// events never fire for the logical config file (config.yaml ->
	// ..data/config.yaml); only the resolved target changes. EvalSymlinks may
	// fail if the file is briefly absent mid-swap — treat that as "unchanged"
	// and rely on a later event once the swap settles.
	resolvedPath := func() string {
		real, err := filepath.EvalSymlinks(fw.path)
		if err != nil {
			return ""
		}
		return real
	}
	lastResolved := resolvedPath()

	// Arm the debounce timer; shared by both the fsnotify and poll paths so
	// rapid changes from either source coalesce into a single reload.
	armDebounce := func() {
		if debounceTimer == nil {
			debounceTimer = time.NewTimer(fw.cfg.debounce)
			debounceCh = debounceTimer.C
		} else {
			debounceTimer.Reset(fw.cfg.debounce)
		}
	}

	// Poll fallback: re-resolve the real path on a ticker so Kubernetes
	// `..data` symlink swaps are detected even when fsnotify drops the event.
	var pollCh <-chan time.Time
	if fw.cfg.pollInterval > 0 {
		ticker := time.NewTicker(fw.cfg.pollInterval)
		defer ticker.Stop()
		pollCh = ticker.C
	}

	for {
		select {
		case <-ctx.Done():
			if debounceTimer != nil {
				debounceTimer.Stop()
			}
			return nil

		case <-pollCh:
			// Resolved real path changed without (or before) a matching event:
			// the hallmark of a k8s ConfigMap/Secret symlink swap.
			if real := resolvedPath(); real != "" && real != lastResolved {
				lastResolved = real
				armDebounce()
			}

		case event, ok := <-watcher.Events:
			if !ok {
				return errors.New("config: fsnotify Events channel closed unexpectedly")
			}
			if !isRelevantEvent(event) {
				continue
			}
			// React when either the watched file's base name matches (ordinary
			// edits / atomic-rename saves) or the resolved real path changed
			// (Kubernetes ..data symlink swap, which never names the config
			// file in its events).
			matchesBase := filepath.Base(event.Name) == base
			if !matchesBase {
				if real := resolvedPath(); real == "" || real == lastResolved {
					continue
				} else {
					lastResolved = real
				}
			} else if real := resolvedPath(); real != "" {
				lastResolved = real
			}
			armDebounce()

		case watchErr, ok := <-watcher.Errors:
			if !ok {
				return errors.New("config: fsnotify Errors channel closed unexpectedly")
			}
			fw.cfg.logger.Warn("file watcher error", redact.Error(watchErr))

		case <-debounceCh:
			// Disarm and clear the timer so the next change event re-creates
			// it and re-arms debounceCh. Without resetting debounceTimer to
			// nil, every subsequent event would take the Reset branch while
			// debounceCh stayed nil — meaning only the first change ever
			// triggered a reload.
			debounceCh = nil
			debounceTimer = nil
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
// Start is intentionally one-shot: there is no fallible init before
// the signal loop (signal channel creation cannot fail in a way that
// leaves the reloader half-armed), so started is set immediately and
// a second Start — including after a clean ctx-cancel exit — always
// returns "already started". Construct a new EnvReloader to restart.
//
// If [WithImmediateLoad] was passed, an initial Load runs once before the
// signal loop so the Watchable reflects current environment state instead of
// the construction-time `initial` value. A failed immediate Load is
// non-fatal (logged; construction-time value is kept) and does not
// reset started — the signal loop still runs.
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
