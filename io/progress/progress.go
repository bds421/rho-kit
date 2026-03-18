package progress

import "io"

// ProgressFunc is called periodically during reads to report progress.
// bytesRead is the cumulative number of bytes transferred so far.
// totalBytes is the expected total, or -1 if unknown.
type ProgressFunc func(bytesRead int64, totalBytes int64)

// NewProgressReader wraps an io.Reader and calls fn after every Read.
// If fn is nil, the original reader is returned unwrapped (no-op passthrough)
// to avoid unnecessary allocation and indirection.
//
// Usage:
//
//	pr := progress.NewProgressReader(reader, totalSize, func(n, total int64) {
//	    fmt.Printf("%.0f%%\n", float64(n)/float64(total)*100)
//	})
//	io.Copy(dst, pr)
func NewProgressReader(r io.Reader, totalBytes int64, fn ProgressFunc) io.Reader {
	if fn == nil {
		return r
	}
	return &progressReader{r: r, total: totalBytes, fn: fn}
}

type progressReader struct {
	r     io.Reader
	read  int64
	total int64
	fn    ProgressFunc
}

func (p *progressReader) Read(buf []byte) (int, error) {
	n, err := p.r.Read(buf)
	p.read += int64(n)
	if n > 0 {
		p.fn(p.read, p.total)
	}
	return n, err
}
