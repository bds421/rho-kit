package progress

import (
	"io"
	"time"
)

// ProgressFunc is called periodically during reads to report progress.
// bytesRead is the cumulative number of bytes transferred so far.
// totalBytes is the expected total, or -1 if unknown.
type ProgressFunc func(bytesRead int64, totalBytes int64)

// ReaderOption configures a [NewReader].
type ReaderOption func(*readerConfig)

type readerConfig struct {
	throttle time.Duration
	minDelta int64
}

// WithThrottle coalesces progress callbacks so fn is invoked at most once
// per d. The first read always fires fn; subsequent reads fire only after
// d has elapsed since the previous fire, plus once on EOF.
//
// Use this for multi-GB transfers where the default per-Read callback rate
// (one per ~8 KiB chunk) dominates CPU. d=100*time.Millisecond is a sane
// upper bound for human-visible progress bars.
//
// d <= 0 disables throttling (callback fires on every Read).
func WithThrottle(d time.Duration) ReaderOption {
	return func(c *readerConfig) { c.throttle = d }
}

// WithMinDelta coalesces progress callbacks so fn is invoked only after at
// least bytes have been read since the previous invocation. The final
// callback (on EOF) is always delivered so consumers see the completed
// total.
//
// bytes <= 0 disables byte-delta throttling.
func WithMinDelta(bytes int64) ReaderOption {
	return func(c *readerConfig) { c.minDelta = bytes }
}

// NewReader wraps an io.Reader and calls fn after every Read.
// If fn is nil, the original reader is returned unwrapped (no-op passthrough)
// to avoid unnecessary allocation and indirection.
//
// progressReader is NOT safe for concurrent Read calls — like the io.Reader
// contract itself. The internal counters are mutated without synchronization.
// Wrap it in your own lock if multiple goroutines must share a single
// reader (rare in practice).
//
// Pass [WithThrottle] or [WithMinDelta] to coalesce callbacks for
// high-volume transfers; without them, fn is invoked once per non-zero
// Read which can dominate CPU on multi-GB streams.
//
// Usage:
//
//	pr := progress.NewReader(reader, totalSize, func(n, total int64) {
//	    fmt.Printf("%.0f%%\n", float64(n)/float64(total)*100)
//	}, progress.WithThrottle(100*time.Millisecond))
//	io.Copy(dst, pr)
func NewReader(r io.Reader, totalBytes int64, fn ProgressFunc, opts ...ReaderOption) io.Reader {
	if fn == nil {
		return r
	}
	cfg := readerConfig{}
	for _, opt := range opts {
		if opt == nil {
			panic("progress: Reader option must not be nil")
		}
		opt(&cfg)
	}
	return &progressReader{
		r:     r,
		total: totalBytes,
		fn:    fn,
		cfg:   cfg,
	}
}

type progressReader struct {
	r          io.Reader
	read       int64
	total      int64
	fn         ProgressFunc
	cfg        readerConfig
	lastFireAt time.Time
	lastFireB  int64
}

func (p *progressReader) Read(buf []byte) (int, error) {
	n, err := p.r.Read(buf)
	p.read += int64(n)
	if n == 0 && err == nil {
		return n, err
	}

	shouldFire := true
	if p.cfg.throttle > 0 || p.cfg.minDelta > 0 {
		shouldFire = false
		now := time.Now()
		if p.lastFireAt.IsZero() {
			shouldFire = true
		} else {
			if p.cfg.throttle > 0 && now.Sub(p.lastFireAt) >= p.cfg.throttle {
				shouldFire = true
			}
			if p.cfg.minDelta > 0 && p.read-p.lastFireB >= p.cfg.minDelta {
				shouldFire = true
			}
		}
		// Always fire the final callback so the consumer sees completion.
		if err != nil {
			shouldFire = true
		}
		if shouldFire {
			p.lastFireAt = now
			p.lastFireB = p.read
		}
	}

	if shouldFire && (n > 0 || err != nil) {
		p.fn(p.read, p.total)
	}
	return n, err
}
