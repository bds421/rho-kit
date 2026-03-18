package storage

import (
	"io"

	"github.com/bds421/rho-kit/io/progress"
)

// ProgressFunc is called periodically during Put/Get to report progress.
// This is an alias for [progress.ProgressFunc].
type ProgressFunc = progress.ProgressFunc

// NewProgressReader wraps an io.Reader and calls fn after every Read.
// Delegates to [progress.NewProgressReader].
//
// Usage:
//
//	pr := storage.NewProgressReader(reader, totalSize, func(n, total int64) {
//	    fmt.Printf("%.0f%%\n", float64(n)/float64(total)*100)
//	})
//	backend.Put(ctx, key, pr, meta)
func NewProgressReader(r io.Reader, totalBytes int64, fn ProgressFunc) io.Reader {
	return progress.NewProgressReader(r, totalBytes, fn)
}
