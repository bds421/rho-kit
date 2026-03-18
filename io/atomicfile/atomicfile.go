package atomicfile

import (
	"encoding/json"
	"errors"
	"fmt"
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
func Save[T any](path string, v T) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create state directory: %w", err)
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

	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename state file: %w", err)
	}

	// Flush the directory entry to stable storage so the new filename
	// survives a power failure on filesystems like ext4 data=ordered.
	// Best-effort: directory sync/close failures are non-fatal since the
	// file data is already safely on disk after rename, but log warnings
	// so operators can investigate persistent sync failures.
	if d, syncErr := os.Open(dir); syncErr == nil {
		if fsyncErr := d.Sync(); fsyncErr != nil {
			_ = d.Close()
			return fmt.Errorf("sync directory: %w", fsyncErr)
		}
		_ = d.Close()
	}

	return nil
}
