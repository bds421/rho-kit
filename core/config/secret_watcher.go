package config

import (
	"context"
	"os"
	"strings"
)

// SecretWatcher watches a file-backed secret (the _FILE convention) for
// changes and updates a [Watchable] when the file content changes.
//
// Kubernetes rotates mounted secrets by atomically swapping a symlink.
// SecretWatcher detects this via fsnotify (directory-level watch catches
// symlink swaps) and re-reads the file content.
//
// If no _FILE env var is set for the given key, SecretWatcher is a no-op:
// Start blocks until ctx is cancelled and never fires.
//
// # Usage
//
//	w := config.NewWatchable("initial-password")
//	sw := config.NewSecretWatcher("RABBITMQ_PASSWORD", w)
//	w.OnChange(func(old, new string) {
//	    conn.UpdateCredentials(new)
//	})
//	// Register sw.Start on the lifecycle runner.
type SecretWatcher struct {
	key       string
	filePath  string
	watchable *Watchable[string]
	fw        *FileWatcher[string]
}

// NewSecretWatcher creates a watcher for the secret identified by key.
// It resolves the file path from KEY_FILE env var. If the env var is not
// set, the watcher is a no-op.
//
// The watchable should be seeded with the current secret value (from
// [GetSecret]). On file change, the watchable is updated with the new
// file contents (trimmed of whitespace).
func NewSecretWatcher(key string, w *Watchable[string], opts ...WatcherOption) *SecretWatcher {
	filePath := GetSecretPath(key)

	sw := &SecretWatcher{
		key:       key,
		filePath:  filePath,
		watchable: w,
	}

	if filePath != "" {
		sw.fw = NewFileWatcher[string](filePath, readSecretFile, w, opts...)
	}

	return sw
}

// Active reports whether the watcher has a file to watch.
// Returns false when no _FILE env var is set (no-op mode).
func (sw *SecretWatcher) Active() bool {
	return sw.filePath != ""
}

// Watchable returns the underlying Watchable for subscribing to changes.
func (sw *SecretWatcher) Watchable() *Watchable[string] {
	return sw.watchable
}

// Start begins watching the secret file. It blocks until ctx is cancelled.
// If no _FILE env var is set, Start blocks without watching (no-op).
// Implements lifecycle.Component-compatible signature.
func (sw *SecretWatcher) Start(ctx context.Context) error {
	if sw.fw == nil {
		<-ctx.Done()
		return nil
	}
	return sw.fw.Start(ctx)
}

// readSecretFile reads a secret file and trims whitespace, matching the
// behavior of [GetSecret].
func readSecretFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}
