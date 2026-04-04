package config

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetSecretPath_Set(t *testing.T) {
	t.Setenv("TEST_CRED_FILE", "/run/secrets/cred")
	assert.Equal(t, "/run/secrets/cred", GetSecretPath("TEST_CRED"))
}

func TestGetSecretPath_NotSet(t *testing.T) {
	assert.Empty(t, GetSecretPath("NONEXISTENT_VAR"))
}

func TestSecretWatcher_NoFilePath_IsNoOp(t *testing.T) {
	w := NewWatchable("initial")
	sw := NewSecretWatcher("NONEXISTENT_SECRET", w)

	assert.False(t, sw.Active())

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := sw.Start(ctx)
	assert.NoError(t, err)
	assert.Equal(t, "initial", w.Get())
}

func TestSecretWatcher_DetectsFileChange(t *testing.T) {
	dir := t.TempDir()
	secretFile := filepath.Join(dir, "password.txt")
	require.NoError(t, os.WriteFile(secretFile, []byte("old-password\n"), 0600))

	t.Setenv("TEST_ROTATE_PASSWORD_FILE", secretFile)

	w := NewWatchable("old-password")
	sw := NewSecretWatcher("TEST_ROTATE_PASSWORD", w,
		WithDebounce(20*time.Millisecond),
	)

	assert.True(t, sw.Active())

	changed := make(chan string, 1)
	w.OnChange(func(_, newVal string) {
		changed <- newVal
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = sw.Start(ctx) }()

	// Wait for watcher to initialize.
	time.Sleep(50 * time.Millisecond)

	// Write new secret content.
	require.NoError(t, os.WriteFile(secretFile, []byte("new-password\n"), 0600))

	select {
	case val := <-changed:
		assert.Equal(t, "new-password", val)
	case <-time.After(2 * time.Second):
		t.Fatal("secret change not detected within timeout")
	}
}

func TestSecretWatcher_AtomicFileReplace(t *testing.T) {
	// Simulates the Kubernetes secret rotation pattern: write new content
	// to a temp file, then rename over the original. This is detected by
	// fsnotify as a Create event on the directory.
	dir := t.TempDir()
	secretFile := filepath.Join(dir, "password.txt")
	require.NoError(t, os.WriteFile(secretFile, []byte("password-v1"), 0600))

	t.Setenv("TEST_ATOMIC_SECRET_FILE", secretFile)

	w := NewWatchable("password-v1")
	sw := NewSecretWatcher("TEST_ATOMIC_SECRET", w,
		WithDebounce(20*time.Millisecond),
	)

	changed := make(chan string, 1)
	w.OnChange(func(_, newVal string) {
		changed <- newVal
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = sw.Start(ctx) }()
	time.Sleep(50 * time.Millisecond)

	// Atomic replace: write to temp, rename over original.
	tmpFile := filepath.Join(dir, "password.txt.tmp")
	require.NoError(t, os.WriteFile(tmpFile, []byte("password-v2"), 0600))
	require.NoError(t, os.Rename(tmpFile, secretFile))

	select {
	case val := <-changed:
		assert.Equal(t, "password-v2", val)
	case <-time.After(2 * time.Second):
		t.Fatal("atomic file replace not detected within timeout")
	}
}

func TestSecretWatcher_BadFileKeepsOldValue(t *testing.T) {
	dir := t.TempDir()
	secretFile := filepath.Join(dir, "password.txt")
	require.NoError(t, os.WriteFile(secretFile, []byte("good-password"), 0600))

	t.Setenv("TEST_BADFILE_SECRET_FILE", secretFile)

	w := NewWatchable("good-password")
	sw := NewSecretWatcher("TEST_BADFILE_SECRET", w,
		WithDebounce(20*time.Millisecond),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = sw.Start(ctx) }()
	time.Sleep(50 * time.Millisecond)

	// Remove the file — next reload should fail and keep old value.
	require.NoError(t, os.Remove(secretFile))

	// Give watcher time to detect and attempt reload.
	time.Sleep(200 * time.Millisecond)

	assert.Equal(t, "good-password", w.Get())
}
