//go:build unix

package atomicfile

import (
	"errors"
	"os"
	"syscall"
)

// openReadNoFollow opens path for reading without following a final-path
// symlink. Combined with fstat on the returned descriptor this closes the
// Load TOCTOU between Lstat and ReadFile.
func openReadNoFollow(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
}

// isNoFollowSymlink reports whether err is the platform signal that the
// final path component was a symlink (O_NOFOLLOW → ELOOP on Unix).
func isNoFollowSymlink(err error) bool {
	return errors.Is(err, syscall.ELOOP)
}
