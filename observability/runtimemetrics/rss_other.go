//go:build !linux && !darwin

package runtimemetrics

// readMaxRSS returns -1 on platforms that don't expose getrusage's
// max-RSS field; the collector simply omits the metric.
func readMaxRSS() int64 { return -1 }
