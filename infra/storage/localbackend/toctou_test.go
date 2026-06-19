package localbackend

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/v2/storage"
)

// TestTOCTOU_EscapingSymlinkComponentRefused pins the os.Root traversal
// guarantee that closes the symlink TOCTOU race: a pre-existing path component
// that is a symlink escaping the configured root must be refused by the
// os.Root-confined operation on BOTH the write and read paths. This is the
// property that makes the defense race-free — os.Root re-validates every
// component at the time of the syscall instead of pre-checking with Lstat and
// then performing the operation through a separate, swappable path.
func TestTOCTOU_EscapingSymlinkComponentRefused(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("put refuses write through escaping symlink dir component", func(t *testing.T) {
		t.Parallel()
		b := newBackend(t)
		outside := t.TempDir()
		// "escape" is a directory component on the key path that points
		// outside the root. An os.Root-confined write must refuse to
		// traverse it, so the bytes never land outside.
		require.NoError(t, os.Symlink(outside, filepath.Join(b.root, "escape")))

		err := b.Put(ctx, "escape/owned.txt", bytes.NewReader([]byte("owned")), storage.ObjectMeta{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unsafe")

		_, statErr := os.Stat(filepath.Join(outside, "owned.txt"))
		assert.True(t, errors.Is(statErr, os.ErrNotExist), "write escaped the root")
	})

	t.Run("get refuses read through escaping symlink dir component", func(t *testing.T) {
		t.Parallel()
		b := newBackend(t)
		outside := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o600))
		require.NoError(t, os.Symlink(outside, filepath.Join(b.root, "escape")))

		_, _, err := b.Get(ctx, "escape/secret.txt")
		require.Error(t, err)
		// The object resolved through an escaping component is treated as not
		// found rather than handing back data from outside the root.
		assert.ErrorIs(t, err, storage.ErrObjectNotFound)
	})

	t.Run("copy refuses write through escaping symlink dir component", func(t *testing.T) {
		t.Parallel()
		b := newBackend(t)
		require.NoError(t, b.Put(ctx, "source.txt", bytes.NewReader([]byte("src")), storage.ObjectMeta{}))
		outside := t.TempDir()
		require.NoError(t, os.Symlink(outside, filepath.Join(b.root, "escape")))

		err := b.Copy(ctx, "source.txt", "escape/copy.txt")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unsafe")

		_, statErr := os.Stat(filepath.Join(outside, "copy.txt"))
		assert.True(t, errors.Is(statErr, os.ErrNotExist), "copy escaped the root")
	})

	t.Run("delete refuses unlink through escaping symlink dir component", func(t *testing.T) {
		t.Parallel()
		b := newBackend(t)
		outside := t.TempDir()
		victim := filepath.Join(outside, "victim.txt")
		require.NoError(t, os.WriteFile(victim, []byte("victim"), 0o600))
		require.NoError(t, os.Symlink(outside, filepath.Join(b.root, "escape")))

		// Delete is idempotent on a path it cannot reach, but it must never
		// unlink a file outside the root by traversing the escaping component.
		_ = b.Delete(ctx, "escape/victim.txt")

		_, statErr := os.Stat(victim)
		require.NoError(t, statErr, "delete unlinked a file outside the root")
	})

	t.Run("exists does not resolve object through escaping symlink dir component", func(t *testing.T) {
		t.Parallel()
		b := newBackend(t)
		outside := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o600))
		require.NoError(t, os.Symlink(outside, filepath.Join(b.root, "escape")))

		exists, err := b.Exists(ctx, "escape/secret.txt")
		require.NoError(t, err)
		assert.False(t, exists, "exists reported an object reachable only outside the root")
	})
}
