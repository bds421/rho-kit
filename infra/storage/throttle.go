package storage

import (
	"io"

	"github.com/bds421/rho-kit/io/progress"
)

// NewThrottledReader wraps an io.Reader and limits read throughput to the
// specified bytes per second. Delegates to [progress.NewThrottledReader].
//
// Usage:
//
//	tr := storage.NewThrottledReader(reader, 1<<20) // 1 MiB/s
//	backend.Put(ctx, key, tr, meta)
func NewThrottledReader(r io.Reader, bytesPerSecond int64) io.Reader {
	return progress.NewThrottledReader(r, bytesPerSecond)
}
