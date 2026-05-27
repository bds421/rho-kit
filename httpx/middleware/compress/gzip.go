package compress

import (
	"compress/gzip"
	"io"
	"sync"
)

// GzipEncoder is the built-in [Encoder] for gzip. Construct via
// [NewGzipEncoder] so the underlying sync.Pool is wired correctly.
type GzipEncoder struct {
	level int
	pool  *sync.Pool
}

// NewGzipEncoder returns a gzip [Encoder] at the given compression
// level. Pass [gzip.DefaultCompression] (-1) for the stdlib default.
// Panics if level is outside [gzip.HuffmanOnly, gzip.BestCompression].
func NewGzipEncoder(level int) *GzipEncoder {
	if level < gzip.HuffmanOnly || level > gzip.BestCompression {
		panic("compress: NewGzipEncoder level out of range")
	}
	enc := &GzipEncoder{level: level}
	enc.pool = &sync.Pool{
		New: func() any {
			// Writer is reset per acquisition with the real underlying
			// writer; io.Discard is just a placeholder to construct it.
			w, err := gzip.NewWriterLevel(io.Discard, enc.level)
			if err != nil {
				// gzip.NewWriterLevel only errors on invalid level,
				// which we validated above.
				panic("compress: gzip.NewWriterLevel: " + err.Error())
			}
			return w
		},
	}
	return enc
}

// ContentEncoding implements [Encoder].
func (e *GzipEncoder) ContentEncoding() string { return "gzip" }

// Acquire implements [Encoder].
func (e *GzipEncoder) Acquire(w io.Writer) WriterReleaser {
	gw := e.pool.Get().(*gzip.Writer)
	gw.Reset(w)
	return newPoolWriter(gw, e.pool)
}
