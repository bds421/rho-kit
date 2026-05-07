//go:build darwin

package runtimemetrics

// On Darwin, rusage.Maxrss is in bytes.
func scaleMaxRSS(maxrss int64) int64 { return maxrss }
