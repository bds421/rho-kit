package localbackend

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/bds421/rho-kit/infra/v2/storage"
)

// Compile-time interface compliance check.
var _ storage.Storage = (*Backend)(nil)

// Backend implements [storage.Storage] using the local filesystem.
// Keys are converted to relative paths within the root directory.
// Directory components are created automatically on Put.
type Backend struct {
	root       string
	validators []storage.Validator
}

// Option configures a Backend.
type Option func(*Backend)

// WithValidators sets upload validators applied in order before every Put.
func WithValidators(validators ...storage.Validator) Option {
	copied := storage.CloneValidators(validators...)
	return func(b *Backend) {
		b.validators = storage.AppendValidators(b.validators, copied...)
	}
}

// New creates a Backend rooted at dir. The directory is created if it
// does not exist. Panics if dir is empty — this catches misconfigured tests.
func New(dir string, opts ...Option) (*Backend, error) {
	if dir == "" {
		panic("localbackend: root directory must not be empty")
	}
	absRoot, err := filepath.Abs(dir)
	if err != nil {
		return nil, localPathError("resolve root dir")
	}
	if err := os.MkdirAll(absRoot, 0o750); err != nil {
		return nil, localFileError("create root dir", err)
	}
	realRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return nil, localFileError("resolve root symlinks", err)
	}
	b := &Backend{root: realRoot}
	for _, o := range opts {
		if o == nil {
			panic("localbackend: option must not be nil")
		}
		o(b)
	}
	return b, nil
}

// Put writes content from r to <root>/<key>. Uses atomic write via temp file
// and rename to prevent partial writes on crash.
func (b *Backend) Put(ctx context.Context, key string, r io.Reader, meta storage.ObjectMeta) error {
	if err := storage.ValidateKey(key); err != nil {
		return err
	}

	validated, err := storage.ApplyValidators(ctx, r, &meta, b.validators)
	if err != nil {
		return err
	}
	if len(b.validators) > 0 {
		defer func() { _ = storage.CloseValidatedReader(validated) }()
	}
	if err := storage.ValidateObjectMeta(meta); err != nil {
		return err
	}

	path, err := b.keyPath(key)
	if err != nil {
		return err
	}
	if err := b.rejectSymlinkPath(filepath.Dir(path)); err != nil {
		return fmt.Errorf("localbackend: unsafe parent: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return localFileError("create dirs", err)
	}
	if err := b.rejectSymlinkPath(filepath.Dir(path)); err != nil {
		return fmt.Errorf("localbackend: unsafe parent: %w", err)
	}

	// Atomic write: write to temp file, then rename.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return localFileError("create temp file", err)
	}
	tmpPath := tmp.Name()

	if _, err := io.Copy(tmp, validated); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		if errors.Is(err, syscall.ENOSPC) {
			return wrapInsufficientCapacity("write object", err)
		}
		return localFileError("write object", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		if errors.Is(err, syscall.ENOSPC) {
			return wrapInsufficientCapacity("sync object", err)
		}
		return localFileError("sync object", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return localFileError("close object", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return localFileError("rename object", err)
	}
	// rename(2) on Linux is durable across crashes only if the containing
	// directory is also fsynced. Without this step a crash after rename but
	// before the directory entry is flushed can leave the file with stale or
	// zero contents — silent data loss for an operation that just returned ok.
	if err := fsyncDir(filepath.Dir(path)); err != nil {
		return localFileError("fsync object dir", err)
	}

	return nil
}

// fsyncDir opens dir read-only and calls Sync on it. Best-effort on platforms
// where directory fsync isn't required (or is a no-op).
func fsyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	syncErr := d.Sync()
	closeErr := d.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

// Get opens <root>/<key> for reading. Caller must close the returned ReadCloser.
func (b *Backend) Get(_ context.Context, key string) (io.ReadCloser, storage.ObjectMeta, error) {
	if err := storage.ValidateKey(key); err != nil {
		return nil, storage.ObjectMeta{}, err
	}

	path, err := b.existingRegularPath(key)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, storage.ObjectMeta{}, fmt.Errorf("localbackend: get: %w", storage.ErrObjectNotFound)
		}
		return nil, storage.ObjectMeta{}, err
	}

	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, storage.ObjectMeta{}, fmt.Errorf("localbackend: get: %w", storage.ErrObjectNotFound)
		}
		return nil, storage.ObjectMeta{}, localFileError("get object", err)
	}

	meta := storage.ObjectMeta{}
	if info, statErr := f.Stat(); statErr == nil {
		meta.Size = info.Size()
	}

	return f, meta, nil
}

// Delete removes <root>/<key>. Returns nil if the file does not exist (idempotent).
func (b *Backend) Delete(_ context.Context, key string) error {
	if err := storage.ValidateKey(key); err != nil {
		return err
	}

	path, err := b.keyPath(key)
	if err != nil {
		return err
	}
	if err := b.rejectSymlinkPath(filepath.Dir(path)); err != nil {
		return fmt.Errorf("localbackend: unsafe parent: %w", err)
	}

	err = os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return localFileError("delete object", err)
	}
	return nil
}

// Exists reports whether <root>/<key> exists on disk.
func (b *Backend) Exists(_ context.Context, key string) (bool, error) {
	if err := storage.ValidateKey(key); err != nil {
		return false, err
	}

	path, err := b.existingRegularPath(key)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	_, err = os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, localFileError("inspect object", err)
	}
	return true, nil
}

func (b *Backend) keyPath(key string) (string, error) {
	path := filepath.Join(b.root, filepath.FromSlash(key))
	if err := b.ensureContained(path); err != nil {
		return "", err
	}
	return path, nil
}

func (b *Backend) existingRegularPath(key string) (string, error) {
	path, err := b.keyPath(key)
	if err != nil {
		return "", err
	}
	if err := b.rejectSymlinkPath(filepath.Dir(path)); err != nil {
		return "", fmt.Errorf("localbackend: unsafe parent: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", localFileError("inspect object", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("localbackend: refusing symlink object")
	}
	return path, nil
}

func (b *Backend) rejectSymlinkPath(path string) error {
	if err := b.ensureContained(path); err != nil {
		return err
	}
	rootInfo, err := os.Lstat(b.root)
	if err != nil {
		return localFileError("inspect root", err)
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("root directory is a symlink")
	}
	if !rootInfo.IsDir() {
		return fmt.Errorf("root path is not a directory")
	}
	rel, err := filepath.Rel(b.root, path)
	if err != nil {
		return localPathError("resolve path")
	}
	if rel == "." {
		return nil
	}
	cur := b.root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return localFileError("inspect path", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("path component is a symlink")
		}
	}
	return nil
}

func (b *Backend) ensureContained(path string) error {
	rel, err := filepath.Rel(b.root, path)
	if err != nil {
		return localPathError("resolve path")
	}
	if rel == "." {
		return nil
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("path escapes root directory")
	}
	return nil
}

func localFileError(op string, err error) error {
	switch {
	case errors.Is(err, storage.ErrValidation):
		return fmt.Errorf("localbackend: %w", err)
	case errors.Is(err, os.ErrPermission):
		return fmt.Errorf("localbackend: %s: %w", op, os.ErrPermission)
	case errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("localbackend: %s: %w", op, os.ErrNotExist)
	case errors.Is(err, os.ErrExist):
		return fmt.Errorf("localbackend: %s: %w", op, os.ErrExist)
	case errors.Is(err, os.ErrClosed):
		return fmt.Errorf("localbackend: %s: %w", op, os.ErrClosed)
	case errors.Is(err, os.ErrInvalid):
		return fmt.Errorf("localbackend: %s: %w", op, os.ErrInvalid)
	default:
		return fmt.Errorf("localbackend: %s failed", op)
	}
}

func localPathError(op string) error {
	return fmt.Errorf("localbackend: %s failed", op)
}

// wrapInsufficientCapacity wraps an ENOSPC-bearing error so callers can
// match both the kit-level sentinel ([storage.ErrInsufficientCapacity],
// which carries the 507 code) and the original syscall.ENOSPC via
// errors.Is. Two %w arguments preserve both targets in the error chain.
func wrapInsufficientCapacity(op string, cause error) error {
	return fmt.Errorf("localbackend: %s: %w (cause: %w)", op, storage.ErrInsufficientCapacity, cause)
}
