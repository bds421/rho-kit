//go:build linux

package runtimemetrics

// On Linux, rusage.Maxrss is in kilobytes.
func scaleMaxRSS(maxrss int64) int64 { return maxrss * 1024 }
