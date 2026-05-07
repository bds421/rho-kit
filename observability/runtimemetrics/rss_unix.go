//go:build linux || darwin

package runtimemetrics

import "syscall"

// readMaxRSS returns the max resident-set size in bytes from getrusage,
// or -1 if unavailable. On Linux ru_maxrss is in kilobytes; on Darwin
// it is bytes.
func readMaxRSS() int64 {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return -1
	}
	return scaleMaxRSS(ru.Maxrss)
}
