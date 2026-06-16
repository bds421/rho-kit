package localbackend

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"syscall"

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/infra/v2/storage"
)

// Compile-time interface compliance check.
var _ storage.Copier = (*Backend)(nil)

// copyFileError maps a copy write/sync failure to the appropriate kit error.
// Disk-full (ENOSPC) is translated to [storage.ErrInsufficientCapacity] (the
// retryable 507 sentinel) exactly as Put does, so a disk-full Copy is not
// mistaken for a permanent failure; any other error falls through to the
// redacted localFileError mapping.
func copyFileError(op string, err error) error {
	if errors.Is(err, syscall.ENOSPC) {
		return wrapInsufficientCapacity(op, err)
	}
	return localFileError(op, err)
}

// Copy duplicates a file within the local filesystem.
// Uses direct file-to-file copy with atomic rename for crash safety.
//
// Source open, destination directory creation, and the temp-file rename all go
// through a single [os.Root] confined to the backend's root, so neither the
// read of srcKey nor the write of dstKey can traverse a symlink escaping the
// root even if a path component is swapped concurrently.
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

	srcRel, err := b.keyRel(srcKey)
	if err != nil {
		return err
	}
	dstRel, err := b.keyRel(dstKey)
	if err != nil {
		return err
	}

	root, err := b.openRoot()
	if err != nil {
		return redact.WrapError("localbackend: unsafe root", err)
	}
	defer func() { _ = root.Close() }()

	if err := b.ensureRegular(root, srcRel); err != nil {
		// ensureRegular wraps the not-exist sentinel (including for implicit
		// directory keys and escaping components), so match via errors.Is.
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("localbackend: copy: %w", storage.ErrObjectNotFound)
		}
		return err
	}

	src, err := root.Open(srcRel)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || isEscapeError(err) {
			return fmt.Errorf("localbackend: copy: %w", storage.ErrObjectNotFound)
		}
		return localFileError("copy open", err)
	}
	defer func() { _ = src.Close() }()

	dstRoot, err := b.openDestDir(root, path.Dir(dstRel))
	if err != nil {
		return err
	}
	defer func() { _ = dstRoot.Close() }()

	if err := ctxErr(ctx); err != nil {
		return err
	}

	dstBase := path.Base(dstRel)
	tmpName, tmp, err := createTempIn(dstRoot)
	if err != nil {
		return mapUnsafeOrFileError("copy temp file", err)
	}

	if _, err := io.Copy(tmp, src); err != nil {
		_ = tmp.Close()
		_ = dstRoot.Remove(tmpName)
		return copyFileError("copy write", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = dstRoot.Remove(tmpName)
		return copyFileError("copy sync", err)
	}
	if err := tmp.Close(); err != nil {
		_ = dstRoot.Remove(tmpName)
		return localFileError("copy close", err)
	}
	if err := dstRoot.Rename(tmpName, dstBase); err != nil {
		_ = dstRoot.Remove(tmpName)
		return localFileError("copy rename", err)
	}
	if err := fsyncDir(dstRoot); err != nil {
		return localFileError("copy fsync dir", err)
	}

	return nil
}
