package atomicfile

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Load reads a JSON-encoded value from path. Returns the zero value of T
// if the file does not exist (first run).
func Load[T any](path string) (T, error) {
	var zero T

	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return zero, nil
	}
	if err != nil {
		return zero, fmt.Errorf("read state file: %w", err)
	}

	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		return zero, fmt.Errorf("unmarshal state: %w", err)
	}
	return v, nil
}

// Save persists a JSON-encoded value to path using atomic write
// (temp file + fsync + rename). The parent directory is created if needed.
//
// If path already exists, its file mode is preserved on the new file —
// previously the temp file's default 0600 silently replaced any operator
// chmod (e.g. a 0644 config readable by other processes). On EXDEV (temp
// and target on different filesystems, common in container bind-mounts),
// Save falls back to a copy+remove so the operation still succeeds.
func Save[T any](path string, v T) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}

	// Capture existing mode before we replace it. Missing target is fine —
	// new files will use the temp-file default (0600).
	var preserveMode os.FileMode
	if info, statErr := os.Stat(path); statErr == nil {
		preserveMode = info.Mode().Perm()
	}

	tmp, err := os.CreateTemp(dir, "state-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write temp file: %w", err)
	}

	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("sync temp file: %w", err)
	}

	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close temp file: %w", err)
	}

	if preserveMode != 0 {
		if err := os.Chmod(tmpPath, preserveMode); err != nil {
			_ = os.Remove(tmpPath)
			return fmt.Errorf("preserve file mode: %w", err)
		}
	}

	if err := os.Rename(tmpPath, path); err != nil {
		// EXDEV: temp file and target are on different filesystems. This
		// happens when the parent directory is a bind mount whose backing
		// store differs from where CreateTemp landed. Fall back to a
		// copy + remove so atomic semantics are preserved at the
		// filesystem-of-target level (the rename target is replaced
		// atomically once the copy completes; the only loss vs same-fs
		// rename is durability if the process dies mid-copy).
		var linkErr *os.LinkError
		if errors.As(err, &linkErr) && errors.Is(linkErr.Err, errExdev) {
			if copyErr := copyAndReplace(tmpPath, path, preserveMode); copyErr != nil {
				_ = os.Remove(tmpPath)
				return fmt.Errorf("rename state file (cross-device fallback): %w", copyErr)
			}
		} else {
			_ = os.Remove(tmpPath)
			return fmt.Errorf("rename state file: %w", err)
		}
	}

	// Flush the directory entry to stable storage so the new filename
	// survives a power failure on filesystems like ext4 data=ordered.
	if d, syncErr := os.Open(dir); syncErr == nil {
		if fsyncErr := d.Sync(); fsyncErr != nil {
			_ = d.Close()
			return fmt.Errorf("sync directory: %w", fsyncErr)
		}
		_ = d.Close()
	}

	return nil
}

// copyAndReplace performs a fall-back path for EXDEV: copy temp into a sibling
// of dst on dst's filesystem, then rename within that filesystem.
func copyAndReplace(src, dst string, mode os.FileMode) error {
	srcF, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = srcF.Close() }()

	dstDir := filepath.Dir(dst)
	dstTmp, err := os.CreateTemp(dstDir, "state-xdev-*.tmp")
	if err != nil {
		return err
	}
	dstTmpPath := dstTmp.Name()

	if _, err := io.Copy(dstTmp, srcF); err != nil {
		_ = dstTmp.Close()
		_ = os.Remove(dstTmpPath)
		return err
	}
	if err := dstTmp.Sync(); err != nil {
		_ = dstTmp.Close()
		_ = os.Remove(dstTmpPath)
		return err
	}
	if err := dstTmp.Close(); err != nil {
		_ = os.Remove(dstTmpPath)
		return err
	}
	if mode != 0 {
		if err := os.Chmod(dstTmpPath, mode); err != nil {
			_ = os.Remove(dstTmpPath)
			return err
		}
	}
	if err := os.Rename(dstTmpPath, dst); err != nil {
		_ = os.Remove(dstTmpPath)
		return err
	}
	_ = os.Remove(src)
	return nil
}
