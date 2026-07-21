//go:build windows

package atomicfile

import "os"

// openReadNoFollow opens path for reading. Windows has no portable O_NOFOLLOW
// equivalent for CreateFile without FILE_FLAG_OPEN_REPARSE_POINT + special
// handling; Load still Lstats first and re-checks ModeSymlink via fstat after
// open, which is the best portable guarantee without a CGO/syscall path.
func openReadNoFollow(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY, 0)
}

// isNoFollowSymlink is always false on Windows; symlink refusal relies on
// the ModeSymlink check against the opened file's Stat result / Lstat.
func isNoFollowSymlink(err error) bool {
	return false
}
