package localbackend

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/bds421/rho-kit/infra/storage"
)

// Compile-time interface compliance check.
var _ storage.Storage = (*LocalBackend)(nil)

// LocalBackend implements [storage.Storage] using the local filesystem.
// Keys are converted to relative paths within the root directory.
// Directory components are created automatically on Put.
type LocalBackend struct {
	root       string
	validators []storage.Validator
}

// Option configures a LocalBackend.
type Option func(*LocalBackend)

// WithValidators sets upload validators applied in order before every Put.
func WithValidators(validators ...storage.Validator) Option {
	return func(b *LocalBackend) {
		b.validators = append(b.validators, validators...)
	}
}

// New creates a LocalBackend rooted at dir. The directory is created if it
// does not exist. Panics if dir is empty — this catches misconfigured tests.
func New(dir string, opts ...Option) (*LocalBackend, error) {
	if dir == "" {
		panic("localbackend: root directory must not be empty")
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("localbackend: create root dir: %w", err)
	}
	b := &LocalBackend{root: dir}
	for _, o := range opts {
		o(b)
	}
	return b, nil
}

// Put writes content from r to <root>/<key>. Uses atomic write via temp file
// and rename to prevent partial writes on crash.
func (b *LocalBackend) Put(_ context.Context, key string, r io.Reader, meta storage.ObjectMeta) error {
	if err := storage.ValidateKey(key); err != nil {
		return err
	}

	validated, err := storage.ApplyValidators(r, &meta, b.validators)
	if err != nil {
		return err
	}

	path := b.keyPath(key)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("localbackend: create dirs for %q: %w", key, err)
	}

	// Atomic write: write to temp file, then rename.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return fmt.Errorf("localbackend: create temp file for %q: %w", key, err)
	}
	tmpPath := tmp.Name()

	if _, err := io.Copy(tmp, validated); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("localbackend: write %q: %w", key, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("localbackend: sync %q: %w", key, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("localbackend: close %q: %w", key, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("localbackend: rename %q: %w", key, err)
	}

	return nil
}

// Get opens <root>/<key> for reading. Caller must close the returned ReadCloser.
func (b *LocalBackend) Get(_ context.Context, key string) (io.ReadCloser, storage.ObjectMeta, error) {
	if err := storage.ValidateKey(key); err != nil {
		return nil, storage.ObjectMeta{}, err
	}

	f, err := os.Open(b.keyPath(key))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, storage.ObjectMeta{}, fmt.Errorf("localbackend: get %q: %w", key, storage.ErrObjectNotFound)
		}
		return nil, storage.ObjectMeta{}, fmt.Errorf("localbackend: get %q: %w", key, err)
	}

	meta := storage.ObjectMeta{}
	if info, statErr := f.Stat(); statErr == nil {
		meta.Size = info.Size()
	}

	return f, meta, nil
}

// Delete removes <root>/<key>. Returns nil if the file does not exist (idempotent).
func (b *LocalBackend) Delete(_ context.Context, key string) error {
	if err := storage.ValidateKey(key); err != nil {
		return err
	}

	err := os.Remove(b.keyPath(key))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("localbackend: delete %q: %w", key, err)
	}
	return nil
}

// Exists reports whether <root>/<key> exists on disk.
func (b *LocalBackend) Exists(_ context.Context, key string) (bool, error) {
	if err := storage.ValidateKey(key); err != nil {
		return false, err
	}

	_, err := os.Stat(b.keyPath(key))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("localbackend: exists %q: %w", key, err)
	}
	return true, nil
}

// keyPath returns the absolute filesystem path for the given key.
// filepath.Join cleans ".." traversal attempts.
func (b *LocalBackend) keyPath(key string) string {
	return filepath.Join(b.root, filepath.FromSlash(key))
}
