package progress

import (
	"context"
	"io"
	"time"
)

// NewThrottledReader wraps an io.Reader and limits read throughput to the
// specified bytes per second. Useful for bandwidth-limited environments
// or preventing a single transfer from saturating the network.
//
// If bytesPerSecond is zero or negative, the reader is returned as-is.
//
// For rates below 10 bytes/second, the throttle may slightly exceed the
// target rate because the internal chunk size is clamped to a minimum of
// 1 byte per 100ms tick.
//
// After a pause longer than 1 second, the throttle resets its byte counter.
// This means the first chunk after idle may be delivered at full speed before
// the throttle kicks in. This is by design to prevent accumulated credit.
//
// For context-cancellation-aware throttling, use [NewThrottledReaderContext].
//
// Usage:
//
//	tr := progress.NewThrottledReader(reader, 1<<20) // 1 MiB/s
//	io.Copy(dst, tr)
func NewThrottledReader(r io.Reader, bytesPerSecond int64) io.Reader {
	if bytesPerSecond <= 0 {
		return r
	}
	return &throttledReader{
		r:              r,
		bytesPerSecond: bytesPerSecond,
		lastTime:       time.Now(),
	}
}

// NewThrottledReaderContext wraps an io.Reader with rate limiting and context
// awareness. When ctx is cancelled, the throttle delay is interrupted and the
// next Read returns the context error.
//
// If bytesPerSecond is zero or negative, the reader is returned as-is.
//
// Usage:
//
//	tr := progress.NewThrottledReaderContext(ctx, reader, 1<<20) // 1 MiB/s
//	io.Copy(dst, tr)
func NewThrottledReaderContext(ctx context.Context, r io.Reader, bytesPerSecond int64) io.Reader {
	if bytesPerSecond <= 0 {
		return r
	}
	return &throttledReader{
		r:              r,
		bytesPerSecond: bytesPerSecond,
		lastTime:       time.Now(),
		ctx:            ctx,
	}
}

type throttledReader struct {
	r              io.Reader
	bytesPerSecond int64
	bytesSent      int64
	lastTime       time.Time
	ctx            context.Context
}

func (t *throttledReader) Read(p []byte) (int, error) {
	// Check context before starting work.
	if t.ctx != nil {
		if err := t.ctx.Err(); err != nil {
			return 0, err
		}
	}

	// Cap the read size to a chunk that can be sent within ~100ms.
	maxChunk := t.bytesPerSecond / 10
	if maxChunk < 1 {
		maxChunk = 1
	}
	if int64(len(p)) > maxChunk {
		p = p[:maxChunk]
	}

	n, err := t.r.Read(p)
	t.bytesSent += int64(n)

	// Calculate how long we should have taken to send bytesSent bytes.
	// float64 has 53 bits of mantissa, so precision is exact for transfers
	// up to ~9 PiB — well beyond any practical single-stream transfer.
	expectedDuration := time.Duration(float64(t.bytesSent) / float64(t.bytesPerSecond) * float64(time.Second))
	elapsed := time.Since(t.lastTime)

	// If the reader was idle for longer than 1 second, reset the timing
	// state to prevent an unbounded burst. Without this, accumulated
	// elapsed time during idle periods would allow full-speed reads until
	// bytesSent catches up with the expected rate.
	if deficit := elapsed - expectedDuration; deficit > time.Second {
		t.lastTime = time.Now()
		t.bytesSent = int64(n)
		elapsed = 0
		expectedDuration = time.Duration(float64(t.bytesSent) / float64(t.bytesPerSecond) * float64(time.Second))
	}

	if wait := expectedDuration - elapsed; wait > 0 {
		if t.ctx != nil {
			timer := time.NewTimer(wait)
			select {
			case <-t.ctx.Done():
				timer.Stop()
				// Preserve the upstream error (e.g. io.EOF) when it's already set.
				// Without this, callers never see EOF and may spin on the next Read.
				if err != nil {
					return n, err
				}
				return n, t.ctx.Err()
			case <-timer.C:
			}
		} else {
			time.Sleep(wait)
		}
	}

	return n, err
}
