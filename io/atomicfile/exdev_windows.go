//go:build windows

package atomicfile

import "syscall"

// Windows reports cross-volume rename via ERROR_NOT_SAME_DEVICE (0x11).
// The kit's atomicfile.Save consumers run on Linux containers in practice;
// this stub keeps the package buildable on Windows for tooling.
var errExdev = syscall.Errno(0x11)
