package localbackend

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/bds421/rho-kit/infra/storage"
)

// Compile-time interface compliance check.
var _ storage.Copier = (*LocalBackend)(nil)

// Copy duplicates a file within the local filesystem.
// Uses direct file-to-file copy with atomic rename for crash safety.
func (b *LocalBackend) Copy(_ context.Context, srcKey, dstKey string) error {
	if err := storage.ValidateKey(srcKey); err != nil {
		return err
	}
	if err := storage.ValidateKey(dstKey); err != nil {
		return err
	}

	srcPath := b.keyPath(srcKey)
	dstPath := b.keyPath(dstKey)

	src, err := os.Open(srcPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("localbackend: copy %q: %w", srcKey, storage.ErrObjectNotFound)
		}
		return fmt.Errorf("localbackend: copy open %q: %w", srcKey, err)
	}
	defer func() { _ = src.Close() }()

	if err := os.MkdirAll(filepath.Dir(dstPath), 0o750); err != nil {
		return fmt.Errorf("localbackend: copy mkdir %q: %w", dstKey, err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(dstPath), ".tmp-*")
	if err != nil {
		return fmt.Errorf("localbackend: copy temp file: %w", err)
	}
	tmpPath := tmp.Name()

	if _, err := io.Copy(tmp, src); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("localbackend: copy write %q: %w", dstKey, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("localbackend: copy sync %q: %w", dstKey, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("localbackend: copy close %q: %w", dstKey, err)
	}
	if err := os.Rename(tmpPath, dstPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("localbackend: copy rename %q: %w", dstKey, err)
	}

	return nil
}
