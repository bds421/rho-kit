package timeout

import (
	"errors"
	"log/slog"
	"net/http"
	"sync"
)

// ErrResponseTooLarge is returned by Write when the buffered response
// exceeds the maximum buffer size configured for this middleware
// (default 1 MiB, see [defaultMaxBufferSize]; override with
// [WithMaxBufferSize]). This prevents silent data loss — callers can
// detect truncation and stop writing.
var ErrResponseTooLarge = errors.New("timeout: response exceeds buffer limit")

// timeoutWriter buffers the handler's response so we can discard it if the
// timeout fires first. Only one of writeToReal (success) or writeTimeout
// writes to the real ResponseWriter.

// defaultMaxBufferSize is the default per-request response buffer cap.
// Lowered from 10 MiB to 1 MiB: under a thundering-herd of timing-out
// requests, the previous cap let 10k concurrent attackers hold 100 GiB of
// transient memory. Production callers expecting larger response payloads
// should pair Timeout with body-size limits and override via
// [WithMaxBufferSize].
const defaultMaxBufferSize = 1 << 20 // 1 MiB

type timeoutWriter struct {
	w http.ResponseWriter

	mu          sync.Mutex
	h           http.Header
	code        int
	buf         []byte
	written     bool
	flushWarned bool

	maxBuffer int // per-instance cap; 0 falls back to defaultMaxBufferSize
}

func (tw *timeoutWriter) bufferCap() int {
	if tw.maxBuffer > 0 {
		return tw.maxBuffer
	}
	return defaultMaxBufferSize
}

// Header returns the buffered header map.
//
// The map itself is not mutex-protected (matching the http.ResponseWriter
// contract that Header/Write/WriteHeader must not be called concurrently
// from user code). However, the timeout middleware creates concurrency
// between the handler goroutine and the timeout goroutine. To handle this
// safely, writeToReal and writeTimeout snapshot the headers under the mutex
// before accessing them, ensuring no concurrent map read/write.
func (tw *timeoutWriter) Header() http.Header {
	return tw.h
}

// WriteHeader buffers the status code for later flushing. No-op after timeout.
func (tw *timeoutWriter) WriteHeader(code int) {
	tw.mu.Lock()
	defer tw.mu.Unlock()
	if tw.written {
		return
	}
	if code < 100 || code > 999 {
		panic("middleware/timeout: writer WriteHeader received invalid status code")
	}
	tw.code = code
}

// Write buffers response data. Returns ErrHandlerTimeout after timeout.
func (tw *timeoutWriter) Write(b []byte) (int, error) {
	tw.mu.Lock()
	defer tw.mu.Unlock()
	if tw.written {
		return 0, http.ErrHandlerTimeout
	}
	remaining := tw.bufferCap() - len(tw.buf)
	if remaining <= 0 {
		return 0, ErrResponseTooLarge
	}
	if len(b) > remaining {
		tw.buf = append(tw.buf, b[:remaining]...)
		return remaining, ErrResponseTooLarge
	}
	tw.buf = append(tw.buf, b...)
	return len(b), nil
}

// writeToReal flushes the buffered response to the real ResponseWriter.
// Headers are snapshot under the mutex to prevent concurrent map read/write
// with the handler goroutine that may still be calling Header().Set().
func (tw *timeoutWriter) writeToReal() {
	tw.mu.Lock()
	if tw.written {
		tw.mu.Unlock()
		return
	}
	tw.written = true

	// Snapshot headers and buffered data under the lock, then release
	// before writing to the real writer (which may be slow).
	hdrs := make(http.Header, len(tw.h))
	for k, v := range tw.h {
		cp := make([]string, len(v))
		copy(cp, v)
		hdrs[k] = cp
	}
	code := tw.code
	if code == 0 {
		code = http.StatusOK
	}
	buf := tw.buf
	tw.mu.Unlock()

	for k, v := range hdrs {
		tw.w.Header()[k] = v
	}
	tw.w.WriteHeader(code)
	if len(buf) > 0 {
		_, _ = tw.w.Write(buf)
	}
}

// Unwrap returns the underlying ResponseWriter for http.ResponseController
// compatibility (Go 1.20+).
//
// Returns nil after the timeout has fired or the response has already been
// flushed, preventing writes that would corrupt the already-sent response.
func (tw *timeoutWriter) Unwrap() http.ResponseWriter {
	tw.mu.Lock()
	defer tw.mu.Unlock()
	if tw.written {
		return nil
	}
	return tw.w
}

// Flush implements http.Flusher. Since the timeout writer buffers the response,
// Flush is a no-op — flushing the buffer before the timeout decision is made
// would defeat the purpose of the timeout guard. SSE endpoints should bypass
// the timeout middleware entirely (like WebSocket endpoints).
func (tw *timeoutWriter) Flush() {
	// Intentionally a no-op. The buffer is flushed to the real writer only
	// when writeToReal is called (handler completed before timeout).
	tw.mu.Lock()
	if !tw.flushWarned {
		tw.flushWarned = true
		tw.mu.Unlock()
		slog.Warn("timeout middleware: Flush() called but is a no-op; SSE/streaming endpoints should bypass the timeout middleware")
		return
	}
	tw.mu.Unlock()
}

// writeTimeout writes the JSON timeout response if the handler hasn't already
// written a response.
func (tw *timeoutWriter) writeTimeout() {
	tw.mu.Lock()
	defer tw.mu.Unlock()
	if tw.written {
		return
	}
	tw.written = true

	tw.w.Header().Set("Content-Type", "application/json")
	tw.w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = tw.w.Write([]byte(`{"error":"request timeout","code":"TIMEOUT"}` + "\n"))
}
