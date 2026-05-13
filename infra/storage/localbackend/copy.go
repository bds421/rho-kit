package localbackend

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/bds421/rho-kit/infra/v2/storage"
)

// Compile-time interface compliance check.
var _ storage.Copier = (*Backend)(nil)

// Copy duplicates a file within the local filesystem.
// Uses direct file-to-file copy with atomic rename for crash safety.
//
// Honours context cancellation symmetrically with remote backends:
// ctx.Err is checked at method entry and again before the body copy.
func (b *Backend) Copy(ctx context.Context, srcKey, dstKey string) error {
	if err := ctxErr(ctx); err != nil {
		return err
	}
	if err := storage.ValidateKey(srcKey); err != nil {
		return err
	}
	if err := storage.ValidateKey(dstKey); err != nil {
		return err
	}

	srcPath, err := b.existingRegularPath(srcKey)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("localbackend: copy: %w", storage.ErrObjectNotFound)
		}
		return err
	}
	dstPath, err := b.keyPath(dstKey)
	if err != nil {
		return err
	}

	src, err := os.Open(srcPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("localbackend: copy: %w", storage.ErrObjectNotFound)
		}
		return localFileError("copy open", err)
	}
	defer func() { _ = src.Close() }()

	if err := b.rejectSymlinkPath(filepath.Dir(dstPath)); err != nil {
		return fmt.Errorf("localbackend: copy unsafe parent: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(dstPath), 0o750); err != nil {
		return localFileError("copy mkdir", err)
	}
	if err := b.rejectSymlinkPath(filepath.Dir(dstPath)); err != nil {
		return fmt.Errorf("localbackend: copy unsafe parent: %w", err)
	}

	if err := ctxErr(ctx); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(filepath.Dir(dstPath), ".tmp-*")
	if err != nil {
		return localFileError("copy temp file", err)
	}
	tmpPath := tmp.Name()

	if _, err := io.Copy(tmp, src); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return localFileError("copy write", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return localFileError("copy sync", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return localFileError("copy close", err)
	}
	if err := os.Rename(tmpPath, dstPath); err != nil {
		_ = os.Remove(tmpPath)
		return localFileError("copy rename", err)
	}
	if err := fsyncDir(filepath.Dir(dstPath)); err != nil {
		return localFileError("copy fsync dir", err)
	}

	return nil
}
