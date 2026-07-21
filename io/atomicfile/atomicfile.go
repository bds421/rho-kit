package atomicfile

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// MaxLoadBytes caps the state file Load will read. State files are
// operator-controlled, but a shared filesystem co-tenant could grow
// one without bound; this cap stops a slow OOM cascade if that
// happens. 16 MiB is comfortably above any realistic state file
// (BufferedPublisher state, snapshots, persisted counters) while
// orders-of-magnitude short of a host's swap budget. The cap is a
// fixed package constant; [Load] enforces it for every call.
const MaxLoadBytes = 16 * 1024 * 1024

// Load reads a JSON-encoded value from path. Returns the zero value of T
// if the file does not exist (first run). The existence flag lets
// callers distinguish "file missing" from "file present and decoded as
// zero-value" (e.g. an integer counter that was legitimately persisted
// as 0).
//
// Use [LoadOrZero] when the distinction is irrelevant.
//
// Load opens the path once (O_NOFOLLOW on Unix), fstats the open
// descriptor for the mode/size checks, and reads through a
// MaxLoadBytes-capped LimitReader so a TOCTOU swap between Lstat and
// ReadFile cannot redirect the read through a symlink or grow the file
// past the cap after the size check.
func Load[T any](path string) (value T, exists bool, err error) {
	if err := rejectSymlinkAncestors(path); err != nil {
		return value, false, fmt.Errorf("unsafe state path: %w", err)
	}

	// Fast path for missing files (common on first run) without opening.
	if _, statErr := os.Lstat(path); statErr != nil {
		if errors.Is(statErr, os.ErrNotExist) {
			return value, false, nil
		}
		// Fall through to openReadNoFollow so permission/type errors share
		// one code path; a racing create is fine — open will see it.
	}

	f, openErr := openReadNoFollow(path)
	if openErr != nil {
		if errors.Is(openErr, os.ErrNotExist) {
			return value, false, nil
		}
		// O_NOFOLLOW reports ELOOP (or platform equivalent) when the final
		// path component is a symlink. Map that to the same refusal text
		// the Lstat path used so callers/tests stay stable.
		if isNoFollowSymlink(openErr) {
			return value, false, errors.New("refusing to read through symlink")
		}
		return value, false, fileError("open state file", openErr)
	}
	defer func() { _ = f.Close() }()

	info, statErr := f.Stat()
	if statErr != nil {
		return value, false, fileError("stat state file", statErr)
	}
	// On platforms without O_NOFOLLOW, re-check ModeSymlink via the
	// opened path's Lstat (best-effort; the open already raced).
	if info.Mode()&os.ModeSymlink != 0 {
		return value, false, errors.New("refusing to read through symlink")
	}
	if info.Size() > MaxLoadBytes {
		return value, false, fmt.Errorf("state file exceeds %d bytes (got %d)", MaxLoadBytes, info.Size())
	}

	// Read at most MaxLoadBytes+1 so a file that grows after Stat still
	// cannot pull unbounded bytes into memory.
	limited := io.LimitReader(f, int64(MaxLoadBytes)+1)
	data, readErr := io.ReadAll(limited)
	if readErr != nil {
		return value, false, fileError("read state file", readErr)
	}
	if int64(len(data)) > MaxLoadBytes {
		return value, false, fmt.Errorf("state file exceeds %d bytes (got >%d)", MaxLoadBytes, MaxLoadBytes)
	}

	if uErr := json.Unmarshal(data, &value); uErr != nil {
		var zero T
		return zero, false, fmt.Errorf("unmarshal state: %w", uErr)
	}
	return value, true, nil
}

// LoadOrZero is the convenience wrapper that drops the exists flag.
// Use this when "missing" and "decoded zero-value" are interchangeable
// for your caller.
func LoadOrZero[T any](path string) (T, error) {
	v, _, err := Load[T](path)
	return v, err
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
	if err := rejectSymlinkAncestors(path); err != nil {
		return fmt.Errorf("unsafe state path: %w", err)
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fileError("create state directory", err)
	}
	if err := rejectSymlinkAncestors(path); err != nil {
		return fmt.Errorf("unsafe state path: %w", err)
	}

	// Refuse to follow a symlink at the destination. An attacker with
	// write access to the parent dir could otherwise plant a symlink
	// at `path` pointing at a sensitive target (e.g. /etc/passwd) and
	// trick Save into clobbering it. lstat does not follow links, so
	// this catches the case before we open the temp file.
	info, lerr := os.Lstat(path)
	if lerr != nil && !errors.Is(lerr, os.ErrNotExist) {
		return fileError("stat state file", lerr)
	}
	if lerr == nil && info.Mode()&os.ModeSymlink != 0 {
		return errors.New("refusing to write through symlink")
	}

	// Capture existing mode before we replace it. Missing target is fine —
	// new files will use the temp-file default (0600). Use Lstat so we
	// inspect the path itself, not a symlink target.
	var preserveMode os.FileMode
	if lerr == nil {
		preserveMode = info.Mode().Perm()
	}

	tmp, err := os.CreateTemp(dir, "state-*.tmp")
	if err != nil {
		return fileError("create temp file", err)
	}
	tmpPath := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fileError("write temp file", err)
	}

	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fileError("sync temp file", err)
	}

	// Chmod via the open fd (not the path) so a concurrent symlink plant
	// in the same directory cannot redirect the mode change.
	if preserveMode != 0 {
		if err := tmp.Chmod(preserveMode); err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmpPath)
			return fileError("preserve file mode", err)
		}
	}

	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fileError("close temp file", err)
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
			return fileError("rename state file", err)
		}
	}

	// Flush the directory entry to stable storage so the new filename
	// survives a power failure on filesystems like ext4 data=ordered.
	// Directory fsync is best-effort: the rename already succeeded, so a
	// failure here must not make Save appear to fail (callers would retry
	// and rewrite). Open and Sync failures are treated the same way —
	// durability is not guaranteed, but the file content is in place.
	if d, syncErr := os.Open(dir); syncErr == nil {
		_ = d.Sync()
		_ = d.Close()
	}

	return nil
}

// copyAndReplace performs a fall-back path for EXDEV: copy temp into a sibling
// of dst on dst's filesystem, then rename within that filesystem.
func copyAndReplace(src, dst string, mode os.FileMode) error {
	srcF, err := os.Open(src)
	if err != nil {
		return fileError("open temp file", err)
	}
	defer func() { _ = srcF.Close() }()

	dstDir := filepath.Dir(dst)
	if err := rejectSymlinkAncestors(dst); err != nil {
		return err
	}
	dstTmp, err := os.CreateTemp(dstDir, "state-xdev-*.tmp")
	if err != nil {
		return fileError("create temp file", err)
	}
	dstTmpPath := dstTmp.Name()

	if _, err := io.Copy(dstTmp, srcF); err != nil {
		_ = dstTmp.Close()
		_ = os.Remove(dstTmpPath)
		return fileError("copy temp file", err)
	}
	if err := dstTmp.Sync(); err != nil {
		_ = dstTmp.Close()
		_ = os.Remove(dstTmpPath)
		return fileError("sync temp file", err)
	}
	if err := dstTmp.Close(); err != nil {
		_ = os.Remove(dstTmpPath)
		return fileError("close temp file", err)
	}
	if mode != 0 {
		if err := os.Chmod(dstTmpPath, mode); err != nil {
			_ = os.Remove(dstTmpPath)
			return fileError("preserve file mode", err)
		}
	}
	if err := os.Rename(dstTmpPath, dst); err != nil {
		_ = os.Remove(dstTmpPath)
		return fileError("replace state file", err)
	}
	_ = os.Remove(src)
	return nil
}

func rejectSymlinkAncestors(path string) error {
	cur, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		return errors.New("state path is invalid")
	}
	cur = filepath.Clean(cur)
	for {
		info, err := os.Lstat(cur)
		if errors.Is(err, os.ErrNotExist) {
			// Missing descendants are fine: Save will create them after we
			// verify the existing ancestors are not symlinks.
		} else if err != nil {
			return fileError("inspect state path", err)
		} else {
			if info.Mode()&os.ModeSymlink != 0 {
				// Darwin commonly exposes root-level compatibility
				// links such as /var -> /private/var. Reject symlinks
				// below the filesystem root, where service/user-writable
				// state directories can be pivoted by an attacker.
				parent := filepath.Dir(cur)
				if filepath.Dir(parent) == parent {
					cur = parent
					continue
				}
				return errors.New("state path component is a symlink")
			}
			if !info.IsDir() {
				return errors.New("state path component is not a directory")
			}
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return nil
		}
		cur = parent
	}
}

func fileError(op string, err error) error {
	switch {
	case errors.Is(err, os.ErrPermission):
		return fmt.Errorf("%s: %w", op, os.ErrPermission)
	case errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("%s: %w", op, os.ErrNotExist)
	case errors.Is(err, os.ErrExist):
		return fmt.Errorf("%s: %w", op, os.ErrExist)
	case errors.Is(err, os.ErrClosed):
		return fmt.Errorf("%s: %w", op, os.ErrClosed)
	case errors.Is(err, os.ErrInvalid):
		return fmt.Errorf("%s: %w", op, os.ErrInvalid)
	default:
		return fmt.Errorf("%s: operation failed", op)
	}
}
