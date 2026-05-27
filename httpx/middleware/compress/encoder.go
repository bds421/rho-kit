package compress

import (
	"io"
	"sync"
)

// Encoder is the pluggable compression-algorithm contract. Each Encoder
// owns one Content-Encoding token (e.g. "gzip", "br", "zstd") and a
// factory for streaming writers.
//
// Implementations must be safe for concurrent use across goroutines —
// the middleware calls [Encoder.Acquire] on every eligible response.
// Returned writers are reset for the supplied io.Writer; callers MUST
// call [WriterReleaser.Release] (typically via defer) to return the
// writer to the pool.
type Encoder interface {
	// ContentEncoding returns the token used in the Accept-Encoding
	// negotiation and the Content-Encoding response header. Lowercase.
	ContentEncoding() string

	// Acquire returns a streaming writer that wraps w. The returned
	// WriterReleaser MUST be Released after the response is finalised.
	Acquire(w io.Writer) WriterReleaser
}

// WriterReleaser is a Writer that returns underlying resources to a
// pool on Release. Implementations typically also implement io.Closer
// to flush buffered bytes; the middleware closes before releasing.
type WriterReleaser interface {
	io.WriteCloser
	// Release returns the writer to its owner's pool. Calling Release
	// without first calling Close may leak buffered bytes; the
	// middleware always Closes first.
	Release()
}

// poolWriter is a helper for Encoder implementations backed by sync.Pool.
// Embed this type or compose it to satisfy WriterReleaser with a single
// Release that returns to the supplied pool.
type poolWriter struct {
	io.WriteCloser
	pool *sync.Pool
}

func (p *poolWriter) Release() {
	if p.pool != nil && p.WriteCloser != nil {
		p.pool.Put(p.WriteCloser)
		p.WriteCloser = nil
	}
}

// newPoolWriter wraps wc with a Release hook that returns wc to pool.
// Callers Reset wc before returning; this helper only manages pool
// lifecycle.
func newPoolWriter(wc io.WriteCloser, pool *sync.Pool) WriterReleaser {
	return &poolWriter{WriteCloser: wc, pool: pool}
}
