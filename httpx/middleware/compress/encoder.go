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
//
// Flush is required so mid-stream Flush() from the response wrapper
// can push encoder-buffered bytes (e.g. gzip.Writer) to the wire for
// SSE / chunked streaming. A no-op Flush is acceptable for encoders
// that buffer nothing between Writes.
type WriterReleaser interface {
	io.WriteCloser
	// Flush pushes any encoder-buffered bytes to the underlying
	// writer. Called by compressWriter.Flush in compressed mode.
	Flush() error
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

// Flush delegates to the underlying writer when it implements
// Flush() error (gzip.Writer, flate.Writer, …). Without this method
// the interface-typed WriteCloser embed would hide the dynamic
// Flush, and mid-stream Flush would never reach the encoder.
func (p *poolWriter) Flush() error {
	if p == nil || p.WriteCloser == nil {
		return nil
	}
	if f, ok := p.WriteCloser.(interface{ Flush() error }); ok {
		return f.Flush()
	}
	return nil
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
