//go:build unix

package atomicfile

import "syscall"

var errExdev = syscall.EXDEV
